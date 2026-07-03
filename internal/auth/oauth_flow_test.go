package auth_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
	"github.com/dakhar/yandex2mqtt/internal/store"
	"github.com/dakhar/yandex2mqtt/internal/yandex"
)

func testConfig() *config.Config {
	return &config.Config{
		Admin:   config.Admin{ID: "1", Username: "admin", Password: "pw", Name: "Admin"},
		OAuth:   config.OAuthClient{ClientID: "cid", ClientSecret: "csecret"},
		Session: config.Session{Secret: "0123456789abcdef0123456789abcdef"},
	}
}

func buildApp(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := testConfig()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	users := store.NewUserRepo(db)
	hash, err := auth.HashPassword(cfg.Admin.Password)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.CreateWithID(context.Background(), cfg.Admin.ID, cfg.Admin.Username, cfg.Admin.Name, hash, true); err != nil {
		t.Fatal(err)
	}
	sm := auth.NewSessionManager(cfg.Session.Secret, users)
	o := auth.NewOAuth(cfg, store.NewTokenStore(db), sm, log)

	dev := device.New(config.Device{
		ID: "L1", Type: "devices.types.light", AllowedUsers: []string{"1"},
		MQTT:         config.MQTTMapping{Capabilities: []config.MQTTTopic{{Instance: "on", Set: "l/set", State: "l/state"}}},
		Capabilities: []config.Capability{{Type: "devices.capabilities.on_off", Retrievable: true}},
	}, func(string, string) {}, nil)
	api := yandex.New(device.NewRegistry([]*device.Device{dev}), o.Bearer, log)

	r := chi.NewRouter()
	r.Get("/login", sm.LoginForm)
	r.Post("/login", sm.Login)
	r.Get("/account", sm.Account)
	r.Get("/dialog/authorize", o.Authorize)
	r.Post("/oauth/token", o.Token)
	r.Mount("/provider", api.Routes())

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, dbPath
}

func noRedirectClient(t *testing.T) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Full Yandex-style authorization_code flow: login -> authorize -> code ->
// token -> use bearer on the provider API.
func TestAuthorizationCodeFlow(t *testing.T) {
	srv, _ := buildApp(t)
	c := noRedirectClient(t)

	// Unauthenticated authorize must redirect to /login.
	authURL := srv.URL + "/dialog/authorize?response_type=code&client_id=cid&redirect_uri=" +
		url.QueryEscape("http://localhost/cb") + "&state=xyz"
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), "/login") {
		t.Fatalf("expected redirect to /login, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Log in (sets session cookie in the jar).
	resp = postForm(t, c, srv.URL+"/login", url.Values{
		"username": {"admin"}, "password": {"pw"}, "redirect": {"/account"},
	})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login status = %d", resp.StatusCode)
	}

	// Authorize again — now authenticated — and capture the code.
	resp, err = c.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.Contains(loc, "code=") {
		t.Fatalf("expected redirect with code, got %d %q", resp.StatusCode, loc)
	}
	redirected, _ := url.Parse(loc)
	code := redirected.Query().Get("code")
	if redirected.Query().Get("state") != "xyz" {
		t.Fatalf("state not preserved: %q", loc)
	}

	// Exchange the code for an access token.
	resp = postForm(t, c, srv.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"http://localhost/cb"},
		"client_id":     {"cid"},
		"client_secret": {"csecret"},
	})
	tok := decodeToken(t, resp)
	if tok.AccessToken == "" {
		t.Fatalf("no access token in %+v", tok)
	}

	// Use the bearer token against the provider API.
	assertProviderOK(t, srv.URL, tok.AccessToken)
}

func TestPasswordGrant(t *testing.T) {
	srv, _ := buildApp(t)
	c := &http.Client{}
	resp := postForm(t, c, srv.URL+"/oauth/token", url.Values{
		"grant_type":    {"password"},
		"username":      {"admin"},
		"password":      {"pw"},
		"client_id":     {"cid"},
		"client_secret": {"csecret"},
	})
	tok := decodeToken(t, resp)
	if tok.AccessToken == "" {
		t.Fatalf("no access token: %+v", tok)
	}
	assertProviderOK(t, srv.URL, tok.AccessToken)

	// Wrong password must fail.
	resp = postForm(t, c, srv.URL+"/oauth/token", url.Values{
		"grant_type": {"password"}, "username": {"admin"}, "password": {"WRONG"},
		"client_id": {"cid"}, "client_secret": {"csecret"},
	})
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("bad password should not yield a token")
	}
}

func TestBearerRejectsInvalidToken(t *testing.T) {
	srv, _ := buildApp(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/provider/v1.0/user/devices", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid token = %d, want 401", resp.StatusCode)
	}
}

// Token must survive a process restart (reopen the same SQLite file).
func TestTokenPersistsAcrossRestart(t *testing.T) {
	srv, dbPath := buildApp(t)
	tok := decodeToken(t, postForm(t, &http.Client{}, srv.URL+"/oauth/token", url.Values{
		"grant_type": {"password"}, "username": {"admin"}, "password": {"pw"},
		"client_id": {"cid"}, "client_secret": {"csecret"},
	}))
	if tok.AccessToken == "" {
		t.Fatal("no token")
	}

	// Reopen the DB as a fresh store (simulating restart) and look the token up.
	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	ti, err := store.NewTokenStore(db2).GetByAccess(t.Context(), tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if ti == nil || ti.GetUserID() != "1" {
		t.Fatalf("token not persisted / wrong user: %+v", ti)
	}
}

// --- helpers ---

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func postForm(t *testing.T, c *http.Client, url string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeToken(t *testing.T, resp *http.Response) tokenResp {
	t.Helper()
	defer resp.Body.Close()
	var tr tokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	return tr
}

func assertProviderOK(t *testing.T, base, token string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/provider/v1.0/user/devices", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Request-Id", "r1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider API with valid token = %d, want 200", resp.StatusCode)
	}
	var dr yandex.DevicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		t.Fatal(err)
	}
	if len(dr.Payload.Devices) != 1 || dr.Payload.Devices[0].ID != "L1" {
		t.Fatalf("unexpected devices: %+v", dr.Payload.Devices)
	}
}
