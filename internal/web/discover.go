package web

import (
	"context"
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
	"github.com/dakhar/yandex2mqtt/internal/openhab"
	"github.com/dakhar/yandex2mqtt/internal/store"
)

// itemLister is the optional capability (satisfied by *openhab.Connector) to list
// the openHAB item model for the builder's autocomplete + mode-hint dropdowns.
type itemLister interface {
	Items(ctx context.Context) ([]openhab.ItemInfo, error)
}

// OpenHABItems returns the openHAB item model as JSON for the builder UI
// (GET /app/openhab/items). It degrades gracefully: when openHAB is absent or
// unreachable it returns an empty list, and the builder falls back to plain
// text inputs.
func (h *Handlers) OpenHABItems(w http.ResponseWriter, r *http.Request) {
	items := []openhab.ItemInfo{}
	if il, ok := h.discoverer.(itemLister); ok && il != nil {
		if got, err := il.Items(r.Context()); err == nil {
			items = got
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

// Go2RTCStreams returns the go2rtc streams as [{name,url}] for the builder's
// video_stream picker (GET /app/go2rtc/streams); url is the HLS URL to store in
// the capability. Empty list when go2rtc is absent or unreachable — the builder
// just hides the picker and the user types a URL as before.
func (h *Handlers) Go2RTCStreams(w http.ResponseWriter, r *http.Request) {
	type stream struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	out := []stream{}
	if h.go2rtc != nil {
		if names, err := h.go2rtc.Streams(r.Context()); err == nil {
			for _, n := range names {
				out = append(out, stream{Name: n, URL: h.go2rtc.StreamURL(n)})
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// defaultDiscoveryTag is the initial per-user openHAB discovery filter. Users
// can change it (or clear it, meaning "all items") in the discovery settings.
const defaultDiscoveryTag = "ya2mqtt"

// Discoverer reads device drafts from openHAB, optionally filtered by a tag
// ("" = all items). Nil when openHAB isn't configured.
type Discoverer interface {
	Discover(ctx context.Context, tag string, flat bool) ([]config.Device, error)
}

// SetDiscovery wires the openHAB discoverer and the per-user settings/ignore
// repositories.
func (h *Handlers) SetDiscovery(d Discoverer, settings *store.SettingsRepo, ignore *store.IgnoreRepo) {
	h.discoverer = d
	h.settings = settings
	h.ignore = ignore
}

type draftView struct {
	Item         string
	Name         string
	Type         string
	Room         string
	Capabilities []string
	Properties   []string
}

// discoveryFlat reports whether the user selected the flat discovery mode.
func (h *Handlers) discoveryFlat(ctx context.Context, userID string) bool {
	if h.settings == nil {
		return false
	}
	v, _ := h.settings.GetOr(ctx, userID, store.SettingDiscoveryMode, "semantic")
	return v == "flat"
}

// SetDiscoveryMode switches a user between semantic and flat discovery
// (POST /app/discover/mode).
func (h *Handlers) SetDiscoveryMode(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if h.settings != nil {
		_ = r.ParseForm()
		mode := r.PostFormValue("mode")
		if mode != "flat" {
			mode = "semantic"
		}
		_ = h.settings.Set(r.Context(), u.ID, store.SettingDiscoveryMode, mode)
	}
	http.Redirect(w, r, "/app/discover", http.StatusFound)
}

func (h *Handlers) discoveryTag(ctx context.Context, userID string) string {
	if h.settings == nil {
		return defaultDiscoveryTag
	}
	tag, err := h.settings.GetOr(ctx, userID, store.SettingDiscoveryTag, defaultDiscoveryTag)
	if err != nil {
		return defaultDiscoveryTag
	}
	return tag
}

// Discover lists openHAB device drafts for the user, hiding items already
// imported or ignored (GET /app/discover).
func (h *Handlers) Discover(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()
	if h.discoverer == nil {
		h.render(w, "discover.html", map[string]any{"User": u, "NotConfigured": true})
		return
	}
	tag := h.discoveryTag(ctx, u.ID)
	flat := h.discoveryFlat(ctx, u.ID)
	drafts, err := h.discoverer.Discover(ctx, tag, flat)
	if err != nil {
		h.render(w, "discover.html", map[string]any{"User": u, "Error": err.Error(), "Tag": tag, "Flat": flat})
		return
	}

	// Segment-driven robot vacuums are configured on their own page (parent +
	// per-room zones); hide their equipment from the flat list so users don't
	// land in the useless single-composite card.
	vacuumItems := map[string]bool{}
	hasVacuums := false
	if vl, ok := h.discoverer.(vacuumLister); ok {
		if setups, err := vl.VacuumSetups(ctx); err == nil {
			for _, s := range setups {
				vacuumItems[s.Item] = true
			}
			hasVacuums = len(setups) > 0
		}
	}

	imported := map[string]bool{}
	if items, err := h.catalog.ImportedOpenHABItems(ctx, u.ID); err == nil {
		for _, it := range items {
			imported[it] = true
		}
	}
	ignored := map[string]bool{}
	if h.ignore != nil {
		if items, err := h.ignore.List(ctx, u.ID); err == nil {
			for _, it := range items {
				ignored[it] = true
			}
		}
	}

	views := make([]draftView, 0, len(drafts))
	for _, d := range drafts {
		src := sourceItem(d)
		if src == "" || ignored[src] || vacuumItems[src] || draftImported(d, imported) {
			continue
		}
		v := draftView{Item: src, Name: d.Name, Type: d.Type, Room: d.Room}
		for _, c := range d.Capabilities {
			v.Capabilities = append(v.Capabilities, actShort(c.Type))
		}
		for _, p := range d.Properties {
			v.Properties = append(v.Properties, actShort(p.Type))
		}
		views = append(views, v)
	}
	h.render(w, "discover.html", map[string]any{
		"User": u, "Drafts": views, "Tag": tag, "Flat": flat, "HasVacuums": hasVacuums,
		"AddError": r.URL.Query().Get("err"),
	})
}

// AddDiscovered saves a chosen draft as a new device, placing it into its
// openHAB location room (POST /app/discover/add).
func (h *Handlers) AddDiscovered(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()
	if h.discoverer == nil {
		http.Error(w, "discovery not configured", http.StatusBadRequest)
		return
	}
	_ = r.ParseForm()
	item := r.PostFormValue("item")

	drafts, err := h.discoverer.Discover(ctx, h.discoveryTag(ctx, u.ID), h.discoveryFlat(ctx, u.ID))
	if err != nil {
		http.Redirect(w, r, "/app/discover?err=1", http.StatusFound)
		return
	}
	d, ok := draftByItem(drafts, item)
	if !ok {
		http.Redirect(w, r, "/app/discover", http.StatusFound)
		return
	}
	// Alice caps names at 25 chars; a long openHAB label can't be imported as-is.
	// Reject with a clear error rather than silently truncating — the user can
	// shorten it via "Настроить".
	if utf8.RuneCountInString(d.Name) > maxDeviceNameLen {
		http.Redirect(w, r, "/app/discover?err=name", http.StatusFound)
		return
	}
	d.ID = uuid.NewString()
	d.AllowedUsers = []string{u.ID}
	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		http.Redirect(w, r, "/app/discover?err=1", http.StatusFound)
		return
	}

	var roomPtr *string
	if d.Room != "" {
		rid, err := h.rooms.Ensure(ctx, u.ID, d.Room)
		if err == nil && rid != "" {
			roomPtr = &rid
		}
	}
	if err := h.catalog.SaveDevice(ctx, u.ID, roomPtr, d); err != nil {
		http.Error(w, "save", http.StatusInternalServerError)
		return
	}
	h.reload(ctx)
	http.Redirect(w, r, "/app/discover", http.StatusFound)
}

// IgnoreDiscovered hides a draft from the user's discovery (POST /app/discover/ignore).
func (h *Handlers) IgnoreDiscovered(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if h.ignore != nil {
		_ = r.ParseForm()
		if item := r.PostFormValue("item"); item != "" {
			_ = h.ignore.Add(r.Context(), u.ID, item)
		}
	}
	http.Redirect(w, r, "/app/discover", http.StatusFound)
}

// SetDiscoveryTag updates the user's discovery tag filter (POST /app/discover/tag).
func (h *Handlers) SetDiscoveryTag(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if h.settings != nil {
		_ = r.ParseForm()
		_ = h.settings.Set(r.Context(), u.ID, store.SettingDiscoveryTag, r.PostFormValue("tag"))
	}
	http.Redirect(w, r, "/app/discover", http.StatusFound)
}

// IgnoredList shows the user's ignored openHAB items (GET /app/discover/ignored).
func (h *Handlers) IgnoredList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var items []string
	if h.ignore != nil {
		items, _ = h.ignore.List(r.Context(), u.ID)
	}
	h.render(w, "ignored.html", map[string]any{"User": u, "Items": items})
}

// Unignore restores a single ignored item to discovery (POST /app/discover/unignore).
func (h *Handlers) Unignore(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if h.ignore != nil {
		_ = r.ParseForm()
		if item := r.PostFormValue("item"); item != "" {
			_ = h.ignore.Remove(r.Context(), u.ID, item)
		}
	}
	http.Redirect(w, r, "/app/discover/ignored", http.StatusFound)
}

// ClearIgnore empties the user's ignore list (POST /app/discover/clear-ignore).
func (h *Handlers) ClearIgnore(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if h.ignore != nil {
		_ = h.ignore.Clear(r.Context(), u.ID)
	}
	http.Redirect(w, r, "/app/discover", http.StatusFound)
}

func draftByItem(drafts []config.Device, item string) (config.Device, bool) {
	for _, d := range drafts {
		if sourceItem(d) == item {
			return d, true
		}
	}
	return config.Device{}, false
}

// sourceItem is a draft's stable openHAB identity: the Equipment group item for
// a composite device, otherwise the single item it binds to.
func sourceItem(d config.Device) string {
	for _, b := range d.OpenHAB {
		if b.Kind == "equipment" {
			return b.Item
		}
	}
	if len(d.OpenHAB) > 0 {
		return d.OpenHAB[0].Item
	}
	return ""
}

// draftImported reports whether any of a draft's member items is already bound
// to one of the user's devices (so a composite whose Points were imported — or
// later edited — is still hidden regardless of its identity marker).
func draftImported(d config.Device, imported map[string]bool) bool {
	for _, b := range d.OpenHAB {
		if b.Kind != "equipment" && b.Item != "" && imported[b.Item] {
			return true
		}
	}
	return false
}

// actShort turns "devices.capabilities.on_off" into "on_off".
func actShort(t string) string {
	parts := splitDots(t)
	if len(parts) >= 3 {
		return parts[2]
	}
	return t
}

func splitDots(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
