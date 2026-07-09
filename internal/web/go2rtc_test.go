package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubGo2RTC struct{ names []string }

func (s stubGo2RTC) Streams(context.Context) ([]string, error) { return s.names, nil }
func (s stubGo2RTC) StreamURL(n string) string                 { return "http://g/api/stream.m3u8?src=" + n }

// Go2RTCStreams returns [{name,url}] from the wired lister.
func TestGo2RTCStreamsHandler(t *testing.T) {
	h := &Handlers{}
	h.SetGo2RTC(stubGo2RTC{names: []string{"door", "elevator"}})

	rec := httptest.NewRecorder()
	h.Go2RTCStreams(rec, httptest.NewRequest(http.MethodGet, "/app/go2rtc/streams", nil))

	var got []struct{ Name, URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if len(got) != 2 || got[0].Name != "door" || got[0].URL != "http://g/api/stream.m3u8?src=door" {
		t.Fatalf("streams = %+v", got)
	}
}

// Without go2rtc wired, the endpoint returns an empty JSON array (picker hidden).
func TestGo2RTCStreamsDisabled(t *testing.T) {
	h := &Handlers{}
	rec := httptest.NewRecorder()
	h.Go2RTCStreams(rec, httptest.NewRequest(http.MethodGet, "/app/go2rtc/streams", nil))
	if body := rec.Body.String(); body != "[]\n" {
		t.Fatalf("want empty array, got %q", body)
	}
}
