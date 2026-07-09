package go2rtc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamsSortedAndURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/streams" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flat19":{},"door":{},"elevator":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL + "/") // trailing slash trimmed
	names, err := c.Streams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || names[0] != "door" || names[1] != "elevator" || names[2] != "flat19" {
		t.Fatalf("streams = %v, want sorted [door elevator flat19]", names)
	}
	if got, want := c.StreamURL("cam a"), srv.URL+"/api/stream.m3u8?src=cam+a"; got != want {
		t.Fatalf("StreamURL = %q, want %q", got, want)
	}
}

func TestSetBaseRuntime(t *testing.T) {
	c := New("")
	if c.Base() != "" {
		t.Fatalf("initial base = %q", c.Base())
	}
	if names, _ := c.Streams(context.Background()); names != nil {
		t.Fatalf("empty base should list nothing, got %v", names)
	}
	c.SetBase("http://127.0.0.1:1984/") // admin sets it at runtime
	if c.Base() != "http://127.0.0.1:1984" {
		t.Fatalf("base after SetBase = %q", c.Base())
	}
	if got := c.StreamURL("door"); got != "http://127.0.0.1:1984/api/stream.m3u8?src=door" {
		t.Fatalf("StreamURL after SetBase = %q", got)
	}
}

func TestStreamsDisabled(t *testing.T) {
	// A nil client and an empty base both mean "feature off": no error, no names.
	var nilC *Client
	if names, err := nilC.Streams(context.Background()); err != nil || names != nil {
		t.Fatalf("nil client: names=%v err=%v", names, err)
	}
	if names, err := New("").Streams(context.Background()); err != nil || names != nil {
		t.Fatalf("empty base: names=%v err=%v", names, err)
	}
}
