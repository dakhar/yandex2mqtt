package store

import (
	"context"
	"database/sql"
)

// Global (instance-wide) config keys stored in app_config, editable by an admin
// in the UI. Empty/absent means "use the value from the environment".
const (
	CfgMQTTHost     = "mqtt_host"
	CfgMQTTPort     = "mqtt_port"
	CfgMQTTUser     = "mqtt_user"
	CfgMQTTPassword = "mqtt_password"
	CfgOpenHABURL   = "openhab_url"
	CfgOpenHABToken = "openhab_token"
	CfgGo2RTCURL    = "go2rtc_url"
	CfgGo2RTCKeep   = "go2rtc_keepalive_sec"
)

// ConfigRepo stores instance-wide server settings (MQTT/openHAB connection).
type ConfigRepo struct{ db *sql.DB }

// NewConfigRepo returns the global app-config repository.
func NewConfigRepo(db *sql.DB) *ConfigRepo { return &ConfigRepo{db: db} }

// All returns every stored config key/value.
func (r *ConfigRepo) All(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key, value FROM app_config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// Set upserts one config key.
func (r *ConfigRepo) Set(ctx context.Context, key, value string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO app_config (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
