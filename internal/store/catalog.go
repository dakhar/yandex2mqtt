package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

// catalogSchema is the normalized device catalog: rooms, devices, and the
// device's capabilities/properties/mqtt topics/value mappings as related rows.
// Only the leaf Yandex `params` map (which varies per type) is kept as JSON.
const catalogSchema = `
CREATE TABLE IF NOT EXISTS rooms (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id  TEXT    NOT NULL,
    name     TEXT    NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    UNIQUE(user_id, name)
);
CREATE TABLE IF NOT EXISTS devices (
    id          TEXT    PRIMARY KEY,
    user_id     TEXT    NOT NULL,
    room_id     INTEGER REFERENCES rooms(id) ON DELETE SET NULL,
    name        TEXT    NOT NULL,
    type        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    transport   TEXT    NOT NULL DEFAULT 'mqtt',
    position    INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS openhab_bindings (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id TEXT    NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    kind      TEXT    NOT NULL,   -- 'cap' | 'prop'
    instance  TEXT    NOT NULL,
    item      TEXT    NOT NULL,
    ord       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ohb_dev ON openhab_bindings(device_id);
CREATE TABLE IF NOT EXISTS settings (
    user_id TEXT NOT NULL,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (user_id, key)
);
CREATE TABLE IF NOT EXISTS openhab_ignore (
    user_id TEXT NOT NULL,
    item    TEXT NOT NULL,
    PRIMARY KEY (user_id, item)
);
CREATE TABLE IF NOT EXISTS app_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS device_errors (
    device_id   TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    item        TEXT NOT NULL DEFAULT '',   -- openHAB item
    state_topic TEXT NOT NULL DEFAULT '',   -- MQTT topic
    state_path  TEXT NOT NULL DEFAULT '',
    mapping     TEXT NOT NULL DEFAULT ''    -- JSON [{value,code}]
);
CREATE TABLE IF NOT EXISTS capabilities (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id   TEXT    NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    type        TEXT    NOT NULL,
    retrievable INTEGER NOT NULL DEFAULT 0,
    reportable  INTEGER NOT NULL DEFAULT 0,
    params      TEXT,
    invert      INTEGER NOT NULL DEFAULT 0,
    ord         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS properties (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id   TEXT    NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    type        TEXT    NOT NULL,
    retrievable INTEGER NOT NULL DEFAULT 0,
    reportable  INTEGER NOT NULL DEFAULT 0,
    params      TEXT,
    invert      INTEGER NOT NULL DEFAULT 0,
    ord         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS mqtt_topics (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id  TEXT    NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL,             -- 'cap' | 'prop'
    instance   TEXT    NOT NULL,
    set_topic  TEXT    NOT NULL DEFAULT '',
    state_topic TEXT   NOT NULL DEFAULT '',
    state_path TEXT    NOT NULL DEFAULT '',   -- optional JSON dot-path into state payload
    ord        INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS value_mappings (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id TEXT    NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    type      TEXT    NOT NULL,
    instance  TEXT    NOT NULL,
    ord       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS value_mapping_rows (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    mapping_id   INTEGER NOT NULL REFERENCES value_mappings(id) ON DELETE CASCADE,
    ord          INTEGER NOT NULL,
    yandex_value TEXT    NOT NULL,           -- JSON (preserves bool/number/string type)
    mqtt_value   TEXT    NOT NULL            -- JSON
);
CREATE TABLE IF NOT EXISTS vacuum_zones (
    device_id    TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    group_id     TEXT NOT NULL,
    segment_id   TEXT NOT NULL,
    clean_target TEXT NOT NULL DEFAULT '',
    op_target    TEXT NOT NULL DEFAULT '',
    home_cmd     TEXT NOT NULL DEFAULT '',
    debounce_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_dev_user  ON devices(user_id);
CREATE INDEX IF NOT EXISTS idx_cap_dev   ON capabilities(device_id);
CREATE INDEX IF NOT EXISTS idx_prop_dev  ON properties(device_id);
CREATE INDEX IF NOT EXISTS idx_topic_dev ON mqtt_topics(device_id);
CREATE INDEX IF NOT EXISTS idx_vm_dev    ON value_mappings(device_id);
CREATE INDEX IF NOT EXISTS idx_vmr_map   ON value_mapping_rows(mapping_id);
`

// CatalogRepo reads and writes the normalized device catalog.
type CatalogRepo struct {
	db *sql.DB
}

// NewCatalogRepo returns a catalog repository backed by db.
func NewCatalogRepo(db *sql.DB) *CatalogRepo { return &CatalogRepo{db: db} }

// CountDevices returns how many devices exist (used to decide first-run import).
func (r *CatalogRepo) CountDevices(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices`).Scan(&n)
	return n, err
}

// ImportCatalog seeds the catalog from a parsed YAML/JS catalog, assigning every
// device to userID. Runs in a single transaction.
func (r *CatalogRepo) ImportCatalog(ctx context.Context, userID string, devices []config.Device) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rooms := map[string]int64{} // name -> room id (cache within the tx)
	for _, d := range devices {
		if err := insertDevice(ctx, tx, userID, d, rooms); err != nil {
			return fmt.Errorf("device %q: %w", d.ID, err)
		}
	}
	return tx.Commit()
}

func ensureRoom(ctx context.Context, tx *sql.Tx, userID, name string, cache map[string]int64) (sql.NullInt64, error) {
	if name == "" {
		return sql.NullInt64{}, nil
	}
	if id, ok := cache[name]; ok {
		return sql.NullInt64{Int64: id, Valid: true}, nil
	}
	// Upsert-ish: insert if new, otherwise fetch existing id.
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO rooms (user_id, name) VALUES (?, ?)`, userID, name)
	if err != nil {
		return sql.NullInt64{}, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM rooms WHERE user_id = ? AND name = ?`, userID, name).Scan(&id); err != nil {
			return sql.NullInt64{}, err
		}
	}
	cache[name] = id
	return sql.NullInt64{Int64: id, Valid: true}, nil
}

func insertDevice(ctx context.Context, tx *sql.Tx, userID string, d config.Device, rooms map[string]int64) error {
	roomID, err := ensureRoom(ctx, tx, userID, d.Room, rooms)
	if err != nil {
		return err
	}
	return insertDeviceRow(ctx, tx, userID, d, roomID)
}

// insertDeviceRow inserts the device and all its children with an explicit
// room_id (used by the builder, which assigns an existing room by id).
func insertDeviceRow(ctx context.Context, tx *sql.Tx, userID string, d config.Device, roomID sql.NullInt64) error {
	transport := d.Transport
	if transport == "" {
		transport = "mqtt"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO devices (id, user_id, room_id, name, type, description, transport, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, userID, roomID, d.Name, d.Type, d.Description, transport, time.Now().Unix()); err != nil {
		return err
	}
	return insertChildren(ctx, tx, d)
}

func insertChildren(ctx context.Context, tx *sql.Tx, d config.Device) error {
	for i, c := range d.Capabilities {
		if err := insertCapProp(ctx, tx, "capabilities", d.ID, c.Type, c.Retrievable, c.Reportable, c.Parameters, c.Invert, i); err != nil {
			return err
		}
	}
	for i, p := range d.Properties {
		if err := insertCapProp(ctx, tx, "properties", d.ID, p.Type, p.Retrievable, p.Reportable, p.Parameters, false, i); err != nil {
			return err
		}
	}
	if err := insertTopics(ctx, tx, d.ID, "cap", d.MQTT.Capabilities); err != nil {
		return err
	}
	if err := insertTopics(ctx, tx, d.ID, "prop", d.MQTT.Properties); err != nil {
		return err
	}
	for i, b := range d.OpenHAB {
		if b.Kind == "equipment" {
			continue // discovery identity marker, not a real item binding
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO openhab_bindings (device_id, kind, instance, item, ord) VALUES (?, ?, ?, ?, ?)`,
			d.ID, b.Kind, b.Instance, b.Item, i); err != nil {
			return err
		}
	}
	if d.Error != nil {
		mj, err := json.Marshal(d.Error.Mapping)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO device_errors (device_id, item, state_topic, state_path, mapping) VALUES (?, ?, ?, ?, ?)`,
			d.ID, d.Error.Item, d.Error.State, d.Error.StatePath, string(mj)); err != nil {
			return err
		}
	}
	if v := d.Vacuum; v != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vacuum_zones (device_id, group_id, segment_id, clean_target, op_target, home_cmd, debounce_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			d.ID, v.GroupID, v.SegmentID, v.CleanTarget, v.OpTarget, v.HomeCmd, v.DebounceMs); err != nil {
			return err
		}
	}
	return insertValueMappings(ctx, tx, d.ID, d.ValueMapping)
}

// SaveDevice creates a device owned by userID and assigned to roomID (nil =
// unassigned). The device's ID must already be set (a generated UUID for
// builder-created devices).
func (r *CatalogRepo) SaveDevice(ctx context.Context, userID string, roomID *string, d config.Device) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rid, err := nullRoomID(roomID)
	if err != nil {
		return err
	}
	if err := insertDeviceRow(ctx, tx, userID, d, rid); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceDevice overwrites an existing device (scoped to the owning user) with
// new data, keeping the same id. Reports whether a device was replaced.
func (r *CatalogRepo) ReplaceDevice(ctx context.Context, userID string, roomID *string, d config.Device) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Delete the old row (children cascade), scoped to the owner.
	res, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id = ? AND user_id = ?`, d.ID, userID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	rid, err := nullRoomID(roomID)
	if err != nil {
		return false, err
	}
	if err := insertDeviceRow(ctx, tx, userID, d, rid); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// DeleteDevice removes a device (and its children via cascade), scoped to the
// owning user. Reports whether a row was deleted.
func (r *CatalogRepo) DeleteDevice(ctx context.Context, userID, id string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteAllDevices removes all of a user's devices (cascades to their
// capabilities, topics, mappings and bindings). Used by reset/import.
func (r *CatalogRepo) DeleteAllDevices(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM devices WHERE user_id = ?`, userID)
	return err
}

func nullRoomID(roomID *string) (sql.NullInt64, error) {
	if roomID == nil || *roomID == "" {
		return sql.NullInt64{}, nil
	}
	n, err := strconv.ParseInt(*roomID, 10, 64)
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: n, Valid: true}, nil
}

func insertCapProp(ctx context.Context, tx *sql.Tx, table, deviceID, typ string, retr, rep bool, params map[string]any, invert bool, ord int) error {
	var paramsJSON any
	if len(params) > 0 {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		paramsJSON = string(b)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO `+table+` (device_id, type, retrievable, reportable, params, invert, ord)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		deviceID, typ, retr, rep, paramsJSON, invert, ord)
	return err
}

func insertTopics(ctx context.Context, tx *sql.Tx, deviceID, kind string, topics []config.MQTTTopic) error {
	for i, t := range topics {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO mqtt_topics (device_id, kind, instance, set_topic, state_topic, state_path, ord)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			deviceID, kind, t.Instance, t.Set, t.State, t.StatePath, i); err != nil {
			return err
		}
	}
	return nil
}

func insertValueMappings(ctx context.Context, tx *sql.Tx, deviceID string, vms []config.ValueMapping) error {
	ord := 0
	for _, vm := range vms {
		for _, im := range vm.Mapping {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO value_mappings (device_id, type, instance, ord) VALUES (?, ?, ?, ?)`,
				deviceID, vm.Type, im.Instance, ord)
			if err != nil {
				return err
			}
			ord++
			mappingID, _ := res.LastInsertId()
			// im.Mapping is [[yandex...],[mqtt...]] — store column-wise as pairs.
			if len(im.Mapping) < 2 {
				continue
			}
			yandex, mqtt := im.Mapping[0], im.Mapping[1]
			for j := 0; j < len(yandex) && j < len(mqtt); j++ {
				yj, err := json.Marshal(yandex[j])
				if err != nil {
					return err
				}
				mj, err := json.Marshal(mqtt[j])
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO value_mapping_rows (mapping_id, ord, yandex_value, mqtt_value)
					 VALUES (?, ?, ?, ?)`,
					mappingID, j, string(yj), string(mj)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
