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

func TestUnseenSensorOmittedFromQuery(t *testing.T) {
	d := New(config.Device{
		ID: "S", Type: "devices.types.sensor.climate",
		MQTT: config.MQTTMapping{
			Capabilities: []config.MQTTTopic{{Instance: "on", Set: "x/set", State: "x/st"}},
			Properties:   []config.MQTTTopic{{Instance: "temperature", State: "t/st"}},
		},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
		Properties: []config.Property{{Type: "devices.properties.float", Retrievable: true,
			Parameters: map[string]any{"instance": "temperature", "unit": "unit.temperature.celsius"}}},
	}, nil, nil)

	// Before any real value: the sensor (float) is omitted so Yandex doesn't see a
	// fake 0 °C, but the controllable on_off keeps its default.
	q := d.QueryState()
	if len(q.Properties) != 0 {
		t.Fatalf("unseen sensor must be omitted, got %+v", q.Properties)
	}
	if len(q.Capabilities) != 1 {
		t.Fatalf("controllable capability must report its default, got %+v", q.Capabilities)
	}

	// After a real value arrives, the sensor is reported.
	d.UpdateFromMQTT("21.5", "temperature", true)
	q = d.QueryState()
	if len(q.Properties) != 1 || q.Properties[0].State.Value != 21.5 {
		t.Fatalf("seen sensor must report its value, got %+v", q.Properties)
	}
}

func TestRangeInversion(t *testing.T) {
	var gotMsg string
	d := New(config.Device{
		ID: "C", Type: "devices.types.openable.curtain",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{{Instance: "open", Set: "c/set", State: "c/state"}}},
		Capabilities: []config.Capability{{
			Type: "devices.capabilities.range", Invert: true,
			Parameters: map[string]any{"instance": "open", "unit": "unit.percent",
				"range": map[string]any{"min": 0, "max": 100, "precision": 1}},
		}},
	}, func(_, msg string) { gotMsg = msg }, nil)

	// Outbound: Yandex 80% open -> device 20% (and cur keeps the Yandex value).
	d.SetCapabilityState(80.0, "devices.capabilities.range", "open", false)
	if gotMsg != "20" {
		t.Fatalf("outbound msg = %q, want 20", gotMsg)
	}
	c := d.findCapByInstance("open")
	if toFloatOr(c.cur.Value, 0) != 80 {
		t.Fatalf("cur (Yandex) = %v, want 80", c.cur.Value)
	}
	// Inbound: device 30% -> Yandex 70%.
	d.UpdateFromMQTT("30", "open", false)
	if toFloatOr(c.cur.Value, 0) != 70 {
		t.Fatalf("inbound = %v, want 70", c.cur.Value)
	}
}

func TestVideoStreamGetStream(t *testing.T) {
	d := New(config.Device{
		ID: "Cam", Type: "devices.types.camera",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{{Instance: "get_stream", State: "cam/url"}}},
		Capabilities: []config.Capability{{
			Type: "devices.capabilities.video_stream", Retrievable: false, Reportable: false,
			Parameters: map[string]any{"protocols": []any{"hls"}},
		}},
	}, nil, nil)

	// Before a URL arrives, get_stream errors.
	if r := d.SetCapabilityState(map[string]any{"protocols": []any{"hls"}}, "devices.capabilities.video_stream", "get_stream", false); r.State.ActionResult.Status != "ERROR" {
		t.Fatalf("expected ERROR before URL, got %+v", r.State)
	}
	// The URL source updates the stream URL (not reported to Yandex).
	d.UpdateFromMQTT("https://host/play.m3u8?t=1", StreamInstance, false)
	if len(d.QueryState().Capabilities) != 0 {
		t.Fatal("video_stream must not appear in query (not retrievable)")
	}
	r := d.SetCapabilityState(map[string]any{"protocols": []any{"hls"}}, "devices.capabilities.video_stream", "get_stream", false)
	if r.State.ActionResult.Status != "DONE" {
		t.Fatalf("get_stream status = %q", r.State.ActionResult.Status)
	}
	v, _ := r.State.Value.(map[string]any)
	if v["stream_url"] != "https://host/play.m3u8?t=1" || v["protocol"] != "hls" {
		t.Fatalf("stream value = %+v", r.State.Value)
	}
}

// A camera declaring mjpeg returns protocol="mjpeg" on get_stream, so the proxy
// and the app pick the MJPEG path.
func TestVideoStreamMJPEGProtocol(t *testing.T) {
	d := New(config.Device{
		ID: "Cam", Type: "devices.types.camera",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{{Instance: "get_stream", State: "cam/url"}}},
		Capabilities: []config.Capability{{
			Type: "devices.capabilities.video_stream", Retrievable: false, Reportable: false,
			Parameters: map[string]any{"protocols": []any{"mjpeg"}},
		}},
	}, nil, nil)

	d.UpdateFromMQTT("https://host/live", StreamInstance, false)
	r := d.SetCapabilityState(map[string]any{"protocols": []any{"mjpeg"}}, "devices.capabilities.video_stream", "get_stream", false)
	v, _ := r.State.Value.(map[string]any)
	if v["protocol"] != "mjpeg" || v["stream_url"] != "https://host/live" {
		t.Fatalf("mjpeg stream value = %+v", r.State.Value)
	}
}

func TestErrorCodeFromRules(t *testing.T) {
	d := New(config.Device{
		ID: "V", Type: "devices.types.vacuum_cleaner",
		MQTT: config.MQTTMapping{Capabilities: []config.MQTTTopic{{Instance: "on", Set: "v/set", State: "v/state"}}},
		// Different codes from different sources; first active rule wins.
		Errors: []config.ErrorRule{
			{Code: "DEVICE_STUCK", State: "v/status", Value: "stuck"},
			{Code: "CONTAINER_FULL", State: "v/dustbag", Value: "full"},
		},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
	}, nil, nil)

	// Each rule binds to its own source.
	errInst := func(src string) string {
		for _, b := range d.StateBindings() {
			if IsErrorInstance(b.Instance) && b.Source == src {
				return b.Instance
			}
		}
		return ""
	}
	if errInst("v/status") == "" || errInst("v/dustbag") == "" {
		t.Fatalf("missing error bindings: %+v", d.StateBindings())
	}
	if d.QueryState().ErrorCode != "" {
		t.Fatal("expected no error initially")
	}

	// dustbag full -> CONTAINER_FULL (second rule).
	d.UpdateFromMQTT("full", errInst("v/dustbag"), true)
	if got := d.QueryState().ErrorCode; got != "CONTAINER_FULL" {
		t.Fatalf("error = %q, want CONTAINER_FULL", got)
	}
	// status stuck -> DEVICE_STUCK wins (earlier rule) even with dustbag full.
	d.UpdateFromMQTT("stuck", errInst("v/status"), true)
	if got := d.QueryState().ErrorCode; got != "DEVICE_STUCK" {
		t.Fatalf("error = %q, want DEVICE_STUCK", got)
	}
	// clearing status falls back to the still-active dustbag rule.
	d.UpdateFromMQTT("ok", errInst("v/status"), true)
	if got := d.QueryState().ErrorCode; got != "CONTAINER_FULL" {
		t.Fatalf("error = %q, want CONTAINER_FULL after status clear", got)
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
