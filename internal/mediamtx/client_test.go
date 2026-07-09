package mediamtx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamsAndURL(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config/paths/list" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// concrete paths + a regex path and a catch-all, which must be filtered out.
		_, _ = w.Write([]byte(`{"itemCount":4,"items":[
			{"name":"flat19"},{"name":"door"},{"name":"~^cam.*$"},{"name":"all_others"}
		]}`))
	}))
	defer api.Close()

	c := New(api.URL+"/", "http://127.0.0.1:8888/")
	names, err := c.Streams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "door" || names[1] != "flat19" {
		t.Fatalf("streams = %v, want sorted concrete [door flat19]", names)
	}
	if got, want := c.StreamURL("door"), "http://127.0.0.1:8888/door/index.m3u8"; got != want {
		t.Fatalf("StreamURL = %q, want %q", got, want)
	}
}

func TestStreamsDisabled(t *testing.T) {
	var nilC *Client
	if names, err := nilC.Streams(context.Background()); err != nil || names != nil {
		t.Fatalf("nil client: names=%v err=%v", names, err)
	}
	if names, err := New("", "").Streams(context.Background()); err != nil || names != nil {
		t.Fatalf("empty API base: names=%v err=%v", names, err)
	}
}

func TestSetBasesRuntime(t *testing.T) {
	c := New("", "")
	c.SetBases("http://127.0.0.1:9997", "http://127.0.0.1:8888/")
	if got := c.StreamURL("elevator"); got != "http://127.0.0.1:8888/elevator/index.m3u8" {
		t.Fatalf("StreamURL after SetBases = %q", got)
	}
}
