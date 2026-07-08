package openhab

import (
	"strings"

	"github.com/dakhar/yandex2mqtt/internal/device"
)

// yahome is the openHAB metadata namespace that overrides semantic inference for
// an item: the value declares the Yandex capability/property to expose, config
// carries its parameters. It is the source of truth when present.
const yahomeNS = "yahome"

// yahomeSpec returns an item's yahome metadata value and config (ok=false when
// absent/empty).
func yahomeSpec(it ohItem) (string, map[string]any, bool) {
	m, ok := it.Meta[yahomeNS]
	if !ok || strings.TrimSpace(m.Value) == "" {
		return "", nil, false
	}
	return strings.TrimSpace(m.Value), m.Config, true
}

// hasYahome reports whether an item carries a recognized yahome override.
func hasYahome(it ohItem) bool {
	_, _, ok := yahomeFeatures(it)
	return ok
}

// yahomeFeatures maps an item to Yandex features from its yahome metadata. The
// returned device type is a sensible standalone default (group members ignore
// it). ok=false when there is no recognized yahome spec.
func yahomeFeatures(it ohItem) ([]feature, string, bool) {
	val, cfg, ok := yahomeSpec(it)
	if !ok {
		return nil, "", false
	}
	switch val {
	case "on_off":
		return []feature{capFeat("on", capOnOff(), it.Name)}, "devices.types.switch", true
	case "mode":
		modes := yahomeModeValues(cfg)
		return []feature{capFeat("mode", capMode("thermostat", modes), it.Name)}, "devices.types.thermostat", true
	case "video_stream":
		return []feature{capFeat("get_stream", capVideoStream(), it.Name)}, "devices.types.camera", true
	}
	return nil, "", false
}

// yahomeModeValues resolves a mode capability's values: the Yandex-side keys of
// a `modes="heat=ON,off=OFF"` config, or Yandex's recommended thermostat modes.
func yahomeModeValues(cfg map[string]any) []string {
	if raw, _ := cfg["modes"].(string); raw != "" {
		var out []string
		for _, pair := range strings.Split(raw, ",") {
			k, _, _ := strings.Cut(strings.TrimSpace(pair), "=")
			if k != "" {
				out = append(out, k)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return device.RecommendedModes("thermostat")
}
