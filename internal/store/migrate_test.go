package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A database created by an older version (devices table without `transport`)
// must gain the column on Open, idempotently.
func TestMigrateAddsTransportColumn(t *testing.T) {
	dir, err := os.MkdirTemp("", "migrate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "old.db")

	// Simulate an old schema: a devices table lacking `transport`.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE devices (id TEXT PRIMARY KEY, user_id TEXT, name TEXT, type TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO devices (id, user_id, name, type) VALUES ('d1','1','L','devices.types.light')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	// Open via the store (runs migrations).
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open/migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// The column now exists with the default applied to the existing row.
	var transport string
	if err := db.QueryRow(`SELECT transport FROM devices WHERE id = 'd1'`).Scan(&transport); err != nil {
		t.Fatalf("transport column missing after migrate: %v", err)
	}
	if transport != "mqtt" {
		t.Fatalf("default transport = %q, want mqtt", transport)
	}

	// Idempotent: opening again must not fail.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second open failed (migration not idempotent): %v", err)
	}
	db2.Close()
}
