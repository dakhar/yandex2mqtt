package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func openUserRepo(t *testing.T) *UserRepo {
	t.Helper()
	dir, err := os.MkdirTemp("", "users")
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _ = os.RemoveAll(dir) })
	return NewUserRepo(db)
}

func TestUserCreateAndLookup(t *testing.T) {
	repo := openUserRepo(t)
	ctx := context.Background()

	// Admin bootstrap with explicit id.
	if err := repo.CreateWithID(ctx, "1", "admin", "Admin", "hash1", true); err != nil {
		t.Fatal(err)
	}
	u, err := repo.ByID(ctx, "1")
	if err != nil || u == nil {
		t.Fatalf("admin not found: %v", err)
	}
	if u.Username != "admin" || !u.IsAdmin {
		t.Fatalf("admin fields: %+v", u)
	}

	// Regular user, auto id.
	u2, err := repo.Create(ctx, "bob", "Bob", "hash2", false)
	if err != nil {
		t.Fatal(err)
	}
	if u2.ID == "1" || u2.IsAdmin {
		t.Fatalf("unexpected user: %+v", u2)
	}
	if got, _ := repo.ByUsername(ctx, "bob"); got == nil || got.ID != u2.ID {
		t.Fatalf("ByUsername mismatch")
	}

	// Unknown user -> (nil, nil).
	if got, err := repo.ByUsername(ctx, "nobody"); err != nil || got != nil {
		t.Fatalf("unknown user: got=%v err=%v", got, err)
	}
}

func TestUserDuplicateUsername(t *testing.T) {
	repo := openUserRepo(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, "dup", "", "h", false); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, "dup", "", "h", false); err != ErrUserExists {
		t.Fatalf("want ErrUserExists, got %v", err)
	}
}

func TestUserAdminCountAndDelete(t *testing.T) {
	repo := openUserRepo(t)
	ctx := context.Background()
	_ = repo.CreateWithID(ctx, "1", "admin", "", "h", true)
	bob, _ := repo.Create(ctx, "bob", "", "h", false)

	if n, _ := repo.CountAdmins(ctx); n != 1 {
		t.Fatalf("admins = %d, want 1", n)
	}
	if err := repo.Delete(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ByID(ctx, bob.ID); got != nil {
		t.Fatal("bob not deleted")
	}
	list, _ := repo.List(ctx)
	if len(list) != 1 {
		t.Fatalf("users after delete = %d, want 1", len(list))
	}
}
