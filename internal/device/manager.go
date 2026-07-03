package device

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// CatalogLoader supplies device definitions (satisfied by the store's
// CatalogRepo). Kept as an interface so the domain layer does not depend on the
// persistence package.
type CatalogLoader interface {
	LoadAll(ctx context.Context) ([]config.Device, error)
}

// Bridge is the MQTT side the manager drives on reload (satisfied by
// *mqtt.Bridge): it re-subscribes to the new device set and publishes actions.
type Bridge interface {
	Resync(devices []*Device)
	Publish(topic, payload string)
}

// Manager owns the live device registry. It rebuilds an immutable snapshot from
// the catalog on Reload and swaps it in atomically, so provider-API reads never
// block on catalog edits. It satisfies the provider API's device store.
type Manager struct {
	loader CatalogLoader
	bridge Bridge
	log    *slog.Logger
	reg    atomic.Pointer[Registry]
}

// NewManager returns a manager with an empty registry; call Reload to populate.
func NewManager(loader CatalogLoader, bridge Bridge, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{loader: loader, bridge: bridge, log: log}
	m.reg.Store(NewRegistry(nil))
	return m
}

// Reload rebuilds the registry from the catalog, wires the MQTT publisher into
// each device, swaps the snapshot in atomically, and re-syncs subscriptions.
func (m *Manager) Reload(ctx context.Context) error {
	defs, err := m.loader.LoadAll(ctx)
	if err != nil {
		return err
	}
	devices := make([]*Device, 0, len(defs))
	for _, def := range defs {
		devices = append(devices, New(def, m.bridge.Publish, m.log))
	}
	m.reg.Store(NewRegistry(devices))
	m.bridge.Resync(devices)
	m.log.Info("catalog reloaded", "devices", len(devices))
	return nil
}

func (m *Manager) registry() *Registry { return m.reg.Load() }

// ByID / ForUser / All read the current snapshot (lock-free).
func (m *Manager) ByID(id string) (*Device, bool)  { return m.registry().ByID(id) }
func (m *Manager) ForUser(userID string) []*Device { return m.registry().ForUser(userID) }
func (m *Manager) All() []*Device                  { return m.registry().All() }
