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
	case "cleanup_mode":
		// A vacuum's cleaning-type selector (dry/wet/mixed). Modes + an optional
		// device value mapping come from `modes="dry_cleaning=vacuum,..."`, else the
		// recommended set (user maps device values in the builder via item hints).
		modes, mapY, mapO := yahomeModeSpec(cfg, "cleanup_mode")
		f := capFeat("cleanup_mode", capMode("cleanup_mode", modes), it.Name)
		f.mapY, f.mapO = mapY, mapO
		return []feature{f}, "devices.types.vacuum_cleaner", true
	case "video_stream":
		return []feature{capFeat("get_stream", capVideoStream(), it.Name)}, "devices.types.camera", true
	}
	return nil, "", false
}

// yahomeModeValues resolves a thermostat mode capability's Yandex-side values.
func yahomeModeValues(cfg map[string]any) []string {
	modes, _, _ := yahomeModeSpec(cfg, "thermostat")
	return modes
}

// yahomeModeSpec parses a `modes="yandex=device,..."` config into the Yandex mode
// values and an optional value mapping (Yandex <-> device). A key without "=dev"
// contributes a mode with no mapping. With no config it falls back to the
// instance's recommended modes and no mapping.
func yahomeModeSpec(cfg map[string]any, instance string) (modes []string, mapY, mapO []any) {
	if raw, _ := cfg["modes"].(string); raw != "" {
		for _, pair := range strings.Split(raw, ",") {
			k, v, hasV := strings.Cut(strings.TrimSpace(pair), "=")
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			modes = append(modes, k)
			if hasV && strings.TrimSpace(v) != "" {
				mapY = append(mapY, k)
				mapO = append(mapO, strings.TrimSpace(v))
			}
		}
		if len(modes) > 0 {
			return modes, mapY, mapO
		}
	}
	return device.RecommendedModes(instance), nil, nil
}
