package store

import (
	"context"
	"testing"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

func TestRoomsCRUDAndMove(t *testing.T) {
	repo := openTestRepo(t) // *CatalogRepo
	rooms := NewRoomRepo(repo.db)
	ctx := context.Background()

	// Seed a device owned by user 1, initially unassigned.
	if err := repo.ImportCatalog(ctx, "1", []config.Device{{
		ID: "Lamp", Name: "Лампа", Type: "devices.types.light",
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off"}},
	}}); err != nil {
		t.Fatal(err)
	}

	// Create rooms for two different users.
	kitchen, err := rooms.Create(ctx, "1", "Кухня")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Create(ctx, "2", "Чужая"); err != nil {
		t.Fatal(err)
	}
	// Duplicate name for same user -> error.
	if _, err := rooms.Create(ctx, "1", "Кухня"); err != ErrRoomExists {
		t.Fatalf("want ErrRoomExists, got %v", err)
	}

	// User 1 sees only their room.
	if list, _ := rooms.List(ctx, "1"); len(list) != 1 || list[0].Name != "Кухня" {
		t.Fatalf("user1 rooms = %+v", list)
	}

	// Move the lamp into the kitchen.
	changed, err := repo.MoveDevice(ctx, "1", "Lamp", &kitchen.ID, 0)
	if err != nil || !changed {
		t.Fatalf("move: changed=%v err=%v", changed, err)
	}
	devs, _ := repo.ListDevicesForUser(ctx, "1")
	if len(devs) != 1 || devs[0].RoomID != kitchen.ID {
		t.Fatalf("lamp not in kitchen: %+v", devs)
	}

	// A different user cannot move user 1's device.
	if changed, _ := repo.MoveDevice(ctx, "2", "Lamp", nil, 0); changed {
		t.Fatalf("user 2 must not move user 1's device")
	}

	// Deleting the room unassigns the device (FK ON DELETE SET NULL).
	if err := rooms.Delete(ctx, "1", kitchen.ID); err != nil {
		t.Fatal(err)
	}
	devs, _ = repo.ListDevicesForUser(ctx, "1")
	if len(devs) != 1 || devs[0].RoomID != "" {
		t.Fatalf("device should be unassigned after room delete: %+v", devs)
	}
}
