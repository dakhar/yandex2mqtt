package openhab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

func TestDraftForItemInference(t *testing.T) {
	tests := []struct {
		item     ohItem
		wantType string
		caps     int
		props    int
		wantInst string // an expected binding instance
	}{
		{ohItem{Name: "Sw", Type: "Switch", Tags: []string{"Light"}}, "devices.types.light", 1, 0, "on"},
		{ohItem{Name: "Dim", Type: "Dimmer"}, "devices.types.light", 2, 0, "brightness"},
		{ohItem{Name: "Col", Type: "Color"}, "devices.types.light", 3, 0, "hsv"},
		{ohItem{Name: "Roll", Type: "Rollershutter"}, "devices.types.openable.curtain", 2, 0, "open"},
		{ohItem{Name: "T", Type: "Number:Temperature"}, "devices.types.sensor.climate", 0, 1, "temperature"},
		{ohItem{Name: "H", Type: "Number", Tags: []string{"Humidity"}}, "devices.types.sensor.climate", 0, 1, "humidity"},
		{ohItem{Name: "C", Type: "Contact"}, "devices.types.sensor.open", 0, 1, "open"},
	}
	for _, tt := range tests {
		d, ok := draftForItem(tt.item)
		if !ok {
			t.Fatalf("%s: not inferred", tt.item.Name)
		}
		if d.Type != tt.wantType {
			t.Fatalf("%s: type=%q want %q", tt.item.Name, d.Type, tt.wantType)
		}
		if len(d.Capabilities) != tt.caps || len(d.Properties) != tt.props {
			t.Fatalf("%s: caps=%d props=%d want %d/%d", tt.item.Name, len(d.Capabilities), len(d.Properties), tt.caps, tt.props)
		}
		if d.Transport != "openhab" {
			t.Fatalf("%s: transport=%q", tt.item.Name, d.Transport)
		}
		found := false
		for _, b := range d.OpenHAB {
			if b.Item != tt.item.Name {
				t.Fatalf("%s: binding item=%q", tt.item.Name, b.Item)
			}
			if b.Instance == tt.wantInst {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no binding for instance %q", tt.item.Name, tt.wantInst)
		}
	}

	// Unknown type -> not inferred.
	if _, ok := draftForItem(ohItem{Name: "S", Type: "String"}); ok {
		t.Fatal("String must not be inferred")
	}
}

// Drafts must validate against the Yandex schema so they can be saved.
func TestDraftsValidate(t *testing.T) {
	for _, it := range []ohItem{
		{Name: "Dim", Type: "Dimmer"}, {Name: "Col", Type: "Color"},
		{Name: "T", Type: "Number:Temperature"}, {Name: "C", Type: "Contact"},
	} {
		d, _ := draftForItem(it)
		if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
			t.Fatalf("%s draft has validation errors: %v", it.Name, errs)
		}
	}
}

func TestNumberMeterAndSetpoint(t *testing.T) {
	// Water meter -> float property water_meter.
	d, ok := draftForItem(ohItem{Name: "Cnt_CWater", Type: "Number", Tags: []string{"Water", "Measurement"}})
	if !ok || d.Type != "devices.types.smart_meter" || len(d.Properties) != 1 {
		t.Fatalf("water meter: ok=%v type=%q props=%d", ok, d.Type, len(d.Properties))
	}
	if d.Properties[0].Parameters["instance"] != "water_meter" || d.OpenHAB[0].Kind != "prop" {
		t.Fatalf("water meter mapping: %+v / %+v", d.Properties[0].Parameters, d.OpenHAB)
	}

	// Temperature setpoint -> controllable range capability (thermostat).
	d2, ok := draftForItem(ohItem{Name: "Th_Set", Type: "Number:Temperature", Tags: []string{"Setpoint"}})
	if !ok || d2.Type != "devices.types.thermostat" || len(d2.Capabilities) != 1 || len(d2.Properties) != 0 {
		t.Fatalf("setpoint: ok=%v type=%q caps=%d props=%d", ok, d2.Type, len(d2.Capabilities), len(d2.Properties))
	}
	if d2.OpenHAB[0].Kind != "cap" || d2.OpenHAB[0].Instance != "temperature" {
		t.Fatalf("setpoint binding: %+v", d2.OpenHAB[0])
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d, d2}); len(errs) > 0 {
		t.Fatalf("meter/setpoint drafts invalid: %v", errs)
	}
}

func TestGroupExpansion(t *testing.T) {
	g := ohItem{Name: "Light_Kitchen", Type: "Group", Label: "Свет кухня", Tags: []string{"ya2mqtt", "Lightbulb"}}
	members := []ohItem{
		{Name: "LK_Power", Type: "Switch", Tags: []string{"Point"}},
		{Name: "LK_Bright", Type: "Dimmer", Tags: []string{"Point"}},
		{Name: "LK_Temp", Type: "Number:Temperature", Tags: []string{"Measurement"}},
	}
	d, ok := draftForGroup(g, members)
	if !ok {
		t.Fatal("group not inferred")
	}
	if d.Type != "devices.types.light" || d.Name != "Свет кухня" {
		t.Fatalf("group device: type=%q name=%q", d.Type, d.Name)
	}
	// on_off (dedup: Switch + Dimmer both offer it -> once) + brightness + temp property.
	if len(d.Capabilities) != 2 || len(d.Properties) != 1 {
		t.Fatalf("composite caps=%d props=%d, want 2/1", len(d.Capabilities), len(d.Properties))
	}
	// Each capability binds to its own member item.
	byInst := map[string]string{}
	for _, b := range d.OpenHAB {
		byInst[b.Instance] = b.Item
	}
	if byInst["on"] != "LK_Power" || byInst["brightness"] != "LK_Bright" || byInst["temperature"] != "LK_Temp" {
		t.Fatalf("composite bindings wrong: %+v", byInst)
	}
	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		t.Fatalf("composite draft invalid: %v", errs)
	}
}

func TestDiscoverReadsTaggedItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tags") != DiscoveryTag {
			t.Errorf("missing tags filter: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"name":"Light_Kitchen","type":"Dimmer","label":"Свет кухня","tags":["ya2mqtt","Light"]},
			{"name":"Ignore","type":"String","tags":["ya2mqtt"]}]`))
	}))
	defer srv.Close()

	c := NewConnector(config.OpenHAB{URL: srv.URL, Token: "t"}, discardLog(), nil)
	defer c.Close()
	drafts, err := c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 {
		t.Fatalf("want 1 draft (String skipped), got %d", len(drafts))
	}
	if drafts[0].Name != "Свет кухня" || len(drafts[0].Capabilities) != 2 {
		t.Fatalf("unexpected draft: %+v", drafts[0])
	}
}
