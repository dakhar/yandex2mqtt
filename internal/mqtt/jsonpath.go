package mqtt

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// extractJSONPath reads a value out of a JSON payload by dot-path and renders it
// as the plain string the device layer expects. Segments navigate objects by
// key and arrays by index, so one topic can feed several instances regardless of
// vendor layout: z2m "state", Tasmota "ENERGY.Power", "StatusSNS.DS18B20.Temperature".
// ok=false when the payload isn't JSON or the path doesn't resolve.
func extractJSONPath(payload, path string) (string, bool) {
	var root any
	if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return "", false
	}
	v := root
	for _, seg := range strings.Split(path, ".") {
		switch node := v.(type) {
		case map[string]any:
			nv, ok := node[seg]
			if !ok {
				return "", false
			}
			v = nv
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return "", false
			}
			v = node[idx]
		default:
			return "", false
		}
	}
	return jsonValueString(v), true
}

// jsonValueString renders a decoded JSON value the way a raw single-value topic
// would look, so downstream value-mapping/parsing is unchanged.
func jsonValueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default: // nested object/array — hand back its JSON
		b, _ := json.Marshal(t)
		return string(b)
	}
}
