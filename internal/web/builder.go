package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

// Schema serves the Yandex reference schema that drives the builder form
// (GET /app/schema).
func (h *Handlers) Schema(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(device.BuildSchema())
}

// NewDevice renders the builder for a new device (GET /app/devices/new).
func (h *Handlers) NewDevice(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	rooms, err := h.rooms.List(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "list rooms", http.StatusInternalServerError)
		return
	}
	h.render(w, "builder.html", map[string]any{"User": u, "Rooms": rooms})
}

// EditDevice renders the builder prefilled with an existing device
// (GET /app/devices/{id}/edit).
func (h *Handlers) EditDevice(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ctx := r.Context()

	d, roomID, found, err := h.catalog.GetDevice(ctx, u.ID, chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "load device", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	rooms, err := h.rooms.List(ctx, u.ID)
	if err != nil {
		http.Error(w, "list rooms", http.StatusInternalServerError)
		return
	}
	devJSON, _ := json.Marshal(toInput(d, roomID))
	h.render(w, "builder.html", map[string]any{
		"User":   u,
		"Rooms":  rooms,
		"EditID": d.ID,
		"Device": string(devJSON),
	})
}

// mapPair is one Yandex<->MQTT value pair. Values keep their JSON type (a bool
// stays a bool, a number a number) so the strict-equality matching is preserved.
type mapPair struct {
	Yandex any `json:"yandex"`
	Mqtt   any `json:"mqtt"`
}

// capInput is one capability/property from the builder form.
type capInput struct {
	Type        string         `json:"type"`
	Instance    string         `json:"instance"`
	Retrievable bool           `json:"retrievable"`
	Reportable  bool           `json:"reportable"`
	Params      map[string]any `json:"params"`
	Set         string         `json:"set"`
	State       string         `json:"state"`
	Mapping     []mapPair      `json:"mapping,omitempty"`
}

// deviceInput is the builder's create/update payload.
type deviceInput struct {
	Name         string     `json:"name"`
	Type         string     `json:"type"`
	RoomID       string     `json:"room_id"`
	Description  string     `json:"description"`
	Capabilities []capInput `json:"capabilities"`
	Properties   []capInput `json:"properties"`
}

// CreateDevice builds, validates, persists and hot-reloads a new device
// (POST /app/devices). The id is a generated UUID.
func (h *Handlers) CreateDevice(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeInput(w, r)
	if !ok {
		return
	}
	u := auth.UserFrom(r.Context())
	d := buildDevice(u.ID, uuid.NewString(), in)
	h.finishSave(w, r, in.RoomID, d, func(roomPtr *string) (bool, error) {
		return true, h.catalog.SaveDevice(r.Context(), u.ID, roomPtr, d)
	})
}

// UpdateDevice overwrites an existing device (POST /app/devices/{id}).
func (h *Handlers) UpdateDevice(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeInput(w, r)
	if !ok {
		return
	}
	u := auth.UserFrom(r.Context())
	d := buildDevice(u.ID, chi.URLParam(r, "id"), in)
	h.finishSave(w, r, in.RoomID, d, func(roomPtr *string) (bool, error) {
		return h.catalog.ReplaceDevice(r.Context(), u.ID, roomPtr, d)
	})
}

// DeleteDevice removes a device (POST /app/devices/{id}/delete).
func (h *Handlers) DeleteDevice(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	changed, err := h.catalog.DeleteDevice(r.Context(), u.ID, chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "delete", http.StatusInternalServerError)
		return
	}
	if changed {
		h.reload(r.Context())
	}
	http.Redirect(w, r, "/app", http.StatusFound)
}

func decodeInput(w http.ResponseWriter, r *http.Request) (deviceInput, bool) {
	var in deviceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErrors(w, http.StatusBadRequest, "некорректный запрос")
		return in, false
	}
	if in.Name == "" || in.Type == "" {
		writeErrors(w, http.StatusBadRequest, "укажите название и тип устройства")
		return in, false
	}
	return in, true
}

// buildDevice assembles a config.Device from the form input, including value
// mappings (grouped by capability act-type).
func buildDevice(userID, id string, in deviceInput) config.Device {
	d := config.Device{
		ID: id, Name: in.Name, Type: in.Type, Description: in.Description,
		AllowedUsers: []string{userID},
	}
	vmIndex := map[string]int{} // actType -> index in d.ValueMapping
	for _, c := range in.Capabilities {
		d.Capabilities = append(d.Capabilities, config.Capability{
			Type: c.Type, Retrievable: c.Retrievable, Reportable: c.Reportable, Parameters: c.Params,
		})
		if c.Set != "" || c.State != "" {
			d.MQTT.Capabilities = append(d.MQTT.Capabilities, config.MQTTTopic{Instance: c.Instance, Set: c.Set, State: c.State})
		}
		if len(c.Mapping) > 0 {
			actType := actType(c.Type)
			var yandex, mqtt []any
			for _, p := range c.Mapping {
				yandex = append(yandex, p.Yandex)
				mqtt = append(mqtt, p.Mqtt)
			}
			im := config.InstanceMapping{Instance: c.Instance, Mapping: [][]any{yandex, mqtt}}
			if idx, ok := vmIndex[actType]; ok {
				d.ValueMapping[idx].Mapping = append(d.ValueMapping[idx].Mapping, im)
			} else {
				vmIndex[actType] = len(d.ValueMapping)
				d.ValueMapping = append(d.ValueMapping, config.ValueMapping{Type: actType, Mapping: []config.InstanceMapping{im}})
			}
		}
	}
	for _, p := range in.Properties {
		d.Properties = append(d.Properties, config.Property{
			Type: p.Type, Retrievable: p.Retrievable, Reportable: p.Reportable, Parameters: p.Params,
		})
		if p.State != "" {
			d.MQTT.Properties = append(d.MQTT.Properties, config.MQTTTopic{Instance: p.Instance, State: p.State})
		}
	}
	return d
}

// finishSave validates the device, checks room ownership, saves via save, and
// hot-reloads. save reports whether a row was affected (false = not found).
func (h *Handlers) finishSave(w http.ResponseWriter, r *http.Request, roomID string, d config.Device, save func(*string) (bool, error)) {
	ctx := r.Context()
	u := auth.UserFrom(ctx)

	if errs, _ := device.ValidateCatalog([]config.Device{d}); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		writeErrors(w, http.StatusBadRequest, msgs...)
		return
	}

	var roomPtr *string
	if roomID != "" {
		ok, err := h.rooms.BelongsToUser(ctx, u.ID, roomID)
		if err != nil || !ok {
			writeErrors(w, http.StatusBadRequest, "неизвестная комната")
			return
		}
		roomPtr = &roomID
	}

	changed, err := save(roomPtr)
	if err != nil {
		writeErrors(w, http.StatusInternalServerError, "не удалось сохранить устройство")
		return
	}
	if !changed {
		writeErrors(w, http.StatusNotFound, "устройство не найдено")
		return
	}
	h.reload(ctx)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": d.ID})
}

// toInput converts a stored device back into the builder's input shape (for the
// edit form prefill), splitting the MQTT topics onto their capabilities/props.
func toInput(d config.Device, roomID string) deviceInput {
	setByInst := map[string]config.MQTTTopic{}
	for _, t := range d.MQTT.Capabilities {
		setByInst[t.Instance] = t
	}
	stateByInst := map[string]config.MQTTTopic{}
	for _, t := range d.MQTT.Properties {
		stateByInst[t.Instance] = t
	}
	// Value mappings indexed by (actType,instance).
	mappings := map[string][]mapPair{}
	for _, vm := range d.ValueMapping {
		for _, im := range vm.Mapping {
			if len(im.Mapping) == 2 {
				var pairs []mapPair
				for i := 0; i < len(im.Mapping[0]) && i < len(im.Mapping[1]); i++ {
					pairs = append(pairs, mapPair{Yandex: im.Mapping[0][i], Mqtt: im.Mapping[1][i]})
				}
				mappings[vm.Type+"|"+im.Instance] = pairs
			}
		}
	}

	in := deviceInput{Name: d.Name, Type: d.Type, RoomID: roomID, Description: d.Description}
	for _, c := range d.Capabilities {
		inst, _ := c.Parameters["instance"].(string)
		if inst == "" {
			inst = defaultInstance(c.Type)
		}
		t := setByInst[inst]
		in.Capabilities = append(in.Capabilities, capInput{
			Type: c.Type, Instance: inst, Retrievable: c.Retrievable, Reportable: c.Reportable,
			Params: c.Parameters, Set: t.Set, State: t.State,
			Mapping: mappings[actType(c.Type)+"|"+inst],
		})
	}
	for _, p := range d.Properties {
		inst, _ := p.Parameters["instance"].(string)
		t := stateByInst[inst]
		in.Properties = append(in.Properties, capInput{
			Type: p.Type, Instance: inst, Retrievable: p.Retrievable, Reportable: p.Reportable,
			Params: p.Parameters, State: t.State,
		})
	}
	return in
}

func actType(t string) string {
	parts := strings.Split(t, ".")
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

func defaultInstance(capType string) string {
	if actType(capType) == "on_off" {
		return "on"
	}
	return ""
}

func writeErrors(w http.ResponseWriter, status int, msgs ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": msgs})
}
