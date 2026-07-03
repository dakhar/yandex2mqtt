package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSchemaEndpoint(t *testing.T) {
	srv, _, _, _ := setup(t)
	resp, _ := http.Get(srv.URL + "/app/schema")
	body := readAll(t, resp)
	for _, want := range []string{"device_types", "devices.capabilities.on_off", "devices.properties.float", "mode_values"} {
		if !strings.Contains(body, want) {
			t.Fatalf("schema missing %q", want)
		}
	}
}

func TestCreateDeviceValid(t *testing.T) {
	srv, _, catalog, reloader := setup(t)
	payload := `{
      "name":"Свет на кухне","type":"devices.types.light","room_id":"","description":"",
      "capabilities":[
        {"type":"devices.capabilities.on_off","instance":"on","retrievable":true,"reportable":true,"params":{},"set":"yandex/Kitchen/set","state":"yandex/Kitchen/state"},
        {"type":"devices.capabilities.range","instance":"brightness","retrievable":true,"reportable":true,
         "params":{"instance":"brightness","unit":"unit.percent","range":{"min":0,"max":100,"precision":1}},
         "set":"yandex/Kitchen/br/set","state":"yandex/Kitchen/br/state"}
      ],
      "properties":[]
    }`
	resp := postJSON(t, srv.URL+"/app/devices", payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, want 200", resp.StatusCode)
	}

	// Verify persisted with a generated UUID id and a reload happened.
	devs, _ := catalog.ListDevicesForUser(context.Background(), "1")
	var created bool
	for _, d := range devs {
		if d.Name == "Свет на кухне" {
			created = true
			if len(d.ID) != 36 { // UUID length
				t.Fatalf("expected UUID id, got %q", d.ID)
			}
		}
	}
	if !created {
		t.Fatalf("new device not persisted; devices=%+v", devs)
	}
	if reloader.calls == 0 {
		t.Fatal("create must trigger a registry reload")
	}
}

func TestCreateDeviceInvalidBlocked(t *testing.T) {
	srv, _, catalog, _ := setup(t)
	// Unknown capability type must fail validation.
	payload := `{"name":"Bad","type":"devices.types.light","capabilities":[
      {"type":"devices.capabilities.bogus","instance":"x","params":{},"state":"s"}],"properties":[]}`
	resp := http.Response{}
	r := postJSON(t, srv.URL+"/app/devices", payload)
	resp = *r
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid device status = %d, want 400", resp.StatusCode)
	}
	// Nothing beyond the seeded Lamp should be stored.
	devs, _ := catalog.ListDevicesForUser(context.Background(), "1")
	if len(devs) != 1 {
		t.Fatalf("invalid device must not be saved; devices=%d", len(devs))
	}
}

func TestCreateDeviceReturnsID(t *testing.T) {
	srv, _, _, _ := setup(t)
	payload := `{"name":"L","type":"devices.types.light","capabilities":[
      {"type":"devices.capabilities.on_off","instance":"on","retrievable":true,"params":{},"set":"a/set","state":"a/state"}],"properties":[]}`
	resp := postJSONKeepBody(t, srv.URL+"/app/devices", payload)
	defer resp.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.ID) != 36 {
		t.Fatalf("expected UUID in response, got %q", out.ID)
	}
}

func TestDeleteDeviceViaBuilder(t *testing.T) {
	srv, _, catalog, _ := setup(t)
	postForm(t, srv.URL+"/app/devices/Lamp/delete")
	devs, _ := catalog.ListDevicesForUser(context.Background(), "1")
	if len(devs) != 0 {
		t.Fatalf("device not deleted; devices=%d", len(devs))
	}
}

func postJSONKeepBody(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
