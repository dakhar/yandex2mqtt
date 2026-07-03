// Package store provides the SQLite-backed persistence: currently the OAuth2
// token store. Pure-Go driver (modernc.org/sqlite), so no CGO and it builds on
// Windows and Alpine alike. Schema is intentionally simple to make the eventual
// move to multitenancy straightforward.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-oauth2/oauth2/v4"
	"github.com/go-oauth2/oauth2/v4/models"
	_ "modernc.org/sqlite"
)

// Open opens (creating if needed) the SQLite database and applies the schema.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writers, avoids "database is locked"
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	for _, s := range []string{schema, usersSchema, catalogSchema} {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("schema: %w", err)
		}
	}
	return db, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS oauth_tokens (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    code       TEXT,
    access     TEXT,
    refresh    TEXT,
    user_id    TEXT,
    data       BLOB    NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tokens_code    ON oauth_tokens(code);
CREATE INDEX IF NOT EXISTS idx_tokens_access  ON oauth_tokens(access);
CREATE INDEX IF NOT EXISTS idx_tokens_refresh ON oauth_tokens(refresh);
CREATE INDEX IF NOT EXISTS idx_tokens_user    ON oauth_tokens(user_id);
`

// TokenStore implements oauth2.TokenStore over SQLite.
type TokenStore struct {
	db *sql.DB
}

// NewTokenStore returns a TokenStore backed by db.
func NewTokenStore(db *sql.DB) *TokenStore { return &TokenStore{db: db} }

var _ oauth2.TokenStore = (*TokenStore)(nil)

// Create persists a new token record.
func (s *TokenStore) Create(ctx context.Context, info oauth2.TokenInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO oauth_tokens (code, access, refresh, user_id, data, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		info.GetCode(), info.GetAccess(), info.GetRefresh(), info.GetUserID(), data, expiresAt(info),
	)
	return err
}

// expiresAt returns the latest expiry (unix seconds) across code/access/refresh,
// or 0 when the token never expires.
func expiresAt(info oauth2.TokenInfo) int64 {
	var latest int64
	consider := func(create time.Time, exp time.Duration) {
		if exp <= 0 {
			return
		}
		if t := create.Add(exp).Unix(); t > latest {
			latest = t
		}
	}
	consider(info.GetCodeCreateAt(), info.GetCodeExpiresIn())
	consider(info.GetAccessCreateAt(), info.GetAccessExpiresIn())
	consider(info.GetRefreshCreateAt(), info.GetRefreshExpiresIn())
	return latest
}

func (s *TokenStore) RemoveByCode(ctx context.Context, code string) error {
	return s.remove(ctx, "code", code)
}

func (s *TokenStore) RemoveByAccess(ctx context.Context, access string) error {
	return s.remove(ctx, "access", access)
}

func (s *TokenStore) RemoveByRefresh(ctx context.Context, refresh string) error {
	return s.remove(ctx, "refresh", refresh)
}

func (s *TokenStore) remove(ctx context.Context, column, value string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE `+column+` = ?`, value)
	return err
}

// RemoveByUser deletes all tokens for a user (used on account unlink).
func (s *TokenStore) RemoveByUser(ctx context.Context, userID string) error {
	return s.remove(ctx, "user_id", userID)
}

func (s *TokenStore) GetByCode(ctx context.Context, code string) (oauth2.TokenInfo, error) {
	return s.get(ctx, "code", code)
}

func (s *TokenStore) GetByAccess(ctx context.Context, access string) (oauth2.TokenInfo, error) {
	return s.get(ctx, "access", access)
}

func (s *TokenStore) GetByRefresh(ctx context.Context, refresh string) (oauth2.TokenInfo, error) {
	return s.get(ctx, "refresh", refresh)
}

// get returns the token for a lookup column, or (nil, nil) when absent/expired
// (the convention go-oauth2 expects for "not found").
func (s *TokenStore) get(ctx context.Context, column, value string) (oauth2.TokenInfo, error) {
	if value == "" {
		return nil, nil
	}
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM oauth_tokens
		 WHERE `+column+` = ? AND (expires_at = 0 OR expires_at > ?)`,
		value, time.Now().Unix(),
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tok models.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// DeleteExpired removes tokens whose expiry has passed. Safe to call periodically.
func (s *TokenStore) DeleteExpired(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_tokens WHERE expires_at != 0 AND expires_at <= ?`, time.Now().Unix())
	return err
}
