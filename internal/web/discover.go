package web

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
	"github.com/dakhar/yandex2mqtt/internal/store"
)

// defaultDiscoveryTag is the initial per-user openHAB discovery filter. Users
// can change it (or clear it, meaning "all items") in the discovery settings.
const defaultDiscoveryTag = "ya2mqtt"

// Discoverer reads device drafts from openHAB, optionally filtered by a tag
// ("" = all items). Nil when openHAB isn't configured.
type Discoverer interface {
	Discover(ctx context.Context, tag string) ([]config.Device, error)
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
	drafts, err := h.discoverer.Discover(ctx, tag)
	if err != nil {
		h.render(w, "discover.html", map[string]any{"User": u, "Error": err.Error(), "Tag": tag})
		return
	}

	hide := map[string]bool{}
	if items, err := h.catalog.ImportedOpenHABItems(ctx, u.ID); err == nil {
		for _, it := range items {
			hide[it] = true
		}
	}
	if h.ignore != nil {
		if items, err := h.ignore.List(ctx, u.ID); err == nil {
			for _, it := range items {
				hide[it] = true
			}
		}
	}

	views := make([]draftView, 0, len(drafts))
	for _, d := range drafts {
		src := sourceItem(d)
		if src == "" || hide[src] {
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
	h.render(w, "discover.html", map[string]any{"User": u, "Drafts": views, "Tag": tag})
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

	drafts, err := h.discoverer.Discover(ctx, h.discoveryTag(ctx, u.ID))
	if err != nil {
		http.Redirect(w, r, "/app/discover?err=1", http.StatusFound)
		return
	}
	d, ok := draftByItem(drafts, item)
	if !ok {
		http.Redirect(w, r, "/app/discover", http.StatusFound)
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

func sourceItem(d config.Device) string {
	if len(d.OpenHAB) > 0 {
		return d.OpenHAB[0].Item
	}
	return ""
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
