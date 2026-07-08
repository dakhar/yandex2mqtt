package openhab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

func TestDraftForItemInference(t *testing.T) {
	tests := []struct {
		item     ohItem
		wantType string
		caps     int
		props    int
		wantInst string // an expected binding instance
	}{
		{ohItem{Name: "Sw", Type: "Switch", Tags: []string{"Light"}}, "devices.types.light", 1, 0, "on"},
		{ohItem{Name: "Dim", Type: "Dimmer"}, "devices.types.light", 2, 0, "brightness"},
		{ohItem{Name: "Col", Type: "Color"}, "devices.types.light", 3, 0, "hsv"},
		{ohItem{Name: "Roll", Type: "Rollershutter"}, "devices.types.openable.curtain", 2, 0, "open"},
		{ohItem{Name: "T", Type: "Number:Temperature"}, "devices.types.sensor.climate", 0, 1, "temperature"},
		{ohItem{Name: "H", Type: "Number", Tags: []string{"Humidity"}}, "devices.types.sensor.climate", 0, 1, "humidity"},
		{ohItem{Name: "C", Type: "Contact"}, "devices.types.sensor.open", 0, 1, "open"},
	}
	for _, tt := range tests {
		d, ok := draftForItem(tt.item)
		if !ok {
			t.Fatalf("%s: not inferred", tt.item.Name)
		}
		if d.Type != tt.wantType {
			t.Fatalf("%s: type=%q want %q", tt.item.Name, d.Type, tt.wantType)
		}
		if len(d.Capabilities) != tt.caps || len(d.Properties) != tt.props {
			t.Fatalf("%s: caps=%d props=%d want %d/%d", tt.item.Name, len(d.Capabilities), len(d.Properties), tt.caps, tt.props)
		}
		if d.Transport != "openhab" {
			t.Fatalf("%s: transport=%q", tt.item.Name, d.Transport)
		}
		found := false
		for _, b := range d.OpenHAB {
			if b.Item != tt.item.Name {
				t.Fatalf("%s: binding item=%q", tt.item.Name, b.Item)
			}
			if b.Instance == tt.wantInst {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no binding for instance %q", tt.item.Name, tt.wantInst)
		}
	}

	// Unknown type -> not inferred.
	if _, ok := draftForItem(ohItem{Name: "S", Type: "String"}); ok {
		t.Fatal("String must not be inferred")
	}
}

// Drafts must validate against the Yandex schema so they can be saved.
func TestDraftsValidate(t *testing.T) {
	for _, it := range []ohItem{
		{Name: "Dim", Type: "Dimmer"}, {Name: "Col", Type: "Color"},
		{Name: "T", Type: "Number:Temperature"}, {Name: "C", Type: "Contact"},
	} {
		d, _ := draftForItem(it)
		if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
			t.Fatalf("%s draft has validation errors: %v", it.Name, errs)
		}
	}
}

func TestNumberMeterAndSetpoint(t *testing.T) {
	// Water meter -> float property water_meter.
	d, ok := draftForItem(ohItem{Name: "Cnt_CWater", Type: "Number", Tags: []string{"Water", "Measurement"}})
	if !ok || d.Type != "devices.types.smart_meter" || len(d.Properties) != 1 {
		t.Fatalf("water meter: ok=%v type=%q props=%d", ok, d.Type, len(d.Properties))
	}
	if d.Properties[0].Parameters["instance"] != "water_meter" || d.OpenHAB[0].Kind != "prop" {
		t.Fatalf("water meter mapping: %+v / %+v", d.Properties[0].Parameters, d.OpenHAB)
	}

	// Temperature setpoint -> controllable range capability (thermostat).
	d2, ok := draftForItem(ohItem{Name: "Th_Set", Type: "Number:Temperature", Tags: []string{"Setpoint"}})
	if !ok || d2.Type != "devices.types.thermostat" || len(d2.Capabilities) != 1 || len(d2.Properties) != 0 {
		t.Fatalf("setpoint: ok=%v type=%q caps=%d props=%d", ok, d2.Type, len(d2.Capabilities), len(d2.Properties))
	}
	if d2.OpenHAB[0].Kind != "cap" || d2.OpenHAB[0].Instance != "temperature" {
		t.Fatalf("setpoint binding: %+v", d2.OpenHAB[0])
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d, d2}); len(errs) > 0 {
		t.Fatalf("meter/setpoint drafts invalid: %v", errs)
	}
}

func TestGroupExpansion(t *testing.T) {
	g := ohItem{Name: "Light_Kitchen", Type: "Group", Label: "Свет кухня", Tags: []string{"ya2mqtt", "Lightbulb"}}
	members := []ohItem{
		{Name: "LK_Power", Type: "Switch", Tags: []string{"Point"}},
		{Name: "LK_Bright", Type: "Dimmer", Tags: []string{"Point"}},
		{Name: "LK_Temp", Type: "Number:Temperature", Tags: []string{"Measurement"}},
	}
	d, ok := draftForGroup(g, members)
	if !ok {
		t.Fatal("group not inferred")
	}
	if d.Type != "devices.types.light" || d.Name != "Свет кухня" {
		t.Fatalf("group device: type=%q name=%q", d.Type, d.Name)
	}
	// on_off (dedup: Switch + Dimmer both offer it -> once) + brightness + temp property.
	if len(d.Capabilities) != 2 || len(d.Properties) != 1 {
		t.Fatalf("composite caps=%d props=%d, want 2/1", len(d.Capabilities), len(d.Properties))
	}
	// Each capability binds to its own member item.
	byInst := map[string]string{}
	for _, b := range d.OpenHAB {
		byInst[b.Instance] = b.Item
	}
	if byInst["on"] != "LK_Power" || byInst["brightness"] != "LK_Bright" || byInst["temperature"] != "LK_Temp" {
		t.Fatalf("composite bindings wrong: %+v", byInst)
	}
	// The composite carries its Equipment group as an identity marker.
	var equip string
	for _, b := range d.OpenHAB {
		if b.Kind == "equipment" {
			equip = b.Item
		}
	}
	if equip != "Light_Kitchen" {
		t.Fatalf("composite identity = %q, want group item Light_Kitchen", equip)
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		t.Fatalf("composite draft invalid: %v", errs)
	}
}

func TestLightColorTempAndTaggedDedup(t *testing.T) {
	g := ohItem{Name: "e_Light", Type: "Group", Label: "Люстра", Tags: []string{"Chandelier"}}
	members := []ohItem{
		{Name: "Virt", Type: "Switch", Tags: []string{"Light", "Switch"}},
		{Name: "Wtemp_Start", Type: "Dimmer", Tags: []string{"Setpoint"}},               // helper, untagged
		{Name: "Wtemp", Type: "Dimmer", Tags: []string{"Setpoint", "ColorTemperature"}}, // real color temp
		{Name: "Bright_Start", Type: "Dimmer", Tags: []string{"Setpoint"}},              // helper, untagged
		{Name: "Bright", Type: "Dimmer", Tags: []string{"Setpoint", "Brightness"}},      // real brightness
	}
	d, ok := draftForGroup(g, members)
	if !ok {
		t.Fatal("chandelier not inferred")
	}
	// Chandelier -> Люстра, not the generic light.
	if d.Type != "devices.types.light.ceiling" {
		t.Fatalf("type = %q, want devices.types.light.ceiling", d.Type)
	}
	byInst := map[string]string{}
	for _, b := range d.OpenHAB {
		if b.Kind == "cap" {
			byInst[b.Instance] = b.Item
		}
	}
	// The tagged points win their slots; the _Start helpers are dropped.
	if byInst["on"] != "Virt" {
		t.Fatalf("on <- %q, want Virt", byInst["on"])
	}
	if byInst["brightness"] != "Bright" {
		t.Fatalf("brightness <- %q, want Bright (not a _Start helper)", byInst["brightness"])
	}
	if byInst["temperature_k"] != "Wtemp" {
		t.Fatalf("temperature_k <- %q, want Wtemp", byInst["temperature_k"])
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		t.Fatalf("chandelier draft invalid: %v", errs)
	}
}

func TestFanSpeedSetpoint(t *testing.T) {
	// A Speed setpoint (fan) -> a fan_speed mode capability with recommended values.
	d, ok := draftForItem(ohItem{Name: "Fan_Speed", Type: "Number", Tags: []string{"Setpoint", "Speed"}})
	if !ok || d.Type != "devices.types.ventilation.fan" {
		t.Fatalf("fan speed: ok=%v type=%q", ok, d.Type)
	}
	if len(d.Capabilities) != 1 || d.Capabilities[0].Type != "devices.capabilities.mode" {
		t.Fatalf("want one mode capability, got %+v", d.Capabilities)
	}
	if d.Capabilities[0].Parameters["instance"] != "fan_speed" {
		t.Fatalf("instance = %v, want fan_speed", d.Capabilities[0].Parameters["instance"])
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		t.Fatalf("fan draft invalid: %v", errs)
	}
}

func TestFanSpeedAutoMapping(t *testing.T) {
	mn, mx, st := 0.0, 3.0, 1.0
	it := ohItem{Name: "Fan_Speed", Type: "Number", Tags: []string{"Setpoint", "Speed"},
		StateDesc: &ohStateDesc{Minimum: &mn, Maximum: &mx, Step: &st}}
	d, ok := draftForItem(it)
	if !ok {
		t.Fatal("fan not inferred")
	}
	// 0 is "off" (skipped); 1..3 -> low/medium/high with a value mapping.
	if len(d.ValueMapping) != 1 || d.ValueMapping[0].Type != "mode" {
		t.Fatalf("value mapping = %+v", d.ValueMapping)
	}
	im := d.ValueMapping[0].Mapping[0]
	if im.Instance != "fan_speed" || len(im.Mapping[0]) != 3 {
		t.Fatalf("mapping = %+v", im)
	}
	if im.Mapping[0][0] != "low" || im.Mapping[1][0] != int64(1) ||
		im.Mapping[0][2] != "high" || im.Mapping[1][2] != int64(3) {
		t.Fatalf("pairs = %v <-> %v", im.Mapping[0], im.Mapping[1])
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		t.Fatalf("fan draft invalid: %v", errs)
	}
}

func TestGroupPointAggregation(t *testing.T) {
	// A light whose points are aggregation Groups (Group:Switch/Dimmer) mapped by
	// groupType; the Point-groups must not become their own devices.
	g := ohItem{Name: "e_Light", Type: "Group", Label: "Свет", Tags: []string{"AccentLight"}}
	sw := ohItem{Name: "Agg_Sw", Type: "Group", GroupType: "Switch", Tags: []string{"Light", "Switch"},
		Meta: map[string]ohMeta{"semantics": {Value: "Point_Control_Switch"}}}
	dim := ohItem{Name: "Agg_Dim", Type: "Group", GroupType: "Dimmer", Tags: []string{"Setpoint", "Brightness"},
		Meta: map[string]ohMeta{"semantics": {Value: "Point_Setpoint"}}}

	if isSemanticPoint(g) {
		t.Fatal("equipment must not be a semantic point")
	}
	if !isSemanticPoint(sw) {
		t.Fatal("aggregation switch must be a semantic point")
	}
	d, ok := draftForGroup(g, []ohItem{sw, dim})
	if !ok || d.Type != "devices.types.light" {
		t.Fatalf("light: ok=%v type=%q", ok, d.Type)
	}
	byInst := map[string]string{}
	for _, b := range d.OpenHAB {
		if b.Kind == "cap" {
			byInst[b.Instance] = b.Item
		}
	}
	if byInst["brightness"] != "Agg_Dim" {
		t.Fatalf("brightness should bind to the Dimmer group: %+v", byInst)
	}
	if _, ok := byInst["on"]; !ok {
		t.Fatalf("expected an on_off, got %+v", byInst)
	}
}

func TestColorSettingMerge(t *testing.T) {
	// hsv (from a Color point) and temperature_k (from a ColorTemperature Dimmer)
	// must merge into ONE color_setting capability with per-instance bindings.
	g := ohItem{Name: "e_Light", Type: "Group", Label: "Свет", Tags: []string{"Lightbulb"}}
	members := []ohItem{
		{Name: "Col", Type: "Color", Tags: []string{"Color"}},
		{Name: "Wt", Type: "Dimmer", Tags: []string{"Setpoint", "ColorTemperature"}},
	}
	d, ok := draftForGroup(g, members)
	if !ok {
		t.Fatal("not inferred")
	}
	n := 0
	var cs config.Capability
	for _, c := range d.Capabilities {
		if c.Type == "devices.capabilities.color_setting" {
			n++
			cs = c
		}
	}
	if n != 1 {
		t.Fatalf("color_setting caps = %d, want 1", n)
	}
	if cs.Parameters["color_model"] != "hsv" || cs.Parameters["temperature_k"] == nil {
		t.Fatalf("merged params missing color_model/temperature_k: %+v", cs.Parameters)
	}
	byInst := map[string]string{}
	for _, b := range d.OpenHAB {
		byInst[b.Instance] = b.Item
	}
	if byInst["hsv"] != "Col" || byInst["temperature_k"] != "Wt" {
		t.Fatalf("bindings wrong: %+v", byInst)
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		t.Fatalf("merged draft invalid: %v", errs)
	}
}

func TestKitchenHoodInference(t *testing.T) {
	g := ohItem{Name: "e_Hood", Type: "Group", Label: "Вытяжка", Tags: []string{"KitchenHood"}}
	members := []ohItem{
		{Name: "Power", Type: "Switch", Tags: []string{"Switch"}},
		{Name: "Light", Type: "Switch", Tags: []string{"Switch", "Light"}}, // tagged as a light
		{Name: "Speed", Type: "String", Tags: []string{"Setpoint", "Speed"},
			StateDesc: &ohStateDesc{Options: []ohOption{{Value: "off"}, {Value: "low"}, {Value: "medium"}, {Value: "high"}}}},
	}
	d, ok := draftForGroup(g, members)
	if !ok || d.Type != "devices.types.ventilation" {
		t.Fatalf("hood: ok=%v type=%q", ok, d.Type)
	}
	byInst := map[string]string{} // "type|instance" -> item
	for i, c := range d.Capabilities {
		byInst[c.Type+"|"+capInst(c)] = d.OpenHAB[bindingIndex(d, "cap", capInst(c))].Item
		_ = i
	}
	// Power -> on_off, Light -> backlight toggle (not a 2nd on_off), Speed -> fan_speed.
	if byInst["devices.capabilities.on_off|on"] != "Power" {
		t.Fatalf("on_off should be Power: %+v", byInst)
	}
	if byInst["devices.capabilities.toggle|backlight"] != "Light" {
		t.Fatalf("light should be a backlight toggle bound to Light: %+v", byInst)
	}
	if byInst["devices.capabilities.mode|fan_speed"] != "Speed" {
		t.Fatalf("speed should be fan_speed bound to Speed: %+v", byInst)
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		t.Fatalf("hood draft invalid: %v", errs)
	}
}

func capInst(c config.Capability) string {
	if s, _ := c.Parameters["instance"].(string); s != "" {
		return s
	}
	return "on" // on_off
}

func bindingIndex(d config.Device, kind, instance string) int {
	for i, b := range d.OpenHAB {
		if b.Kind == kind && b.Instance == instance {
			return i
		}
	}
	return 0
}

func TestThermostatYahome(t *testing.T) {
	group := ohItem{Name: "e_Thermostat_Artem", Type: "Group", Label: "Термостат", Tags: []string{"HVAC"}}
	warmer := ohItem{Name: "Warmer", Type: "Switch", Tags: []string{"Switch"}}                 // heater relay
	setp := ohItem{Name: "Target", Type: "Dimmer", Tags: []string{"Setpoint", "Temperature"}}  // setpoint
	temp := ohItem{Name: "Temp", Type: "Number", Tags: []string{"Temperature", "Measurement"}} // sensor
	mode := ohItem{Name: "Mode", Type: "String"}                                               // untagged control
	members := []ohItem{warmer, setp, temp, mode}

	// Without yahome: heater on/off and the untagged Mode are dropped; only the
	// setpoint (range) and sensor (float) remain.
	d, ok := draftForGroup(group, members)
	if !ok || d.Type != "devices.types.thermostat" {
		t.Fatalf("thermostat: ok=%v type=%q", ok, d.Type)
	}
	if kinds := capKinds(d); len(kinds["devices.capabilities.on_off"]) != 0 {
		t.Fatalf("heater on_off must be excluded, got caps: %+v", d.Capabilities)
	}
	if len(d.Capabilities) != 1 || d.Capabilities[0].Type != "devices.capabilities.range" {
		t.Fatalf("want only range setpoint, got: %+v", d.Capabilities)
	}
	if len(d.Properties) != 1 || d.Properties[0].Type != "devices.properties.float" {
		t.Fatalf("want only float sensor, got: %+v", d.Properties)
	}

	// With yahome="on_off" on the Mode item, on/off is exposed (from Mode), while
	// the Warmer relay stays hidden. The on_off binds to the Mode item.
	mode.Meta = map[string]ohMeta{"yahome": {Value: "on_off"}}
	members = []ohItem{warmer, setp, temp, mode}
	d2, _ := draftForGroup(group, members)
	if len(capKinds(d2)["devices.capabilities.on_off"]) != 1 {
		t.Fatalf("want exactly one on_off (from Mode), got caps: %+v", d2.Capabilities)
	}
	onoffItem := ""
	for _, b := range d2.OpenHAB {
		if b.Kind == "cap" && b.Instance == "on" {
			onoffItem = b.Item
		}
	}
	if onoffItem != "Mode" {
		t.Fatalf("on_off must bind to the Mode item, got %q", onoffItem)
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d, d2}); len(errs) > 0 {
		t.Fatalf("thermostat drafts invalid: %v", errs)
	}
}

func TestYahomeModeOverride(t *testing.T) {
	it := ohItem{Name: "M", Type: "String", Meta: map[string]ohMeta{"yahome": {Value: "mode"}}}
	feats, dt, ok := yahomeFeatures(it)
	if !ok || dt != "devices.types.thermostat" || len(feats) != 1 {
		t.Fatalf("mode override: ok=%v dt=%q feats=%d", ok, dt, len(feats))
	}
	modes, _ := feats[0].cap.Parameters["modes"].([]any)
	if len(modes) != len(device.RecommendedModes("thermostat")) {
		t.Fatalf("mode values = %d, want recommended thermostat set", len(modes))
	}
}

func capKinds(d config.Device) map[string][]int {
	out := map[string][]int{}
	for i, c := range d.Capabilities {
		out[c.Type] = append(out[c.Type], i)
	}
	return out
}

func TestSemanticEventSensors(t *testing.T) {
	// A "Switch" tagged Motion is a motion event sensor, NOT an on_off toggle.
	d, ok := draftForItem(ohItem{Name: "Move_Bath", Type: "Switch", Tags: []string{"Point", "Status", "Motion"}})
	if !ok || d.Type != "devices.types.sensor.motion" || len(d.Capabilities) != 0 || len(d.Properties) != 1 {
		t.Fatalf("motion: ok=%v type=%q caps=%d props=%d", ok, d.Type, len(d.Capabilities), len(d.Properties))
	}
	if d.Properties[0].Parameters["instance"] != "motion" {
		t.Fatalf("motion instance: %+v", d.Properties[0].Parameters)
	}
	// Smoke contact -> smoke event.
	ds, _ := draftForItem(ohItem{Name: "Smoke", Type: "Contact", Tags: []string{"Alarm", "Smoke"}})
	if ds.Type != "devices.types.sensor.smoke" || ds.Properties[0].Parameters["instance"] != "smoke" {
		t.Fatalf("smoke: %+v", ds)
	}
	// Water: a readonly Contact is a leak sensor; a Number is a meter.
	leak, _ := draftForItem(ohItem{Name: "Leak", Type: "Contact", Tags: []string{"Status", "Water"}})
	if leak.Type != "devices.types.sensor.water_leak" || leak.Properties[0].Parameters["instance"] != "water_leak" {
		t.Fatalf("leak: %+v", leak)
	}
	meter, _ := draftForItem(ohItem{Name: "Cnt", Type: "Number", Tags: []string{"Measurement", "Water"}})
	if meter.Type != "devices.types.smart_meter" || meter.Properties[0].Parameters["instance"] != "water_meter" {
		t.Fatalf("water meter: %+v", meter)
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d, ds, leak, meter}); len(errs) > 0 {
		t.Fatalf("event/meter drafts invalid: %v", errs)
	}
}

func TestSemanticSetpointVolumeAndPower(t *testing.T) {
	vol, ok := draftForItem(ohItem{Name: "Vol", Type: "Number", Tags: []string{"Setpoint", "SoundVolume"}})
	if !ok || len(vol.Capabilities) != 1 || vol.Capabilities[0].Parameters["instance"] != "volume" {
		t.Fatalf("volume setpoint: ok=%v %+v", ok, vol.Capabilities)
	}
	pw, ok := draftForItem(ohItem{Name: "Pwr", Type: "Number", Tags: []string{"Measurement", "Power"}})
	if !ok || len(pw.Properties) != 1 || pw.Properties[0].Parameters["instance"] != "power" {
		t.Fatalf("power measurement: ok=%v %+v", ok, pw.Properties)
	}
}

func TestDimmerContextDisambiguation(t *testing.T) {
	// An under-tagged volume Dimmer (only Setpoint) inside a VoiceAssistant
	// equipment must become a media_device volume, NOT a light.
	g := ohItem{Name: "e_Station", Type: "Group", Label: "Станция", Tags: []string{"VoiceAssistant"}}
	members := []ohItem{{Name: "YaStation_Volume", Type: "Dimmer", Tags: []string{"Setpoint"}, GroupNames: []string{"e_Station"}}}
	d, ok := draftForGroup(g, members)
	if !ok || d.Type != "devices.types.media_device" {
		t.Fatalf("station: ok=%v type=%q", ok, d.Type)
	}
	if len(d.Capabilities) != 1 || d.Capabilities[0].Parameters["instance"] != "volume" {
		t.Fatalf("station volume cap: %+v", d.Capabilities)
	}

	// The same Dimmer standalone (no equipment context) falls back to a light.
	sd, _ := draftForItem(ohItem{Name: "D", Type: "Dimmer", Tags: []string{"Setpoint"}})
	if sd.Type != "devices.types.light" || len(sd.Capabilities) != 2 {
		t.Fatalf("standalone dimmer: type=%q caps=%d", sd.Type, len(sd.Capabilities))
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d, sd}); len(errs) > 0 {
		t.Fatalf("drafts invalid: %v", errs)
	}
}

func TestEquipmentTypeMapping(t *testing.T) {
	cases := map[string]string{
		"Blinds":         "devices.types.openable.curtain",
		"CleaningRobot":  "devices.types.vacuum_cleaner",
		"AirConditioner": "devices.types.thermostat.ac",
		"Lock":           "devices.types.openable.door_lock",
		"MotionDetector": "devices.types.sensor.motion",
		"CoffeeMaker":    "devices.types.cooking.coffee_maker",
		"Television":     "devices.types.media_device.tv",
	}
	for tag, want := range cases {
		if got := equipmentType([]string{tag}); got != want {
			t.Fatalf("equipmentType(%q) = %q, want %q", tag, got, want)
		}
	}
}

func serveItems(t *testing.T, jsonBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(jsonBody))
	}))
}

func TestDiscoverTagFilter(t *testing.T) {
	srv := serveItems(t, `[
		{"name":"Light_Kitchen","type":"Dimmer","label":"Свет кухня","tags":["ya2mqtt","Light"]},
		{"name":"Ignore","type":"String","tags":["ya2mqtt"]},
		{"name":"Untagged","type":"Switch","tags":[]}
	]`)
	defer srv.Close()
	c := NewConnector(config.OpenHAB{URL: srv.URL, Token: "t"}, discardLog(), nil)
	defer c.Close()

	// With a tag, only tagged & inferable items (Dimmer) are exposed.
	drafts, err := c.Discover(context.Background(), "ya2mqtt", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].Name != "Свет кухня" || len(drafts[0].Capabilities) != 2 {
		t.Fatalf("tagged discover: %+v", drafts)
	}

	// Without a tag, all inferable items (Dimmer + untagged Switch) are exposed.
	all, _ := c.Discover(context.Background(), "", false)
	if len(all) != 2 {
		t.Fatalf("untagged discover: want 2, got %d", len(all))
	}
}

// An Equipment group and its member yield ONE composite device (member consumed
// via the groupNames hierarchy, not duplicated as a standalone).
func TestDiscoverGroupDedup(t *testing.T) {
	srv := serveItems(t, `[
		{"name":"Light_Kitchen","type":"Group","label":"Свет кухня","tags":["ya2mqtt","Lightbulb"]},
		{"name":"LK_Switch","type":"Switch","tags":["ya2mqtt","Point"],"groupNames":["Light_Kitchen"]},
		{"name":"LK_Dim","type":"Dimmer","tags":["Point"],"groupNames":["Light_Kitchen"]}
	]`)
	defer srv.Close()
	c := NewConnector(config.OpenHAB{URL: srv.URL, Token: "t"}, discardLog(), nil)
	defer c.Close()

	drafts, err := c.Discover(context.Background(), "ya2mqtt", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 {
		t.Fatalf("want 1 composite (member consumed), got %d: %+v", len(drafts), drafts)
	}
	if d := drafts[0]; d.Type != "devices.types.light" || len(d.Capabilities) != 2 {
		t.Fatalf("composite: type=%q caps=%d", d.Type, len(d.Capabilities))
	}
}

// Location groups become rooms; non-Equipment group members become standalone
// devices; both inherit the room from their Location ancestor.
func TestDiscoverRoomsAndStandalone(t *testing.T) {
	srv := serveItems(t, `[
		{"name":"Kitchen","type":"Group","label":"Кухня","tags":["Kitchen"]},
		{"name":"KLight","type":"Group","tags":["Lightbulb"],"groupNames":["Kitchen"]},
		{"name":"KLight_Sw","type":"Switch","tags":["Point"],"groupNames":["KLight"]},
		{"name":"KFanGroup","type":"Group","tags":["gFan"],"groupNames":["Kitchen"]},
		{"name":"KFan_Sw","type":"Switch","tags":["Point"],"groupNames":["KFanGroup"]}
	]`)
	defer srv.Close()
	c := NewConnector(config.OpenHAB{URL: srv.URL, Token: "t"}, discardLog(), nil)
	defer c.Close()

	drafts, err := c.Discover(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]config.Device{}
	for _, d := range drafts {
		byName[d.Name] = d
	}
	// Equipment KLight -> composite light in Кухня; KFan_Sw (member of a
	// non-Equipment group) -> standalone switch in Кухня. Kitchen/KFanGroup are
	// not devices.
	if len(drafts) != 2 {
		t.Fatalf("want 2 devices, got %d: %v", len(drafts), drafts)
	}
	if d, ok := byName["KLight"]; !ok || d.Type != "devices.types.light" || d.Room != "Кухня" {
		t.Fatalf("equipment device: %+v", d)
	}
	if d, ok := byName["KFan_Sw"]; !ok || d.Room != "Кухня" {
		t.Fatalf("standalone device room: %+v", d)
	}
}
