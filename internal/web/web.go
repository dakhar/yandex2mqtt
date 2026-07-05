// Package web serves the authenticated management UI: the room board (device
// cards dragged between rooms) and, later, the device builder.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/store"
)

// roomTooLongMsg is shown when a room name exceeds Alice's 20-character limit.
var roomTooLongMsg = url.QueryEscape("Название комнаты — не длиннее 20 символов")

//go:embed templates/*.html
var templatesFS embed.FS

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

// Reloader rebuilds the live device registry after a catalog change (satisfied
// by *device.Manager).
type Reloader interface {
	Reload(ctx context.Context) error
}

// Handlers serves the board and room/device mutations.
type Handlers struct {
	rooms       *store.RoomRepo
	catalog     *store.CatalogRepo
	reloader    Reloader
	discoverer  Discoverer // nil when openHAB isn't configured
	settings    *store.SettingsRepo
	ignore      *store.IgnoreRepo
	onDiscovery func(userID string) // notify Yandex the device list changed (nil = off)

	// Admin-editable server (MQTT/openHAB) connection config.
	configRepo   *store.ConfigRepo
	effectiveCfg func() (config.MQTT, config.OpenHAB)
	applyServer  func() error

	log *slog.Logger
}

// SetDiscoveryNotifier registers a hook fired after a user's catalog changes, so
// Yandex re-syncs the device list (Notification API /callback/discovery).
func (h *Handlers) SetDiscoveryNotifier(f func(userID string)) { h.onDiscovery = f }

// SetServerConfig wires the admin server-config editor: the repo to persist to,
// an accessor for the effective (env+DB) config, and an apply hook that
// reconnects MQTT/openHAB.
func (h *Handlers) SetServerConfig(cr *store.ConfigRepo, effective func() (config.MQTT, config.OpenHAB), apply func() error) {
	h.configRepo = cr
	h.effectiveCfg = effective
	h.applyServer = apply
}

// New builds the web handlers.
func New(rooms *store.RoomRepo, catalog *store.CatalogRepo, reloader Reloader, log *slog.Logger) *Handlers {
	if log == nil {
		log = slog.Default()
	}
	return &Handlers{rooms: rooms, catalog: catalog, reloader: reloader, log: log}
}

type boardColumn struct {
	ID        string // "" = unassigned
	Name      string
	Deletable bool
	Devices   []store.BoardDevice
}

// Board renders the room board for the logged-in user (GET /app).
func (h *Handlers) Board(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()

	rooms, err := h.rooms.List(ctx, u.ID)
	if err != nil {
		http.Error(w, "list rooms", http.StatusInternalServerError)
		return
	}
	devs, err := h.catalog.ListDevicesForUser(ctx, u.ID)
	if err != nil {
		http.Error(w, "list devices", http.StatusInternalServerError)
		return
	}
	byRoom := map[string][]store.BoardDevice{}
	for _, d := range devs {
		byRoom[d.RoomID] = append(byRoom[d.RoomID], d)
	}
	cols := make([]boardColumn, 0, len(rooms)+1)
	for _, rm := range rooms {
		cols = append(cols, boardColumn{ID: rm.ID, Name: rm.Name, Deletable: true, Devices: byRoom[rm.ID]})
	}
	cols = append(cols, boardColumn{ID: "", Name: "Без комнаты", Deletable: false, Devices: byRoom[""]})

	h.render(w, "board.html", map[string]any{
		"User":    u,
		"Columns": cols,
		"Error":   r.URL.Query().Get("err"),
	})
}

// CreateRoom adds a room (POST /app/rooms).
func (h *Handlers) CreateRoom(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	_ = r.ParseForm()
	name := r.PostFormValue("name")
	if utf8.RuneCountInString(name) > maxRoomNameLen {
		http.Redirect(w, r, "/app?err="+roomTooLongMsg, http.StatusFound)
		return
	}
	if _, err := h.rooms.Create(r.Context(), u.ID, name); err != nil {
		h.redirectErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/app", http.StatusFound)
}

// RenameRoom renames a room (POST /app/rooms/{id}/rename).
func (h *Handlers) RenameRoom(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	_ = r.ParseForm()
	name := r.PostFormValue("name")
	if utf8.RuneCountInString(name) > maxRoomNameLen {
		http.Redirect(w, r, "/app?err="+roomTooLongMsg, http.StatusFound)
		return
	}
	if err := h.rooms.Rename(r.Context(), u.ID, chi.URLParam(r, "id"), name); err != nil {
		h.redirectErr(w, r, err)
		return
	}
	h.reload(r.Context())
	http.Redirect(w, r, "/app", http.StatusFound)
}

// DeleteRoom removes a room; its devices become unassigned (POST /app/rooms/{id}/delete).
func (h *Handlers) DeleteRoom(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if err := h.rooms.Delete(r.Context(), u.ID, chi.URLParam(r, "id")); err != nil {
		h.redirectErr(w, r, err)
		return
	}
	h.reload(r.Context())
	http.Redirect(w, r, "/app", http.StatusFound)
}

// MoveDevice reassigns a device to a room and sets its position
// (POST /app/devices/{id}/move, JSON body). Returns 204.
func (h *Handlers) MoveDevice(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()

	var body struct {
		RoomID   string `json:"room_id"`
		Position int    `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var roomPtr *string
	if body.RoomID != "" {
		ok, err := h.rooms.BelongsToUser(ctx, u.ID, body.RoomID)
		if err != nil {
			http.Error(w, "room check", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "unknown room", http.StatusBadRequest)
			return
		}
		roomPtr = &body.RoomID
	}

	changed, err := h.catalog.MoveDevice(ctx, u.ID, chi.URLParam(r, "id"), roomPtr, body.Position)
	if err != nil {
		http.Error(w, "move", http.StatusInternalServerError)
		return
	}
	if !changed {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	h.reload(ctx) // provider API must reflect the new room
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) reload(ctx context.Context) {
	if err := h.reloader.Reload(ctx); err != nil {
		h.log.Error("registry reload", "err", err)
		return
	}
	// The catalog changed: tell Yandex to re-discover this user's devices so new
	// or edited devices appear in Alice without a manual skill refresh.
	if h.onDiscovery != nil {
		if u := auth.UserFrom(ctx); u != nil {
			h.onDiscovery(u.ID)
		}
	}
}

func (h *Handlers) redirectErr(w http.ResponseWriter, r *http.Request, err error) {
	msg := "ошибка"
	switch err {
	case store.ErrRoomExists:
		msg = "Комната с таким названием уже есть"
	case store.ErrRoomNameTooLong:
		msg = "Название комнаты — не длиннее 20 символов"
	}
	http.Redirect(w, r, "/app?err="+url.QueryEscape(msg), http.StatusFound)
}

func (h *Handlers) render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t := template.Must(templates.Clone())
	template.Must(t.ParseFS(templatesFS, "templates/"+page))
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
