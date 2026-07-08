package device

import (
	"fmt"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// ValidateCatalog checks the device catalog against the Yandex reference
// schema. Errors are structural problems that should stop startup; warnings are
// suspicious-but-tolerated issues (unknown instance/unit/value) that stay
// forward-compatible with new Yandex additions.
func ValidateCatalog(devs []config.Device) (errs, warns []error) {
	for _, d := range devs {
		where := fmt.Sprintf("device %q", d.ID)

		if !deviceTypes.has(d.Type) {
			warns = append(warns, fmt.Errorf("%s: unknown device type %q", where, d.Type))
		}
		colorCount := 0
		for _, c := range d.Capabilities {
			if actTypeOf(c.Type) == "color_setting" {
				colorCount++
			}
			e, w := validateCapability(where, c)
			errs = append(errs, e...)
			warns = append(warns, w...)
		}
		if colorCount > 1 {
			warns = append(warns, fmt.Errorf("%s: %d color_setting capabilities; Yandex allows one (merge hsv/temperature_k into a single capability)", where, colorCount))
		}
		for _, p := range d.Properties {
			e, w := validateProperty(where, p)
			errs = append(errs, e...)
			warns = append(warns, w...)
		}
	}
	return errs, warns
}

func validateCapability(where string, c config.Capability) (errs, warns []error) {
	if !capabilityTypes.has(c.Type) {
		errs = append(errs, fmt.Errorf("%s: unknown capability type %q", where, c.Type))
		return
	}
	instance, _ := c.Parameters["instance"].(string)

	switch actTypeOf(c.Type) {
	case "on_off":
		// instance is always "on"; nothing to validate.
	case "toggle":
		if instance == "" {
			errs = append(errs, fmt.Errorf("%s: toggle capability missing parameters.instance", where))
		} else if !toggleInstances.has(instance) {
			warns = append(warns, fmt.Errorf("%s: unknown toggle instance %q", where, instance))
		}
	case "range":
		if instance == "" {
			errs = append(errs, fmt.Errorf("%s: range capability missing parameters.instance", where))
			return
		}
		units, ok := rangeUnits[instance]
		if !ok {
			warns = append(warns, fmt.Errorf("%s: unknown range instance %q", where, instance))
			return
		}
		if unit, _ := c.Parameters["unit"].(string); unit != "" && len(units) > 0 && !units.has(unit) {
			warns = append(warns, fmt.Errorf("%s: unit %q not allowed for range %q", where, unit, instance))
		}
	case "mode":
		if instance == "" {
			errs = append(errs, fmt.Errorf("%s: mode capability missing parameters.instance", where))
		} else if !modeInstances.has(instance) {
			warns = append(warns, fmt.Errorf("%s: unknown mode instance %q", where, instance))
		}
		for _, m := range modesOf(c.Parameters) {
			if !modeValues.has(m) {
				warns = append(warns, fmt.Errorf("%s: unknown mode value %q (instance %q)", where, m, instance))
			}
		}
	case "color_setting":
		for _, s := range scenesOf(c.Parameters) {
			if !colorScenes.has(s) {
				warns = append(warns, fmt.Errorf("%s: unknown color scene %q", where, s))
			}
		}
	}
	return
}

func validateProperty(where string, p config.Property) (errs, warns []error) {
	if !propertyTypes.has(p.Type) {
		errs = append(errs, fmt.Errorf("%s: unknown property type %q", where, p.Type))
		return
	}
	instance, _ := p.Parameters["instance"].(string)
	if instance == "" {
		errs = append(errs, fmt.Errorf("%s: property %q missing parameters.instance", where, p.Type))
		return
	}

	switch actTypeOf(p.Type) {
	case "float":
		units, ok := floatUnits[instance]
		if !ok {
			warns = append(warns, fmt.Errorf("%s: unknown float instance %q", where, instance))
			return
		}
		if unit, _ := p.Parameters["unit"].(string); unit != "" && len(units) > 0 && !units.has(unit) {
			warns = append(warns, fmt.Errorf("%s: unit %q not allowed for float %q", where, unit, instance))
		}
	case "event":
		allowed, ok := eventValues[instance]
		if !ok {
			warns = append(warns, fmt.Errorf("%s: unknown event instance %q", where, instance))
			return
		}
		for _, ev := range eventsOf(p.Parameters) {
			if !allowed.has(ev) {
				warns = append(warns, fmt.Errorf("%s: unknown event value %q (instance %q)", where, ev, instance))
			}
		}
	}
	return
}

// modesOf extracts modes[].value strings from mode parameters.
func modesOf(params map[string]any) []string {
	return valuesFrom(params, "modes")
}

// eventsOf extracts events[].value strings from event parameters.
func eventsOf(params map[string]any) []string {
	return valuesFrom(params, "events")
}

func valuesFrom(params map[string]any, key string) []string {
	list, ok := params[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if v, ok := m["value"].(string); ok {
				out = append(out, v)
			}
		}
	}
	return out
}

// scenesOf extracts color_scene.scenes[].id strings.
func scenesOf(params map[string]any) []string {
	cs, ok := params["color_scene"].(map[string]any)
	if !ok {
		return nil
	}
	list, ok := cs["scenes"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				out = append(out, id)
			}
		}
	}
	return out
}
