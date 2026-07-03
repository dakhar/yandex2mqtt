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

// Connector links a transport (MQTT, openHAB, ...) to the device model: it
// re-subscribes to its device set on reload and publishes actions. Each device
// is routed to the connector named by its Transport.
type Connector interface {
	Transport() string
	Resync(devices []*Device)
	Publish(target, payload string)
}

// Manager owns the live device registry. It rebuilds an immutable snapshot from
// the catalog on Reload and swaps it in atomically, so provider-API reads never
// block on catalog edits. It satisfies the provider API's device store.
type Manager struct {
	loader     CatalogLoader
	connectors map[string]Connector
	log        *slog.Logger
	reg        atomic.Pointer[Registry]
}

// NewManager returns a manager with an empty registry; call Reload to populate.
// connectors are keyed by transport name.
func NewManager(loader CatalogLoader, connectors map[string]Connector, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{loader: loader, connectors: connectors, log: log}
	m.reg.Store(NewRegistry(nil))
	return m
}

// Reload rebuilds the registry from the catalog, wires each device to its
// transport's connector (publisher + subscriptions), and swaps the snapshot in
// atomically.
func (m *Manager) Reload(ctx context.Context) error {
	defs, err := m.loader.LoadAll(ctx)
	if err != nil {
		return err
	}
	devices := make([]*Device, 0, len(defs))
	byTransport := map[string][]*Device{}
	for _, def := range defs {
		d := New(def, nil, m.log)
		if c, ok := m.connectors[d.Transport]; ok {
			d.SetPublisher(c.Publish)
		} else {
			m.log.Warn("no connector for transport", "device", d.ID, "transport", d.Transport)
		}
		byTransport[d.Transport] = append(byTransport[d.Transport], d)
		devices = append(devices, d)
	}

	m.reg.Store(NewRegistry(devices))
	// Re-sync every connector, even ones with no devices (clears old subs).
	for name, c := range m.connectors {
		c.Resync(byTransport[name])
	}
	m.log.Info("catalog reloaded", "devices", len(devices), "transports", len(byTransport))
	return nil
}

func (m *Manager) registry() *Registry { return m.reg.Load() }

// ByID / ForUser / All read the current snapshot (lock-free).
func (m *Manager) ByID(id string) (*Device, bool)  { return m.registry().ByID(id) }
func (m *Manager) ForUser(userID string) []*Device { return m.registry().ForUser(userID) }
func (m *Manager) All() []*Device                  { return m.registry().All() }
