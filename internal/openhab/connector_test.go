package openhab

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// An openHAB-transport light bound to item "Light_Kitchen".
func lightDevice() *device.Device {
	return device.New(config.Device{
		ID: "L1", Type: "devices.types.light", AllowedUsers: []string{"1"},
		Transport:    "openhab",
		OpenHAB:      []config.OpenHABBinding{{Kind: "cap", Instance: "on", Item: "Light_Kitchen"}},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
	}, nil, nil)
}

func TestToOpenHABCommand(t *testing.T) {
	cases := map[string]string{"true": "ON", "false": "OFF", "50": "50", "120,100,50": "120,100,50"}
	for in, want := range cases {
		if got := toOpenHABCommand(in); got != want {
			t.Fatalf("toOpenHABCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

// Publish must POST the (translated) command to /rest/items/{item} with Bearer.
func TestPublishSendsCommand(t *testing.T) {
	var gotPath, gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewConnector(config.OpenHAB{URL: srv.URL, Token: "tok"}, discardLog(), nil)
	defer c.Close()

	// A Yandex on_off=true action -> "ON" command on the bound item.
	d := lightDevice()
	d.SetPublisher(c.Publish)
	d.SetCapabilityState(true, "devices.capabilities.on_off", "on", false)

	if gotPath != "/rest/items/Light_Kitchen" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody != "ON" {
		t.Fatalf("body = %q, want ON", gotBody)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

// An inbound SSE ItemStateChangedEvent must update the bound device's state.
func TestHandleSSEUpdatesDevice(t *testing.T) {
	c := NewConnector(config.OpenHAB{URL: "http://x", Token: "t"}, discardLog(), nil)
	defer c.Close()
	d := lightDevice()
	// Wire routing manually (Resync would also start SSE/HTTP we don't want here).
	c.devices["L1"] = d
	c.subs["Light_Kitchen"] = []sub{{deviceID: "L1", instance: "on", isProp: false}}

	c.handleSSEData(`{"topic":"openhab/items/Light_Kitchen/statechanged","payload":"{\"type\":\"OnOff\",\"value\":\"ON\"}","type":"ItemStateChangedEvent"}`)

	q := d.QueryState()
	if len(q.Capabilities) == 0 || q.Capabilities[0].State.Value != true {
		t.Fatalf("device state not updated from SSE: %+v", q.Capabilities)
	}

	// OFF -> false.
	c.handleSSEData(`{"topic":"openhab/items/Light_Kitchen/statechanged","payload":"{\"value\":\"OFF\"}"}`)
	if d.QueryState().Capabilities[0].State.Value != false {
		t.Fatal("OFF not applied")
	}
}

func TestHandleSSEIgnoresUnknownAndUndef(t *testing.T) {
	var hookCalls int
	c := NewConnector(config.OpenHAB{URL: "http://x", Token: "t"}, discardLog(),
		func(*device.Device, string, bool) { hookCalls++ })
	defer c.Close()
	d := lightDevice()
	c.devices["L1"] = d
	c.subs["Light_Kitchen"] = []sub{{deviceID: "L1", instance: "on"}}

	c.handleSSEData(`{"topic":"openhab/items/Other/statechanged","payload":"{\"value\":\"ON\"}"}`) // unsubscribed item
	c.handleSSEData(`{"topic":"openhab/items/Light_Kitchen/statechanged","payload":"{\"value\":\"UNDEF\"}"}`)
	if hookCalls != 0 {
		t.Fatalf("unknown/undef events must not trigger updates; hookCalls=%d", hookCalls)
	}
}

// A video_stream item that holds a server-relative HLS path must be resolved
// against the openHAB base URL so get_stream returns a fetchable absolute URL.
func TestStreamItemPathResolvedToAbsolute(t *testing.T) {
	c := NewConnector(config.OpenHAB{URL: "http://oh:8080/", Token: "t"}, discardLog(), nil)
	defer c.Close()

	cam := device.New(config.Device{
		ID: "CAM", Type: "devices.types.camera", AllowedUsers: []string{"1"},
		Transport:    "openhab",
		OpenHAB:      []config.OpenHABBinding{{Kind: "cap", Instance: device.StreamInstance, Item: "CamDoorHlsUrl"}},
		Capabilities: []config.Capability{{Type: "devices.capabilities.video_stream"}},
	}, nil, nil)
	c.devices["CAM"] = cam
	c.subs["CamDoorHlsUrl"] = []sub{{deviceID: "CAM", instance: device.StreamInstance}}

	// Canonical IpCamera state has no leading slash; a leading-slash and a full
	// URL must also resolve correctly.
	cases := map[string]string{
		"ipcamera/door/ipcamera.m3u8":      "http://oh:8080/ipcamera/door/ipcamera.m3u8",
		"/ipcamera/door/ipcamera.m3u8":     "http://oh:8080/ipcamera/door/ipcamera.m3u8",
		"http://cam.local/door/index.m3u8": "http://cam.local/door/index.m3u8",
	}
	for state, want := range cases {
		payload, _ := json.Marshal(map[string]string{"value": state})
		c.handleSSEData(`{"topic":"openhab/items/CamDoorHlsUrl/statechanged","payload":` + strconv.Quote(string(payload)) + `}`)

		res := cam.SetCapabilityState(nil, "devices.capabilities.video_stream", device.StreamInstance, false)
		m, ok := res.State.Value.(map[string]any)
		if !ok {
			t.Fatalf("get_stream did not return a value map: %+v", res)
		}
		if got := m["stream_url"]; got != want {
			t.Fatalf("state %q -> stream_url = %v, want %q", state, got, want)
		}
	}
}

// Items must expose name/type/label plus enumerated state options (hints) for
// the builder's autocomplete + mode-mapping dropdowns.
func TestItemsListsNamesAndOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/items" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `[
			{"name":"VacuumCleaner_Operation_Mode","type":"String","label":"Режим",
			 "stateDescription":{"options":[{"value":"vacuum"},{"value":"mop"}]}},
			{"name":"Light_Kitchen","type":"Switch","label":"Свет"}
		]`)
	}))
	defer srv.Close()

	c := NewConnector(config.OpenHAB{URL: srv.URL, Token: "t"}, discardLog(), nil)
	defer c.Close()
	items, err := c.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Name != "VacuumCleaner_Operation_Mode" || items[0].Type != "String" || items[0].Label != "Режим" {
		t.Fatalf("item[0]=%+v", items[0])
	}
	if len(items[0].Options) != 2 || items[0].Options[0] != "vacuum" || items[0].Options[1] != "mop" {
		t.Fatalf("options=%v", items[0].Options)
	}
	if len(items[1].Options) != 0 {
		t.Fatalf("Switch should have no options: %v", items[1].Options)
	}
}

func TestItemsUnconfiguredErrors(t *testing.T) {
	c := NewConnector(config.OpenHAB{}, discardLog(), nil) // no URL/token
	defer c.Close()
	if _, err := c.Items(context.Background()); err == nil {
		t.Fatal("expected error when openHAB is unconfigured")
	}
}

func TestConnectorTransport(t *testing.T) {
	c := NewConnector(config.OpenHAB{URL: "http://x", Token: "t"}, discardLog(), nil)
	if c.Transport() != "openhab" {
		t.Fatalf("transport = %q", c.Transport())
	}
}
