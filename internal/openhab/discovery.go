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
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Tags    []string `json:"tags"`
	Members []ohItem `json:"members"` // populated for Group items
}

// feature is one capability or property inferred from an item, with the item it
// binds to. Groups aggregate the features of their member Points.
type feature struct {
	kind     string // "cap" | "prop"
	instance string
	cap      config.Capability
	prop     config.Property
	item     string
}

// Discover reads items tagged ya2mqtt and returns device drafts. Group items
// (openHAB Equipment) become composite devices built from their member Points;
// plain items become single devices.
func (c *Connector) Discover(ctx context.Context) ([]config.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var items []ohItem
	if err := c.getJSON(ctx, "/rest/items?tags="+DiscoveryTag+"&fields=name,type,label,tags", &items); err != nil {
		return nil, err
	}
	var drafts []config.Device
	for _, it := range items {
		if it.Type == "Group" {
			members, err := c.groupMembers(ctx, it.Name)
			if err != nil {
				c.log.Warn("openhab group members", "group", it.Name, "err", err)
				continue
			}
			if d, ok := draftForGroup(it, members); ok {
				drafts = append(drafts, d)
			}
			continue
		}
		if d, ok := draftForItem(it); ok {
			drafts = append(drafts, d)
		}
	}
	return drafts, nil
}

func (c *Connector) groupMembers(ctx context.Context, name string) ([]ohItem, error) {
	var g ohItem
	if err := c.getJSON(ctx, "/rest/items/"+name, &g); err != nil {
		return nil, err
	}
	return g.Members, nil
}

func (c *Connector) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// draftForItem builds a single-item device from an item's features.
func draftForItem(it ohItem) (config.Device, bool) {
	feats, dtype := featuresForItem(it)
	if len(feats) == 0 {
		return config.Device{}, false
	}
	return assemble(itemLabel(it), dtype, feats), true
}

// draftForGroup builds one composite device from a Group's member Points,
// de-duplicating capabilities/properties by instance (first member wins).
func draftForGroup(g ohItem, members []ohItem) (config.Device, bool) {
	var feats []feature
	seen := map[string]bool{}
	fallbackType := ""
	for _, m := range members {
		mf, mt := featuresForItem(m)
		if fallbackType == "" && mt != "" {
			fallbackType = mt
		}
		for _, f := range mf {
			key := f.kind + "|" + f.instance
			if seen[key] {
				continue
			}
			seen[key] = true
			feats = append(feats, f)
		}
	}
	if len(feats) == 0 {
		return config.Device{}, false
	}
	dtype := equipmentType(g.Tags)
	if dtype == "" {
		dtype = fallbackType
	}
	if dtype == "" {
		dtype = "devices.types.other"
	}
	return assemble(itemLabel(g), dtype, feats), true
}

func assemble(name, deviceType string, feats []feature) config.Device {
	d := config.Device{Name: name, Type: deviceType, Transport: "openhab"}
	for _, f := range feats {
		if f.kind == "prop" {
			d.Properties = append(d.Properties, f.prop)
			d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "prop", Instance: f.instance, Item: f.item})
			continue
		}
		d.Capabilities = append(d.Capabilities, f.cap)
		d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "cap", Instance: f.instance, Item: f.item})
	}
	return d
}

// featuresForItem maps an openHAB item to its Yandex features + a suggested
// standalone device type. Returns no features for unsupported items.
func featuresForItem(it ohItem) ([]feature, string) {
	base, _, _ := strings.Cut(it.Type, ":")
	switch base {
	case "Switch":
		return []feature{capFeat("on", capOnOff(), it.Name)}, deviceTypeFor(it.Tags, "devices.types.switch")
	case "Dimmer":
		return []feature{
			capFeat("on", capOnOff(), it.Name),
			capFeat("brightness", capBrightness(), it.Name),
		}, deviceTypeFor(it.Tags, "devices.types.light")
	case "Color":
		return []feature{
			capFeat("on", capOnOff(), it.Name),
			capFeat("brightness", capBrightness(), it.Name),
			capFeat("hsv", capColorHSV(), it.Name),
		}, "devices.types.light"
	case "Rollershutter":
		return []feature{
			capFeat("open", capRange("open", "unit.percent", 0, 100, 1), it.Name),
			capFeat("on", capOnOff(), it.Name),
		}, "devices.types.openable.curtain"
	case "Contact":
		return []feature{propFeat("open", propEventOpen(), it.Name)}, "devices.types.sensor.open"
	case "Number":
		f, dtype, ok := numberFeature(it)
		if !ok {
			return nil, ""
		}
		return []feature{f}, dtype
	}
	return nil, ""
}

// numberFeature classifies a Number item: a temperature Setpoint is a
// controllable range; meters and measurements are float properties.
func numberFeature(it ohItem) (feature, string, bool) {
	_, dim, _ := strings.Cut(it.Type, ":")
	has := func(t string) bool {
		for _, x := range it.Tags {
			if x == t {
				return true
			}
		}
		return false
	}
	// Controllable temperature setpoint -> range capability (thermostat).
	if has("Setpoint") && (dim == "Temperature" || has("Temperature")) {
		return capFeat("temperature", capRange("temperature", "unit.temperature.celsius", 10, 30, 0.5), it.Name),
			"devices.types.thermostat", true
	}
	// Meters (cumulative).
	switch {
	case has("Water"):
		return propFeat("water_meter", propFloat("water_meter", "unit.cubic_meter"), it.Name), "devices.types.smart_meter", true
	case has("Gas"):
		return propFeat("gas_meter", propFloat("gas_meter", "unit.cubic_meter"), it.Name), "devices.types.smart_meter.gas", true
	case has("Electricity"), has("Energy"):
		return propFeat("electricity_meter", propFloat("electricity_meter", "unit.kilowatt_hour"), it.Name), "devices.types.smart_meter.electricity", true
	case has("Heat"):
		return propFeat("heat_meter", propFloat("heat_meter", "unit.gigacalorie"), it.Name), "devices.types.smart_meter.heat", true
	}
	// Measurements (instantaneous).
	switch {
	case dim == "Temperature" || has("Temperature"):
		return propFeat("temperature", propFloat("temperature", "unit.temperature.celsius"), it.Name), "devices.types.sensor.climate", true
	case has("Humidity"):
		return propFeat("humidity", propFloat("humidity", "unit.percent"), it.Name), "devices.types.sensor.climate", true
	case dim == "Pressure" || has("Pressure"):
		return propFeat("pressure", propFloat("pressure", "unit.pressure.mmhg"), it.Name), "devices.types.sensor.climate", true
	case has("Power"):
		return propFeat("power", propFloat("power", "unit.watt"), it.Name), "devices.types.sensor", true
	case has("Illuminance"), has("Light"):
		return propFeat("illumination", propFloat("illumination", "unit.illumination.lux"), it.Name), "devices.types.sensor.illumination", true
	}
	return feature{}, "", false
}

func capFeat(instance string, c config.Capability, item string) feature {
	return feature{kind: "cap", instance: instance, cap: c, item: item}
}
func propFeat(instance string, p config.Property, item string) feature {
	return feature{kind: "prop", instance: instance, prop: p, item: item}
}

func itemLabel(it ohItem) string {
	if it.Label != "" {
		return it.Label
	}
	return it.Name
}

// deviceTypeFor picks a device type from semantic tags, falling back to def.
func deviceTypeFor(tags []string, def string) string {
	for _, t := range tags {
		switch t {
		case "Light", "Lightbulb", "Pendant", "Ceiling", "LightStripe", "Spotlight":
			return "devices.types.light"
		case "PowerOutlet", "WallOutlet":
			return "devices.types.socket"
		case "Fan":
			return "devices.types.ventilation.fan"
		}
	}
	return def
}

// equipmentType maps an openHAB Equipment semantic tag to a Yandex device type.
func equipmentType(tags []string) string {
	for _, t := range tags {
		switch t {
		case "Lightbulb", "Light":
			return "devices.types.light"
		case "PowerOutlet", "WallOutlet":
			return "devices.types.socket"
		case "Blinds":
			return "devices.types.openable.curtain"
		case "Fan":
			return "devices.types.ventilation.fan"
		case "HVAC", "Thermostat", "RadiatorControl":
			return "devices.types.thermostat"
		case "WhiteGood", "WashingMachine":
			return "devices.types.washing_machine"
		case "Television":
			return "devices.types.media_device.tv"
		case "Camera":
			return "devices.types.camera"
		}
	}
	return ""
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
