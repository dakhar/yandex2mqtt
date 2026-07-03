package openhab

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

// TestScanAllItems runs the full discovery inference over a live openHAB and
// reports how many devices it would produce, their Yandex types, room coverage,
// and any drafts that would fail validation. Guarded by OH_SCAN_URL.
//
//	OH_SCAN_URL=http://host:8080 OH_SCAN_TOKEN_FILE=/path/token \
//	  ./scan.test -test.run TestScanAllItems -test.v
func TestScanAllItems(t *testing.T) {
	url := os.Getenv("OH_SCAN_URL")
	if url == "" {
		t.Skip("set OH_SCAN_URL to scan a live openHAB")
	}
	token := os.Getenv("OH_SCAN_TOKEN")
	if tf := os.Getenv("OH_SCAN_TOKEN_FILE"); token == "" && tf != "" {
		b, err := os.ReadFile(tf)
		if err != nil {
			t.Fatal(err)
		}
		token = strings.TrimSpace(string(b))
	}

	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(url, "/")+"/rest/items?fields=name,type,label,tags,groupNames", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var items []ohItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}

	// No tag filter -> consider all items.
	devices := inferDevices(items, "")

	typeCount := map[string]int{}
	for _, it := range items {
		typeCount[it.Type]++
	}
	yandexType := map[string]int{}
	roomed := 0
	var fails []string
	for _, d := range devices {
		yandexType[d.Type]++
		if d.Room != "" {
			roomed++
		}
		d.ID = "scan"
		if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
			fails = append(fails, d.Name+": "+errs[0].Error())
		}
	}

	t.Logf("=== openHAB scan: %d items -> %d devices ===", len(items), len(devices))
	t.Logf("devices with a room: %d", roomed)

	t.Logf("--- item types (openHAB) ---")
	for _, kv := range sortedCounts(typeCount) {
		t.Logf("  %-22s %d", kv.k, kv.v)
	}
	t.Logf("--- resulting Yandex device types ---")
	for _, kv := range sortedCounts(yandexType) {
		t.Logf("  %-34s %d", kv.k, kv.v)
	}

	if len(fails) > 0 {
		t.Errorf("--- %d drafts FAILED validation ---", len(fails))
		for _, s := range fails {
			t.Errorf("  %s", s)
		}
	} else {
		t.Logf("all %d device drafts pass Yandex validation", len(devices))
	}
}

type kv struct {
	k string
	v int
}

func sortedCounts(m map[string]int) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].v != out[j].v {
			return out[i].v > out[j].v
		}
		return out[i].k < out[j].k
	})
	return out
}
