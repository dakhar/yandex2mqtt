package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

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
	ctx := r.Context()
	rooms, err := h.rooms.List(ctx, u.ID)
	if err != nil {
		http.Error(w, "list rooms", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"User": u, "Rooms": rooms}

	// "Configure before add": prefill from an openHAB discovery draft.
	if from := r.URL.Query().Get("from"); from != "" && h.discoverer != nil {
		if drafts, err := h.discoverer.Discover(ctx, h.discoveryTag(ctx, u.ID), h.discoveryFlat(ctx, u.ID)); err == nil {
			if d, ok := draftByItem(drafts, from); ok {
				roomID := ""
				if d.Room != "" {
					if id, err := h.rooms.Ensure(ctx, u.ID, d.Room); err == nil {
						roomID = id
						rooms, _ = h.rooms.List(ctx, u.ID) // include the just-ensured room
						data["Rooms"] = rooms
					}
				}
				devJSON, _ := json.Marshal(toInput(d, roomID))
				data["Device"] = string(devJSON)
			}
		}
	}
	h.render(w, "builder.html", data)
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
	Set         string         `json:"set"`        // MQTT command topic
	State       string         `json:"state"`      // MQTT state topic
	StatePath   string         `json:"state_path"` // optional JSON dot-path into state payload
	Item        string         `json:"item"`       // openHAB item
	Invert      bool           `json:"invert"`     // invert range percentage
	Mapping     []mapPair      `json:"mapping,omitempty"`
}

// deviceInput is the builder's create/update payload.
type deviceInput struct {
	Name         string      `json:"name"`
	Type         string      `json:"type"`
	Transport    string      `json:"transport"` // "mqtt" (default) | "openhab"
	RoomID       string      `json:"room_id"`
	Description  string      `json:"description"`
	Capabilities []capInput  `json:"capabilities"`
	Properties   []capInput  `json:"properties"`
	Error        *errorInput `json:"error,omitempty"`
}

// errorInput is the device status -> error_code binding from the form.
type errorInput struct {
	Item      string      `json:"item"`
	State     string      `json:"state"`
	StatePath string      `json:"state_path"`
	Mapping   []errorPair `json:"mapping"`
}

type errorPair struct {
	Value string `json:"value"`
	Code  string `json:"code"`
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
	if utf8.RuneCountInString(in.Name) > maxDeviceNameLen {
		writeErrors(w, http.StatusBadRequest, "название устройства — не длиннее 25 символов (ограничение Алисы)")
		return in, false
	}
	return in, true
}

// Alice limits device names to 25 and room names to 20 characters.
const (
	maxDeviceNameLen = 25
	maxRoomNameLen   = 20
)

// buildDevice assembles a config.Device from the form input, including value
// mappings (grouped by capability act-type).
func buildDevice(userID, id string, in deviceInput) config.Device {
	transport := in.Transport
	if transport == "" {
		transport = "mqtt"
	}
	openhab := transport == "openhab"
	d := config.Device{
		ID: id, Name: in.Name, Type: in.Type, Description: in.Description,
		Transport: transport, AllowedUsers: []string{userID},
	}
	vmIndex := map[string]int{} // actType -> index in d.ValueMapping
	// addMapping records a value-mapping table for a capability/property instance
	// (grouped by act-type). Event properties use it to translate a raw sensor
	// value to the Yandex event enum (opened/closed, dry/leak, ...).
	addMapping := func(at, instance string, pairs []mapPair) {
		if len(pairs) == 0 {
			return
		}
		var yandex, mqtt []any
		for _, p := range pairs {
			yandex = append(yandex, p.Yandex)
			mqtt = append(mqtt, p.Mqtt)
		}
		im := config.InstanceMapping{Instance: instance, Mapping: [][]any{yandex, mqtt}}
		if idx, ok := vmIndex[at]; ok {
			d.ValueMapping[idx].Mapping = append(d.ValueMapping[idx].Mapping, im)
		} else {
			vmIndex[at] = len(d.ValueMapping)
			d.ValueMapping = append(d.ValueMapping, config.ValueMapping{Type: at, Mapping: []config.InstanceMapping{im}})
		}
	}
	// Yandex allows only one color_setting per device: merge the builder's
	// per-instance color rows (hsv/temperature_k/scene) into a single capability
	// with combined params, while keeping each instance's own binding.
	colorIdx := -1
	for _, c := range in.Capabilities {
		if actType(c.Type) == "color_setting" {
			if colorIdx < 0 {
				merged := map[string]any{}
				for k, v := range c.Params {
					merged[k] = v
				}
				d.Capabilities = append(d.Capabilities, config.Capability{
					Type: c.Type, Retrievable: c.Retrievable, Reportable: c.Reportable, Parameters: merged,
				})
				colorIdx = len(d.Capabilities) - 1
			} else {
				for k, v := range c.Params {
					d.Capabilities[colorIdx].Parameters[k] = v
				}
			}
		} else {
			d.Capabilities = append(d.Capabilities, config.Capability{
				Type: c.Type, Retrievable: c.Retrievable, Reportable: c.Reportable, Parameters: c.Params, Invert: c.Invert,
			})
		}
		if openhab {
			if c.Item != "" {
				d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "cap", Instance: c.Instance, Item: c.Item})
			}
		} else if c.Set != "" || c.State != "" {
			d.MQTT.Capabilities = append(d.MQTT.Capabilities, config.MQTTTopic{Instance: c.Instance, Set: c.Set, State: c.State, StatePath: c.StatePath})
		}
		addMapping(actType(c.Type), c.Instance, c.Mapping)
	}
	for _, p := range in.Properties {
		d.Properties = append(d.Properties, config.Property{
			Type: p.Type, Retrievable: p.Retrievable, Reportable: p.Reportable, Parameters: p.Params,
		})
		if openhab {
			if p.Item != "" {
				d.OpenHAB = append(d.OpenHAB, config.OpenHABBinding{Kind: "prop", Instance: p.Instance, Item: p.Item})
			}
		} else if p.State != "" {
			d.MQTT.Properties = append(d.MQTT.Properties, config.MQTTTopic{Instance: p.Instance, State: p.State, StatePath: p.StatePath})
		}
		addMapping(actType(p.Type), p.Instance, p.Mapping)
	}
	if e := in.Error; e != nil && (e.Item != "" || e.State != "") {
		eb := &config.ErrorBinding{Item: e.Item, State: e.State, StatePath: e.StatePath}
		for _, m := range e.Mapping {
			if m.Value != "" && m.Code != "" {
				eb.Mapping = append(eb.Mapping, config.ErrorPair{Value: m.Value, Code: m.Code})
			}
		}
		if len(eb.Mapping) > 0 {
			d.Error = eb
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

	in := deviceInput{Name: d.Name, Type: d.Type, Transport: d.Transport, RoomID: roomID, Description: d.Description}
	if d.Error != nil {
		ei := &errorInput{Item: d.Error.Item, State: d.Error.State, StatePath: d.Error.StatePath}
		for _, p := range d.Error.Mapping {
			ei.Mapping = append(ei.Mapping, errorPair{Value: p.Value, Code: p.Code})
		}
		in.Error = ei
	}

	if d.Transport == "openhab" {
		itemByKey := map[string]string{}
		for _, b := range d.OpenHAB {
			itemByKey[b.Kind+"|"+b.Instance] = b.Item
		}
		for _, c := range d.Capabilities {
			for _, r := range capRows(c) {
				in.Capabilities = append(in.Capabilities, capInput{
					Type: c.Type, Instance: r.inst, Retrievable: c.Retrievable, Reportable: c.Reportable,
					Params: r.params, Item: itemByKey["cap|"+r.inst], Invert: c.Invert,
					Mapping: mappings[actType(c.Type)+"|"+r.inst],
				})
			}
		}
		for _, p := range d.Properties {
			inst, _ := p.Parameters["instance"].(string)
			in.Properties = append(in.Properties, capInput{
				Type: p.Type, Instance: inst, Retrievable: p.Retrievable, Reportable: p.Reportable,
				Params: p.Parameters, Item: itemByKey["prop|"+inst],
				Mapping: mappings[actType(p.Type)+"|"+inst],
			})
		}
		return in
	}

	for _, c := range d.Capabilities {
		for _, r := range capRows(c) {
			t := setByInst[r.inst]
			in.Capabilities = append(in.Capabilities, capInput{
				Type: c.Type, Instance: r.inst, Retrievable: c.Retrievable, Reportable: c.Reportable,
				Params: r.params, Set: t.Set, State: t.State, StatePath: t.StatePath, Invert: c.Invert,
				Mapping: mappings[actType(c.Type)+"|"+r.inst],
			})
		}
	}
	for _, p := range d.Properties {
		inst, _ := p.Parameters["instance"].(string)
		t := stateByInst[inst]
		in.Properties = append(in.Properties, capInput{
			Type: p.Type, Instance: inst, Retrievable: p.Retrievable, Reportable: p.Reportable,
			Params: p.Parameters, State: t.State, StatePath: t.StatePath,
			Mapping: mappings[actType(p.Type)+"|"+inst],
		})
	}
	return in
}

// capInstance returns a capability's instance for prefill. color_setting keeps
// its instance in the shape of its parameters (temperature_k/color_model/scene),
// not in an "instance" key, so it needs its own derivation.
func capInstance(c config.Capability) string {
	if inst, _ := c.Parameters["instance"].(string); inst != "" {
		return inst
	}
	if actType(c.Type) == "color_setting" {
		return colorInstance(c.Parameters)
	}
	return defaultInstance(c.Type)
}

// capRow is one editable builder row (instance + its params) derived from a
// stored capability. A merged color_setting expands to one row per sub-instance
// (hsv/temperature_k/scene) so each stays visible and editable.
type capRow struct {
	inst   string
	params map[string]any
}

func capRows(c config.Capability) []capRow {
	if actType(c.Type) == "color_setting" {
		subs := colorSubInstances(c.Parameters)
		rows := make([]capRow, 0, len(subs))
		for _, s := range subs {
			rows = append(rows, capRow{inst: s, params: colorSubParams(c.Parameters, s)})
		}
		return rows
	}
	return []capRow{{inst: capInstance(c), params: c.Parameters}}
}

// colorInstance derives a color_setting capability's instance from its params.
func colorInstance(p map[string]any) string {
	switch {
	case hasKey(p, "temperature_k"):
		return "temperature_k"
	case p["color_model"] == "rgb", hasKey(p, "rgb"):
		return "rgb"
	case hasKey(p, "color_scene"), hasKey(p, "scene"):
		return "scene"
	default:
		return "hsv" // color_model hsv or unspecified
	}
}

// colorSubInstances lists the sub-instances present in a (possibly merged)
// color_setting capability's params — Yandex allows one color_setting to carry
// several (color_model + temperature_k + scene). Used to split a merged
// capability back into one editable builder row per instance.
func colorSubInstances(p map[string]any) []string {
	var out []string
	switch {
	case p["color_model"] == "rgb", hasKey(p, "rgb"):
		out = append(out, "rgb")
	case p["color_model"] == "hsv":
		out = append(out, "hsv")
	}
	if hasKey(p, "temperature_k") {
		out = append(out, "temperature_k")
	}
	if hasKey(p, "color_scene") || hasKey(p, "scene") {
		out = append(out, "scene")
	}
	if len(out) == 0 {
		out = append(out, "hsv")
	}
	return out
}

// colorSubParams returns just the params belonging to one color_setting instance.
func colorSubParams(p map[string]any, inst string) map[string]any {
	m := map[string]any{}
	switch inst {
	case "hsv":
		m["color_model"] = "hsv"
	case "rgb":
		if hasKey(p, "rgb") {
			m["rgb"] = p["rgb"]
		} else {
			m["color_model"] = "rgb"
		}
	case "temperature_k":
		m["temperature_k"] = p["temperature_k"]
	case "scene":
		if hasKey(p, "color_scene") {
			m["color_scene"] = p["color_scene"]
		} else if hasKey(p, "scene") {
			m["scene"] = p["scene"]
		}
	}
	return m
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func actType(t string) string {
	parts := strings.Split(t, ".")
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

func defaultInstance(capType string) string {
	switch actType(capType) {
	case "on_off":
		return "on"
	case "color_setting":
		return "hsv" // discovery color drafts use hsv; user can adjust
	}
	return ""
}

func writeErrors(w http.ResponseWriter, status int, msgs ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": msgs})
}
