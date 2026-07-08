package device

import (
	"sync"
	"testing"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// capture collects published (target,payload) pairs from the group.
type capture struct {
	mu   sync.Mutex
	msgs [][2]string
}

func (c *capture) publish(target, payload string) {
	c.mu.Lock()
	c.msgs = append(c.msgs, [2]string{target, payload})
	c.mu.Unlock()
}

func (c *capture) last() ([2]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		return [2]string{}, false
	}
	return c.msgs[len(c.msgs)-1], true
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

// Rooms toggled within the debounce window collapse into one union command.
func TestVacuumGroupBatchesUnion(t *testing.T) {
	cap := &capture{}
	g := NewVacuumGroup("Clean", "Op", "HOME", 40*time.Millisecond)
	g.SetPublisher(cap.publish)

	g.SetRoom("2", true)
	time.Sleep(15 * time.Millisecond)
	g.SetRoom("1", true) // arrives within the window -> re-arms, batches
	time.Sleep(15 * time.Millisecond)
	g.SetRoom("3", true)

	if cap.count() != 0 {
		t.Fatalf("dispatched before debounce settled: %v", cap.msgs)
	}
	time.Sleep(80 * time.Millisecond)

	msg, ok := cap.last()
	if !ok || msg[0] != "Clean" {
		t.Fatalf("no clean command: %v", cap.msgs)
	}
	if msg[1] != `{"segment_ids":["1","2","3"]}` {
		t.Fatalf("union payload = %q", msg[1])
	}
	if cap.count() != 1 {
		t.Fatalf("expected one dispatch, got %d: %v", cap.count(), cap.msgs)
	}
}

// Turning a room off after a job started stops the robot (home).
func TestVacuumGroupOffStopsAfterDispatch(t *testing.T) {
	cap := &capture{}
	g := NewVacuumGroup("Clean", "Op", "HOME", 20*time.Millisecond)
	g.SetPublisher(cap.publish)

	g.SetRoom("1", true)
	time.Sleep(50 * time.Millisecond) // dispatch {1}
	if n := cap.count(); n != 1 {
		t.Fatalf("expected clean dispatch, got %d", n)
	}
	g.SetRoom("1", false) // job running -> HOME
	msg, _ := cap.last()
	if msg[0] != "Op" || msg[1] != "HOME" {
		t.Fatalf("off after dispatch should send HOME, got %v", msg)
	}
}

// Turning a pending room off before dispatch just cancels it (no command).
func TestVacuumGroupOffBeforeDispatchCancels(t *testing.T) {
	cap := &capture{}
	g := NewVacuumGroup("Clean", "Op", "HOME", 30*time.Millisecond)
	g.SetPublisher(cap.publish)

	g.SetRoom("1", true)
	g.SetRoom("1", false) // cancels the only pending room
	time.Sleep(60 * time.Millisecond)
	if cap.count() != 0 {
		t.Fatalf("expected no command, got %v", cap.msgs)
	}
}

// A vacuum-zone device routes on_off to the group instead of publishing.
func TestDeviceZoneOnOffRoutesToGroup(t *testing.T) {
	cap := &capture{}
	g := NewVacuumGroup("Clean", "Op", "HOME", 20*time.Millisecond)
	g.SetPublisher(cap.publish)

	d := New(config.Device{
		ID: "Z", Type: "devices.types.vacuum_cleaner",
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
	}, func(string, string) { t.Fatal("zone on_off must not publish directly") }, nil)
	d.SetVacuumGroup(g, "5")

	res := d.SetCapabilityState(true, "devices.capabilities.on_off", "on", false)
	if res.State.ActionResult.Status != "DONE" {
		t.Fatalf("status=%q", res.State.ActionResult.Status)
	}
	time.Sleep(50 * time.Millisecond)
	msg, ok := cap.last()
	if !ok || msg[0] != "Clean" || msg[1] != `{"segment_ids":["5"]}` {
		t.Fatalf("zone on_off did not reach group: %v", cap.msgs)
	}
}
