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
	p := New("secret", time.Hour, "")
	tok := p.sign("http://cam/door/index.m3u8")
	got, ok := p.verify(tok)
	if !ok || got != "http://cam/door/index.m3u8" {
		t.Fatalf("verify=%q ok=%v", got, ok)
	}
}

func TestVerifyRejectsTamperAndExpiry(t *testing.T) {
	p := New("secret", time.Hour, "")
	tok := p.sign("http://cam/x.m3u8")

	// Flip a byte in the signature part.
	i := strings.IndexByte(tok, '.')
	bad := tok[:i+1] + "AAAA" + tok[i+2:]
	if _, ok := p.verify(bad); ok {
		t.Fatal("tampered token verified")
	}

	// Wrong key must not verify.
	if _, ok := New("other", time.Hour, "").verify(tok); ok {
		t.Fatal("token verified under a different key")
	}

	// Already-expired token.
	exp := New("secret", -time.Minute, "")
	if _, ok := p.verify(exp.sign("http://cam/x.m3u8")); ok {
		t.Fatal("expired token verified")
	}
}

// PublicURL uses the configured base (ignoring a Host-mangling proxy) when set,
// and falls back to the request's forwarded host otherwise.
func TestPublicURLBase(t *testing.T) {
	req := httptest.NewRequest("POST", "http://10.0.0.5:8080/provider", nil)
	req.Host = "10.0.0.5:8080" // proxy forwarded an internal Host

	withBase := New("secret", time.Hour, "https://yahome.bels.pw/")
	if got := withBase.PublicURL(req, "http://cam/x.m3u8", "hls"); !strings.HasPrefix(got, "https://yahome.bels.pw/stream/index.m3u8?token=") {
		t.Fatalf("configured base ignored: %q", got)
	}
	noBase := New("secret", time.Hour, "")
	if got := noBase.PublicURL(req, "http://cam/x.m3u8", "hls"); !strings.HasPrefix(got, "https://10.0.0.5:8080/stream/index.m3u8?token=") {
		t.Fatalf("fallback host wrong: %q", got)
	}
	// MJPEG gets an .mjpeg path so the handler picks the streaming path.
	if got := withBase.PublicURL(req, "http://cam/live", "mjpeg"); !strings.HasPrefix(got, "https://yahome.bels.pw/stream/index.mjpeg?token=") {
		t.Fatalf("mjpeg url wrong: %q", got)
	}
}

func TestPlaylistRewrite(t *testing.T) {
	p := New("secret", time.Hour, "")
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
	// Every URI (segment + key) must become a proxied "<name>?token=<token>" ref whose
	// token maps back to the resolved absolute upstream URL.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line == "#EXTM3U" || strings.HasPrefix(line, "#EXTINF"):
			continue
		case strings.HasPrefix(line, "#EXT-X-KEY"):
			uri := line[strings.Index(line, `URI="`)+5 : strings.LastIndex(line, `"`)]
			if raw, ok := p.verify(tokParam(uri)); !ok || raw != "http://cam:8080/ipcamera/door/key.bin" {
				t.Fatalf("key URI not rewritten: %q -> %q ok=%v", line, raw, ok)
			}
		default: // a segment ref: s.ts?token=<token>
			if !strings.HasPrefix(line, "s.ts?token=") {
				t.Fatalf("segment ref lacks .ts extension: %q", line)
			}
			raw, ok := p.verify(tokParam(line))
			if !ok || !strings.HasPrefix(raw, "http://cam:8080/ipcamera/door/seg") {
				t.Fatalf("segment not rewritten: %q -> %q ok=%v", line, raw, ok)
			}
		}
	}
}

// tokParam extracts the ?token=<token> query value from a proxied reference.
func tokParam(ref string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return u.Query().Get("token")
}

// An upstream error page served at a .m3u8 URL must pass through unchanged, not
// be mangled into a fake playlist of rewritten HTML lines.
func TestHandlerPassesThroughNonPlaylist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "<!doctype html><html><head></head></html>")
	}))
	defer upstream.Close()

	p := New("secret", time.Hour, "")
	r := chi.NewRouter()
	r.HandleFunc("/stream/{name}", p.Handler())
	front := httptest.NewServer(r)
	defer front.Close()

	resp, err := http.Get(front.URL + "/stream/index.m3u8?token=" + p.sign(upstream.URL+"/dead.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 passthrough", resp.StatusCode)
	}
	if !strings.HasPrefix(string(body), "<!doctype html>") {
		t.Fatalf("body was mangled instead of passed through: %q", body)
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

	p := New("secret", time.Hour, "")
	r := chi.NewRouter()
	r.HandleFunc("/stream/{name}", p.Handler())
	front := httptest.NewServer(r)
	defer front.Close()

	// Fetch the proxied playlist (URL carries the .m3u8 extension + ?token= token).
	playlistURL := front.URL + "/stream/index.m3u8?token=" + p.sign(upstream.URL+"/index.m3u8")
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

	// The rewritten segment line is a relative "s.ts?token=<token>" ref; resolve it
	// against the playlist URL (/stream/index.m3u8?token=…).
	var segRef string
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			segRef = strings.TrimSpace(line)
		}
	}
	if !strings.HasPrefix(segRef, "s.ts?token=") {
		t.Fatalf("segment ref not a .ts URL: %q\n%s", segRef, body)
	}

	segResp, err := http.Get(front.URL + "/stream/" + segRef)
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

// TestHandlerStreamsMJPEG checks that an MJPEG feed proxied via /stream/*.mjpeg is
// streamed through verbatim with CORS and its multipart content type — not run
// through the HLS-playlist path.
func TestHandlerStreamsMJPEG(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		io.WriteString(w, "--frame\r\nContent-Type: image/jpeg\r\n\r\nJPEGDATA\r\n")
	}))
	defer upstream.Close()

	p := New("secret", time.Hour, "")
	r := chi.NewRouter()
	r.HandleFunc("/stream/{name}", p.Handler())
	front := httptest.NewServer(r)
	defer front.Close()

	resp, err := http.Get(front.URL + "/stream/index.mjpeg?token=" + p.sign(upstream.URL+"/live"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/x-mixed-replace") {
		t.Fatalf("mjpeg content-type = %q", ct)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != corsOrigin {
		t.Fatalf("mjpeg CORS = %q", got)
	}
	if !strings.Contains(string(body), "JPEGDATA") {
		t.Fatalf("mjpeg body not streamed through: %q", body)
	}
}

// The CORS Access-Control-Allow-Origin must ECHO the request's Origin: Alice's
// hls.js fetches the manifest in CORS mode from a Yandex origin that varies
// (observed https://yandex.ru), and a mismatched header makes the browser block
// it from reading the response — video never starts.
func TestHandlerEchoesOrigin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegURL")
		io.WriteString(w, "#EXTM3U\n#EXTINF:2.0,\nseg0.ts\n")
	}))
	defer upstream.Close()

	p := New("secret", time.Hour, "")
	r := chi.NewRouter()
	r.HandleFunc("/stream/{name}", p.Handler())
	front := httptest.NewServer(r)
	defer front.Close()

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/stream/index.m3u8?token="+p.sign(upstream.URL+"/index.m3u8"), nil)
	req.Header.Set("Origin", "https://yandex.ru")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://yandex.ru" {
		t.Fatalf("ACAO = %q, want echoed https://yandex.ru", got)
	}
	if got := resp.Header.Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q", got)
	}
}
