// Package device implements the smart-home device domain model: the value
// conversions and mappings between MQTT payloads and Yandex Smart Home
// states. It is a faithful port of the original device.js, with a few clearly
// marked corrections (see mapping.go).
package device

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// HSV is the Yandex hue/saturation/value color triple.
type HSV struct {
	H float64 `json:"h"`
	S float64 `json:"s"`
	V float64 `json:"v"`
}

// actTypeOf returns the third dotted segment of a capability/property type,
// e.g. "devices.capabilities.on_off" -> "on_off".
func actTypeOf(t string) string {
	parts := strings.Split(t, ".")
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// convertToYandexValue converts an MQTT-side value into the Yandex-typed value
// for the given capability/property type and instance. Port of
// convertToYandexValue in device.js.
func convertToYandexValue(val any, actType, instance string, params map[string]any) any {
	switch actType {
	case "range", "float":
		return toFloatOr(val, 0.0)
	case "toggle", "on_off":
		return toBool(val)
	case "color_setting":
		switch instance {
		case "temperature_k":
			ctMin, ctMax := tempRange(params)
			divider := (ctMax - ctMin) / 100
			return math.Floor(toFloatOr(val, 0)*divider + ctMin)
		case "rgb":
			return toFloatOr(val, 0)
		case "hsv":
			return parseHSV(val)
		default:
			return val
		}
	default:
		return val
	}
}

// toBool mirrors the JS truthiness used for on_off/toggle: "true"/"on"/"1" (case
// insensitive) or a number > 1.
func toBool(val any) bool {
	s := strings.ToLower(strings.TrimSpace(fmt.Sprint(val)))
	if s == "true" || s == "on" || s == "1" {
		return true
	}
	if f, err := toFloat(val); err == nil && f > 1 {
		return true
	}
	return false
}

// parseHSV parses an MQTT "h,s,v" string into an HSV. Anything malformed yields
// a zero color, matching the original.
func parseHSV(val any) HSV {
	s, ok := val.(string)
	if !ok {
		return HSV{}
	}
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return HSV{}
	}
	h, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	sat, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	v, err3 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return HSV{}
	}
	return HSV{H: h, S: sat, V: v}
}

// tempRange extracts the temperature_k min/max from color_setting parameters,
// defaulting to 2700/6500.
func tempRange(params map[string]any) (min, max float64) {
	min, max = 2700, 6500
	tk, ok := params["temperature_k"].(map[string]any)
	if !ok {
		return
	}
	if v, err := toFloat(tk["min"]); err == nil {
		min = v
	}
	if v, err := toFloat(tk["max"]); err == nil {
		max = v
	}
	return
}

// toFloat coerces numeric-ish values (numbers, numeric strings, bools) to float64.
func toFloat(val any) (float64, error) {
	switch v := val.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(v), 64)
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("not numeric: %v", val)
	}
}

func toFloatOr(val any, def float64) float64 {
	if f, err := toFloat(val); err == nil {
		return f
	}
	return def
}
