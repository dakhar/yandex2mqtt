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

type fakeDiscoverer struct{ drafts []config.Device }

func (f *fakeDiscoverer) Discover(context.Context) ([]config.Device, error) { return f.drafts, nil }

func openHABDraft() config.Device {
	return config.Device{
		Name: "Свет кухня", Type: "devices.types.light", Transport: "openhab",
		OpenHAB:      []config.OpenHABBinding{{Kind: "cap", Instance: "on", Item: "Light_Kitchen"}},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true, Reportable: true}},
	}
}

func setupDiscover(t *testing.T) (*httptest.Server, *store.CatalogRepo, *fakeReloader) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "disc")
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _ = os.RemoveAll(dir) })

	catalog := store.NewCatalogRepo(db)
	reloader := &fakeReloader{}
	h := web.New(store.NewRoomRepo(db), catalog, reloader, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.SetDiscoverer(&fakeDiscoverer{drafts: []config.Device{openHABDraft()}})

	user := &store.User{ID: "1", Name: "Admin", IsAdmin: true}
	withUser := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
	r := chi.NewRouter()
	r.With(withUser).Get("/app/discover", h.Discover)
	r.With(withUser).Post("/app/discover/add", h.AddDiscovered)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, catalog, reloader
}

func TestDiscoverListsDrafts(t *testing.T) {
	srv, _, _ := setupDiscover(t)
	resp, _ := http.Get(srv.URL + "/app/discover")
	body := readAll(t, resp)
	for _, want := range []string{"Light_Kitchen", "Свет кухня", "devices.types.light", "on_off"} {
		if !strings.Contains(body, want) {
			t.Fatalf("discover page missing %q", want)
		}
	}
}

func TestAddDiscoveredCreatesDevice(t *testing.T) {
	srv, catalog, reloader := setupDiscover(t)

	form := "item=Light_Kitchen"
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/app/discover/add", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("add status = %d, want 302", resp.StatusCode)
	}

	devs, _ := catalog.ListDevicesForUser(context.Background(), "1")
	if len(devs) != 1 || devs[0].Name != "Свет кухня" {
		t.Fatalf("device not created from draft: %+v", devs)
	}
	if len(devs[0].ID) != 36 { // UUID assigned
		t.Fatalf("expected UUID id, got %q", devs[0].ID)
	}
	// The stored device must be openHAB-transport with its item binding.
	d, _, _, _ := catalog.GetDevice(context.Background(), "1", devs[0].ID)
	if d.Transport != "openhab" || len(d.OpenHAB) != 1 || d.OpenHAB[0].Item != "Light_Kitchen" {
		t.Fatalf("stored device not openHAB-bound: transport=%q openhab=%+v", d.Transport, d.OpenHAB)
	}
	if reloader.calls == 0 {
		t.Fatal("add must reload the registry")
	}
}
