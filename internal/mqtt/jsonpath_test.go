package mqtt

import "testing"

func TestExtractJSONPath(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		path    string
		want    string
		ok      bool
	}{
		{"z2m flat string", `{"state":"ON","brightness":150}`, "state", "ON", true},
		{"z2m number int", `{"state":"ON","brightness":150}`, "brightness", "150", true},
		{"z2m float", `{"temperature":21.5}`, "temperature", "21.5", true},
		{"tasmota nested", `{"ENERGY":{"Power":12,"Voltage":230}}`, "ENERGY.Power", "12", true},
		{"tasmota deep", `{"StatusSNS":{"DS18B20":{"Temperature":19.4}}}`, "StatusSNS.DS18B20.Temperature", "19.4", true},
		{"array index", `{"items":[{"v":"a"},{"v":"b"}]}`, "items.1.v", "b", true},
		{"bool", `{"on":true}`, "on", "true", true},
		{"missing key", `{"state":"ON"}`, "power", "", false},
		{"not json", `ON`, "state", "", false},
		{"path into scalar", `{"state":"ON"}`, "state.deep", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractJSONPath(tt.payload, tt.path)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("extract(%q,%q) = %q,%v; want %q,%v", tt.payload, tt.path, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// Two instances share one topic, each pulling its own JSON field.
func TestDispatchSharedTopicJSONPaths(t *testing.T) {
	// Build a bridge with no client; exercise dispatch via buildTables wiring is
	// covered elsewhere. Here we just assert the extractor split used by dispatch.
	payload := `{"POWER":"ON","Dimmer":42}`
	if v, ok := extractJSONPath(payload, "POWER"); !ok || v != "ON" {
		t.Fatalf("POWER = %q,%v", v, ok)
	}
	if v, ok := extractJSONPath(payload, "Dimmer"); !ok || v != "42" {
		t.Fatalf("Dimmer = %q,%v", v, ok)
	}
}
