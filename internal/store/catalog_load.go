package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// LoadAll assembles every device from the normalized tables back into the
// canonical config.Device shape the rest of the system uses.
func (r *CatalogRepo) LoadAll(ctx context.Context) ([]config.Device, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT d.id, d.user_id, COALESCE(rm.name, ''), d.name, d.type, d.description, d.transport
		FROM devices d
		LEFT JOIN rooms rm ON rm.id = d.room_id
		ORDER BY d.position, d.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []config.Device
	for rows.Next() {
		var d config.Device
		var userID string
		if err := rows.Scan(&d.ID, &userID, &d.Room, &d.Name, &d.Type, &d.Description, &d.Transport); err != nil {
			return nil, err
		}
		d.AllowedUsers = []string{userID}
		devices = append(devices, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fill in each device's children.
	for i := range devices {
		id := devices[i].ID
		if devices[i].Capabilities, err = r.loadCapProps(ctx, "capabilities", id); err != nil {
			return nil, err
		}
		props, err := r.loadCapProps(ctx, "properties", id)
		if err != nil {
			return nil, err
		}
		devices[i].Properties = toProperties(props)
		if devices[i].MQTT, err = r.loadTopics(ctx, id); err != nil {
			return nil, err
		}
		if devices[i].ValueMapping, err = r.loadValueMappings(ctx, id); err != nil {
			return nil, err
		}
		if devices[i].OpenHAB, err = r.loadOpenHABBindings(ctx, id); err != nil {
			return nil, err
		}
		if devices[i].Error, err = r.loadErrorBinding(ctx, id); err != nil {
			return nil, err
		}
	}
	return devices, nil
}

// loadErrorBinding loads the device's status->error_code binding (nil if none).
func (r *CatalogRepo) loadErrorBinding(ctx context.Context, deviceID string) (*config.ErrorBinding, error) {
	var b config.ErrorBinding
	var mapping string
	err := r.db.QueryRowContext(ctx,
		`SELECT item, state_topic, state_path, mapping FROM device_errors WHERE device_id = ?`, deviceID).
		Scan(&b.Item, &b.State, &b.StatePath, &mapping)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mapping != "" {
		if err := json.Unmarshal([]byte(mapping), &b.Mapping); err != nil {
			return nil, err
		}
	}
	return &b, nil
}

func (r *CatalogRepo) loadOpenHABBindings(ctx context.Context, deviceID string) ([]config.OpenHABBinding, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT kind, instance, item FROM openhab_bindings WHERE device_id = ? ORDER BY ord, id`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []config.OpenHABBinding
	for rows.Next() {
		var b config.OpenHABBinding
		if err := rows.Scan(&b.Kind, &b.Instance, &b.Item); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetDevice assembles a single device owned by userID, returning its room id
// (for the edit form) separately from the room name kept on the device.
func (r *CatalogRepo) GetDevice(ctx context.Context, userID, id string) (config.Device, string, bool, error) {
	var d config.Device
	var roomID string
	err := r.db.QueryRowContext(ctx, `
		SELECT d.id, COALESCE(rm.name, ''), COALESCE(CAST(d.room_id AS TEXT), ''), d.name, d.type, d.description, d.transport
		FROM devices d LEFT JOIN rooms rm ON rm.id = d.room_id
		WHERE d.id = ? AND d.user_id = ?`, id, userID).
		Scan(&d.ID, &d.Room, &roomID, &d.Name, &d.Type, &d.Description, &d.Transport)
	if err == sql.ErrNoRows {
		return config.Device{}, "", false, nil
	}
	if err != nil {
		return config.Device{}, "", false, err
	}
	d.AllowedUsers = []string{userID}
	if d.OpenHAB, err = r.loadOpenHABBindings(ctx, id); err != nil {
		return config.Device{}, "", false, err
	}
	if d.Capabilities, err = r.loadCapProps(ctx, "capabilities", id); err != nil {
		return config.Device{}, "", false, err
	}
	props, err := r.loadCapProps(ctx, "properties", id)
	if err != nil {
		return config.Device{}, "", false, err
	}
	d.Properties = toProperties(props)
	if d.MQTT, err = r.loadTopics(ctx, id); err != nil {
		return config.Device{}, "", false, err
	}
	if d.ValueMapping, err = r.loadValueMappings(ctx, id); err != nil {
		return config.Device{}, "", false, err
	}
	if d.Error, err = r.loadErrorBinding(ctx, id); err != nil {
		return config.Device{}, "", false, err
	}
	return d, roomID, true, nil
}

func (r *CatalogRepo) loadCapProps(ctx context.Context, table, deviceID string) ([]config.Capability, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT type, retrievable, reportable, params, invert FROM `+table+
			` WHERE device_id = ? ORDER BY ord, id`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.Capability
	for rows.Next() {
		var c config.Capability
		var params sql.NullString
		if err := rows.Scan(&c.Type, &c.Retrievable, &c.Reportable, &params, &c.Invert); err != nil {
			return nil, err
		}
		if params.Valid && params.String != "" {
			if err := json.Unmarshal([]byte(params.String), &c.Parameters); err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// toProperties reuses the Capability shape (identical fields) as Property.
func toProperties(caps []config.Capability) []config.Property {
	if caps == nil {
		return nil
	}
	props := make([]config.Property, len(caps))
	for i, c := range caps {
		props[i] = config.Property{
			Type:        c.Type,
			Retrievable: c.Retrievable,
			Reportable:  c.Reportable,
			Parameters:  c.Parameters,
		}
	}
	return props
}

func (r *CatalogRepo) loadTopics(ctx context.Context, deviceID string) (config.MQTTMapping, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT kind, instance, set_topic, state_topic, state_path FROM mqtt_topics
		 WHERE device_id = ? ORDER BY ord, id`, deviceID)
	if err != nil {
		return config.MQTTMapping{}, err
	}
	defer rows.Close()

	var m config.MQTTMapping
	for rows.Next() {
		var kind string
		var t config.MQTTTopic
		if err := rows.Scan(&kind, &t.Instance, &t.Set, &t.State, &t.StatePath); err != nil {
			return config.MQTTMapping{}, err
		}
		if kind == "prop" {
			m.Properties = append(m.Properties, t)
		} else {
			m.Capabilities = append(m.Capabilities, t)
		}
	}
	return m, rows.Err()
}

func (r *CatalogRepo) loadValueMappings(ctx context.Context, deviceID string) ([]config.ValueMapping, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, type, instance FROM value_mappings WHERE device_id = ? ORDER BY ord, id`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type vmRow struct {
		id       int64
		typ      string
		instance string
	}
	var vmRows []vmRow
	for rows.Next() {
		var v vmRow
		if err := rows.Scan(&v.id, &v.typ, &v.instance); err != nil {
			return nil, err
		}
		vmRows = append(vmRows, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Group instance-mappings by type, preserving order.
	byType := map[string]int{} // type -> index in result
	var result []config.ValueMapping
	for _, v := range vmRows {
		im, err := r.loadMappingRows(ctx, v.id, v.instance)
		if err != nil {
			return nil, err
		}
		if idx, ok := byType[v.typ]; ok {
			result[idx].Mapping = append(result[idx].Mapping, im)
		} else {
			byType[v.typ] = len(result)
			result = append(result, config.ValueMapping{Type: v.typ, Mapping: []config.InstanceMapping{im}})
		}
	}
	return result, nil
}

func (r *CatalogRepo) loadMappingRows(ctx context.Context, mappingID int64, instance string) (config.InstanceMapping, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT yandex_value, mqtt_value FROM value_mapping_rows
		 WHERE mapping_id = ? ORDER BY ord, id`, mappingID)
	if err != nil {
		return config.InstanceMapping{}, err
	}
	defer rows.Close()

	var yandex, mqtt []any
	for rows.Next() {
		var yj, mj string
		if err := rows.Scan(&yj, &mj); err != nil {
			return config.InstanceMapping{}, err
		}
		var yv, mv any
		if err := json.Unmarshal([]byte(yj), &yv); err != nil {
			return config.InstanceMapping{}, err
		}
		if err := json.Unmarshal([]byte(mj), &mv); err != nil {
			return config.InstanceMapping{}, err
		}
		yandex = append(yandex, yv)
		mqtt = append(mqtt, mv)
	}
	if err := rows.Err(); err != nil {
		return config.InstanceMapping{}, err
	}
	return config.InstanceMapping{Instance: instance, Mapping: [][]any{yandex, mqtt}}, nil
}
