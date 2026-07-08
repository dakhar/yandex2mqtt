package web

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
	"github.com/dakhar/yandex2mqtt/internal/openhab"
	"github.com/dakhar/yandex2mqtt/internal/version"
)

// Version writes the build version as plain text (GET /version), for health
// checks and quick "what's deployed" queries.
func (h *Handlers) Version(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(version.String()))
}

// vacuumLister is the optional capability (satisfied by *openhab.Connector) to
// discover segment-driven robot vacuums.
type vacuumLister interface {
	VacuumSetups(ctx context.Context) ([]openhab.VacuumSetup, error)
}

type vacuumSegView struct {
	ID   string
	Name string
}

type vacuumView struct {
	Item     string
	Name     string
	Segments []vacuumSegView
}

// VacuumSetupPage lists discoverable segment-vacuums with a per-segment room
// picker (GET /app/discover/vacuum).
func (h *Handlers) VacuumSetupPage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	vl, ok := h.discoverer.(vacuumLister)
	if !ok {
		http.Redirect(w, r, "/app/discover", http.StatusFound)
		return
	}
	setups, err := vl.VacuumSetups(r.Context())
	if err != nil {
		h.render(w, "vacuum.html", map[string]any{"User": u, "Error": err.Error()})
		return
	}
	views := make([]vacuumView, 0, len(setups))
	for _, s := range setups {
		v := vacuumView{Item: s.Item, Name: s.Name}
		for _, seg := range s.Segments {
			v.Segments = append(v.Segments, vacuumSegView{ID: seg.ID, Name: seg.Name})
		}
		views = append(views, v)
	}
	rooms, _ := h.rooms.List(r.Context(), u.ID)
	h.render(w, "vacuum.html", map[string]any{"User": u, "Setups": views, "Rooms": rooms,
		"Done": r.URL.Query().Get("done")})
}

// CreateVacuum materializes a vacuum setup: the parent (in "Дом") plus one on/off
// device per segment with a room assigned (POST /app/discover/vacuum). It is
// idempotent — the group's devices are replaced.
func (h *Handlers) CreateVacuum(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()
	vl, ok := h.discoverer.(vacuumLister)
	if !ok {
		http.Error(w, "discovery not configured", http.StatusBadRequest)
		return
	}
	_ = r.ParseForm()
	item := r.PostFormValue("item")

	setups, err := vl.VacuumSetups(ctx)
	if err != nil {
		http.Redirect(w, r, "/app/discover/vacuum", http.StatusFound)
		return
	}
	var setup *openhab.VacuumSetup
	for i := range setups {
		if setups[i].Item == item {
			setup = &setups[i]
			break
		}
	}
	if setup == nil {
		http.Redirect(w, r, "/app/discover/vacuum", http.StatusFound)
		return
	}

	parentID := vacuumID(item, "")
	// Re-create idempotently: drop any previous parent + zones of this group.
	if err := h.catalog.DeleteVacuumGroup(ctx, u.ID, parentID, item); err != nil {
		http.Error(w, "reset group", http.StatusInternalServerError)
		return
	}

	var toSave []config.Device
	// Parent (whole-house). Its room is the equipment's openHAB Location ancestor
	// (set during inference); empty = unassigned. SegmentID "" marks it whole-house
	// so its on_off routes to the group as START/home.
	parent := setup.Parent
	parent.ID = parentID
	parent.AllowedUsers = []string{u.ID}
	parent.Vacuum = &config.VacuumZone{
		GroupID: item, SegmentID: "",
		CleanTarget: setup.CleanTarget, OpTarget: setup.OpTarget, HomeCmd: "HOME",
	}
	toSave = append(toSave, parent)

	// One zone device per segment that got a room. on_off command is routed to the
	// shared VacuumGroup (segment aggregation); its state mirrors the robot Status.
	for _, seg := range setup.Segments {
		room := strings.TrimSpace(r.PostFormValue("room_" + seg.ID))
		if room == "" {
			continue
		}
		name := strings.TrimSpace(r.PostFormValue("name_" + seg.ID))
		if name == "" {
			name = "Пылесос"
		}
		if utf8.RuneCountInString(name) > maxDeviceNameLen {
			h.render(w, "vacuum.html", map[string]any{"User": u,
				"Error": "Название зоны — не длиннее 25 символов: " + name})
			return
		}
		zone := config.Device{
			ID: vacuumID(item, seg.ID), Name: name, Type: "devices.types.vacuum_cleaner",
			Transport: "openhab", Room: room, AllowedUsers: []string{u.ID},
			Capabilities: []config.Capability{{Type: "devices.capabilities.on_off",
				Retrievable: setup.StatusItem != "", Reportable: setup.StatusItem != ""}},
			// The identity binding (skipped in wiring) keeps each zone distinct.
			OpenHAB: []config.OpenHABBinding{{Kind: "equipment", Item: item + "#" + seg.ID}},
			Vacuum: &config.VacuumZone{
				GroupID: item, SegmentID: seg.ID,
				CleanTarget: setup.CleanTarget, OpTarget: setup.OpTarget, HomeCmd: "HOME",
			},
		}
		if setup.StatusItem != "" {
			zone.OpenHAB = append(zone.OpenHAB, config.OpenHABBinding{Kind: "cap", Instance: "on", Item: setup.StatusItem})
			zone.ValueMapping = []config.ValueMapping{openhab.OnOffStatusMapping()}
		}
		toSave = append(toSave, zone)
	}

	if errs, _ := device.ValidateCatalog(toSave); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		h.render(w, "vacuum.html", map[string]any{"User": u, "Error": strings.Join(msgs, "; ")})
		return
	}
	for _, d := range toSave {
		var roomPtr *string
		if d.Room != "" {
			if rid, err := h.rooms.Ensure(ctx, u.ID, d.Room); err == nil && rid != "" {
				roomPtr = &rid
			}
		}
		if err := h.catalog.SaveDevice(ctx, u.ID, roomPtr, d); err != nil {
			http.Error(w, "save", http.StatusInternalServerError)
			return
		}
	}
	h.reload(ctx)
	http.Redirect(w, r, "/app/discover/vacuum?done=1", http.StatusFound)
}

// vacuumID builds a stable device id for a vacuum parent (segID="") or zone.
func vacuumID(item, segID string) string {
	base := "vac_" + sanitizeID(item)
	if segID == "" {
		return base
	}
	return base + "_" + sanitizeID(segID)
}

// sanitizeID keeps id-safe characters, replacing others with '_'.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
