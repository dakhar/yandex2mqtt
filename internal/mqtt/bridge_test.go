package mqtt

import (
	"log/slog"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

func testDevices() []*device.Device {
	vac := device.New(config.Device{
		ID:   "Vac",
		Type: "devices.types.vacuum_cleaner",
		MQTT: config.MQTTMapping{
			Capabilities: []config.MQTTTopic{
				{Instance: "on", Set: "vac/power/set", State: "vac/state"},
			},
			Properties: []config.MQTTTopic{
				{Instance: "battery_level", State: "shared/battery/state"},
			},
		},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
		Properties: []config.Property{{
			Type: "devices.properties.float", Retrievable: true,
			Parameters: map[string]any{"instance": "battery_level", "unit": "unit.percent"},
		}},
	}, nil, nil)

	// A second device sharing the battery state topic (as in the real catalog).
	vac2 := device.New(config.Device{
		ID:   "Vac2",
		Type: "devices.types.vacuum_cleaner",
		MQTT: config.MQTTMapping{
			Properties: []config.MQTTTopic{
				{Instance: "battery_level", State: "shared/battery/state"},
			},
		},
		Properties: []config.Property{{
			Type: "devices.properties.float", Retrievable: true,
			Parameters: map[string]any{"instance": "battery_level", "unit": "unit.percent"},
		}},
	}, nil, nil)

	return []*device.Device{vac, vac2}
}

// newBridge builds a bridge and loads devices via Resync (offline).
func newBridge(devs []*device.Device, onUpdate UpdateHook) *Bridge {
	b := New(config.MQTT{Host: "localhost", Port: 1883},
		slog.New(slog.NewTextHandler(discardW{}, nil)), onUpdate)
	b.Resync(devs)
	return b
}

func TestDispatchRoutesToDevice(t *testing.T) {
	devs := testDevices()
	b := newBridge(devs, nil)

	// on/off state update
	b.dispatch("vac/state", "ON")
	got := devs[0].QueryState()
	if len(got.Capabilities) == 0 || got.Capabilities[0].State.Value != true {
		t.Fatalf("on_off not updated: %+v", got.Capabilities)
	}

	// shared battery topic must update BOTH devices
	b.dispatch("shared/battery/state", "88")
	for _, d := range devs {
		q := d.QueryState()
		if len(q.Properties) == 0 || q.Properties[0].State.Value != 88.0 {
			t.Fatalf("battery not updated on %s: %+v", d.ID, q.Properties)
		}
	}
}

func TestDispatchCaseInsensitiveAndUnknown(t *testing.T) {
	var hookCalls int
	b := newBridge(testDevices(), func(*device.Device, string, bool) { hookCalls++ })

	// Different case still routes (bridge lowercases keys).
	b.dispatch("VAC/STATE", "ON")
	if hookCalls != 1 {
		t.Fatalf("update hook calls = %d, want 1", hookCalls)
	}
	// Unknown topic is a no-op.
	b.dispatch("nope/state", "x")
	if hookCalls != 1 {
		t.Fatalf("unknown topic must not trigger hook, calls = %d", hookCalls)
	}
}

func TestSubscriptionTablesBuilt(t *testing.T) {
	b := newBridge(testDevices(), nil)

	// Unique subscribe filters: vac/state + shared/battery/state = 2.
	if len(b.filters) != 2 {
		t.Fatalf("filters = %d, want 2: %v", len(b.filters), b.filters)
	}
	// The shared topic has two subscriptions.
	if got := len(b.subs["shared/battery/state"]); got != 2 {
		t.Fatalf("shared topic subs = %d, want 2", got)
	}
}

// Resync (offline) must swap the device set and subscription tables, and route
// to the new devices.
func TestResyncSwapsDevices(t *testing.T) {
	b := newBridge(testDevices(), nil) // Vac + Vac2, 2 filters

	// Replace with a single new device on a different topic.
	newDev := device.New(config.Device{
		ID: "Lamp", Type: "devices.types.light",
		MQTT:         config.MQTTMapping{Capabilities: []config.MQTTTopic{{Instance: "on", Set: "lamp/set", State: "lamp/state"}}},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
	}, nil, nil)
	b.Resync([]*device.Device{newDev})

	if len(b.filters) != 1 || b.filters["lamp/state"] != 0 {
		t.Fatalf("filters after resync = %v, want {lamp/state}", b.filters)
	}
	if _, ok := b.subs["shared/battery/state"]; ok {
		t.Fatalf("old subscription not cleared after resync")
	}
	// Routing now hits the new device.
	b.dispatch("lamp/state", "ON")
	if q := newDev.QueryState(); len(q.Capabilities) == 0 || q.Capabilities[0].State.Value != true {
		t.Fatalf("new device not updated after resync: %+v", q.Capabilities)
	}
}

type discardW struct{}

func (discardW) Write(p []byte) (int, error) { return len(p), nil }
