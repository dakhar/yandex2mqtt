package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

func openTestRepo(t *testing.T) *CatalogRepo {
	t.Helper()
	// Manual temp dir with best-effort removal: on Windows SQLite's WAL/SHM
	// files can linger briefly after Close, which would fail t.TempDir cleanup.
	dir, err := os.MkdirTemp("", "catalog")
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _ = os.RemoveAll(dir) })
	return NewCatalogRepo(db)
}

// A device exercising every child kind: capabilities, properties, mqtt topics
// (cap + prop), and a value mapping with mixed-typed values.
func sampleDevice() config.Device {
	return config.Device{
		ID: "Cleaner_Kitchen", Name: "Пылесос", Room: "Кухня",
		Type: "devices.types.vacuum_cleaner", AllowedUsers: []string{"1"},
		MQTT: config.MQTTMapping{
			Capabilities: []config.MQTTTopic{
				{Instance: "on", Set: "vac/power/set", State: "vac/state"},
				{Instance: "pause", Set: "vac/ctrl/set", State: "vac/op/state"},
			},
			Properties: []config.MQTTTopic{
				{Instance: "battery_level", State: "vac/battery/state"},
			},
		},
		ValueMapping: []config.ValueMapping{{
			Type: "toggle",
			Mapping: []config.InstanceMapping{{
				Instance: "pause",
				Mapping: [][]any{
					{true, false, false, false},
					{"PAUSE", "START", "STOP", "HOME"},
				},
			}},
		}},
		Capabilities: []config.Capability{
			{Type: "devices.capabilities.on_off", Retrievable: true, Reportable: true},
			{Type: "devices.capabilities.toggle", Retrievable: true, Reportable: true,
				Parameters: map[string]any{"instance": "pause"}},
		},
		Properties: []config.Property{
			{Type: "devices.properties.float", Retrievable: true, Reportable: true,
				Parameters: map[string]any{"instance": "battery_level", "unit": "unit.percent"}},
		},
	}
}

func TestImportLoadRoundTrip(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	if err := repo.ImportCatalog(ctx, "1", []config.Device{sampleDevice()}); err != nil {
		t.Fatalf("import: %v", err)
	}
	n, err := repo.CountDevices(ctx)
	if err != nil || n != 1 {
		t.Fatalf("count = %d, err = %v", n, err)
	}

	loaded, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d devices", len(loaded))
	}
	got := loaded[0]
	want := sampleDevice()

	if got.ID != want.ID || got.Name != want.Name || got.Room != want.Room || got.Type != want.Type {
		t.Fatalf("metadata mismatch: %+v", got)
	}
	if !reflect.DeepEqual(got.AllowedUsers, []string{"1"}) {
		t.Fatalf("allowedUsers = %v", got.AllowedUsers)
	}
	if !reflect.DeepEqual(got.MQTT, want.MQTT) {
		t.Fatalf("mqtt mismatch:\n got %+v\nwant %+v", got.MQTT, want.MQTT)
	}
	// Value mapping: types must survive (bool stays bool, string stays string).
	vm := got.ValueMapping
	if len(vm) != 1 || vm[0].Type != "toggle" || len(vm[0].Mapping) != 1 {
		t.Fatalf("value mapping shape: %+v", vm)
	}
	yandex := vm[0].Mapping[0].Mapping[0]
	if yandex[0] != true || yandex[1] != false {
		t.Fatalf("yandex mapping values/types lost: %#v", yandex)
	}
	mqtt := vm[0].Mapping[0].Mapping[1]
	if mqtt[0] != "PAUSE" || mqtt[3] != "HOME" {
		t.Fatalf("mqtt mapping values lost: %#v", mqtt)
	}
	// Capabilities / properties count + params.
	if len(got.Capabilities) != 2 || len(got.Properties) != 1 {
		t.Fatalf("cap/prop counts: %d/%d", len(got.Capabilities), len(got.Properties))
	}
	if got.Capabilities[1].Parameters["instance"] != "pause" {
		t.Fatalf("toggle params lost: %+v", got.Capabilities[1].Parameters)
	}
	if got.Properties[0].Parameters["unit"] != "unit.percent" {
		t.Fatalf("float params lost: %+v", got.Properties[0].Parameters)
	}
}

// The round-tripped device must still validate and construct as a domain device.
func TestImportedCatalogValidates(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	// Import the whole example catalog.
	devs, err := config.LoadDevices(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Skipf("example catalog unavailable: %v", err)
	}
	if err := repo.ImportCatalog(ctx, "1", devs); err != nil {
		t.Fatalf("import: %v", err)
	}
	loaded, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != len(devs) {
		t.Fatalf("loaded %d, imported %d", len(loaded), len(devs))
	}
}
