package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Device is one smart-home device as exposed to Yandex. The shape mirrors the
// original config so the catalog can be migrated 1:1. Note: `instance` values
// such as "on" MUST be quoted in YAML, otherwise YAML 1.1 parses them as
// booleans.
type Device struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description,omitempty"`
	Room         string   `yaml:"room"`
	Type         string   `yaml:"type"`
	AllowedUsers []string `yaml:"allowedUsers"`
	// Transport selects the connector: "mqtt" (default) or "openhab".
	Transport    string         `yaml:"transport,omitempty"`
	MQTT         MQTTMapping    `yaml:"mqtt"`
	ValueMapping []ValueMapping `yaml:"valueMapping,omitempty"`
	Capabilities []Capability   `yaml:"capabilities,omitempty"`
	Properties   []Property     `yaml:"properties,omitempty"`
	// OpenHAB holds per-instance item bindings when Transport == "openhab".
	OpenHAB []OpenHABBinding `yaml:"openhab,omitempty"`
}

// OpenHABBinding ties a capability/property instance to an openHAB item.
type OpenHABBinding struct {
	Kind     string `yaml:"kind"` // "cap" | "prop"
	Instance string `yaml:"instance"`
	Item     string `yaml:"item"`
}

// MQTTMapping ties Yandex instances to MQTT topics.
type MQTTMapping struct {
	Capabilities []MQTTTopic `yaml:"capabilities,omitempty"`
	Properties   []MQTTTopic `yaml:"properties,omitempty"`
}

// MQTTTopic maps a single instance to its set/state topics.
type MQTTTopic struct {
	Instance string `yaml:"instance"`
	Set      string `yaml:"set,omitempty"`
	State    string `yaml:"state"`
}

// Capability is a Yandex capability declaration. Parameters vary by type, so
// they are kept generic here and interpreted in the device domain layer.
type Capability struct {
	Type        string         `yaml:"type"`
	Retrievable bool           `yaml:"retrievable"`
	Reportable  bool           `yaml:"reportable"`
	Parameters  map[string]any `yaml:"parameters,omitempty"`
}

// Property is a Yandex property declaration.
type Property struct {
	Type        string         `yaml:"type"`
	Retrievable bool           `yaml:"retrievable"`
	Reportable  bool           `yaml:"reportable"`
	Parameters  map[string]any `yaml:"parameters,omitempty"`
}

// ValueMapping translates values between Yandex and MQTT for a capability type.
type ValueMapping struct {
	Type    string            `yaml:"type"`
	Mapping []InstanceMapping `yaml:"mapping"`
}

// InstanceMapping is a per-instance translation table. Mapping is a two-row
// table: row 0 holds Yandex values, row 1 holds the corresponding MQTT values.
type InstanceMapping struct {
	Instance string  `yaml:"instance"`
	Mapping  [][]any `yaml:"mapping"`
}

// devicesFile is the top-level YAML document. Anchor-only keys (e.g. x-*) are
// ignored during unmarshaling.
type devicesFile struct {
	Devices []Device `yaml:"devices"`
}

// LoadDevices reads and parses the device catalog YAML file.
func LoadDevices(path string) ([]Device, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc devicesFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return doc.Devices, nil
}
