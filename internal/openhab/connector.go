// Package openhab implements the openHAB connector: it drives device state from
// the openHAB REST/SSE API (state in via /rest/events, commands out via
// POST /rest/items) as a peer of the MQTT bridge.
package openhab

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

// UpdateHook is called after an inbound state update (to notify Yandex).
type UpdateHook func(d *device.Device, instance string, isProp bool)

type sub struct {
	deviceID string
	instance string
	isProp   bool
}

// Connector bridges openHAB items to the device model. It implements
// device.Connector.
type Connector struct {
	baseURL  string
	token    string
	client   *http.Client
	log      *slog.Logger
	onUpdate UpdateHook

	rootCtx    context.Context
	rootCancel context.CancelFunc

	mu        sync.RWMutex
	devices   map[string]*device.Device
	subs      map[string][]sub // openHAB item -> subscriptions
	sseCancel context.CancelFunc
}

// NewConnector builds the openHAB connector. onUpdate may be nil.
func NewConnector(cfg config.OpenHAB, log *slog.Logger, onUpdate UpdateHook) *Connector {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Connector{
		baseURL:    strings.TrimRight(cfg.URL, "/"),
		token:      cfg.Token,
		client:     &http.Client{}, // no global timeout: the SSE stream is long-lived
		log:        log,
		onUpdate:   onUpdate,
		rootCtx:    ctx,
		rootCancel: cancel,
		devices:    map[string]*device.Device{},
		subs:       map[string][]sub{},
	}
}

// Transport identifies this connector.
func (c *Connector) Transport() string { return "openhab" }

// Close stops the SSE stream and any in-flight work.
func (c *Connector) Close() { c.rootCancel() }

// Resync rebuilds the item->device routing and restarts the SSE stream for the
// new item set, then fetches current states.
func (c *Connector) Resync(devices []*device.Device) {
	newDevices := make(map[string]*device.Device, len(devices))
	newSubs := map[string][]sub{}
	itemSet := map[string]struct{}{}
	for _, d := range devices {
		newDevices[d.ID] = d
		for _, b := range d.StateBindings() {
			newSubs[b.Source] = append(newSubs[b.Source], sub{d.ID, b.Instance, b.IsProp})
			itemSet[b.Source] = struct{}{}
		}
	}
	items := make([]string, 0, len(itemSet))
	for it := range itemSet {
		items = append(items, it)
	}

	c.mu.Lock()
	c.devices, c.subs = newDevices, newSubs
	if c.sseCancel != nil {
		c.sseCancel()
	}
	sseCtx, cancel := context.WithCancel(c.rootCtx)
	c.sseCancel = cancel
	c.mu.Unlock()

	c.log.Info("openhab resync", "devices", len(devices), "items", len(items))
	if len(items) == 0 {
		return
	}
	go c.syncInitialStates(sseCtx, items)
	go c.streamEvents(sseCtx)
}

// Publish sends a command to an openHAB item (POST /rest/items/{item}).
func (c *Connector) Publish(item, payload string) {
	cmd := toOpenHABCommand(payload)
	ctx, cancel := context.WithTimeout(c.rootCtx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rest/items/"+item, strings.NewReader(cmd))
	if err != nil {
		c.log.Error("openhab command build", "err", err)
		return
	}
	req.Header.Set("Content-Type", "text/plain")
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		c.log.Error("openhab command", "item", item, "err", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		c.log.Warn("openhab command non-2xx", "item", item, "status", resp.StatusCode)
		return
	}
	c.log.Debug("openhab command", "item", item, "cmd", cmd)
}

// toOpenHABCommand adapts a Yandex-derived payload to an openHAB command:
// booleans become ON/OFF (Switch/Contact); everything else passes through
// (Dimmer 0-100, Color "h,s,b", mode strings, numbers).
func toOpenHABCommand(payload string) string {
	switch payload {
	case "true":
		return "ON"
	case "false":
		return "OFF"
	}
	return payload
}

func (c *Connector) auth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// syncInitialStates fetches the current state of the watched items so devices
// start correct after (re)connect.
func (c *Connector) syncInitialStates(ctx context.Context, items []string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/rest/items?fields=name,state", nil)
	if err != nil {
		return
	}
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		c.log.Warn("openhab initial states", "err", err)
		return
	}
	defer resp.Body.Close()
	var arr []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return
	}
	want := make(map[string]struct{}, len(items))
	for _, it := range items {
		want[it] = struct{}{}
	}
	for _, it := range arr {
		if _, ok := want[it.Name]; ok && usableState(it.State) {
			c.route(it.Name, it.State)
		}
	}
}

// streamEvents keeps the SSE connection alive, reconnecting with backoff.
func (c *Connector) streamEvents(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.connectSSE(ctx); err != nil && ctx.Err() == nil {
			c.log.Warn("openhab sse", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (c *Connector) connectSSE(ctx context.Context) error {
	url := c.baseURL + "/rest/events?topics=openhab/items/*/statechanged"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sse status %d", resp.StatusCode)
	}
	c.log.Info("openhab sse connected")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			c.handleSSEData(data.String())
			data.Reset()
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimSpace(v))
		}
	}
	return scanner.Err()
}

// handleSSEData parses one SSE event (an ItemStateChangedEvent) and routes it.
func (c *Connector) handleSSEData(data string) {
	if data == "" {
		return
	}
	var ev struct {
		Topic   string `json:"topic"`
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return
	}
	// topic: openhab/items/<Item>/statechanged
	parts := strings.Split(ev.Topic, "/")
	if len(parts) < 4 || parts[1] != "items" {
		return
	}
	item := parts[2]
	var p struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
		return
	}
	if usableState(p.Value) {
		c.route(item, p.Value)
	}
}

// route applies an item's value to every device/instance bound to it.
func (c *Connector) route(item, value string) {
	c.mu.RLock()
	subs := c.subs[item]
	devs := c.devices
	c.mu.RUnlock()
	for _, s := range subs {
		d := devs[s.deviceID]
		if d == nil {
			continue
		}
		d.UpdateFromMQTT(value, s.instance, s.isProp)
		if c.onUpdate != nil {
			c.onUpdate(d, s.instance, s.isProp)
		}
	}
}

func usableState(s string) bool {
	return s != "" && s != "NULL" && s != "UNDEF"
}
