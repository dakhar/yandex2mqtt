package openhab

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// A robot vacuum's per-room cleaning is modelled as one parent device plus a
// device per map segment. Rather than requiring a per-zone item + group + rules
// in openHAB, the setup is driven by the robot's own primitives: a Mapsegments
// item (JSON {segment_id: name}) lists the segments, an Operation item takes
// START/STOP/PAUSE/HOME, and a Cleansegments item takes {"segment_ids":[...]}.
// The user assigns a room to each segment in the UI; the bridge then aggregates
// per-room on/off into segment-clean commands (see device.VacuumGroup).

// VacuumSetup describes a discoverable segment-vacuum: the parent composite
// device plus the segments awaiting a room assignment.
type VacuumSetup struct {
	Item        string          // parent equipment item (stable identity)
	Name        string          // parent label
	CleanTarget string          // Cleansegments item (segment_ids command)
	OpTarget    string          // Operation item (START/STOP/PAUSE/HOME)
	Parent      config.Device   // parent composite draft (caller sets Room)
	Segments    []VacuumSegment // segment id + name, ordered by numeric id
}

// VacuumSegment is one map segment reported by the robot.
type VacuumSegment struct {
	ID   string
	Name string
}

// VacuumSetups discovers segment-driven robot vacuums from the openHAB model.
func (c *Connector) VacuumSetups(ctx context.Context) ([]VacuumSetup, error) {
	if !c.configured() {
		return nil, fmt.Errorf("openHAB не настроен")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var items []ohItem
	if err := c.getJSON(ctx, "/rest/items?metadata=yahome,semantics&fields=name,type,label,state,tags,groupNames,metadata,stateDescription", &items); err != nil {
		return nil, err
	}
	return inferVacuums(items), nil
}

// inferVacuums finds CleaningRobot equipment groups that expose a segment list
// (Mapsegments) and a segment-clean command (Cleansegments), building a parent
// draft plus the segment list for each.
func inferVacuums(items []ohItem) []VacuumSetup {
	byName := make(map[string]ohItem, len(items))
	children := map[string][]ohItem{}
	locName := map[string]string{}
	for _, it := range items {
		byName[it.Name] = it
		for _, g := range it.GroupNames {
			children[g] = append(children[g], it)
		}
		if it.Type == "Group" && isLocation(it.Tags) {
			locName[it.Name] = itemLabel(it)
		}
	}

	var out []VacuumSetup
	for _, it := range items {
		if it.Type != "Group" || equipmentType(it.Tags) != "devices.types.vacuum_cleaner" {
			continue
		}
		var mapItem, cleanItem, opItem string
		for _, m := range children[it.Name] {
			switch {
			case strings.HasSuffix(m.Name, "Mapsegments"):
				mapItem = m.Name
			case strings.HasSuffix(m.Name, "Cleansegments"):
				cleanItem = m.Name
			case strings.HasSuffix(m.Name, "Operation") && !strings.HasSuffix(m.Name, "Operation_Mode"):
				opItem = m.Name
			}
		}
		if mapItem == "" || cleanItem == "" {
			continue // not a segment-driven vacuum
		}
		segs := parseSegments(byName[mapItem].State)
		if len(segs) == 0 {
			continue
		}
		// Flatten the equipment subtree into the parent's members: descend through
		// plain sub-equipment (e.g. the Battery group -> its StateOfCharge point),
		// but skip the segment aggregation group (its zones are separate devices,
		// and the parent's on/off is the whole-house Operation) and Locations.
		var parentMembers []ohItem
		seen := map[string]bool{}
		var walk func(name string)
		walk = func(name string) {
			for _, m := range children[name] {
				if seen[m.Name] {
					continue
				}
				seen[m.Name] = true
				if m.Type == "Group" && (m.GroupType != "" || isLocation(m.Tags)) {
					continue // aggregation (segments) or location: not a parent feature
				}
				parentMembers = append(parentMembers, m)
				if m.Type == "Group" {
					walk(m.Name) // descend into plain sub-equipment (Battery, ...)
				}
			}
		}
		walk(it.Name)

		parent, _ := draftForGroup(it, parentMembers)
		parent.Type = "devices.types.vacuum_cleaner"
		parent.Room = resolveRoom(it, byName, locName) // its Location ancestor
		if opItem != "" {
			addWholeHouseControls(&parent, opItem)
		}
		out = append(out, VacuumSetup{
			Item: it.Name, Name: itemLabel(it),
			CleanTarget: cleanItem, OpTarget: opItem,
			Parent: parent, Segments: segs,
		})
	}
	return out
}

// parseSegments parses a Mapsegments JSON object {"1":"Alex",...} into segments
// ordered by numeric id.
func parseSegments(state string) []VacuumSegment {
	if strings.TrimSpace(state) == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(state), &m); err != nil {
		return nil
	}
	segs := make([]VacuumSegment, 0, len(m))
	for id, name := range m {
		segs = append(segs, VacuumSegment{ID: id, Name: name})
	}
	sort.Slice(segs, func(i, j int) bool {
		ai, ei := strconv.Atoi(segs[i].ID)
		aj, ej := strconv.Atoi(segs[j].ID)
		if ei == nil && ej == nil {
			return ai < aj
		}
		return segs[i].ID < segs[j].ID
	})
	return segs
}

// addWholeHouseControls gives the parent an on_off (START/HOME) and a pause
// toggle (PAUSE/START), both bound to the Operation item via value mappings.
func addWholeHouseControls(d *config.Device, opItem string) {
	d.Capabilities = append(d.Capabilities,
		config.Capability{Type: "devices.capabilities.on_off", Retrievable: false, Reportable: false,
			Parameters: map[string]any{}},
		config.Capability{Type: "devices.capabilities.toggle", Retrievable: false, Reportable: false,
			Parameters: map[string]any{"instance": "pause"}},
	)
	d.OpenHAB = append(d.OpenHAB,
		config.OpenHABBinding{Kind: "cap", Instance: "on", Item: opItem},
		config.OpenHABBinding{Kind: "cap", Instance: "pause", Item: opItem},
	)
	d.ValueMapping = append(d.ValueMapping,
		config.ValueMapping{Type: "on_off", Mapping: []config.InstanceMapping{
			{Instance: "on", Mapping: [][]any{{true, false}, {"START", "HOME"}}},
		}},
		config.ValueMapping{Type: "toggle", Mapping: []config.InstanceMapping{
			{Instance: "pause", Mapping: [][]any{{true, false}, {"PAUSE", "START"}}},
		}},
	)
}
