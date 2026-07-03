package device

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// mapValue applies the device's valueMapping tables in the given direction.
// y2m == true means Yandex -> MQTT (outbound); false means MQTT -> Yandex
// (inbound). Port of getMappedValue in device.js.
func (d *Device) mapValue(val any, actType, instance string, y2m bool) any {
	// color_setting/hsv needs structural conversion, not table lookup.
	if actType == "color_setting" && instance == "hsv" {
		if y2m {
			// Outbound: Yandex {h,s,v} object/JSON -> MQTT "h,s,v" string.
			return hsvToString(val)
		}
		// Inbound: leave the MQTT "h,s,v" string as-is; convertToYandexValue
		// parses it downstream.
		//
		// NOTE: the original ran this branch in BOTH directions, JSON-parsing
		// the inbound "h,s,v" string (which is not valid JSON) and thus always
		// corrupting inbound hsv to 0,0,0. Fixed here to only stringify on the
		// outbound path.
		return val
	}

	vm := findValueMapping(d.valueMapping, actType)
	if vm == nil {
		return val
	}
	im := findInstanceMapping(vm.Mapping, instance)
	if im == nil || len(im.Mapping) < 2 {
		return val
	}

	// Row 0 holds Yandex values, row 1 holds MQTT values.
	var from, to []any
	if y2m {
		from, to = im.Mapping[0], im.Mapping[1]
	} else {
		from, to = im.Mapping[1], im.Mapping[0]
	}

	idx := -1
	if !y2m && instance == "fan_speed" {
		// Inbound fan speed: snap to the nearest configured value first.
		idx = indexOf(from, closest(val, from))
	} else {
		idx = indexOf(from, val)
	}
	if idx >= 0 && idx < len(to) {
		return to[idx]
	}
	return val
}

func findValueMapping(vms []config.ValueMapping, actType string) *config.ValueMapping {
	for i := range vms {
		if vms[i].Type == actType {
			return &vms[i]
		}
	}
	return nil
}

func findInstanceMapping(ims []config.InstanceMapping, instance string) *config.InstanceMapping {
	for i := range ims {
		if ims[i].Instance == instance {
			return &ims[i]
		}
	}
	return nil
}

// closest returns the element of arr numerically nearest to num. On ties it
// keeps the earlier element, matching the JS reduce.
func closest(num any, arr []any) any {
	if len(arr) == 0 {
		return num
	}
	n := toFloatOr(num, 0)
	best := arr[0]
	bestD := math.Abs(toFloatOr(arr[0], 0) - n)
	for _, e := range arr[1:] {
		if d := math.Abs(toFloatOr(e, 0) - n); d < bestD {
			bestD, best = d, e
		}
	}
	return best
}

// indexOf finds val in arr using JS strict-equality semantics.
func indexOf(arr []any, val any) int {
	for i, e := range arr {
		if jsEqual(e, val) {
			return i
		}
	}
	return -1
}

// jsEqual mimics JS === : values are equal only when they share a kind
// (bool/number/string) and value. In particular bool(true) != string("true")
// and number(3) != string("3").
func jsEqual(a, b any) bool {
	ka, va := kindVal(a)
	kb, vb := kindVal(b)
	if ka != kb {
		return false
	}
	return va == vb
}

func kindVal(x any) (string, any) {
	switch v := x.(type) {
	case bool:
		return "bool", v
	case string:
		return "str", v
	case int:
		return "num", float64(v)
	case int32:
		return "num", float64(v)
	case int64:
		return "num", float64(v)
	case float32:
		return "num", float64(v)
	case float64:
		return "num", v
	default:
		return "other", fmt.Sprint(v)
	}
}

// hsvToString converts a Yandex hsv value (object, map, or JSON string) into the
// MQTT "h,s,v" string. Malformed input yields "0,0,0".
func hsvToString(val any) string {
	switch v := val.(type) {
	case HSV:
		return fmt.Sprintf("%s,%s,%s", num(v.H), num(v.S), num(v.V))
	case map[string]any:
		return hsvMapToString(v)
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(v), &m) == nil {
			return hsvMapToString(m)
		}
		return "0,0,0"
	default:
		return "0,0,0"
	}
}

func hsvMapToString(m map[string]any) string {
	h, ok1 := m["h"]
	s, ok2 := m["s"]
	v, ok3 := m["v"]
	if !ok1 || !ok2 || !ok3 {
		return "0,0,0"
	}
	return fmt.Sprintf("%s,%s,%s", num(toFloatOr(h, 0)), num(toFloatOr(s, 0)), num(toFloatOr(v, 0)))
}

// num formats a float without a trailing ".0" (255.0 -> "255", 12.5 -> "12.5").
func num(f float64) string {
	return fmt.Sprintf("%g", f)
}
