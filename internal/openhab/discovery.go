package openhab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// DiscoveryTag is the openHAB item tag that marks an item for export to Yandex.
const DiscoveryTag = "ya2mqtt"

type ohItem struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Tags    []string `json:"tags"`
	Members []ohItem `json:"members"` // populated for Group items
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

// Discover reads items tagged ya2mqtt and returns device drafts. Group items
// (openHAB Equipment) become composite devices built from their member Points;
// plain items become single devices.
func (c *Connector) Discover(ctx context.Context) ([]config.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var items []ohItem
	if err := c.getJSON(ctx, "/rest/items?tags="+DiscoveryTag+"&fields=name,type,label,tags", &items); err != nil {
		return nil, err
	}
	var drafts []config.Device
	for _, it := range items {
		if it.Type == "Group" {
			members, err := c.groupMembers(ctx, it.Name)
			if err != nil {
				c.log.Warn("openhab group members", "group", it.Name, "err", err)
				continue
			}
			if d, ok := draftForGroup(it, members); ok {
				drafts = append(drafts, d)
			}
			continue
		}
		if d, ok := draftForItem(it); ok {
			drafts = append(drafts, d)
		}
	}
	return drafts, nil
}

func (c *Connector) groupMembers(ctx context.Context, name string) ([]ohItem, error) {
	var g ohItem
	if err := c.getJSON(ctx, "/rest/items/"+name, &g); err != nil {
		return nil, err
	}
	return g.Members, nil
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

// draftForItem builds a single-item device from an item's features.
func draftForItem(it ohItem) (config.Device, bool) {
	feats, dtype := featuresForItem(it)
	if len(feats) == 0 {
		return config.Device{}, false
	}
	return assemble(itemLabel(it), dtype, feats), true
}

// draftForGroup builds one composite device from a Group's member Points,
// de-duplicating capabilities/properties by instance (first member wins).
func draftForGroup(g ohItem, members []ohItem) (config.Device, bool) {
	var feats []feature
	seen := map[string]bool{}
	fallbackType := ""
	for _, m := range members {
		mf, mt := featuresForItem(m)
		if fallbackType == "" && mt != "" {
			fallbackType = mt
		}
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
	dtype := equipmentType(g.Tags)
	if dtype == "" {
		dtype = fallbackType
	}
	if dtype == "" {
		dtype = "devices.types.other"
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
