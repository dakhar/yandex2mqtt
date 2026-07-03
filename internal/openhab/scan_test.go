package openhab

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

// TestScanAllItems runs the discovery inference over EVERY item in a live
// openHAB and reports coverage: which types import, which are skipped, and any
// drafts that would fail Yandex validation. Guarded by OH_SCAN_URL so normal
// test runs skip it.
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

	c := NewConnector(config.OpenHAB{URL: url, Token: token}, discardLog(), nil)
	defer c.Close()
	ctx := context.Background()

	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(url, "/")+"/rest/items?fields=name,type,label,tags", nil)
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

	typeCount := map[string]int{}  // openHAB item type -> count
	skipped := map[string]int{}    // skipped openHAB type -> count
	yandexType := map[string]int{} // resulting Yandex device type -> count
	numberSkips := []string{}      // Number items we couldn't classify
	var validationFails []string   // draft -> first validation error
	inferred := 0

	for _, it := range items {
		typeCount[it.Type]++
		var (
			d  config.Device
			ok bool
		)
		if it.Type == "Group" {
			members, err := c.groupMembers(ctx, it.Name)
			if err == nil {
				d, ok = draftForGroup(it, members)
			}
		} else {
			d, ok = draftForItem(it)
		}
		if !ok {
			skipped[it.Type]++
			base, _, _ := strings.Cut(it.Type, ":")
			if base == "Number" {
				numberSkips = append(numberSkips, it.Name+" tags="+strings.Join(it.Tags, ","))
			}
			continue
		}
		inferred++
		yandexType[d.Type]++
		d.ID = "scan"
		if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
			validationFails = append(validationFails, it.Name+" ("+it.Type+"): "+errs[0].Error())
		}
	}

	t.Logf("=== openHAB scan: %d items total ===", len(items))
	t.Logf("inferable: %d, skipped: %d", inferred, len(items)-inferred)

	t.Logf("--- item types (openHAB) ---")
	for _, kv := range sortedCounts(typeCount) {
		mark := ""
		if skipped[kv.k] == kv.v && kv.v > 0 {
			mark = "  [SKIPPED — not exported]"
		} else if skipped[kv.k] > 0 {
			mark = "  [partially skipped]"
		}
		t.Logf("  %-22s %d%s", kv.k, kv.v, mark)
	}

	t.Logf("--- resulting Yandex device types ---")
	for _, kv := range sortedCounts(yandexType) {
		t.Logf("  %-34s %d", kv.k, kv.v)
	}

	if len(numberSkips) > 0 {
		t.Logf("--- Number items skipped (no recognized dimension/tag) : %d ---", len(numberSkips))
		for i, s := range numberSkips {
			if i >= 15 {
				t.Logf("  ... and %d more", len(numberSkips)-15)
				break
			}
			t.Logf("  %s", s)
		}
	}

	if len(validationFails) > 0 {
		t.Errorf("--- %d drafts FAILED validation ---", len(validationFails))
		for _, s := range validationFails {
			t.Errorf("  %s", s)
		}
	} else {
		t.Logf("all inferred drafts pass Yandex validation ✓")
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
