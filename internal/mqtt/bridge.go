// Package mqtt bridges the MQTT broker to the device domain model: it
// subscribes to device state topics and routes incoming messages to
// Device.UpdateFromMQTT, and publishes capability actions to set topics.
package mqtt

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

// UpdateHook is called after an incoming MQTT message has updated a device's
// state. Used by the Yandex notifier (step 4) to push state changes. May be nil.
type UpdateHook func(d *device.Device, instance string, isProp bool)

// Bridge connects the MQTT broker with the device model.
type Bridge struct {
	client   paho.Client
	log      *slog.Logger
	devices  map[string]*device.Device
	subs     map[string][]subscription // lowercased state topic -> subscriptions
	filters  map[string]byte           // original-case topics to subscribe (qos)
	onUpdate UpdateHook
}

type subscription struct {
	deviceID string
	instance string
	isProp   bool
}

// New builds a Bridge and its subscription tables from the devices.
func New(cfg config.MQTT, devices []*device.Device, log *slog.Logger, onUpdate UpdateHook) *Bridge {
	if log == nil {
		log = slog.Default()
	}
	b := &Bridge{
		log:      log,
		devices:  make(map[string]*device.Device, len(devices)),
		subs:     make(map[string][]subscription),
		filters:  make(map[string]byte),
		onUpdate: onUpdate,
	}
	for _, d := range devices {
		b.devices[d.ID] = d
		for _, t := range d.CapabilityTopics() {
			b.addSub(t.State, d.ID, t.Instance, false)
		}
		for _, t := range d.PropertyTopics() {
			b.addSub(t.State, d.ID, t.Instance, true)
		}
	}

	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", cfg.Host, cfg.Port)).
		SetClientID("yandex2mqtt").
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(b.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			b.log.Warn("mqtt connection lost", "err", err)
		})
	if cfg.User != "" {
		opts.SetUsername(cfg.User)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}
	b.client = paho.NewClient(opts)
	return b
}

func (b *Bridge) addSub(topic, deviceID, instance string, isProp bool) {
	if topic == "" {
		return
	}
	key := strings.ToLower(topic)
	b.subs[key] = append(b.subs[key], subscription{deviceID: deviceID, instance: instance, isProp: isProp})
	b.filters[topic] = 0 // subscribe with the original-case topic, qos 0
}

// Publish sends a payload to an MQTT topic. Wire this into each device via
// Device.SetPublisher so capability actions reach the broker.
func (b *Bridge) Publish(topic, payload string) {
	b.log.Debug("mqtt publish", "topic", topic, "payload", payload)
	b.client.Publish(topic, 0, false, payload)
}

// Connect starts the client. With ConnectRetry the initial connection is
// retried in the background, so a broker that is temporarily down does not
// block startup; it returns an error only on immediate token failure.
func (b *Bridge) Connect() error {
	b.log.Info("mqtt connecting", "topics", len(b.filters))
	token := b.client.Connect()
	// Wait briefly for the first connection; tolerate timeout (retry continues).
	if !token.WaitTimeout(5 * time.Second) {
		b.log.Warn("mqtt not connected yet, retrying in background")
		return nil
	}
	return token.Error()
}

// Disconnect stops the client gracefully.
func (b *Bridge) Disconnect() {
	b.client.Disconnect(250)
}

func (b *Bridge) onConnect(c paho.Client) {
	if len(b.filters) == 0 {
		b.log.Info("mqtt connected (no topics to subscribe)")
		return
	}
	token := c.SubscribeMultiple(b.filters, b.handleMessage)
	token.Wait()
	if err := token.Error(); err != nil {
		b.log.Error("mqtt subscribe failed", "err", err)
		return
	}
	b.log.Info("mqtt subscribed", "topics", len(b.filters))
}

func (b *Bridge) handleMessage(_ paho.Client, msg paho.Message) {
	b.dispatch(msg.Topic(), string(msg.Payload()))
}

// dispatch routes a received message to every device subscribed to the topic.
// Separated from the paho callback so it can be unit-tested without a broker.
func (b *Bridge) dispatch(topic, payload string) {
	subs := b.subs[strings.ToLower(topic)]
	if len(subs) == 0 {
		b.log.Debug("mqtt message for unsubscribed topic", "topic", topic)
		return
	}
	b.log.Debug("mqtt message", "topic", topic, "payload", payload, "subs", len(subs))
	for _, s := range subs {
		d := b.devices[s.deviceID]
		if d == nil {
			continue
		}
		d.UpdateFromMQTT(payload, s.instance, s.isProp)
		if b.onUpdate != nil {
			b.onUpdate(d, s.instance, s.isProp)
		}
	}
}
