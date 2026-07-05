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

type fakeDiscoverer struct {
	drafts  []config.Device
	lastTag string
}

func (f *fakeDiscoverer) Discover(_ context.Context, tag string, _ bool) ([]config.Device, error) {
	f.lastTag = tag
	return f.drafts, nil
}

func openHABDraft() config.Device {
	return config.Device{
		Name: "Свет кухня", Type: "devices.types.light", Transport: "openhab", Room: "Кухня",
		OpenHAB:      []config.OpenHABBinding{{Kind: "cap", Instance: "on", Item: "Light_Kitchen"}},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true, Reportable: true}},
	}
}

func setupDiscover(t *testing.T) (*httptest.Server, *store.CatalogRepo, *store.IgnoreRepo, *fakeDiscoverer, *fakeReloader) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "disc")
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _ = os.RemoveAll(dir) })

	catalog := store.NewCatalogRepo(db)
	ignore := store.NewIgnoreRepo(db)
	reloader := &fakeReloader{}
	disc := &fakeDiscoverer{drafts: []config.Device{openHABDraft()}}
	h := web.New(store.NewRoomRepo(db), catalog, reloader, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.SetDiscovery(disc, store.NewSettingsRepo(db), ignore)

	user := &store.User{ID: "1", Name: "Admin", IsAdmin: true}
	withUser := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
	r := chi.NewRouter()
	r.With(withUser).Get("/app/discover", h.Discover)
	r.With(withUser).Post("/app/discover/add", h.AddDiscovered)
	r.With(withUser).Post("/app/discover/ignore", h.IgnoreDiscovered)
	r.With(withUser).Post("/app/discover/tag", h.SetDiscoveryTag)
	r.With(withUser).Post("/app/discover/clear-ignore", h.ClearIgnore)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, catalog, ignore, disc, reloader
}

func post(t *testing.T, url, form string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestDiscoverListsDrafts(t *testing.T) {
	srv, _, _, disc, _ := setupDiscover(t)
	resp, _ := http.Get(srv.URL + "/app/discover")
	body := readAll(t, resp)
	for _, want := range []string{"Light_Kitchen", "Свет кухня", "devices.types.light", "on_off", "Кухня"} {
		if !strings.Contains(body, want) {
			t.Fatalf("discover page missing %q", want)
		}
	}
	// Default tag applied.
	if disc.lastTag != "ya2mqtt" {
		t.Fatalf("default discovery tag = %q, want ya2mqtt", disc.lastTag)
	}
}

func TestAddDiscoveredCreatesDeviceInRoom(t *testing.T) {
	srv, catalog, _, _, reloader := setupDiscover(t)
	post(t, srv.URL+"/app/discover/add", "item=Light_Kitchen")

	devs, _ := catalog.ListDevicesForUser(context.Background(), "1")
	if len(devs) != 1 || devs[0].Name != "Свет кухня" {
		t.Fatalf("device not created: %+v", devs)
	}
	if len(devs[0].ID) != 36 {
		t.Fatalf("expected UUID id, got %q", devs[0].ID)
	}
	// The draft's openHAB location -> a room the device is placed in.
	d, _, _, _ := catalog.GetDevice(context.Background(), "1", devs[0].ID)
	if d.Transport != "openhab" || d.Room != "Кухня" {
		t.Fatalf("device not placed in openHAB room: transport=%q room=%q", d.Transport, d.Room)
	}
	if reloader.calls == 0 {
		t.Fatal("add must reload the registry")
	}

	// After import the item is hidden from discovery.
	body := readAll(t, mustGet(t, srv.URL+"/app/discover"))
	if strings.Contains(body, "Light_Kitchen") {
		t.Fatal("imported item must be hidden from discovery")
	}
}

func TestIgnoreHidesAndClearRestores(t *testing.T) {
	srv, _, ignore, _, _ := setupDiscover(t)

	post(t, srv.URL+"/app/discover/ignore", "item=Light_Kitchen")
	if body := readAll(t, mustGet(t, srv.URL+"/app/discover")); strings.Contains(body, "Light_Kitchen") {
		t.Fatal("ignored item must be hidden")
	}
	if items, _ := ignore.List(context.Background(), "1"); len(items) != 1 {
		t.Fatalf("ignore list = %v", items)
	}

	post(t, srv.URL+"/app/discover/clear-ignore", "")
	if body := readAll(t, mustGet(t, srv.URL+"/app/discover")); !strings.Contains(body, "Light_Kitchen") {
		t.Fatal("cleared ignore must restore the draft")
	}
}

func TestSetDiscoveryTagPersistsPerUser(t *testing.T) {
	srv, _, _, disc, _ := setupDiscover(t)
	post(t, srv.URL+"/app/discover/tag", "tag=myfilter")
	_ = readAll(t, mustGet(t, srv.URL+"/app/discover"))
	if disc.lastTag != "myfilter" {
		t.Fatalf("tag not applied after set: %q", disc.lastTag)
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
