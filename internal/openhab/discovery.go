package openhab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// DiscoveryTag is the openHAB item tag that marks an item for export to Yandex.
const DiscoveryTag = "ya2mqtt"

type ohItem struct {
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Label string   `json:"label"`
	Tags  []string `json:"tags"`
}

// Discover reads items tagged ya2mqtt and returns device drafts inferred from
// each item's type. Drafts have no id (assigned on save) and Transport=openhab.
func (c *Connector) Discover(ctx context.Context) ([]config.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/rest/items?tags="+DiscoveryTag+"&fields=name,type,label,tags", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discover: status %d", resp.StatusCode)
	}
	var items []ohItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	var drafts []config.Device
	for _, it := range items {
		if d, ok := draftForItem(it); ok {
			drafts = append(drafts, d)
		}
	}
	return drafts, nil
}

// draftForItem infers a Yandex device draft from an openHAB item's type.
func draftForItem(it ohItem) (config.Device, bool) {
	name := it.Label
	if name == "" {
		name = it.Name
	}
	d := config.Device{Name: name, Transport: "openhab"}

	baseType, _, _ := strings.Cut(it.Type, ":") // "Number:Temperature" -> "Number"
	switch baseType {
	case "Switch":
		d.Type = deviceTypeFor(it.Tags, "devices.types.switch")
		d.Capabilities = []config.Capability{capOnOff()}
		bind(&d, "cap", "on", it.Name)
	case "Dimmer":
		d.Type = deviceTypeFor(it.Tags, "devices.types.light")
		d.Capabilities = []config.Capability{capOnOff(), capBrightness()}
		bind(&d, "cap", "on", it.Name)
		bind(&d, "cap", "brightness", it.Name)
	case "Color":
		d.Type = "devices.types.light"
		d.Capabilities = []config.Capability{capOnOff(), capBrightness(), capColorHSV()}
		bind(&d, "cap", "on", it.Name)
		bind(&d, "cap", "brightness", it.Name)
		bind(&d, "cap", "hsv", it.Name)
	case "Rollershutter":
		d.Type = "devices.types.openable.curtain"
		d.Capabilities = []config.Capability{capRange("open", "unit.percent", 0, 100, 1), capOnOff()}
		bind(&d, "cap", "open", it.Name)
		bind(&d, "cap", "on", it.Name)
	case "Number":
		inst, unit, dtype, ok := numberProperty(it.Type, it.Tags)
		if !ok {
			return config.Device{}, false
		}
		d.Type = dtype
		d.Properties = []config.Property{propFloat(inst, unit)}
		bind(&d, "prop", inst, it.Name)
	case "Contact":
		d.Type = "devices.types.sensor.open"
		d.Properties = []config.Property{propEventOpen()}
		bind(&d, "prop", "open", it.Name)
	default:
		return config.Device{}, false
	}
	return d, true
}

func bind(d *config.Device, kind, instance, item string) {
	d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: kind, Instance: instance, Item: item})
}

// deviceTypeFor picks a device type from semantic tags, falling back to def.
func deviceTypeFor(tags []string, def string) string {
	for _, t := range tags {
		switch t {
		case "Light", "Lightbulb", "Pendant", "Ceiling", "LightStripe":
			return "devices.types.light"
		case "PowerOutlet", "WallOutlet":
			return "devices.types.socket"
		case "Fan":
			return "devices.types.ventilation.fan"
		}
	}
	return def
}

// numberProperty maps a Number item (+ dimension/tags) to a float property.
func numberProperty(itemType string, tags []string) (instance, unit, deviceType string, ok bool) {
	_, dim, _ := strings.Cut(itemType, ":")
	has := func(t string) bool {
		for _, x := range tags {
			if x == t {
				return true
			}
		}
		return false
	}
	switch {
	case dim == "Temperature" || has("Temperature"):
		return "temperature", "unit.temperature.celsius", "devices.types.sensor.climate", true
	case dim == "Dimensionless" && has("Humidity"), has("Humidity"):
		return "humidity", "unit.percent", "devices.types.sensor.climate", true
	case dim == "Pressure" || has("Pressure"):
		return "pressure", "unit.pressure.mmhg", "devices.types.sensor.climate", true
	case has("Power"):
		return "power", "unit.watt", "devices.types.sensor", true
	}
	return "", "", "", false
}

// --- capability/property constructors ---

func capOnOff() config.Capability {
	return config.Capability{Type: "devices.capabilities.on_off", Retrievable: true, Reportable: true}
}

func capBrightness() config.Capability {
	return capRange("brightness", "unit.percent", 0, 100, 1)
}

func capRange(instance, unit string, min, max, precision float64) config.Capability {
	return config.Capability{
		Type: "devices.capabilities.range", Retrievable: true, Reportable: true,
		Parameters: map[string]any{
			"instance": instance, "unit": unit,
			"range": map[string]any{"min": min, "max": max, "precision": precision},
		},
	}
}

func capColorHSV() config.Capability {
	return config.Capability{
		Type: "devices.capabilities.color_setting", Retrievable: true, Reportable: true,
		Parameters: map[string]any{"color_model": "hsv"},
	}
}

func propFloat(instance, unit string) config.Property {
	return config.Property{
		Type: "devices.properties.float", Retrievable: true, Reportable: true,
		Parameters: map[string]any{"instance": instance, "unit": unit},
	}
}

func propEventOpen() config.Property {
	return config.Property{
		Type: "devices.properties.event", Retrievable: true, Reportable: true,
		Parameters: map[string]any{
			"instance": "open",
			"events":   []any{map[string]any{"value": "opened"}, map[string]any{"value": "closed"}},
		},
	}
}
