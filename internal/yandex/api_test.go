package yandex

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

func testServer(t *testing.T) (*httptest.Server, []*device.Device) {
	t.Helper()
	light := device.New(config.Device{
		ID:           "Light1",
		Name:         "Лампа",
		Room:         "Кухня",
		Type:         "devices.types.light",
		AllowedUsers: []string{"1"},
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{
			{Instance: "on", Set: "light/power/set", State: "light/state"},
			{Instance: "brightness", Set: "light/br/set", State: "light/br/state"},
		}},
		Capabilities: []config.Capability{
			{Type: "devices.capabilities.on_off", Retrievable: true},
			{Type: "devices.capabilities.range", Retrievable: true, Parameters: map[string]any{
				"instance": "brightness", "unit": "unit.percent",
				"range": map[string]any{"min": 0, "max": 100, "precision": 1},
			}},
		},
	}, func(string, string) {}, nil)

	// A device the user is NOT allowed to access.
	other := device.New(config.Device{
		ID: "Secret", Type: "devices.types.light", AllowedUsers: []string{"99"},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
	}, func(string, string) {}, nil)

	reg := device.NewRegistry([]*device.Device{light, other})
	api := New(reg, StubAuth("1"), slog.New(slog.NewTextHandler(io.Discard, nil)))

	return httptest.NewServer(api.Routes()), []*device.Device{light, other}
}

func do(t *testing.T, method, url, body string, auth bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer test-token")
	}
	req.Header.Set("X-Request-Id", "req-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCheckHEAD(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp := do(t, http.MethodHead, srv.URL+"/v1.0", "", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD /v1.0 = %d, want 200", resp.StatusCode)
	}
}

func TestAuthRequired(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp := do(t, http.MethodGet, srv.URL+"/v1.0/user/devices", "", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated devices = %d, want 401", resp.StatusCode)
	}
}

func TestDevicesFiltersByUser(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp := do(t, http.MethodGet, srv.URL+"/v1.0/user/devices", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var dr DevicesResponse
	decode(t, resp, &dr)
	if dr.RequestID != "req-1" {
		t.Fatalf("request_id = %q", dr.RequestID)
	}
	if dr.Payload.UserID != "1" {
		t.Fatalf("user_id = %q", dr.Payload.UserID)
	}
	if len(dr.Payload.Devices) != 1 || dr.Payload.Devices[0].ID != "Light1" {
		t.Fatalf("expected only Light1, got %+v", dr.Payload.Devices)
	}
}

func TestQueryUnknownAndForbidden(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"devices":[{"id":"Light1"},{"id":"Nope"},{"id":"Secret"}]}`
	resp := do(t, http.MethodPost, srv.URL+"/v1.0/user/devices/query", body, true)
	var qr QueryResponse
	decode(t, resp, &qr)
	if len(qr.Payload.Devices) != 3 {
		t.Fatalf("want 3 device results, got %d", len(qr.Payload.Devices))
	}
	byID := map[string]device.QueryResult{}
	for _, d := range qr.Payload.Devices {
		byID[d.ID] = d
	}
	if byID["Nope"].ErrorCode != "DEVICE_NOT_FOUND" {
		t.Fatalf("Nope error = %q", byID["Nope"].ErrorCode)
	}
	if byID["Secret"].ErrorCode != "DEVICE_NOT_FOUND" {
		t.Fatalf("forbidden device should be DEVICE_NOT_FOUND, got %q", byID["Secret"].ErrorCode)
	}
	if byID["Light1"].ErrorCode != "" || len(byID["Light1"].Capabilities) == 0 {
		t.Fatalf("Light1 should have state, got %+v", byID["Light1"])
	}
}

func TestActionPublishesAndReports(t *testing.T) {
	srv, devs := testServer(t)
	defer srv.Close()

	body := `{"payload":{"devices":[{"id":"Light1","capabilities":[
		{"type":"devices.capabilities.on_off","state":{"instance":"on","value":true}},
		{"type":"devices.capabilities.range","state":{"instance":"brightness","value":40}}
	]}]}}`
	resp := do(t, http.MethodPost, srv.URL+"/v1.0/user/devices/action", body, true)
	var ar ActionResponse
	decode(t, resp, &ar)
	if len(ar.Payload.Devices) != 1 {
		t.Fatalf("want 1 device, got %d", len(ar.Payload.Devices))
	}
	caps := ar.Payload.Devices[0].Capabilities
	if len(caps) != 2 {
		t.Fatalf("want 2 cap results, got %d", len(caps))
	}
	for _, c := range caps {
		if c.State.ActionResult.Status != "DONE" {
			t.Fatalf("cap %s status = %s", c.Type, c.State.ActionResult.Status)
		}
	}
	// State should now be reflected in a query.
	q := devs[0].QueryState()
	var on any
	for _, c := range q.Capabilities {
		if c.Type == "devices.capabilities.on_off" {
			on = c.State.Value
		}
	}
	if on != true {
		t.Fatalf("on_off state after action = %#v, want true", on)
	}
}

func TestActionUnknownDevice(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"payload":{"devices":[{"id":"Ghost","capabilities":[
		{"type":"devices.capabilities.on_off","state":{"instance":"on","value":true}}]}]}}`
	resp := do(t, http.MethodPost, srv.URL+"/v1.0/user/devices/action", body, true)
	var ar ActionResponse
	decode(t, resp, &ar)
	dr := ar.Payload.Devices[0]
	if dr.ActionResult == nil || dr.ActionResult.ErrorCode != "DEVICE_NOT_FOUND" {
		t.Fatalf("want device-level DEVICE_NOT_FOUND, got %+v", dr)
	}
}

func TestUnlink(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp := do(t, http.MethodPost, srv.URL+"/v1.0/user/unlink", "", true)
	var ur UnlinkResponse
	decode(t, resp, &ur)
	if ur.RequestID != "req-1" {
		t.Fatalf("unlink request_id = %q", ur.RequestID)
	}
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
