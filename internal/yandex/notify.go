package yandex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

const (
	callbackURL          = "https://dialogs.yandex.net/api/v1/skills/%s/callback/state"
	discoveryCallbackURL = "https://dialogs.yandex.net/api/v1/skills/%s/callback/discovery"
)

// Notifier pushes device state changes to the Yandex callback API.
type Notifier struct {
	skillID string
	token   string
	userID  string
	client  *http.Client
	log     *slog.Logger
}

// NewNotifier builds a Notifier, or returns nil if notifications are not
// configured (empty skill id / token).
func NewNotifier(cfg config.Yandex, log *slog.Logger) *Notifier {
	if cfg.SkillID == "" || cfg.OAuthToken == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	// Force IPv4 for the callback: on the smart-home network dialogs.yandex.net
	// over IPv6 presents an invalid TLS certificate. The original service did
	// the same via `family: 4`.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		ForceAttemptHTTP2: true,
	}
	return &Notifier{
		skillID: cfg.SkillID,
		token:   cfg.OAuthToken,
		userID:  cfg.UserID,
		client:  &http.Client{Timeout: 5 * time.Second, Transport: transport},
		log:     log,
	}
}

// notifyBody is the callback request payload.
type notifyBody struct {
	TS      int64         `json:"ts"`
	Payload notifyPayload `json:"payload"`
}

type notifyPayload struct {
	UserID  string         `json:"user_id"`
	Devices []notifyDevice `json:"devices"`
}

type notifyDevice struct {
	ID           string            `json:"id"`
	Capabilities []device.CapState `json:"capabilities,omitempty"`
	Properties   []device.CapState `json:"properties,omitempty"`
	ErrorCode    string            `json:"error_code,omitempty"`
}

// OnUpdate is the mqtt.UpdateHook: it fires a callback for the changed instance.
// Nil-safe so it can be passed even when notifications are disabled.
func (n *Notifier) OnUpdate(d *device.Device, instance string, _ bool) {
	if n == nil {
		return
	}
	q := d.QueryState()
	dev := notifyDevice{ID: d.ID}
	if instance == device.ErrorInstance {
		// Status changed: report full state + error_code (empty code clears it).
		dev.Capabilities = q.Capabilities
		dev.Properties = q.Properties
		dev.ErrorCode = d.ErrorCode()
	} else {
		dev.Capabilities = filterByInstance(q.Capabilities, instance)
		dev.Properties = filterByInstance(q.Properties, instance)
		if len(dev.Capabilities) == 0 && len(dev.Properties) == 0 {
			return
		}
	}
	body := notifyBody{
		TS: time.Now().Unix(),
		Payload: notifyPayload{
			UserID:  n.userID,
			Devices: []notifyDevice{dev},
		},
	}
	// Fire-and-forget so MQTT message handling isn't blocked on HTTP.
	go n.sendJSON(fmt.Sprintf(callbackURL, n.skillID), body)
}

// discoveryBody is the /callback/discovery payload: it carries only the user
// whose device list changed; Yandex responds by re-requesting get-devices.
type discoveryBody struct {
	TS      int64 `json:"ts"`
	Payload struct {
		UserID string `json:"user_id"`
	} `json:"payload"`
}

// NotifyDiscovery tells Yandex a user's device list changed so Alice re-syncs
// it without the user manually refreshing the skill. Nil-safe.
func (n *Notifier) NotifyDiscovery(userID string) {
	if n == nil {
		return
	}
	if userID == "" {
		userID = n.userID
	}
	var body discoveryBody
	body.TS = time.Now().Unix()
	body.Payload.UserID = userID
	go n.sendJSON(fmt.Sprintf(discoveryCallbackURL, n.skillID), body)
}

func (n *Notifier) sendJSON(url string, body any) {
	buf, err := json.Marshal(body)
	if err != nil {
		n.log.Error("notify marshal", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		n.log.Error("notify request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "OAuth "+n.token)

	resp, err := n.client.Do(req)
	if err != nil {
		n.log.Error("notify send", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.log.Warn("notify non-2xx", "status", resp.StatusCode, "url", url)
		return
	}
}

func filterByInstance(states []device.CapState, instance string) []device.CapState {
	var out []device.CapState
	for _, s := range states {
		if s.State != nil && s.State.Instance == instance {
			out = append(out, s)
		}
	}
	return out
}
