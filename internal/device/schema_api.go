package device

import "sort"

// Schema is the serializable form of the Yandex reference tables, used to drive
// the device-builder UI (dropdowns of valid types/instances/units/values).
type Schema struct {
	DeviceTypes  []string           `json:"device_types"`
	Capabilities []CapabilitySchema `json:"capabilities"`
	Properties   []PropertySchema   `json:"properties"`
	ColorScenes  []string           `json:"color_scenes"`
	ModeValues   []string           `json:"mode_values"`
	// ModeRecommended maps a mode instance to Yandex's recommended values, shown
	// first in the builder when that instance is selected.
	ModeRecommended map[string][]string `json:"mode_recommended"`
	// Labels holds RU/EN display names for every schema term.
	Labels Labels `json:"labels"`
	// ErrorCodes are the Yandex device-level error_code values for status binding.
	ErrorCodes []string `json:"error_codes"`
}

// CapabilitySchema describes a capability type and its allowed instances/units.
type CapabilitySchema struct {
	Type      string              `json:"type"`
	Instances []string            `json:"instances"`
	Units     map[string][]string `json:"units,omitempty"` // instance -> allowed units
}

// PropertySchema describes a property type and its allowed instances.
type PropertySchema struct {
	Type      string              `json:"type"`
	Instances []string            `json:"instances"`
	Units     map[string][]string `json:"units,omitempty"`  // float: instance -> units
	Events    map[string][]string `json:"events,omitempty"` // event: instance -> values
}

// BuildSchema returns the reference schema for the builder.
func BuildSchema() Schema {
	return Schema{
		DeviceTypes:     sortedSet(deviceTypes),
		ColorScenes:     sortedSet(colorScenes),
		ModeValues:      sortedSet(modeValues),
		ModeRecommended: modeRecommended,
		Labels:          BuildLabels(),
		ErrorCodes:      errorCodes,
		Capabilities: []CapabilitySchema{
			{Type: "devices.capabilities.on_off", Instances: []string{"on"}},
			{Type: "devices.capabilities.range", Instances: sortedKeys(rangeUnits), Units: setMapToSlices(rangeUnits)},
			{Type: "devices.capabilities.toggle", Instances: sortedSet(toggleInstances)},
			{Type: "devices.capabilities.mode", Instances: sortedSet(modeInstances)},
			{Type: "devices.capabilities.color_setting", Instances: []string{"hsv", "rgb", "temperature_k", "scene"}},
		},
		Properties: []PropertySchema{
			{Type: "devices.properties.float", Instances: sortedKeys(floatUnits), Units: setMapToSlices(floatUnits)},
			{Type: "devices.properties.event", Instances: sortedKeys(eventValues), Events: setMapToSlices(eventValues)},
		},
	}
}

func sortedSet(s set) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]set) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setMapToSlices(m map[string]set) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = sortedSet(v)
	}
	return out
}
