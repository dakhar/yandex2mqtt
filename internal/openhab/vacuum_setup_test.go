package openhab

import (
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

func TestInferVacuums(t *testing.T) {
	gm := func(parent string) []string { return []string{parent} }
	items := []ohItem{
		{Name: "r_Home", Type: "Group", Label: "Дом", Tags: []string{"House"}},
		{Name: "VacuumCleaner", Type: "Group", Label: "Робот-пылесос", Tags: []string{"CleaningRobot"}, GroupNames: gm("r_Home")},
		{Name: "VacuumCleaner_Mapsegments", Type: "String", GroupNames: gm("VacuumCleaner"),
			State: `{"1":"Alex","2":"Kitchen","10":"Hall"}`, Meta: map[string]ohMeta{"yahome": {Value: "vac_segments"}}},
		{Name: "VacuumCleaner_Cleansegments", Type: "String", GroupNames: gm("VacuumCleaner"),
			Meta: map[string]ohMeta{"yahome": {Value: "vac_queue"}}},
		{Name: "VacuumCleaner_Operation", Type: "String", Tags: []string{"Control"}, GroupNames: gm("VacuumCleaner"),
			Meta: map[string]ohMeta{"yahome": {Value: "vac_operation"}}},
		{Name: "VacuumCleaner_CameraHlsUrl", Type: "String", GroupNames: gm("VacuumCleaner")},
		// Nested battery sub-equipment: its StateOfCharge point must surface on the parent.
		{Name: "VacuumCleaner_Battery", Type: "Group", Tags: []string{"Battery"}, GroupNames: gm("VacuumCleaner")},
		{Name: "VacuumCleaner_Battery_Level", Type: "Number", Tags: []string{"Measurement", "StateOfCharge"}, GroupNames: gm("VacuumCleaner_Battery")},
		// Segment aggregation group (Group:Switch) must NOT add a parent on_off.
		{Name: "gVacuum_Segments", Type: "Group", GroupType: "Switch", GroupNames: gm("VacuumCleaner")},
		{Name: "VacuumCleaner_Status", Type: "String", Tags: []string{"Status"}, GroupNames: gm("VacuumCleaner"),
			Meta: map[string]ohMeta{"yahome": {Value: "vac_state"}}},
	}
	setups := inferVacuums(items)
	if len(setups) != 1 {
		t.Fatalf("got %d setups", len(setups))
	}
	s := setups[0]
	if s.Item != "VacuumCleaner" || s.CleanTarget != "VacuumCleaner_Cleansegments" || s.OpTarget != "VacuumCleaner_Operation" {
		t.Fatalf("targets: %+v", s)
	}
	// Segments ordered by numeric id: 1, 2, 10.
	if len(s.Segments) != 3 || s.Segments[0].ID != "1" || s.Segments[2].ID != "10" {
		t.Fatalf("segments: %+v", s.Segments)
	}
	if s.Segments[0].Name != "Alex" {
		t.Fatalf("segment name: %+v", s.Segments[0])
	}
	// Parent gets whole-house on_off + pause + video_stream from the camera.
	has := map[string]bool{}
	for _, c := range s.Parent.Capabilities {
		has[c.Type] = true
	}
	if !has["devices.capabilities.on_off"] || !has["devices.capabilities.toggle"] || !has["devices.capabilities.video_stream"] {
		t.Fatalf("parent caps: %+v", s.Parent.Capabilities)
	}
	// Only one on_off (whole-house -> Operation), not one from the segment group.
	onoff := 0
	for _, c := range s.Parent.Capabilities {
		if c.Type == "devices.capabilities.on_off" {
			onoff++
		}
	}
	if onoff != 1 {
		t.Fatalf("expected exactly one on_off, got %d", onoff)
	}
	// Battery level from the nested Battery sub-equipment.
	battery := false
	for _, p := range s.Parent.Properties {
		if p.Parameters["instance"] == "battery_level" {
			battery = true
		}
	}
	if !battery {
		t.Fatalf("parent missing battery_level: %+v", s.Parent.Properties)
	}
	// on_off state comes from the Status item (command is group-routed).
	if s.StatusItem != "VacuumCleaner_Status" {
		t.Fatalf("StatusItem = %q", s.StatusItem)
	}
	onState := ""
	for _, b := range s.Parent.OpenHAB {
		if b.Kind == "cap" && b.Instance == "on" {
			onState = b.Item
		}
	}
	if onState != "VacuumCleaner_Status" {
		t.Fatalf("on_off state should bind to Status, got %q", onState)
	}
	// The parent's room comes from its openHAB Location ancestor.
	if s.Parent.Room != "Дом" {
		t.Fatalf("parent room = %q, want Дом (from r_Home Location)", s.Parent.Room)
	}
	if errs, _ := device.ValidateCatalog([]config.Device{s.Parent}); len(errs) > 0 {
		t.Fatalf("parent invalid: %v", errs)
	}
	if s.Parent.OpenHAB[0].Kind != "equipment" || s.Parent.OpenHAB[0].Item != "VacuumCleaner" {
		t.Fatalf("parent identity binding: %+v", s.Parent.OpenHAB)
	}
}

// A vacuum without a segment list (no Mapsegments) is not a segment setup.
func TestInferVacuumsSkipsNonSegment(t *testing.T) {
	items := []ohItem{
		{Name: "Vac", Type: "Group", Tags: []string{"CleaningRobot"}},
		{Name: "Vac_Fanspeed", Type: "String", Tags: []string{"Speed", "Setpoint"}, GroupNames: []string{"Vac"}},
	}
	if got := inferVacuums(items); len(got) != 0 {
		t.Fatalf("expected no segment setups, got %+v", got)
	}
}
