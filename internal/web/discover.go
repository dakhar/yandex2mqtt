package web

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

// Discoverer reads device drafts from an external source (openHAB). Nil when
// no such source is configured.
type Discoverer interface {
	Discover(ctx context.Context) ([]config.Device, error)
}

// SetDiscoverer wires an openHAB discoverer into the handlers.
func (h *Handlers) SetDiscoverer(d Discoverer) { h.discoverer = d }

// draftView is a discovered device shown on the import page.
type draftView struct {
	Item         string
	Name         string
	Type         string
	Capabilities []string
	Properties   []string
	Exists       bool
}

// Discover lists openHAB items tagged ya2mqtt as device drafts (GET /app/discover).
func (h *Handlers) Discover(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if h.discoverer == nil {
		h.render(w, "discover.html", map[string]any{"User": u, "NotConfigured": true})
		return
	}
	drafts, err := h.discoverer.Discover(r.Context())
	if err != nil {
		h.render(w, "discover.html", map[string]any{"User": u, "Error": err.Error()})
		return
	}

	views := make([]draftView, 0, len(drafts))
	for _, d := range drafts {
		v := draftView{Item: sourceItem(d), Name: d.Name, Type: d.Type}
		for _, c := range d.Capabilities {
			v.Capabilities = append(v.Capabilities, actShort(c.Type))
		}
		for _, p := range d.Properties {
			v.Properties = append(v.Properties, actShort(p.Type))
		}
		views = append(views, v)
	}
	h.render(w, "discover.html", map[string]any{"User": u, "Drafts": views})
}

// AddDiscovered saves a chosen draft as a new device (POST /app/discover/add).
func (h *Handlers) AddDiscovered(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()
	if h.discoverer == nil {
		http.Error(w, "discovery not configured", http.StatusBadRequest)
		return
	}
	_ = r.ParseForm()
	item := r.PostFormValue("item")

	drafts, err := h.discoverer.Discover(ctx)
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
	if err := h.catalog.SaveDevice(ctx, u.ID, nil, d); err != nil {
		http.Error(w, "save", http.StatusInternalServerError)
		return
	}
	h.reload(ctx)
	http.Redirect(w, r, "/app", http.StatusFound)
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
