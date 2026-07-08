package stream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	p := New("secret", time.Hour)
	tok := p.sign("http://cam/door/index.m3u8")
	got, ok := p.verify(tok)
	if !ok || got != "http://cam/door/index.m3u8" {
		t.Fatalf("verify=%q ok=%v", got, ok)
	}
}

func TestVerifyRejectsTamperAndExpiry(t *testing.T) {
	p := New("secret", time.Hour)
	tok := p.sign("http://cam/x.m3u8")

	// Flip a byte in the signature part.
	i := strings.IndexByte(tok, '.')
	bad := tok[:i+1] + "AAAA" + tok[i+2:]
	if _, ok := p.verify(bad); ok {
		t.Fatal("tampered token verified")
	}

	// Wrong key must not verify.
	if _, ok := New("other", time.Hour).verify(tok); ok {
		t.Fatal("token verified under a different key")
	}

	// Already-expired token.
	exp := New("secret", -time.Minute)
	if _, ok := p.verify(exp.sign("http://cam/x.m3u8")); ok {
		t.Fatal("expired token verified")
	}
}

func TestPlaylistRewrite(t *testing.T) {
	p := New("secret", time.Hour)
	base, _ := url.Parse("http://cam:8080/ipcamera/door/index.m3u8")
	in := "#EXTM3U\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n" +
		"#EXTINF:2.0,\n" +
		"seg1.ts\n" +
		"http://cam:8080/ipcamera/door/seg2.ts\n"
	out := string(p.rewritePlaylist([]byte(in), base))

	if !strings.HasPrefix(out, "#EXTM3U\n") {
		t.Fatalf("header not preserved:\n%s", out)
	}
	// Every URI (segment + key) must be replaced by a verifiable token that maps
	// back to the resolved absolute upstream URL.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line == "#EXTM3U" || strings.HasPrefix(line, "#EXTINF"):
			continue
		case strings.HasPrefix(line, "#EXT-X-KEY"):
			tok := line[strings.Index(line, `URI="`)+5 : strings.LastIndex(line, `"`)]
			if raw, ok := p.verify(tok); !ok || raw != "http://cam:8080/ipcamera/door/key.bin" {
				t.Fatalf("key URI not rewritten: %q -> %q ok=%v", line, raw, ok)
			}
		default: // a segment token
			raw, ok := p.verify(line)
			if !ok || !strings.HasPrefix(raw, "http://cam:8080/ipcamera/door/seg") {
				t.Fatalf("segment not rewritten: %q -> %q ok=%v", line, raw, ok)
			}
		}
	}
}

// TestHandlerProxiesPlaylistAndSegment drives the full HTTP path: an upstream
// serves a playlist referencing a segment; the handler rewrites the playlist,
// and a follow-up request for the rewritten token streams the segment through
// with CORS + MIME headers.
func TestHandlerProxiesPlaylistAndSegment(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.m3u8":
			w.Header().Set("Content-Type", "application/x-mpegURL")
			io.WriteString(w, "#EXTM3U\n#EXTINF:2.0,\nseg0.ts\n")
		case "/seg0.ts":
			w.Header().Set("Content-Type", "video/MP2T")
			io.WriteString(w, "TSDATA")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	p := New("secret", time.Hour)
	r := chi.NewRouter()
	r.HandleFunc("/stream/{token}", p.Handler())
	front := httptest.NewServer(r)
	defer front.Close()

	// Fetch the proxied playlist.
	playlistURL := front.URL + "/stream/" + p.sign(upstream.URL+"/index.m3u8")
	resp, err := http.Get(playlistURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "application/vnd.apple.mpegurl" {
		t.Fatalf("playlist MIME=%q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != corsOrigin {
		t.Fatalf("playlist CORS=%q", got)
	}

	// The rewritten segment line is a bare token; resolve it against /stream/.
	var segToken string
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			segToken = strings.TrimSpace(line)
		}
	}
	if segToken == "" {
		t.Fatalf("no segment token in playlist:\n%s", body)
	}

	segResp, err := http.Get(front.URL + "/stream/" + segToken)
	if err != nil {
		t.Fatal(err)
	}
	segBody, _ := io.ReadAll(segResp.Body)
	segResp.Body.Close()
	if string(segBody) != "TSDATA" {
		t.Fatalf("segment body=%q", segBody)
	}
	if got := segResp.Header.Get("Content-Type"); got != "video/MP2T" {
		t.Fatalf("segment MIME=%q", got)
	}
	if got := segResp.Header.Get("Access-Control-Allow-Origin"); got != corsOrigin {
		t.Fatalf("segment CORS=%q", got)
	}
}
