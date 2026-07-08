package device

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

type fakeLoader struct{ devices []config.Device }

func (f *fakeLoader) LoadAll(context.Context) ([]config.Device, error) { return f.devices, nil }

type fakeBridge struct {
	resyncs  int
	lastN    int
	publishC int
}

func (b *fakeBridge) Transport() string              { return "mqtt" }
func (b *fakeBridge) Resync(devices []*Device)       { b.resyncs++; b.lastN = len(devices) }
func (b *fakeBridge) Publish(target, payload string) { b.publishC++ }

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
	m := NewManager(loader, map[string]Connector{"mqtt": bridge}, nil)

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

// capturingBridge records published (target,payload) pairs.
type capturingBridge struct {
	mu   sync.Mutex
	msgs [][2]string
}

func (b *capturingBridge) Transport() string        { return "mqtt" }
func (b *capturingBridge) Resync(devices []*Device) {}
func (b *capturingBridge) Publish(target, p string) {
	b.mu.Lock()
	b.msgs = append(b.msgs, [2]string{target, p})
	b.mu.Unlock()
}
func (b *capturingBridge) find(target string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range b.msgs {
		if m[0] == target {
			return m[1], true
		}
	}
	return "", false
}

// Two zone devices with the same group share one aggregator: turning both on
// dispatches a single union to the clean target, wired via the connector.
func TestManagerWiresVacuumGroup(t *testing.T) {
	zone := func(id, seg string) config.Device {
		d := dev(id, "1")
		d.Type = "devices.types.vacuum_cleaner"
		d.Vacuum = &config.VacuumZone{
			GroupID: "robot", SegmentID: seg, CleanTarget: "Clean", OpTarget: "Op",
			HomeCmd: "HOME", DebounceMs: 30,
		}
		return d
	}
	bridge := &capturingBridge{}
	m := NewManager(&fakeLoader{devices: []config.Device{zone("A", "1"), zone("B", "2")}},
		map[string]Connector{"mqtt": bridge}, nil)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	a, _ := m.ByID("A")
	b, _ := m.ByID("B")
	a.SetCapabilityState(true, "devices.capabilities.on_off", "on", false)
	b.SetCapabilityState(true, "devices.capabilities.on_off", "on", false)
	time.Sleep(80 * time.Millisecond)

	got, ok := bridge.find("Clean")
	if !ok {
		t.Fatalf("no clean command published: %v", bridge.msgs)
	}
	if got != `{"segment_ids":["1","2"]}` {
		t.Fatalf("union payload = %q", got)
	}
}

// Devices are routed to the connector named by their Transport; an unknown
// transport leaves the device in the registry but unwired.
func TestManagerRoutesByTransport(t *testing.T) {
	a := dev("A", "1") // default transport -> mqtt
	b := dev("B", "1")
	b.Transport = "openhab"
	b.OpenHAB = []config.OpenHABBinding{{Kind: "cap", Instance: "on", Item: "B_Switch"}}
	c := dev("C", "1")
	c.Transport = "zigbee" // no connector registered

	mq := &fakeBridge{}
	oh := &fakeBridge{}
	m := NewManager(&fakeLoader{devices: []config.Device{a, b, c}},
		map[string]Connector{"mqtt": mq, "openhab": oh}, nil)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if mq.lastN != 1 || oh.lastN != 1 {
		t.Fatalf("routing: mqtt=%d openhab=%d, want 1/1", mq.lastN, oh.lastN)
	}
	// All three are queryable regardless of connector.
	if len(m.All()) != 3 {
		t.Fatalf("registry has %d devices, want 3", len(m.All()))
	}

	// A publishes via the mqtt connector, B via openhab.
	da, _ := m.ByID("A")
	da.SetCapabilityState(true, "devices.capabilities.on_off", "on", false)
	db, _ := m.ByID("B")
	db.SetCapabilityState(true, "devices.capabilities.on_off", "on", false)
	if mq.publishC != 1 || oh.publishC != 1 {
		t.Fatalf("publish routing: mqtt=%d openhab=%d, want 1/1", mq.publishC, oh.publishC)
	}

	// C has no connector: publishing is a no-op (no panic, no wrong connector).
	dc, _ := m.ByID("C")
	dc.SetCapabilityState(true, "devices.capabilities.on_off", "on", false)
	if mq.publishC != 1 || oh.publishC != 1 {
		t.Fatalf("unwired device must not publish anywhere")
	}
}
