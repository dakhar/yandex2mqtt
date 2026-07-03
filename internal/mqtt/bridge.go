// Package mqtt bridges the MQTT broker to the device domain model: it
// subscribes to device state topics and routes incoming messages to
// Device.UpdateFromMQTT, and publishes capability actions to set topics. The
// subscription set can be updated at runtime via Resync (for catalog edits).
package mqtt

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
)

// UpdateHook is called after an incoming MQTT message has updated a device's
// state. Used by the Yandex notifier to push state changes. May be nil.
type UpdateHook func(d *device.Device, instance string, isProp bool)

// Bridge connects the MQTT broker with the device model. Its subscription
// tables are guarded by mu so Resync (catalog edits) can run concurrently with
// message dispatch.
type Bridge struct {
	client   paho.Client
	log      *slog.Logger
	onUpdate UpdateHook

	mu        sync.RWMutex
	devices   map[string]*device.Device
	subs      map[string][]subscription // lowercased state topic -> subscriptions
	filters   map[string]byte           // original-case topics to subscribe (qos)
	connected bool
}

type subscription struct {
	deviceID string
	instance string
	isProp   bool
}

// New builds a Bridge and connects lazily via Connect. Devices are supplied
// afterwards through Resync, so the bridge does not depend on catalog contents
// at construction time.
func New(cfg config.MQTT, log *slog.Logger, onUpdate UpdateHook) *Bridge {
	if log == nil {
		log = slog.Default()
	}
	b := &Bridge{
		log:      log,
		onUpdate: onUpdate,
		devices:  map[string]*device.Device{},
		subs:     map[string][]subscription{},
		filters:  map[string]byte{},
	}
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", cfg.Host, cfg.Port)).
		SetClientID("yandex2mqtt").
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(b.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			b.mu.Lock()
			b.connected = false
			b.mu.Unlock()
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

// buildTables computes the subscription tables for a device set.
func buildTables(devices []*device.Device) (map[string][]subscription, map[string]byte, map[string]*device.Device) {
	subs := map[string][]subscription{}
	filters := map[string]byte{}
	devMap := make(map[string]*device.Device, len(devices))
	add := func(topic, id, instance string, isProp bool) {
		if topic == "" {
			return
		}
		key := strings.ToLower(topic)
		subs[key] = append(subs[key], subscription{deviceID: id, instance: instance, isProp: isProp})
		filters[topic] = 0
	}
	for _, d := range devices {
		devMap[d.ID] = d
		for _, b := range d.StateBindings() {
			add(b.Source, d.ID, b.Instance, b.IsProp)
		}
	}
	return subs, filters, devMap
}

// Resync replaces the device set and, if connected, applies the subscription
// difference (subscribe new state topics, unsubscribe removed ones). Safe to
// call at runtime after a catalog edit.
func (b *Bridge) Resync(devices []*device.Device) {
	newSubs, newFilters, newDevices := buildTables(devices)

	b.mu.Lock()
	oldFilters := b.filters
	b.subs, b.filters, b.devices = newSubs, newFilters, newDevices
	connected := b.connected
	b.mu.Unlock()

	if !connected {
		// onConnect will subscribe the current filters on (re)connect.
		b.log.Info("mqtt resync (offline)", "topics", len(newFilters))
		return
	}

	var toUnsub []string
	for topic := range oldFilters {
		if _, ok := newFilters[topic]; !ok {
			toUnsub = append(toUnsub, topic)
		}
	}
	toSub := map[string]byte{}
	for topic, qos := range newFilters {
		if _, ok := oldFilters[topic]; !ok {
			toSub[topic] = qos
		}
	}
	if len(toUnsub) > 0 {
		b.client.Unsubscribe(toUnsub...).Wait()
	}
	if len(toSub) > 0 {
		b.client.SubscribeMultiple(toSub, b.handleMessage).Wait()
	}
	b.log.Info("mqtt resync", "topics", len(newFilters), "subscribed", len(toSub), "unsubscribed", len(toUnsub))
}

// Transport identifies this connector.
func (b *Bridge) Transport() string { return "mqtt" }

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
	token := b.client.Connect()
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
	b.mu.Lock()
	b.connected = true
	filters := make(map[string]byte, len(b.filters))
	for t, q := range b.filters {
		filters[t] = q
	}
	b.mu.Unlock()

	if len(filters) == 0 {
		b.log.Info("mqtt connected (no topics yet)")
		return
	}
	token := c.SubscribeMultiple(filters, b.handleMessage)
	token.Wait()
	if err := token.Error(); err != nil {
		b.log.Error("mqtt subscribe failed", "err", err)
		return
	}
	b.log.Info("mqtt subscribed", "topics", len(filters))
}

func (b *Bridge) handleMessage(_ paho.Client, msg paho.Message) {
	b.dispatch(msg.Topic(), string(msg.Payload()))
}

// dispatch routes a received message to every device subscribed to the topic.
// Separated from the paho callback so it can be unit-tested without a broker.
func (b *Bridge) dispatch(topic, payload string) {
	b.mu.RLock()
	subs := b.subs[strings.ToLower(topic)]
	devices := b.devices
	b.mu.RUnlock()

	if len(subs) == 0 {
		b.log.Debug("mqtt message for unsubscribed topic", "topic", topic)
		return
	}
	b.log.Debug("mqtt message", "topic", topic, "payload", payload, "subs", len(subs))
	for _, s := range subs {
		d := devices[s.deviceID]
		if d == nil {
			continue
		}
		d.UpdateFromMQTT(payload, s.instance, s.isProp)
		if b.onUpdate != nil {
			b.onUpdate(d, s.instance, s.isProp)
		}
	}
}
