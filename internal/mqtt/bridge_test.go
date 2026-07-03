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

func TestDispatchRoutesToDevice(t *testing.T) {
	devs := testDevices()
	b := New(config.MQTT{Host: "localhost", Port: 1883}, devs,
		slog.New(slog.NewTextHandler(discardW{}, nil)), nil)

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
	devs := testDevices()
	var hookCalls int
	b := New(config.MQTT{Host: "localhost", Port: 1883}, devs,
		slog.New(slog.NewTextHandler(discardW{}, nil)),
		func(*device.Device, string, bool) { hookCalls++ })

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
	b := New(config.MQTT{Host: "localhost", Port: 1883}, testDevices(),
		slog.New(slog.NewTextHandler(discardW{}, nil)), nil)

	// Unique subscribe filters: vac/state + shared/battery/state = 2.
	if len(b.filters) != 2 {
		t.Fatalf("filters = %d, want 2: %v", len(b.filters), b.filters)
	}
	// The shared topic has two subscriptions.
	if got := len(b.subs["shared/battery/state"]); got != 2 {
		t.Fatalf("shared topic subs = %d, want 2", got)
	}
}

type discardW struct{}

func (discardW) Write(p []byte) (int, error) { return len(p), nil }
