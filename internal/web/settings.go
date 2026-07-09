package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
	"github.com/dakhar/yandex2mqtt/internal/store"
	"github.com/dakhar/yandex2mqtt/internal/version"
)

// backup is the exportable snapshot of a user's configuration.
type backup struct {
	Version  int               `json:"version"`
	Exported string            `json:"exported"`
	Settings map[string]string `json:"settings,omitempty"`
	Ignore   []string          `json:"ignore,omitempty"`
	Rooms    []string          `json:"rooms,omitempty"`
	Devices  []backupDevice    `json:"devices"`
}

type backupDevice struct {
	Room   string        `json:"room,omitempty"`
	Device config.Device `json:"device"`
}

// Settings renders the settings page (GET /app/settings).
func (h *Handlers) Settings(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()

	tag := ""
	if h.settings != nil {
		tag, _ = h.settings.GetOr(ctx, u.ID, store.SettingDiscoveryTag, defaultDiscoveryTag)
	}
	rooms, _ := h.rooms.List(ctx, u.ID)
	devs, _ := h.catalog.ListDevicesForUser(ctx, u.ID)
	ignoreCount := 0
	if h.ignore != nil {
		if items, err := h.ignore.List(ctx, u.ID); err == nil {
			ignoreCount = len(items)
		}
	}
	data := map[string]any{
		"User":        u,
		"Tag":         tag,
		"OpenHAB":     h.discoverer != nil,
		"RoomCount":   len(rooms),
		"DeviceCount": len(devs),
		"IgnoreCount": ignoreCount,
		"Version":     version.String(),
		"Notice":      r.URL.Query().Get("ok"),
		"Error":       r.URL.Query().Get("err"),
	}
	// Admin-only server (MQTT/openHAB) connection config, with secrets masked.
	if u.IsAdmin && h.effectiveCfg != nil {
		m, o, g := h.effectiveCfg()
		data["ShowServers"] = true
		data["MQTTHost"] = m.Host
		data["MQTTPort"] = m.Port
		data["MQTTUser"] = m.User
		data["MQTTHasPassword"] = m.Password != ""
		data["OHURL"] = o.URL
		data["OHHasToken"] = o.Token != ""
		data["Go2RTCURL"] = g.URL
		data["Go2RTCKeepalive"] = g.KeepaliveSec
	}
	h.render(w, "settings.html", data)
}

// ServerConfig persists admin-edited MQTT/openHAB connection settings and
// reconnects (POST /app/settings/servers, admin only).
func (h *Handlers) ServerConfig(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if !u.IsAdmin || h.configRepo == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()

	set := func(key, val string) { _ = h.configRepo.Set(ctx, key, val) }
	set(store.CfgMQTTHost, strings.TrimSpace(r.PostFormValue("mqtt_host")))
	set(store.CfgMQTTPort, strings.TrimSpace(r.PostFormValue("mqtt_port")))
	set(store.CfgMQTTUser, strings.TrimSpace(r.PostFormValue("mqtt_user")))
	set(store.CfgOpenHABURL, strings.TrimSpace(r.PostFormValue("openhab_url")))
	set(store.CfgGo2RTCURL, strings.TrimSpace(r.PostFormValue("go2rtc_url")))
	set(store.CfgGo2RTCKeep, strings.TrimSpace(r.PostFormValue("go2rtc_keepalive")))
	// Secrets: only overwrite when a new value is supplied (blank = keep current).
	if v := r.PostFormValue("mqtt_password"); v != "" {
		set(store.CfgMQTTPassword, v)
	}
	if v := r.PostFormValue("openhab_token"); v != "" {
		set(store.CfgOpenHABToken, v)
	}

	if h.applyServer != nil {
		if err := h.applyServer(); err != nil {
			h.log.Error("apply server config", "err", err)
			http.Redirect(w, r, "/app/settings?err=apply", http.StatusFound)
			return
		}
	}
	http.Redirect(w, r, "/app/settings?ok=servers", http.StatusFound)
}

// SetDiscoveryTagSettings updates the discovery tag from the settings page.
func (h *Handlers) SetDiscoveryTagSettings(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if h.settings != nil {
		_ = r.ParseForm()
		_ = h.settings.Set(r.Context(), u.ID, store.SettingDiscoveryTag, r.PostFormValue("tag"))
	}
	http.Redirect(w, r, "/app/settings?ok=tag", http.StatusFound)
}

// ExportConfig streams the user's configuration as a JSON backup
// (GET /app/settings/export).
func (h *Handlers) ExportConfig(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	b, err := h.exportUser(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="yandex2mqtt-%s-%s.json"`, u.Name, time.Now().Format("20060102")))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(b)
}

func (h *Handlers) exportUser(ctx context.Context, userID string) (backup, error) {
	b := backup{Version: 1, Exported: time.Now().Format(time.RFC3339), Devices: []backupDevice{}}
	if h.settings != nil {
		b.Settings, _ = h.settings.All(ctx, userID)
	}
	if h.ignore != nil {
		b.Ignore, _ = h.ignore.List(ctx, userID)
	}
	rooms, err := h.rooms.List(ctx, userID)
	if err != nil {
		return b, err
	}
	roomName := map[string]string{}
	for _, rm := range rooms {
		roomName[rm.ID] = rm.Name
		b.Rooms = append(b.Rooms, rm.Name)
	}
	devs, err := h.catalog.ListDevicesForUser(ctx, userID)
	if err != nil {
		return b, err
	}
	for _, bd := range devs {
		d, roomID, ok, err := h.catalog.GetDevice(ctx, userID, bd.ID)
		if err != nil || !ok {
			continue
		}
		d.AllowedUsers = nil // portable: owner is set on import
		b.Devices = append(b.Devices, backupDevice{Room: roomName[roomID], Device: d})
	}
	return b, nil
}

// ImportConfig replaces the user's configuration from an uploaded backup
// (POST /app/settings/import, multipart file "file").
func (h *Handlers) ImportConfig(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/app/settings?err=file", http.StatusFound)
		return
	}
	defer file.Close()
	var b backup
	if err := json.NewDecoder(file).Decode(&b); err != nil {
		http.Redirect(w, r, "/app/settings?err=parse", http.StatusFound)
		return
	}

	h.resetUser(ctx, u.ID)

	for _, name := range b.Rooms {
		_, _ = h.rooms.Ensure(ctx, u.ID, name)
	}
	rooms, _ := h.rooms.List(ctx, u.ID)
	roomID := map[string]string{}
	for _, rm := range rooms {
		roomID[rm.Name] = rm.ID
	}
	for _, bd := range b.Devices {
		d := bd.Device
		if d.ID == "" {
			d.ID = uuid.NewString()
		}
		d.AllowedUsers = []string{u.ID}
		if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
			continue // skip invalid devices rather than fail the whole import
		}
		var rp *string
		if id, ok := roomID[bd.Room]; ok && bd.Room != "" {
			rp = &id
		}
		_ = h.catalog.SaveDevice(ctx, u.ID, rp, d)
	}
	if h.settings != nil {
		for k, v := range b.Settings {
			_ = h.settings.Set(ctx, u.ID, k, v)
		}
	}
	if h.ignore != nil {
		for _, it := range b.Ignore {
			_ = h.ignore.Add(ctx, u.ID, it)
		}
	}
	h.reload(ctx)
	http.Redirect(w, r, "/app/settings?ok=import", http.StatusFound)
}

// ResetConfig wipes the user's rooms, devices, settings and ignore list
// (POST /app/settings/reset).
func (h *Handlers) ResetConfig(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	h.resetUser(r.Context(), u.ID)
	h.reload(r.Context())
	http.Redirect(w, r, "/app/settings?ok=reset", http.StatusFound)
}

func (h *Handlers) resetUser(ctx context.Context, userID string) {
	_ = h.catalog.DeleteAllDevices(ctx, userID)
	_ = h.rooms.DeleteAll(ctx, userID)
	if h.settings != nil {
		_ = h.settings.ClearAll(ctx, userID)
	}
	if h.ignore != nil {
		_ = h.ignore.Clear(ctx, userID)
	}
}
