package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// Creating a device with a value mapping preserves value types (bool stays
// bool, string stays string) through the whole builder -> DB -> load path.
func TestCreateWithValueMapping(t *testing.T) {
	srv, _, catalog, _ := setup(t)
	// Vacuum with a toggle "pause" mapping: yandex booleans <-> mqtt strings.
	payload := `{
      "name":"Пылесос","type":"devices.types.vacuum_cleaner","capabilities":[
        {"type":"devices.capabilities.toggle","instance":"pause","retrievable":true,"reportable":true,
         "params":{"instance":"pause"},"set":"vac/ctrl/set","state":"vac/op/state",
         "mapping":[{"yandex":true,"mqtt":"PAUSE"},{"yandex":false,"mqtt":"START"}]}
      ],"properties":[]}`
	resp := postJSON(t, srv.URL+"/app/devices", payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	// Reload the stored device and check the mapping types survived.
	devs, _ := catalog.ListDevicesForUser(context.Background(), "1")
	var id string
	for _, d := range devs {
		if d.Name == "Пылесос" {
			id = d.ID
		}
	}
	if id == "" {
		t.Fatal("device not created")
	}
	d, _, ok, err := catalog.GetDevice(context.Background(), "1", id)
	if err != nil || !ok {
		t.Fatalf("get device: %v", err)
	}
	if len(d.ValueMapping) != 1 || d.ValueMapping[0].Type != "toggle" {
		t.Fatalf("value mapping lost: %+v", d.ValueMapping)
	}
	pair := d.ValueMapping[0].Mapping[0]
	if pair.Instance != "pause" {
		t.Fatalf("mapping instance = %q", pair.Instance)
	}
	yandex := pair.Mapping[0]
	if yandex[0] != true || yandex[1] != false {
		t.Fatalf("yandex values/types lost: %#v", yandex)
	}
	if pair.Mapping[1][0] != "PAUSE" {
		t.Fatalf("mqtt value lost: %#v", pair.Mapping[1])
	}
}

// Edit prefill (GET .../edit) round-trips the device, and update replaces it.
func TestEditAndUpdate(t *testing.T) {
	srv, _, catalog, reloader := setup(t)
	ctx := context.Background()

	// The seeded Lamp exists; edit page must include its data.
	resp, _ := http.Get(srv.URL + "/app/devices/Lamp/edit")
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "device-data") || !strings.Contains(body, "Лампа") {
		t.Fatalf("edit page missing prefill; status=%d", resp.StatusCode)
	}

	// Update the Lamp: rename + change topic.
	before := reloader.calls
	payload := `{"name":"Лампа 2","type":"devices.types.light","capabilities":[
      {"type":"devices.capabilities.on_off","instance":"on","retrievable":true,"reportable":true,"params":{},"set":"lamp/new/set","state":"lamp/new/state"}],"properties":[]}`
	up := postJSON(t, srv.URL+"/app/devices/Lamp", payload)
	if up.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", up.StatusCode)
	}
	if reloader.calls <= before {
		t.Fatal("update must trigger reload")
	}

	d, _, _, _ := catalog.GetDevice(ctx, "1", "Lamp")
	if d.Name != "Лампа 2" {
		t.Fatalf("name not updated: %q", d.Name)
	}
	if len(d.MQTT.Capabilities) != 1 || d.MQTT.Capabilities[0].Set != "lamp/new/set" {
		t.Fatalf("topic not updated: %+v", d.MQTT.Capabilities)
	}
}

// Updating a device you don't own must not succeed.
func TestUpdateForeignDeviceRejected(t *testing.T) {
	srv, _, catalog, _ := setup(t)
	// A device owned by another user.
	foreign := config.Device{
		ID: "foreign-dev", Name: "Original", Type: "devices.types.light",
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off"}},
	}
	if err := catalog.SaveDevice(context.Background(), "2", nil, foreign); err != nil {
		t.Fatal(err)
	}
	payload := `{"name":"Hijack","type":"devices.types.light","capabilities":[
      {"type":"devices.capabilities.on_off","instance":"on","params":{},"state":"x"}],"properties":[]}`
	resp := postJSON(t, srv.URL+"/app/devices/foreign-dev", payload)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign update = %d, want 404", resp.StatusCode)
	}
	// Ensure it stayed as user 2's device with the original name.
	d, _, ok, _ := catalog.GetDevice(context.Background(), "2", "foreign-dev")
	if !ok || d.Name == "Hijack" {
		t.Fatalf("foreign device was modified: %+v", d)
	}
}
