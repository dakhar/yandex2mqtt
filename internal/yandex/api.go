// Package yandex implements the Yandex Smart Home provider REST API
// (concepts/reference): check, get-devices, query, action, unlink. Handlers are
// thin envelopes around the device domain model.
package yandex

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dakhar/yandex2mqtt/internal/device"
)

// Store is the device lookup the API needs (satisfied by *device.Registry).
type Store interface {
	ByID(id string) (*device.Device, bool)
	ForUser(userID string) []*device.Device
}

// API serves the provider endpoints.
type API struct {
	store    Store
	log      *slog.Logger
	auth     func(http.Handler) http.Handler
	onUnlink func(userID string) // optional token revocation hook (step 5)
	// streamURL rewrites a raw stream URL into a public proxied one for a given
	// request and protocol ("hls"/"mjpeg"); nil leaves get_stream results untouched.
	streamURL func(r *http.Request, raw, protocol string) string
}

// New builds the API. auth is the authentication middleware (StubAuth in step 4,
// real bearer verification in step 5).
func New(store Store, auth func(http.Handler) http.Handler, log *slog.Logger) *API {
	if log == nil {
		log = slog.Default()
	}
	return &API{store: store, log: log, auth: auth}
}

// SetUnlinkHook registers a callback invoked on account unlink.
func (a *API) SetUnlinkHook(f func(userID string)) { a.onUnlink = f }

// SetStreamRewriter registers the function that turns a raw stream URL into a
// public proxied one for its protocol (see internal/stream). Without it,
// get_stream returns the raw URL.
func (a *API) SetStreamRewriter(f func(r *http.Request, raw, protocol string) string) { a.streamURL = f }

// Routes returns the provider router. Mount it at the endpoint base path
// (the legacy service used "/provider", which Yandex has registered).
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	// Availability check: Yandex uses HEAD; allow GET too.
	r.Head("/v1.0", a.check)
	r.Get("/v1.0", a.check)

	r.Group(func(r chi.Router) {
		r.Use(a.auth)
		r.Get("/v1.0/user/devices", a.devices)
		r.Post("/v1.0/user/devices/query", a.query)
		r.Post("/v1.0/user/devices/action", a.action)
		r.Post("/v1.0/user/unlink", a.unlink)
	})
	return r
}

func (a *API) check(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (a *API) devices(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r.Context())
	resp := DevicesResponse{
		RequestID: r.Header.Get("X-Request-Id"),
		Payload:   DevicesPayload{UserID: userID, Devices: []device.Definition{}},
	}
	for _, d := range a.store.ForUser(userID) {
		resp.Payload.Devices = append(resp.Payload.Devices, d.Definition())
	}
	a.writeJSON(w, resp)
}

func (a *API) query(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r.Context())
	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	resp := QueryResponse{RequestID: r.Header.Get("X-Request-Id")}
	for _, rd := range req.Devices {
		d, ok := a.store.ByID(rd.ID)
		if !ok || !d.AllowedTo(userID) {
			resp.Payload.Devices = append(resp.Payload.Devices, device.QueryResult{
				ID: rd.ID, ErrorCode: "DEVICE_NOT_FOUND", ErrorMessage: "device not found",
			})
			continue
		}
		resp.Payload.Devices = append(resp.Payload.Devices, d.QueryState())
	}
	a.writeJSON(w, resp)
}

func (a *API) action(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r.Context())
	var req ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	resp := ActionResponse{RequestID: r.Header.Get("X-Request-Id")}
	for _, rd := range req.Payload.Devices {
		d, ok := a.store.ByID(rd.ID)
		if !ok || !d.AllowedTo(userID) {
			resp.Payload.Devices = append(resp.Payload.Devices, ActionDeviceResult{
				ID: rd.ID,
				ActionResult: &device.ActionResult{
					Status: "ERROR", ErrorCode: "DEVICE_NOT_FOUND", ErrorMessage: "device not found",
				},
			})
			continue
		}
		out := ActionDeviceResult{ID: rd.ID}
		for _, c := range rd.Capabilities {
			res := d.SetCapabilityState(c.State.Value, c.Type, c.State.Instance, c.State.Relative)
			a.rewriteStream(r, &res)
			out.Capabilities = append(out.Capabilities, res)
		}
		resp.Payload.Devices = append(resp.Payload.Devices, out)
	}
	a.writeJSON(w, resp)
}

// rewriteStream replaces a get_stream result's raw HLS URL with a public proxied
// one, so Alice's player reaches the camera through us (CORS + reachability).
func (a *API) rewriteStream(r *http.Request, res *device.ActionCapResult) {
	if a.streamURL == nil || res.Type != "devices.capabilities.video_stream" {
		return
	}
	m, ok := res.State.Value.(map[string]any)
	if !ok {
		return
	}
	if raw, _ := m["stream_url"].(string); raw != "" {
		protocol, _ := m["protocol"].(string)
		m["stream_url"] = a.streamURL(r, raw, protocol)
	}
}

func (a *API) unlink(w http.ResponseWriter, r *http.Request) {
	if a.onUnlink != nil {
		a.onUnlink(UserID(r.Context()))
	}
	a.writeJSON(w, UnlinkResponse{RequestID: r.Header.Get("X-Request-Id")})
}

func (a *API) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		a.log.Error("write response", "err", err)
	}
}
