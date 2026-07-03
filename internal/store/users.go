package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// usersSchema is the application user table (login accounts, admin-provisioned).
const usersSchema = `
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    name          TEXT    NOT NULL DEFAULT '',
    password_hash TEXT    NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL DEFAULT 0
);
`

// User is an application login account.
type User struct {
	ID           string
	Username     string
	Name         string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    int64
}

// ErrUserExists is returned when creating a user whose username is taken.
var ErrUserExists = errors.New("username already exists")

// UserRepo manages application users.
type UserRepo struct {
	db *sql.DB
}

// NewUserRepo returns a user repository backed by db.
func NewUserRepo(db *sql.DB) *UserRepo { return &UserRepo{db: db} }

// Count returns the number of users.
func (r *UserRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateWithID inserts a user with an explicit id (used to bootstrap the admin
// with the id the existing OAuth token / ADMIN_ID expects).
func (r *UserRepo) CreateWithID(ctx context.Context, id, username, name, passwordHash string, isAdmin bool) error {
	nid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO users (id, username, name, password_hash, is_admin, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		nid, username, name, passwordHash, isAdmin, time.Now().Unix())
	return err
}

// Create inserts a new user with an auto-assigned id.
func (r *UserRepo) Create(ctx context.Context, username, name, passwordHash string, isAdmin bool) (*User, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO users (username, name, password_hash, is_admin, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		username, name, passwordHash, isAdmin, time.Now().Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return r.ByID(ctx, strconv.FormatInt(id, 10))
}

// ByUsername returns the user with the given username, or (nil, nil) if absent.
func (r *UserRepo) ByUsername(ctx context.Context, username string) (*User, error) {
	return r.scanOne(ctx, `WHERE username = ?`, username)
}

// ByID returns the user with the given id, or (nil, nil) if absent.
func (r *UserRepo) ByID(ctx context.Context, id string) (*User, error) {
	return r.scanOne(ctx, `WHERE id = ?`, id)
}

func (r *UserRepo) scanOne(ctx context.Context, where string, arg any) (*User, error) {
	var u User
	var admin int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, username, name, password_hash, is_admin, created_at FROM users `+where, arg).
		Scan(&u.ID, &u.Username, &u.Name, &u.PasswordHash, &admin, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = admin != 0
	return &u, nil
}

// List returns all users ordered by id.
func (r *UserRepo) List(ctx context.Context) ([]User, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, username, name, password_hash, is_admin, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var admin int
		if err := rows.Scan(&u.ID, &u.Username, &u.Name, &u.PasswordHash, &admin, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.IsAdmin = admin != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// Delete removes a user by id.
func (r *UserRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// CountAdmins returns how many admin users exist (used to prevent removing the
// last admin).
func (r *UserRepo) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n)
	return n, err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}
