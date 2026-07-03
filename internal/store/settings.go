package store

import (
	"context"
	"database/sql"
)

// SettingDiscoveryTag is the per-user setting key for the openHAB discovery tag
// filter. An empty/unset value means "consider all items".
const SettingDiscoveryTag = "discovery_tag"

// SettingsRepo stores per-user key/value settings.
type SettingsRepo struct{ db *sql.DB }

// NewSettingsRepo returns a per-user settings repository.
func NewSettingsRepo(db *sql.DB) *SettingsRepo { return &SettingsRepo{db: db} }

// Get returns a user's setting and whether it has been set.
func (r *SettingsRepo) Get(ctx context.Context, userID, key string) (string, bool, error) {
	var v string
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// GetOr returns a user's setting, or def when it has not been set.
func (r *SettingsRepo) GetOr(ctx context.Context, userID, key, def string) (string, error) {
	v, ok, err := r.Get(ctx, userID, key)
	if err != nil {
		return "", err
	}
	if !ok {
		return def, nil
	}
	return v, nil
}

// Set upserts a user's setting.
func (r *SettingsRepo) Set(ctx context.Context, userID, key, value string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO settings (user_id, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, userID, key, value)
	return err
}

// IgnoreRepo stores per-user openHAB items to hide from discovery.
type IgnoreRepo struct{ db *sql.DB }

// NewIgnoreRepo returns a per-user ignore-list repository.
func NewIgnoreRepo(db *sql.DB) *IgnoreRepo { return &IgnoreRepo{db: db} }

// Add marks an openHAB item as ignored for the user.
func (r *IgnoreRepo) Add(ctx context.Context, userID, item string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO openhab_ignore (user_id, item) VALUES (?, ?)`, userID, item)
	return err
}

// List returns the user's ignored items.
func (r *IgnoreRepo) List(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT item FROM openhab_ignore WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var it string
		if err := rows.Scan(&it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Clear removes all of the user's ignored items.
func (r *IgnoreRepo) Clear(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM openhab_ignore WHERE user_id = ?`, userID)
	return err
}

// ImportedOpenHABItems returns the openHAB items already bound to one of the
// user's devices (so discovery can hide them).
func (r *CatalogRepo) ImportedOpenHABItems(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT ob.item FROM openhab_bindings ob
		JOIN devices d ON d.id = ob.device_id
		WHERE d.user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var it string
		if err := rows.Scan(&it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
