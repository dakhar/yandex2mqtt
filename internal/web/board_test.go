package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/store"
	"github.com/dakhar/yandex2mqtt/internal/web"
)

type fakeReloader struct{ calls int }

func (f *fakeReloader) Reload(context.Context) error { f.calls++; return nil }

func setup(t *testing.T) (*httptest.Server, *store.RoomRepo, *store.CatalogRepo, *fakeReloader) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "web")
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _ = os.RemoveAll(dir) })

	catalog := store.NewCatalogRepo(db)
	rooms := store.NewRoomRepo(db)
	if err := catalog.ImportCatalog(context.Background(), "1", []config.Device{{
		ID: "Lamp", Name: "Лампа", Type: "devices.types.light",
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off"}},
	}}); err != nil {
		t.Fatal(err)
	}

	reloader := &fakeReloader{}
	h := web.New(rooms, catalog, reloader, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Inject a logged-in user (id 1) the way RequireLogin would.
	user := &store.User{ID: "1", Name: "Admin", IsAdmin: true}
	withUser := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
	r := chi.NewRouter()
	r.With(withUser).Get("/app", h.Board)
	r.With(withUser).Post("/app/rooms", h.CreateRoom)
	r.With(withUser).Post("/app/rooms/{id}/delete", h.DeleteRoom)
	r.With(withUser).Post("/app/devices/{id}/move", h.MoveDevice)
	r.With(withUser).Get("/app/schema", h.Schema)
	r.With(withUser).Get("/app/devices/{id}/edit", h.EditDevice)
	r.With(withUser).Post("/app/devices", h.CreateDevice)
	r.With(withUser).Post("/app/devices/{id}", h.UpdateDevice)
	r.With(withUser).Post("/app/devices/{id}/delete", h.DeleteDevice)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, rooms, catalog, reloader
}

func TestBoardShowsDevicesAndRooms(t *testing.T) {
	srv, rooms, _, _ := setup(t)
	rooms.Create(context.Background(), "1", "Кухня")

	resp, _ := http.Get(srv.URL + "/app")
	body := readAll(t, resp)
	for _, want := range []string{"Кухня", "Без комнаты", "Лампа", `data-id="Lamp"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("board missing %q", want)
		}
	}
}

func TestMoveDevicePersistsAndReloads(t *testing.T) {
	srv, rooms, catalog, reloader := setup(t)
	ctx := context.Background()
	room, _ := rooms.Create(ctx, "1", "Кухня")

	// Move Lamp into the kitchen.
	resp := postJSON(t, srv.URL+"/app/devices/Lamp/move", `{"room_id":"`+room.ID+`","position":0}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("move status = %d, want 204", resp.StatusCode)
	}
	if reloader.calls == 0 {
		t.Fatal("move must trigger a registry reload")
	}
	devs, _ := catalog.ListDevicesForUser(ctx, "1")
	if devs[0].RoomID != room.ID {
		t.Fatalf("device not moved: %+v", devs[0])
	}

	// Moving to an unknown room is rejected.
	resp = postJSON(t, srv.URL+"/app/devices/Lamp/move", `{"room_id":"9999","position":0}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown room move = %d, want 400", resp.StatusCode)
	}

	// Deleting the room unassigns the device.
	postForm(t, srv.URL+"/app/rooms/"+room.ID+"/delete")
	devs, _ = catalog.ListDevicesForUser(ctx, "1")
	if devs[0].RoomID != "" {
		t.Fatalf("device should be unassigned after room delete: %+v", devs[0])
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func postForm(t *testing.T, url string) {
	t.Helper()
	// Do not follow the redirect; we only care about the side effect.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Post(url, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
