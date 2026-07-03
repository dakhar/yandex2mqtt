package device

import (
	"context"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

type fakeLoader struct{ devices []config.Device }

func (f *fakeLoader) LoadAll(context.Context) ([]config.Device, error) { return f.devices, nil }

type fakeBridge struct {
	resyncs  int
	lastN    int
	publishC int
}

func (b *fakeBridge) Resync(devices []*Device)      { b.resyncs++; b.lastN = len(devices) }
func (b *fakeBridge) Publish(topic, payload string) { b.publishC++ }

func dev(id, user string) config.Device {
	return config.Device{
		ID: id, Type: "devices.types.light", AllowedUsers: []string{user},
		MQTT:         config.MQTTMapping{Capabilities: []config.MQTTTopic{{Instance: "on", Set: id + "/set", State: id + "/state"}}},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
	}
}

func TestManagerReloadAndSwap(t *testing.T) {
	loader := &fakeLoader{devices: []config.Device{dev("A", "1"), dev("B", "2")}}
	bridge := &fakeBridge{}
	m := NewManager(loader, bridge, nil)

	// Before reload: empty.
	if len(m.All()) != 0 {
		t.Fatalf("expected empty registry before reload")
	}

	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.All()) != 2 || bridge.resyncs != 1 || bridge.lastN != 2 {
		t.Fatalf("after reload: all=%d resyncs=%d lastN=%d", len(m.All()), bridge.resyncs, bridge.lastN)
	}
	if _, ok := m.ByID("A"); !ok {
		t.Fatalf("device A not found")
	}
	// Per-user scoping.
	if u1 := m.ForUser("1"); len(u1) != 1 || u1[0].ID != "A" {
		t.Fatalf("ForUser(1) = %+v, want [A]", u1)
	}
	if u2 := m.ForUser("2"); len(u2) != 1 || u2[0].ID != "B" {
		t.Fatalf("ForUser(2) = %+v, want [B]", u2)
	}

	// Reload with a changed catalog swaps the snapshot and re-syncs.
	loader.devices = []config.Device{dev("C", "1")}
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.All()) != 1 || bridge.resyncs != 2 {
		t.Fatalf("after 2nd reload: all=%d resyncs=%d", len(m.All()), bridge.resyncs)
	}
	if _, ok := m.ByID("A"); ok {
		t.Fatalf("device A should be gone after reload")
	}

	// Publisher was wired: an action publishes via the bridge.
	d, _ := m.ByID("C")
	d.SetCapabilityState(true, "devices.capabilities.on_off", "on", false)
	if bridge.publishC == 0 {
		t.Fatalf("expected publish through wired bridge")
	}
}
