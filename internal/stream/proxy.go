// Package stream implements a thin HLS reverse proxy so Alice's in-app player
// (hls.js, which fetches stream_url directly from a Yandex origin, e.g.
// https://yandex.ru) can reach a camera's local HLS playlist. It does no transcoding: it fetches the upstream
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
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/httplog"
)

// corsAnyOrigin allows any origin to read a proxied stream. This is safe and NOT
// a same-origin-policy hole: access is gated by the signed short-lived token (no
// valid token -> 403); the resource carries no cookies/credentials (we never send
// Access-Control-Allow-Credentials); and a token holder could fetch it
// server-side regardless of CORS — so Origin is not a security boundary here.
// "*" is chosen over echoing the request Origin deliberately: it is structurally
// immune to the reflect-Origin+credentials escalation (browsers forbid "*" with
// credentials) and covers the varying / "null" origins Alice's in-app WebView
// player uses. hls.js fetches in CORS mode with withCredentials=false, so "*"
// works (the earlier hardcoded https://yastatic.net did not match the player's
// real Origin, https://yandex.ru, and the browser blocked playback).
const corsAnyOrigin = "*"

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
	// streamClient fetches long-lived streams (MJPEG multipart) that never end,
	// so it has no total timeout — the inbound request's context bounds its life.
	streamClient *http.Client
	log          *slog.Logger
}

// New builds a Proxy. secret keys the HMAC that authenticates tokens (reuse the
// session secret); ttl bounds how long a signed URL stays valid. base, when
// non-empty, is the external base URL (e.g. https://yahome.bels.pw) used for the
// public link instead of the request Host — robust to Host-mangling proxies.
func New(secret string, ttl time.Duration, base string) *Proxy {
	sum := sha256.Sum256([]byte(secret))
	return &Proxy{
		key:          sum[:],
		ttl:          ttl,
		base:         strings.TrimRight(base, "/"),
		client:       &http.Client{Timeout: fetchTimeout},
		streamClient: &http.Client{}, // no total timeout: MJPEG streams forever
		log:          slog.Default(),
	}
}

// SetLogger routes the proxy's diagnostic logs to the app logger. A nil logger
// is ignored (the default remains slog.Default()).
func (p *Proxy) SetLogger(l *slog.Logger) {
	if l != nil {
		p.log = l
	}
}

// mjpegSuffix marks a proxied MJPEG stream in the URL path; the handler keys off
// it to use the long-lived (no-timeout, flushing) streaming path.
const mjpegSuffix = ".mjpeg"

// PublicURL turns a raw (locally reachable) stream URL into an absolute, public
// proxied URL. It uses the configured base if set, else derives scheme/host from
// the inbound request's forwarded headers. This is what get_stream returns.
// protocol is the video_stream protocol ("hls" or "mjpeg"); it only selects the
// cosmetic path extension so the URL looks like the right kind of resource.
func (p *Proxy) PublicURL(r *http.Request, rawURL, protocol string) string {
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
	// The path name carries an extension matching the stream kind: HLS must end in
	// .m3u8 (Yandex's player keys off it and won't fetch an extension-less URL),
	// MJPEG in .mjpeg. The signed token rides in the query string as "token",
	// mirroring Yandex's own example (…/playlist.m3u8?token=…). It must NOT be
	// named "t": the in-app player reserves "t" for its own timestamp/cache-bust
	// value and overwrites it (observed: it replaced our token with t=<unix_ms>,
	// producing a 403).
	name := "index.m3u8"
	if protocol == "mjpeg" {
		name = "index" + mjpegSuffix
	}
	return base + "/stream/" + name + "?token=" + p.sign(rawURL)
}

// Handler serves GET/OPTIONS /stream/{name}?token=<token>: it verifies the token,
// fetches the upstream resource, and either rewrites it (playlist) or streams it
// (segment). The path name (index.m3u8 / s.ts) is cosmetic — it only exists so
// the URL carries an HLS-looking extension for the player.
func (p *Proxy) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", corsAnyOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Range")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		q := r.URL.Query()
		raw, ok := p.verify(q.Get("token"))
		if !ok {
			// A rejected token is worth surfacing loudly: the in-app player mangles
			// query params (it reserves "t" for its own timestamp), so a 403 here is
			// the first sign of that class of regression. vsid/vpuid identify the
			// player session for correlation; the token itself is never logged.
			p.log.Warn("stream: token rejected",
				"path", r.URL.Path,
				"vsid", q.Get("vsid"),
				"vpuid", q.Get("vpuid"),
				"ip", httplog.ClientIP(r),
				"ua", r.UserAgent(),
			)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		p.log.Debug("stream: serving",
			"path", r.URL.Path,
			"vsid", q.Get("vsid"),
			"vpuid", q.Get("vpuid"),
		)

		// A long-lived MJPEG stream (marked by the .mjpeg path) is fetched with the
		// no-timeout client and streamed straight through with per-write flushing;
		// it is never a playlist, so skip that path entirely.
		streaming := strings.HasSuffix(r.URL.Path, mjpegSuffix)

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		if rng := r.Header.Get("Range"); rng != "" {
			req.Header.Set("Range", rng)
		}
		client := p.client
		if streaming {
			client = p.streamClient
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if streaming {
			ct := resp.Header.Get("Content-Type")
			if ct == "" {
				ct = "multipart/x-mixed-replace"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(resp.StatusCode)
			flushCopy(w, resp.Body)
			return
		}

		if isPlaylist(raw, resp.Header.Get("Content-Type")) {
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylist))
			if err != nil {
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			// Only rewrite a genuine playlist. An upstream error (or an HTML error
			// page served at a .m3u8 URL) must pass through untouched, not get
			// mangled into a fake playlist of rewritten HTML lines.
			if resp.StatusCode == http.StatusOK && looksLikePlaylist(body) {
				base, _ := url.Parse(raw)
				out := p.rewritePlaylist(body, base)
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(out)
				return
			}
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
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
// proxied relative reference "<name>?token=<token>", where name carries an
// extension matching the target (p.m3u8 for sub-playlists, s.ts otherwise) so
// each hop still looks like an HLS resource. On parse failure it returns the URI
// unchanged. The reference is relative and resolves against the playlist's own
// /stream/... path on the client.
func (p *Proxy) signResolved(uri string, base *url.URL) string {
	ref, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	abs := base.ResolveReference(ref).String()
	return proxiedName(abs) + "?token=" + p.sign(abs)
}

// proxiedName returns the extension-carrying path segment for a proxied URI: a
// sub-playlist keeps .m3u8, everything else is treated as a segment (.ts).
func proxiedName(rawURL string) string {
	if strings.HasSuffix(pathOf(rawURL), ".m3u8") {
		return "p.m3u8"
	}
	return "s.ts"
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

// looksLikePlaylist reports whether a body is actually an HLS playlist (starts
// with the #EXTM3U tag), guarding against error pages served at a .m3u8 URL.
func looksLikePlaylist(body []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(body)), "#EXTM3U")
}

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

// flushCopy streams src to w, flushing after every chunk so a live MJPEG feed
// reaches the client frame-by-frame instead of being buffered. It returns when
// src ends or the client disconnects (the request context cancels the read).
func flushCopy(w http.ResponseWriter, src io.Reader) {
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

func pathOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return strings.ToLower(u.Path)
}
