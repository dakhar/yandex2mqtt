package device

import (
	"path/filepath"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

func TestValidateCatalog_Errors(t *testing.T) {
	devs := []config.Device{
		{
			ID:   "Bad",
			Type: "devices.types.made_up", // warning
			Capabilities: []config.Capability{
				{Type: "devices.capabilities.nonsense"},                         // error: unknown type
				{Type: "devices.capabilities.range", Parameters: map[string]any{ // error: missing instance
				}},
			},
			Properties: []config.Property{
				{Type: "devices.properties.float"}, // error: missing instance
			},
		},
	}
	errs, warns := ValidateCatalog(devs)
	if len(errs) != 3 {
		t.Fatalf("want 3 errors, got %d: %v", len(errs), errs)
	}
	if len(warns) == 0 {
		t.Fatalf("want at least one warning (unknown device type)")
	}
}

func TestValidateCatalog_Warnings(t *testing.T) {
	devs := []config.Device{
		{
			ID:   "Warn",
			Type: "devices.types.light",
			Capabilities: []config.Capability{
				{Type: "devices.capabilities.range", Parameters: map[string]any{
					"instance": "brightness", "unit": "unit.celsius", // wrong unit -> warn
				}},
				{Type: "devices.capabilities.mode", Parameters: map[string]any{
					"instance": "fan_speed",
					"modes":    []any{map[string]any{"value": "warp_speed"}}, // unknown mode -> warn
				}},
			},
			Properties: []config.Property{
				{Type: "devices.properties.event", Parameters: map[string]any{
					"instance": "open",
					"events":   []any{map[string]any{"value": "ajar"}}, // unknown event -> warn
				}},
			},
		},
	}
	errs, warns := ValidateCatalog(devs)
	if len(errs) != 0 {
		t.Fatalf("want 0 errors, got %v", errs)
	}
	if len(warns) != 3 {
		t.Fatalf("want 3 warnings, got %d: %v", len(warns), warns)
	}
}

// The example catalog must have zero structural errors.
func TestValidateCatalog_RealCatalogClean(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	devs, err := config.LoadDevices(path)
	if err != nil {
		t.Skipf("catalog not available (%v)", err)
	}
	errs, warns := ValidateCatalog(devs)
	if len(errs) != 0 {
		t.Fatalf("real catalog has %d structural error(s): %v", len(errs), errs)
	}
	t.Logf("real catalog: 0 errors, %d warning(s)", len(warns))
	for _, w := range warns {
		t.Logf("  warn: %v", w)
	}
}
