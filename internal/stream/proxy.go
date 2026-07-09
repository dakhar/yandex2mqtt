// Package stream implements a thin HLS reverse proxy so Alice's in-app player
// (which fetches stream_url directly, from the yastatic.net origin) can reach a
// camera's local HLS playlist. It does no transcoding: it fetches the upstream
// .m3u8, rewrites segment/variant URIs to signed short-lived tokens routed back
// through us, and injects the CORS + MIME headers Yandex requires. Segment
// requests pass through verbatim (byte-for-byte, Range-aware).
package stream

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// corsOrigin is the origin Alice's HLS player runs on; it must be allowed or the
// browser blocks the cross-origin playlist/segment fetches.
const corsOrigin = "https://yastatic.net"

const (
	maxPlaylist  = 2 << 20 // cap a rewritten playlist at 2 MiB
	fetchTimeout = 30 * time.Second
)

// Proxy signs and serves proxied HLS URLs. It is safe for concurrent use.
type Proxy struct {
	key    []byte
	ttl    time.Duration
	base   string // configured public base (no trailing slash); "" = derive from request
	client *http.Client
}

// New builds a Proxy. secret keys the HMAC that authenticates tokens (reuse the
// session secret); ttl bounds how long a signed URL stays valid. base, when
// non-empty, is the external base URL (e.g. https://yahome.bels.pw) used for the
// public link instead of the request Host — robust to Host-mangling proxies.
func New(secret string, ttl time.Duration, base string) *Proxy {
	sum := sha256.Sum256([]byte(secret))
	return &Proxy{
		key:    sum[:],
		ttl:    ttl,
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: fetchTimeout},
	}
}

// PublicURL turns a raw (locally reachable) HLS URL into an absolute, public
// proxied URL. It uses the configured base if set, else derives scheme/host from
// the inbound request's forwarded headers. This is what get_stream returns.
func (p *Proxy) PublicURL(r *http.Request, rawURL string) string {
	base := p.base
	if base == "" {
		scheme := "https"
		if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
			scheme = v
		} else if r.TLS != nil {
			scheme = "https"
		}
		host := r.Host
		if v := r.Header.Get("X-Forwarded-Host"); v != "" {
			host = v
		}
		base = scheme + "://" + host
	}
	return base + "/stream/" + p.sign(rawURL)
}

// Handler serves GET/OPTIONS /stream/{token}: it verifies the token, fetches the
// upstream resource, and either rewrites it (playlist) or streams it (segment).
func (p *Proxy) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", corsOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Range")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		raw, ok := p.verify(chi.URLParam(r, "token"))
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		if rng := r.Header.Get("Range"); rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := p.client.Do(req)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if isPlaylist(raw, resp.Header.Get("Content-Type")) {
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylist))
			if err != nil {
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			base, _ := url.Parse(raw)
			out := p.rewritePlaylist(body, base)
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(out)
			return
		}

		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = contentTypeFor(raw)
		}
		w.Header().Set("Content-Type", ct)
		for _, h := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Cache-Control"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// rewritePlaylist resolves every URI in an m3u8 against base and replaces it with
// a signed token, so the player fetches segments and sub-playlists through us.
// Emitted tokens are relative: the client resolves them against the playlist's
// own /stream/<token> path.
func (p *Proxy) rewritePlaylist(body []byte, base *url.URL) []byte {
	var b strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), maxPlaylist)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			b.WriteString(line)
		case strings.HasPrefix(trimmed, "#"):
			b.WriteString(p.rewriteTagURI(line, base))
		default:
			b.WriteString(p.signResolved(trimmed, base))
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// rewriteTagURI rewrites the URI="..." attribute some tags carry (EXT-X-KEY,
// EXT-X-MEDIA, EXT-X-MAP, ...); other tags pass through unchanged.
func (p *Proxy) rewriteTagURI(line string, base *url.URL) string {
	const key = `URI="`
	i := strings.Index(line, key)
	if i < 0 {
		return line
	}
	start := i + len(key)
	j := strings.Index(line[start:], `"`)
	if j < 0 {
		return line
	}
	uri := line[start : start+j]
	return line[:start] + p.signResolved(uri, base) + line[start+j:]
}

// signResolved resolves a (possibly relative) URI against base and returns a
// signed token; on parse failure it returns the URI unchanged.
func (p *Proxy) signResolved(uri string, base *url.URL) string {
	ref, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return p.sign(base.ResolveReference(ref).String())
}

// sign returns "payload.sig", both base64url, where payload is "<exp>|<url>".
func (p *Proxy) sign(rawURL string) string {
	exp := time.Now().Add(p.ttl).Unix()
	payload := strconv.FormatInt(exp, 10) + "|" + rawURL
	return enc([]byte(payload)) + "." + enc(p.mac([]byte(payload)))
}

// verify authenticates a token and returns the upstream URL if the signature is
// valid and unexpired.
func (p *Proxy) verify(token string) (string, bool) {
	pt, st, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	payload, err := dec(pt)
	if err != nil {
		return "", false
	}
	sig, err := dec(st)
	if err != nil {
		return "", false
	}
	if !hmac.Equal(sig, p.mac(payload)) {
		return "", false
	}
	expStr, rawURL, ok := strings.Cut(string(payload), "|")
	if !ok {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return rawURL, true
}

func (p *Proxy) mac(b []byte) []byte {
	h := hmac.New(sha256.New, p.key)
	h.Write(b)
	return h.Sum(nil)
}

func enc(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func dec(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// isPlaylist reports whether a resource is an HLS playlist, by MIME then by the
// .m3u8 extension.
func isPlaylist(rawURL, contentType string) bool {
	if strings.Contains(strings.ToLower(contentType), "mpegurl") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(pathOf(rawURL)), ".m3u8")
}

// contentTypeFor infers a segment's MIME from its extension.
func contentTypeFor(rawURL string) string {
	switch {
	case strings.HasSuffix(pathOf(rawURL), ".ts"):
		return "video/MP2T"
	case strings.HasSuffix(pathOf(rawURL), ".m4s"), strings.HasSuffix(pathOf(rawURL), ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(pathOf(rawURL), ".aac"):
		return "audio/aac"
	default:
		return "application/octet-stream"
	}
}

func pathOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return strings.ToLower(u.Path)
}
