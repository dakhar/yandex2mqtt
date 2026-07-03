package device

// Registry is an in-memory device index with per-user access filtering. It will
// be replaced by a database-backed store when multitenancy lands.
type Registry struct {
	byID map[string]*Device
	all  []*Device
}

// NewRegistry indexes the given devices by id.
func NewRegistry(devices []*Device) *Registry {
	r := &Registry{byID: make(map[string]*Device, len(devices)), all: devices}
	for _, d := range devices {
		r.byID[d.ID] = d
	}
	return r
}

// ByID returns the device with the given id.
func (r *Registry) ByID(id string) (*Device, bool) {
	d, ok := r.byID[id]
	return d, ok
}

// All returns every device.
func (r *Registry) All() []*Device { return r.all }

// ForUser returns the devices the given user is allowed to access.
func (r *Registry) ForUser(userID string) []*Device {
	var out []*Device
	for _, d := range r.all {
		if d.AllowedTo(userID) {
			out = append(out, d)
		}
	}
	return out
}

// AllowedTo reports whether the user may access this device.
func (d *Device) AllowedTo(userID string) bool {
	for _, u := range d.AllowedUsers {
		if u == userID {
			return true
		}
	}
	return false
}
