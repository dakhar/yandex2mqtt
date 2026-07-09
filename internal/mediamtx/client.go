// Package mediamtx is a thin read-only client for a mediamtx instance
// (github.com/bluenviron/mediamtx) used as a camera relay that serves standard
// HLS (a proper multi-segment window, unlike go2rtc's 2-segment low-latency
// HLS). yandex2mqtt doesn't stream through mediamtx itself — the video_stream
// proxy fetches its HLS — so this client only lists configured paths for the
// builder's stream picker and builds the HLS URL a capability should point at.
//
// mediamtx splits the two concerns across ports: the control API (default 9997)
// lists paths, the HLS server (default 8888) serves streams — hence two bases.
package mediamtx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Client talks to a mediamtx control API + HLS server.
type Client struct {
	mu      sync.RWMutex
	apiBase string // control API, e.g. http://127.0.0.1:9997
	hlsBase string // HLS server, e.g. http://127.0.0.1:8888
	client  *http.Client
}

// New builds a Client for the given API and HLS base URLs (trailing slash
// trimmed). Either empty disables the corresponding half.
func New(apiBase, hlsBase string) *Client {
	return &Client{
		apiBase: strings.TrimRight(apiBase, "/"),
		hlsBase: strings.TrimRight(hlsBase, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// SetBases updates both base URLs at runtime (settings page). Safe for
// concurrent use with the readers.
func (c *Client) SetBases(apiBase, hlsBase string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiBase = strings.TrimRight(apiBase, "/")
	c.hlsBase = strings.TrimRight(hlsBase, "/")
}

func (c *Client) bases() (api, hls string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiBase, c.hlsBase
}

// Streams returns the configured path names, sorted. Regex/catch-all paths
// (which aren't concrete streams) are skipped. A nil client or empty API base
// yields an empty list, not an error.
func (c *Client) Streams(ctx context.Context) ([]string, error) {
	if c == nil {
		return nil, nil
	}
	api, _ := c.bases()
	if api == "" {
		return nil, nil
	}
	// Configured (not just active) paths, so on-demand cameras show up even idle.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/v3/config/paths/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mediamtx paths: %s", resp.Status)
	}
	var out struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Items))
	for _, it := range out.Items {
		if isConcretePath(it.Name) {
			names = append(names, it.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// StreamURL is the HLS URL the video_stream proxy should fetch for a path. It
// points at mediamtx's internal HLS base, so it must be reachable from us.
func (c *Client) StreamURL(name string) string {
	_, hls := c.bases()
	return hls + "/" + url.PathEscape(name) + "/index.m3u8"
}

// isConcretePath reports whether a mediamtx path name is a real stream rather
// than a regex path ("~^...") or a catch-all ("all"/"all_others").
func isConcretePath(name string) bool {
	if name == "" || name == "all" || name == "all_others" {
		return false
	}
	return !strings.HasPrefix(name, "~")
}
