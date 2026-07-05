package web

import (
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// A color_setting capability keeps its instance in the shape of its parameters,
// so prefill must derive it (not fall back to the hsv default) — otherwise the
// edit form shows the wrong instance and loses the item binding.
func TestCapInstanceColorSetting(t *testing.T) {
	tests := []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"temperature_k": map[string]any{"min": 2700, "max": 6500}}, "temperature_k"},
		{map[string]any{"color_model": "hsv"}, "hsv"},
		{map[string]any{"color_model": "rgb"}, "rgb"},
		{map[string]any{"color_scene": []any{}}, "scene"},
		{map[string]any{}, "hsv"},
	}
	for _, tt := range tests {
		got := capInstance(config.Capability{Type: "devices.capabilities.color_setting", Parameters: tt.params})
		if got != tt.want {
			t.Fatalf("capInstance(%v) = %q, want %q", tt.params, got, tt.want)
		}
	}
}

// Event properties carry a value mapping (raw sensor value -> Yandex event enum)
// that persists via the generic value_mappings table and round-trips to the form.
func TestEventPropertyMappingRoundTrip(t *testing.T) {
	in := deviceInput{
		Name: "Leak", Type: "devices.types.sensor.water_leak", Transport: "mqtt",
		Properties: []capInput{{
			Type: "devices.properties.event", Instance: "water_leak",
			Params:  map[string]any{"instance": "water_leak"},
			State:   "sensor/leak",
			Mapping: []mapPair{{Yandex: "leak", Mqtt: "ON"}, {Yandex: "dry", Mqtt: "OFF"}},
		}},
	}
	d := buildDevice("1", "id", in)
	if len(d.ValueMapping) != 1 || d.ValueMapping[0].Type != "event" {
		t.Fatalf("value mapping = %+v", d.ValueMapping)
	}
	if d.ValueMapping[0].Mapping[0].Instance != "water_leak" {
		t.Fatalf("mapping instance = %q", d.ValueMapping[0].Mapping[0].Instance)
	}
	back := toInput(d, "")
	if len(back.Properties) != 1 || len(back.Properties[0].Mapping) != 2 {
		t.Fatalf("prefill mapping = %+v", back.Properties)
	}
	if back.Properties[0].Mapping[0].Yandex != "leak" || back.Properties[0].Mapping[0].Mqtt != "ON" {
		t.Fatalf("pair = %+v", back.Properties[0].Mapping[0])
	}
}

// toInput surfaces the color-temperature instance and its openHAB item binding.
func TestToInputColorTempOpenHAB(t *testing.T) {
	d := config.Device{
		Transport: "openhab",
		Type:      "devices.types.light.ceiling",
		Capabilities: []config.Capability{{
			Type:       "devices.capabilities.color_setting",
			Parameters: map[string]any{"temperature_k": map[string]any{"min": 2700, "max": 6500}},
		}},
		OpenHAB: []config.OpenHABBinding{
			{Kind: "equipment", Item: "e_Light"},
			{Kind: "cap", Instance: "temperature_k", Item: "Light_Alex_Highlight_Wtemp"},
		},
	}
	in := toInput(d, "")
	if len(in.Capabilities) != 1 {
		t.Fatalf("caps = %d, want 1", len(in.Capabilities))
	}
	c := in.Capabilities[0]
	if c.Instance != "temperature_k" {
		t.Fatalf("instance = %q, want temperature_k", c.Instance)
	}
	if c.Item != "Light_Alex_Highlight_Wtemp" {
		t.Fatalf("item = %q, want Light_Alex_Highlight_Wtemp", c.Item)
	}
}
