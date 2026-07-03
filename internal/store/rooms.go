package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Room is a grouping of devices, owned by a user.
type Room struct {
	ID       string
	Name     string
	Position int
}

// ErrRoomExists is returned when a room name is already taken for the user.
var ErrRoomExists = errors.New("room already exists")

// RoomRepo manages rooms, always scoped to the owning user.
type RoomRepo struct {
	db *sql.DB
}

// NewRoomRepo returns a room repository backed by db.
func NewRoomRepo(db *sql.DB) *RoomRepo { return &RoomRepo{db: db} }

// List returns the user's rooms ordered by position.
func (r *RoomRepo) List(ctx context.Context, userID string) ([]Room, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, position FROM rooms WHERE user_id = ? ORDER BY position, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		var rm Room
		if err := rows.Scan(&rm.ID, &rm.Name, &rm.Position); err != nil {
			return nil, err
		}
		out = append(out, rm)
	}
	return out, rows.Err()
}

// Create adds a room for the user, appended after existing ones.
func (r *RoomRepo) Create(ctx context.Context, userID, name string) (*Room, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("empty room name")
	}
	var pos int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rooms WHERE user_id = ?`, userID).Scan(&pos); err != nil {
		return nil, err
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO rooms (user_id, name, position) VALUES (?, ?, ?)`, userID, name, pos)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrRoomExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Room{ID: strconv.FormatInt(id, 10), Name: name, Position: pos}, nil
}

// Rename changes a room's name (scoped to the user).
func (r *RoomRepo) Rename(ctx context.Context, userID, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("empty room name")
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE rooms SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if isUniqueViolation(err) {
		return ErrRoomExists
	}
	return err
}

// Delete removes a room (scoped to the user). Devices in it become unassigned
// via the ON DELETE SET NULL foreign key.
func (r *RoomRepo) Delete(ctx context.Context, userID, id string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM rooms WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// BelongsToUser reports whether the room is owned by the user.
func (r *RoomRepo) BelongsToUser(ctx context.Context, userID, id string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM rooms WHERE id = ? AND user_id = ?`, id, userID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// --- board device queries (device rows scoped to the board view) ---

// BoardDevice is the lightweight device view for the board.
type BoardDevice struct {
	ID       string
	Name     string
	Type     string
	RoomID   string // "" = unassigned
	Position int
}

// ListDevicesForUser returns the user's devices with their room assignment.
func (r *CatalogRepo) ListDevicesForUser(ctx context.Context, userID string) ([]BoardDevice, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, type, room_id, position FROM devices WHERE user_id = ? ORDER BY position, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BoardDevice
	for rows.Next() {
		var d BoardDevice
		var roomID sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Name, &d.Type, &roomID, &d.Position); err != nil {
			return nil, err
		}
		if roomID.Valid {
			d.RoomID = strconv.FormatInt(roomID.Int64, 10)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MoveDevice reassigns a device to a room (roomID nil = unassign) and sets its
// position, scoped to the owning user. Reports whether a row was changed.
func (r *CatalogRepo) MoveDevice(ctx context.Context, userID, deviceID string, roomID *string, position int) (bool, error) {
	var roomArg any
	if roomID != nil && *roomID != "" {
		roomArg = *roomID
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE devices SET room_id = ?, position = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		roomArg, position, time.Now().Unix(), deviceID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
