// Package go2rtc is a thin read-only client for a go2rtc instance
// (github.com/AlexxIT/go2rtc) used as the camera relay. yandex2mqtt doesn't
// stream through go2rtc itself — the video_stream proxy fetches go2rtc's HLS —
// so this client only lists the configured streams for the builder's picker and
// builds the HLS URL a video_stream capability should point at.
package go2rtc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client talks to a go2rtc HTTP API base (e.g. http://127.0.0.1:1984).
type Client struct {
	base   string
	client *http.Client
}

// New builds a Client for the given base URL (trailing slash trimmed).
func New(base string) *Client {
	return &Client{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Base returns the configured base URL.
func (c *Client) Base() string { return c.base }

// Streams returns the configured stream names, sorted. A nil client or empty
// base yields an empty list (feature disabled), not an error.
func (c *Client) Streams(ctx context.Context) ([]string, error) {
	if c == nil || c.base == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/streams", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go2rtc streams: %s", resp.Status)
	}
	// /api/streams returns an object keyed by stream name.
	var m map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// StreamURL is the HLS URL the video_stream proxy should fetch for a stream. It
// points at go2rtc's internal base, so it must be reachable from this process.
func (c *Client) StreamURL(name string) string {
	return c.base + "/api/stream.m3u8?src=" + url.QueryEscape(name)
}
