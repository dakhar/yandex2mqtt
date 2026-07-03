package device

import (
	"path/filepath"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// TestRealCatalogConstructs loads the full converted catalog and constructs
// every device, ensuring the YAML shape round-trips into the domain model
// without panics and that definitions/states are producible.
func TestRealCatalogConstructs(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	devs, err := config.LoadDevices(path)
	if err != nil {
		t.Skipf("catalog not available (%v)", err)
	}
	if len(devs) == 0 {
		t.Fatal("catalog is empty")
	}

	for _, c := range devs {
		d := New(c, func(string, string) {}, nil)
		def := d.Definition()
		if def.ID == "" {
			t.Fatalf("device with empty id: %+v", c)
		}
		// Exercise query + subscription extraction on every device.
		_ = d.QueryState()
		_ = d.CapabilityTopics()
		_ = d.PropertyTopics()
	}
	t.Logf("constructed %d devices from real catalog", len(devs))
}
