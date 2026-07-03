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
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Label      string   `json:"label"`
	Tags       []string `json:"tags"`
	GroupNames []string `json:"groupNames"` // parent groups (openHAB semantic hierarchy)
}

// feature is one capability or property inferred from an item, with the item it
// binds to. Groups aggregate the features of their member Points.
type feature struct {
	kind     string // "cap" | "prop"
	instance string
	cap      config.Capability
	prop     config.Property
	item     string
}

// Discover reads the openHAB item model and returns device drafts. tag, when
// non-empty, restricts which items/equipment are exposed (empty = all items).
func (c *Connector) Discover(ctx context.Context, tag string) ([]config.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var items []ohItem
	if err := c.getJSON(ctx, "/rest/items?fields=name,type,label,tags,groupNames", &items); err != nil {
		return nil, err
	}
	return inferDevices(items, tag), nil
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
		d, ok := draftForGroup(it, children[it.Name])
		if !ok {
			continue
		}
		d.Room = roomOf(it)
		drafts = append(drafts, d)
		for _, m := range children[it.Name] {
			consumed[m.Name] = true
		}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
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
	var feats []feature
	seen := map[string]bool{}
	for _, m := range members {
		mf, _ := featuresForItem(m, dtype) // members inherit the equipment context
		for _, f := range mf {
			key := f.kind + "|" + f.instance
			if seen[key] {
				continue
			}
			seen[key] = true
			feats = append(feats, f)
		}
	}
	if len(feats) == 0 {
		return config.Device{}, false
	}
	return assemble(itemLabel(g), dtype, feats), true
}

func assemble(name, deviceType string, feats []feature) config.Device {
	d := config.Device{Name: name, Type: deviceType, Transport: "openhab"}
	for _, f := range feats {
		if f.kind == "prop" {
			d.Properties = append(d.Properties, f.prop)
			d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "prop", Instance: f.instance, Item: f.item})
			continue
		}
		d.Capabilities = append(d.Capabilities, f.cap)
		d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "cap", Instance: f.instance, Item: f.item})
	}
	return d
}
