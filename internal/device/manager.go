package device

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

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

	mu           sync.Mutex
	vacuumGroups []*VacuumGroup // active aggregators, replaced each Reload
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

	// Robot-vacuum zones: one shared VacuumGroup per group id, wired to the same
	// connector's publisher as its devices. devices[i] corresponds to defs[i].
	groups := map[string]*VacuumGroup{}
	for i, def := range defs {
		v := def.Vacuum
		if v == nil {
			continue
		}
		g := groups[v.GroupID]
		if g == nil {
			g = NewVacuumGroup(v.CleanTarget, v.OpTarget, v.HomeCmd, time.Duration(v.DebounceMs)*time.Millisecond)
			if c, ok := m.connectors[devices[i].Transport]; ok {
				g.SetPublisher(c.Publish)
			}
			groups[v.GroupID] = g
		}
		devices[i].SetVacuumGroup(g, v.SegmentID)
	}
	m.swapVacuumGroups(groups)

	m.reg.Store(NewRegistry(devices))
	// Re-sync every connector, even ones with no devices (clears old subs).
	for name, c := range m.connectors {
		c.Resync(byTransport[name])
	}
	m.log.Info("catalog reloaded", "devices", len(devices), "transports", len(byTransport))
	return nil
}

// swapVacuumGroups stops the previous reload's aggregators (cancelling pending
// dispatch timers) and installs the new set.
func (m *Manager) swapVacuumGroups(groups map[string]*VacuumGroup) {
	next := make([]*VacuumGroup, 0, len(groups))
	for _, g := range groups {
		next = append(next, g)
	}
	m.mu.Lock()
	old := m.vacuumGroups
	m.vacuumGroups = next
	m.mu.Unlock()
	for _, g := range old {
		g.Stop()
	}
}

func (m *Manager) registry() *Registry { return m.reg.Load() }

// ByID / ForUser / All read the current snapshot (lock-free).
func (m *Manager) ByID(id string) (*Device, bool)  { return m.registry().ByID(id) }
func (m *Manager) ForUser(userID string) []*Device { return m.registry().ForUser(userID) }
func (m *Manager) All() []*Device                  { return m.registry().All() }
