package device

import (
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

func TestConvertToYandexValue(t *testing.T) {
	tests := []struct {
		name     string
		val      any
		actType  string
		instance string
		params   map[string]any
		want     any
	}{
		{"float parses", "42.5", "float", "battery_level", nil, 42.5},
		{"float bad -> 0", "n/a", "float", "x", nil, 0.0},
		{"on_off true string", "ON", "on_off", "on", nil, true},
		{"on_off false", "off", "on_off", "on", nil, false},
		{"on_off numeric >1", "2", "on_off", "on", nil, true},
		{"toggle 1", "1", "toggle", "mute", nil, true},
		{"rgb parses", "16711680", "color_setting", "rgb", nil, 16711680.0},
		// temperature_k: mqtt 0..100 -> kelvin. divider=(6500-2700)/100=38; 50*38+2700=4600
		{"temp_k mid", "50", "color_setting", "temperature_k",
			map[string]any{"temperature_k": map[string]any{"min": 2700, "max": 6500}}, 4600.0},
		{"hsv parses", "10,20,30", "color_setting", "hsv", nil, HSV{H: 10, S: 20, V: 30}},
		{"hsv bad -> zero", "nope", "color_setting", "hsv", nil, HSV{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertToYandexValue(tt.val, tt.actType, tt.instance, tt.params)
			if got != tt.want {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

// vacuum with the pause toggle mapping used throughout the real catalog.
func vacuumDevice(pub PublishFunc) *Device {
	return New(config.Device{
		ID:   "Cleaner",
		Type: "devices.types.vacuum_cleaner",
		MQTT: config.MQTTMapping{
			Capabilities: []config.MQTTTopic{
				{Instance: "on", Set: "vac/power/set", State: "vac/state"},
				{Instance: "pause", Set: "vac/ctrl/set", State: "vac/op/state"},
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
			{Type: "devices.capabilities.on_off", Retrievable: true},
			{Type: "devices.capabilities.toggle", Retrievable: true, Parameters: map[string]any{"instance": "pause"}},
		},
	}, pub, nil)
}

func TestToggleMappingOutbound(t *testing.T) {
	var gotTopic, gotMsg string
	d := vacuumDevice(func(topic, msg string) { gotTopic, gotMsg = topic, msg })

	// Yandex sends toggle pause=true -> should map to "PAUSE" on ctrl topic.
	res := d.SetCapabilityState(true, "devices.capabilities.toggle", "pause", false)
	if res.State.ActionResult.Status != "DONE" {
		t.Fatalf("status = %s", res.State.ActionResult.Status)
	}
	if gotTopic != "vac/ctrl/set" || gotMsg != "PAUSE" {
		t.Fatalf("published %q=%q, want vac/ctrl/set=PAUSE", gotTopic, gotMsg)
	}
}

func TestToggleMappingInbound(t *testing.T) {
	d := vacuumDevice(nil)
	// MQTT reports "START" on pause -> Yandex value should map to false.
	d.UpdateFromMQTT("START", "pause", false)
	c := d.findCapByInstance("pause")
	if c.cur.Value != false {
		t.Fatalf("inbound pause value = %#v, want false", c.cur.Value)
	}
}

func TestOnOffThermostatInbound(t *testing.T) {
	d := New(config.Device{
		ID:   "Th",
		Type: "devices.types.thermostat",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{
			{Instance: "on", Set: "th/power/set", State: "th/state"},
		}},
		ValueMapping: []config.ValueMapping{{
			Type: "on_off",
			Mapping: []config.InstanceMapping{{
				Instance: "on",
				Mapping: [][]any{
					{"true", "false", "true"},
					{"HEAT", "OFF", "ON"},
				},
			}},
		}},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
	}, nil, nil)

	d.UpdateFromMQTT("HEAT", "on", false)
	c := d.findCapByInstance("on")
	if c.cur.Value != true {
		t.Fatalf("HEAT -> %#v, want true", c.cur.Value)
	}
	d.UpdateFromMQTT("OFF", "on", false)
	if c.cur.Value != false {
		t.Fatalf("OFF -> %#v, want false", c.cur.Value)
	}
}

func TestFanSpeedClosestInbound(t *testing.T) {
	d := New(config.Device{
		ID:   "Fan",
		Type: "devices.types.ventilation.fan",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{
			{Instance: "fan_speed", Set: "fan/speed/set", State: "fan/state/speed"},
		}},
		ValueMapping: []config.ValueMapping{{
			Type: "mode",
			Mapping: []config.InstanceMapping{{
				Instance: "fan_speed",
				Mapping: [][]any{
					{"three", "two", "one", "eco"},
					{"3", "2", "1", "0"},
				},
			}},
		}},
		Capabilities: []config.Capability{{
			Type:       "devices.capabilities.mode",
			Parameters: map[string]any{"instance": "fan_speed", "modes": []any{map[string]any{"value": "one"}}},
		}},
	}, nil, nil)

	// MQTT reports "2" -> nearest is "2" -> Yandex "two".
	d.UpdateFromMQTT("2", "fan_speed", false)
	c := d.findCapByInstance("fan_speed")
	if c.cur.Value != "two" {
		t.Fatalf("mqtt 2 -> %#v, want two", c.cur.Value)
	}
}

func TestTemperatureKOutbound(t *testing.T) {
	var gotMsg string
	d := New(config.Device{
		ID:   "Light",
		Type: "devices.types.light",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{
			{Instance: "temperature_k", Set: "light/ct/set", State: "light/ct/state"},
		}},
		Capabilities: []config.Capability{{
			Type:       "devices.capabilities.color_setting",
			Parameters: map[string]any{"temperature_k": map[string]any{"min": 2700, "max": 6500}},
		}},
	}, func(_, msg string) { gotMsg = msg }, nil)

	// Yandex sends 4600 kelvin -> mqtt percent = (4600-2700)/38 = 50.
	d.SetCapabilityState(4600.0, "devices.capabilities.color_setting", "temperature_k", false)
	if gotMsg != "50" {
		t.Fatalf("temp_k outbound msg = %q, want 50", gotMsg)
	}
}

func TestHSVOutbound(t *testing.T) {
	var gotMsg string
	d := New(config.Device{
		ID:   "Light",
		Type: "devices.types.light",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{
			{Instance: "hsv", Set: "light/hsv/set", State: "light/hsv/state"},
		}},
		Capabilities: []config.Capability{{
			Type:       "devices.capabilities.color_setting",
			Parameters: map[string]any{"color_model": "hsv"},
		}},
	}, func(_, msg string) { gotMsg = msg }, nil)

	d.SetCapabilityState(map[string]any{"h": 255.0, "s": 50.0, "v": 100.0},
		"devices.capabilities.color_setting", "hsv", false)
	if gotMsg != "255,50,100" {
		t.Fatalf("hsv outbound msg = %q, want 255,50,100", gotMsg)
	}
}

func TestOnOffRelativeAndPlain(t *testing.T) {
	var gotMsg string
	d := New(config.Device{
		ID:   "L",
		Type: "devices.types.light",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{
			{Instance: "brightness", Set: "l/br/set", State: "l/br/state"},
		}},
		Capabilities: []config.Capability{{
			Type: "devices.capabilities.range",
			Parameters: map[string]any{"instance": "brightness",
				"range": map[string]any{"min": 0, "max": 100, "precision": 1}},
		}},
	}, func(_, msg string) { gotMsg = msg }, nil)

	d.SetCapabilityState(30.0, "devices.capabilities.range", "brightness", true)
	if gotMsg != "+30" {
		t.Fatalf("relative +30 msg = %q", gotMsg)
	}
	d.SetCapabilityState(-10.0, "devices.capabilities.range", "brightness", true)
	if gotMsg != "-10" {
		t.Fatalf("relative -10 msg = %q", gotMsg)
	}
	d.SetCapabilityState(70.0, "devices.capabilities.range", "brightness", false)
	if gotMsg != "70" {
		t.Fatalf("plain 70 msg = %q", gotMsg)
	}
}

func TestUnknownActionReturnsError(t *testing.T) {
	d := vacuumDevice(nil)
	res := d.SetCapabilityState(true, "devices.capabilities.range", "brightness", false)
	if res.State.ActionResult.Status != "ERROR" || res.State.ActionResult.ErrorCode != "INVALID_ACTION" {
		t.Fatalf("want ERROR/INVALID_ACTION, got %+v", res.State.ActionResult)
	}
}

func TestJSEqualSemantics(t *testing.T) {
	// bool true must NOT equal string "true" (JS === semantics).
	if jsEqual(true, "true") {
		t.Fatal("true == \"true\" should be false")
	}
	if !jsEqual("PAUSE", "PAUSE") {
		t.Fatal("string equality broken")
	}
	if !jsEqual(3, 3.0) {
		t.Fatal("int/float equality broken")
	}
	if jsEqual(3, "3") {
		t.Fatal("number 3 == string \"3\" should be false")
	}
}
