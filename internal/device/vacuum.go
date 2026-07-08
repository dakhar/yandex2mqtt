package device

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultVacuumDebounce is how long the group waits for room toggling to settle
// before dispatching the combined segment-clean command.
const defaultVacuumDebounce = 3 * time.Second

// VacuumGroup batches per-room on/off actions for one robot into a single
// segment-cleaning command. It exists because Yandex sends each room's on_off
// independently, while the robot wants one segment list: turning rooms on
// accumulates their segment ids, and once the toggling settles (debounce) the
// union is published to the clean target. Turning a room off after a job has
// started stops the robot (home). Cross-session queueing (adding a room to an
// already-running job) is left to the robot firmware — the group only batches
// simultaneous toggles. Safe for concurrent use.
type VacuumGroup struct {
	cleanTarget string // segment-clean command target (e.g. Cleansegments)
	opTarget    string // operation target (e.g. Operation) for stop/home
	homeCmd     string // payload sent to opTarget to stop/return (e.g. "HOME")
	debounce    time.Duration

	mu       sync.Mutex
	publish  PublishFunc
	active   map[string]bool
	timer    *time.Timer
	cleaning bool // a segment job has been dispatched and not yet stopped
}

// NewVacuumGroup builds a group publishing to cleanTarget (segment list) and
// opTarget (stop/home). debounce<=0 uses the default; homeCmd defaults to "HOME".
func NewVacuumGroup(cleanTarget, opTarget, homeCmd string, debounce time.Duration) *VacuumGroup {
	if debounce <= 0 {
		debounce = defaultVacuumDebounce
	}
	if homeCmd == "" {
		homeCmd = "HOME"
	}
	return &VacuumGroup{
		cleanTarget: cleanTarget, opTarget: opTarget, homeCmd: homeCmd,
		debounce: debounce, active: map[string]bool{},
	}
}

// SetPublisher wires the command publisher (same one the devices use).
func (g *VacuumGroup) SetPublisher(p PublishFunc) {
	g.mu.Lock()
	g.publish = p
	g.mu.Unlock()
}

// SetRoom records a room's on/off. Turning on accumulates its segment and
// (re)arms the debounce; turning off cancels a pending room or, if a job is
// already running, stops the robot.
func (g *VacuumGroup) SetRoom(segmentID string, on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if on {
		g.active[segmentID] = true
		g.arm()
		return
	}
	delete(g.active, segmentID)
	if g.cleaning {
		g.stopTimer()
		g.active = map[string]bool{}
		g.cleaning = false
		if g.opTarget != "" && g.publish != nil {
			g.publish(g.opTarget, g.homeCmd)
		}
		return
	}
	if len(g.active) == 0 {
		g.stopTimer()
		return
	}
	g.arm()
}

// Stop cancels any pending dispatch (used on registry reload). Caller must not
// hold the lock.
func (g *VacuumGroup) Stop() {
	g.mu.Lock()
	g.stopTimer()
	g.mu.Unlock()
}

// arm (re)starts the debounce timer. The caller holds the lock.
func (g *VacuumGroup) arm() {
	g.stopTimer()
	g.timer = time.AfterFunc(g.debounce, g.dispatch)
}

// stopTimer cancels the pending timer. The caller holds the lock.
func (g *VacuumGroup) stopTimer() {
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// dispatch publishes the union of currently active segments as one clean command
// and resets the batch (the next toggle burst starts a fresh union).
func (g *VacuumGroup) dispatch() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timer = nil
	if len(g.active) == 0 {
		return
	}
	ids := make([]string, 0, len(g.active))
	for id := range g.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	g.active = map[string]bool{}
	g.cleaning = true
	if g.cleanTarget != "" && g.publish != nil {
		g.publish(g.cleanTarget, segmentsPayload(ids))
	}
}

// segmentsPayload renders the robot's clean command, e.g. {"segment_ids":["1","2"]}.
func segmentsPayload(ids []string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = `"` + id + `"`
	}
	return `{"segment_ids":[` + strings.Join(quoted, ",") + `]}`
}
