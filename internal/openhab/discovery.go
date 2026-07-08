package openhab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// DiscoveryTag is the default openHAB item tag used to filter discovery.
const DiscoveryTag = "ya2mqtt"

type ohItem struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	GroupType  string            `json:"groupType"` // base type when Type=="Group" (Group:Switch etc.)
	Label      string            `json:"label"`
	State      string            `json:"state"` // current value (fetched only where needed, e.g. Mapsegments)
	Tags       []string          `json:"tags"`
	GroupNames []string          `json:"groupNames"` // parent groups (openHAB semantic hierarchy)
	Meta       map[string]ohMeta `json:"metadata"`   // e.g. "yahome" override, "ga"
	StateDesc  *ohStateDesc      `json:"stateDescription"`
}

// ohMeta is one openHAB metadata namespace entry (value + config key/values).
type ohMeta struct {
	Value  string         `json:"value"`
	Config map[string]any `json:"config"`
}

// ohStateDesc is an item's state description: numeric bounds (for range/mode
// value derivation) and enumerated options.
type ohStateDesc struct {
	Minimum *float64   `json:"minimum"`
	Maximum *float64   `json:"maximum"`
	Step    *float64   `json:"step"`
	Options []ohOption `json:"options"`
}

type ohOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// feature is one capability or property inferred from an item, with the item it
// binds to. Groups aggregate the features of their member Points.
type feature struct {
	kind     string // "cap" | "prop"
	instance string
	cap      config.Capability
	prop     config.Property
	item     string
	// Optional value mapping for this instance (e.g. fan_speed mode <-> device
	// number): mapY is the Yandex column, mapO the openHAB column.
	mapY []any
	mapO []any
}

// Discover reads the openHAB item model and returns device drafts. tag, when
// non-empty, restricts which items/equipment are exposed (empty = all items).
func (c *Connector) Discover(ctx context.Context, tag string, flat bool) ([]config.Device, error) {
	if !c.configured() {
		return nil, fmt.Errorf("openHAB не настроен — задайте URL и токен в настройках")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var items []ohItem
	if err := c.getJSON(ctx, "/rest/items?metadata=yahome,semantics&fields=name,type,groupType,label,tags,groupNames,metadata,stateDescription", &items); err != nil {
		return nil, err
	}
	if flat {
		return inferDevicesFlat(items, tag), nil
	}
	return inferDevices(items, tag), nil
}

// ItemInfo is a lightweight openHAB item descriptor for the builder UI:
// name/type/label power the item-name autocomplete, Options carries the item's
// enumerated state values (hints) for mode value-mapping dropdowns.
type ItemInfo struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Options []string `json:"options,omitempty"`
}

// Items lists the openHAB item model for the builder (autocomplete + mode hints).
// It is intentionally tolerant: callers treat any error as "no suggestions".
func (c *Connector) Items(ctx context.Context) ([]ItemInfo, error) {
	if !c.configured() {
		return nil, fmt.Errorf("openHAB не настроен")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var items []ohItem
	if err := c.getJSON(ctx, "/rest/items?fields=name,type,label,stateDescription", &items); err != nil {
		return nil, err
	}
	out := make([]ItemInfo, 0, len(items))
	for _, it := range items {
		info := ItemInfo{Name: it.Name, Type: it.Type, Label: it.Label}
		if it.StateDesc != nil {
			for _, o := range it.StateDesc.Options {
				info.Options = append(info.Options, o.Value)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// inferDevices walks the openHAB semantic hierarchy (Location -> Equipment ->
// Point) to produce device drafts:
//   - Equipment groups become one composite device (member Points -> features).
//   - Any other item (including members of non-Equipment groups) becomes its own
//     device.
//   - A Location ancestor sets the device's room.
//
// Only items matching tag (or all, when tag is empty) are exposed.
func inferDevices(items []ohItem, tag string) []config.Device {
	byName := make(map[string]ohItem, len(items))
	children := map[string][]ohItem{}
	locName := map[string]string{} // Location group name -> room label
	for _, it := range items {
		byName[it.Name] = it
		for _, g := range it.GroupNames {
			children[g] = append(children[g], it)
		}
		if it.Type == "Group" && isLocation(it.Tags) {
			locName[it.Name] = itemLabel(it)
		}
	}
	roomOf := func(it ohItem) string { return resolveRoom(it, byName, locName) }
	candidate := func(it ohItem) bool { return tag == "" || hasTag(it.Tags, tag) }

	consumed := map[string]bool{}
	var drafts []config.Device

	// Pass 1: Equipment groups -> composite devices; members are consumed.
	for _, it := range items {
		if it.Type != "Group" || equipmentType(it.Tags) == "" || !candidate(it) {
			continue
		}
		// A Group that openHAB marks as a Point (an aggregation like Group:Switch
		// tagged Light) is a member, not an equipment — skip it here.
		if consumed[it.Name] || isSemanticPoint(it) {
			continue
		}
		d, ok := draftForGroup(it, children[it.Name])
		if !ok {
			continue
		}
		d.Room = roomOf(it)
		drafts = append(drafts, d)
		// Consume the whole subtree: direct member Points and, when a Point is an
		// aggregation Group (Group:Switch etc.), its underlying items too — so none
		// of them resurface as standalone devices.
		var consume func(name string)
		consume = func(name string) {
			for _, m := range children[name] {
				if consumed[m.Name] {
					continue
				}
				consumed[m.Name] = true
				consume(m.Name)
			}
		}
		consume(it.Name)
	}

	// Pass 2: everything else (standalone items and members of non-Equipment
	// groups) -> its own device.
	for _, it := range items {
		if it.Type == "Group" || consumed[it.Name] || !candidate(it) {
			continue
		}
		d, ok := draftForItem(it)
		if !ok {
			continue
		}
		d.Room = roomOf(it)
		drafts = append(drafts, d)
	}
	return drafts
}

// inferDevicesFlat is the "flat tree" mode: every item with a recognized
// capability becomes its own device, with no equipment composition. Leaves of an
// aggregation Group (Group:Switch etc.) are skipped in favour of the group, and
// Locations still set the room.
func inferDevicesFlat(items []ohItem, tag string) []config.Device {
	byName := make(map[string]ohItem, len(items))
	locName := map[string]string{}
	aggregation := map[string]bool{} // Group:X aggregation groups represent their leaves
	for _, it := range items {
		byName[it.Name] = it
		if it.Type == "Group" && isLocation(it.Tags) {
			locName[it.Name] = itemLabel(it)
		}
		if it.GroupType != "" {
			aggregation[it.Name] = true
		}
	}
	candidate := func(it ohItem) bool { return tag == "" || hasTag(it.Tags, tag) }

	var drafts []config.Device
	for _, it := range items {
		if !candidate(it) {
			continue
		}
		leaf := false
		for _, g := range it.GroupNames {
			if aggregation[g] {
				leaf = true
				break
			}
		}
		if leaf {
			continue
		}
		d, ok := draftForItem(it)
		if !ok {
			continue
		}
		d.Room = resolveRoom(it, byName, locName)
		drafts = append(drafts, d)
	}
	return drafts
}

// resolveRoom walks an item's parent groups to the first Location ancestor.
func resolveRoom(it ohItem, byName map[string]ohItem, locName map[string]string) string {
	seen := map[string]bool{}
	var walk func(names []string) string
	walk = func(names []string) string {
		for _, g := range names {
			if seen[g] {
				continue
			}
			seen[g] = true
			if r, ok := locName[g]; ok {
				return r
			}
			if parent, ok := byName[g]; ok {
				if r := walk(parent.GroupNames); r != "" {
					return r
				}
			}
		}
		return ""
	}
	return walk(it.GroupNames)
}

func (c *Connector) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// draftForItem builds a single-item device from an item's features (standalone,
// so no equipment context).
func draftForItem(it ohItem) (config.Device, bool) {
	feats, dtype := featuresForItem(it, "")
	if len(feats) == 0 {
		return config.Device{}, false
	}
	return assemble(itemLabel(it), dtype, feats), true
}

// draftForGroup builds one composite device from an Equipment group: its member
// Points become capabilities/properties (de-duplicated by instance). The device
// type comes from the group's Equipment tag; a group without a suitable
// Equipment tag or with no mappable members is not a device.
func draftForGroup(g ohItem, members []ohItem) (config.Device, bool) {
	dtype := equipmentType(g.Tags)
	if dtype == "" {
		return config.Device{}, false
	}
	// A thermostat's heater relay (a bare on/off Point) is driven by the
	// thermostat, not by Alice: its sensors and setpoint are always exposed, but
	// any on/off or mode control is taken only from members that explicitly opt in
	// via yahome metadata.
	thermostat := dtype == "devices.types.thermostat" || dtype == "devices.types.thermostat.ac"

	// De-duplicate features by kind|instance, but prefer a member with an explicit
	// Property tag (or yahome) over one mapped by a fallback default, so a real
	// "Brightness" point wins the brightness slot over an under-tagged helper.
	type scored struct {
		f        feature
		specific bool
	}
	best := map[string]scored{}
	var order []string
	for _, m := range members {
		mf, _ := featuresForItem(m, dtype) // members inherit the equipment context
		if thermostat && !hasYahome(m) {
			mf = sensorsAndSetpoint(mf)
		}
		specific := propertyTag(m.Tags) != "" || hasYahome(m)
		for _, f := range mf {
			key := f.kind + "|" + f.instance
			cur, exists := best[key]
			switch {
			case !exists:
				best[key] = scored{f, specific}
				order = append(order, key)
			case specific && !cur.specific:
				best[key] = scored{f, specific}
			}
		}
	}
	feats := make([]feature, 0, len(order))
	for _, k := range order {
		feats = append(feats, best[k].f)
	}
	if len(feats) == 0 {
		return config.Device{}, false
	}
	d := assemble(itemLabel(g), dtype, feats)
	// The Equipment group item is the composite device's stable identity (used by
	// discovery for display, ignore, and add/configure), distinct from the member
	// Point items its capabilities bind to. Kept as an "equipment" binding, which
	// wiring and persistence skip.
	d.OpenHAB = append([]config.OpenHABBinding{{Kind: "equipment", Item: g.Name}}, d.OpenHAB...)
	return d, true
}

// sensorsAndSetpoint keeps only float properties (sensors) and range
// capabilities (setpoints), dropping on/off and other controls.
func sensorsAndSetpoint(feats []feature) []feature {
	var out []feature
	for _, f := range feats {
		if f.kind == "prop" && f.prop.Type == "devices.properties.float" ||
			f.kind == "cap" && f.cap.Type == "devices.capabilities.range" {
			out = append(out, f)
		}
	}
	return out
}

func assemble(name, deviceType string, feats []feature) config.Device {
	d := config.Device{Name: name, Type: deviceType, Transport: "openhab"}
	vmIdx := map[string]int{} // actType -> index in d.ValueMapping
	for _, f := range feats {
		if f.kind == "prop" {
			d.Properties = append(d.Properties, f.prop)
			d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "prop", Instance: f.instance, Item: f.item})
		} else if f.cap.Type == "devices.capabilities.color_setting" {
			// Yandex allows only one color_setting: merge params (color_model +
			// temperature_k + scene) into a single capability, but keep each
			// instance's binding so hsv/temperature_k route to their own items.
			ci := -1
			for i := range d.Capabilities {
				if d.Capabilities[i].Type == "devices.capabilities.color_setting" {
					ci = i
					break
				}
			}
			if ci < 0 {
				merged := map[string]any{}
				for k, v := range f.cap.Parameters {
					merged[k] = v
				}
				cp := f.cap
				cp.Parameters = merged
				d.Capabilities = append(d.Capabilities, cp)
			} else {
				for k, v := range f.cap.Parameters {
					d.Capabilities[ci].Parameters[k] = v
				}
			}
			d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "cap", Instance: f.instance, Item: f.item})
		} else {
			d.Capabilities = append(d.Capabilities, f.cap)
			d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "cap", Instance: f.instance, Item: f.item})
		}
		if len(f.mapY) > 0 {
			at := capActType(f.cap.Type)
			im := config.InstanceMapping{Instance: f.instance, Mapping: [][]any{f.mapY, f.mapO}}
			if i, ok := vmIdx[at]; ok {
				d.ValueMapping[i].Mapping = append(d.ValueMapping[i].Mapping, im)
			} else {
				vmIdx[at] = len(d.ValueMapping)
				d.ValueMapping = append(d.ValueMapping, config.ValueMapping{Type: at, Mapping: []config.InstanceMapping{im}})
			}
		}
	}
	return d
}
