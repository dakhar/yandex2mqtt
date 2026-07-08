// Command yandex2mqtt bridges MQTT-controlled devices to Yandex Smart Home.
//
// This is the incremental rewrite of the original Node.js service. Step 1
// wires up configuration only; subsequent steps add the MQTT bridge, the
// Yandex provider API, and the OAuth2 server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dakhar/yandex2mqtt/internal/auth"
	"strconv"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/device"
	"github.com/dakhar/yandex2mqtt/internal/httplog"
	"github.com/dakhar/yandex2mqtt/internal/mqtt"
	"github.com/dakhar/yandex2mqtt/internal/openhab"
	"github.com/dakhar/yandex2mqtt/internal/store"
	"github.com/dakhar/yandex2mqtt/internal/stream"
	"github.com/dakhar/yandex2mqtt/internal/version"
	"github.com/dakhar/yandex2mqtt/internal/web"
	"github.com/dakhar/yandex2mqtt/internal/yandex"
)

func main() {
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}))
	ctx0 := context.Background()

	logger.Info("yandex2mqtt starting", "version", version.String())

	// Log a redacted summary so we can confirm config loading without ever
	// printing secrets (the old code dumped all of process.env).
	logger.Info("configuration loaded",
		"mqtt", fmt.Sprintf("%s:%d", cfg.MQTT.Host, cfg.MQTT.Port),
		"mqtt_user", cfg.MQTT.User,
		"web_port", cfg.Web.Port,
		"behind_proxy", cfg.Web.BehindProxy,
		"admin_user", cfg.Admin.Username,
		"oauth_client_id", cfg.OAuth.ClientID,
		"notifications", cfg.NotificationEnabled(),
		"db_path", cfg.DBPath,
		"devices_file", cfg.DevicesFile,
		"device_count", len(cfg.Devices),
	)

	// Persistent store.
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	tokenStore := store.NewTokenStore(db)
	catalog := store.NewCatalogRepo(db)
	users := store.NewUserRepo(db)

	// Bootstrap the admin user on first run. The id is fixed to ADMIN_ID so it
	// matches any pre-existing OAuth token and the seeded devices' owner.
	if n, err := users.Count(ctx0); err != nil {
		return fmt.Errorf("count users: %w", err)
	} else if n == 0 {
		hash, err := auth.HashPassword(cfg.Admin.Password)
		if err != nil {
			return fmt.Errorf("hash admin password: %w", err)
		}
		if err := users.CreateWithID(ctx0, cfg.Admin.ID, cfg.Admin.Username, cfg.Admin.Name, hash, true); err != nil {
			return fmt.Errorf("create admin: %w", err)
		}
		logger.Info("bootstrapped admin user", "username", cfg.Admin.Username, "id", cfg.Admin.ID)
	}

	// First run: seed the DB catalog from the YAML file (DB is authoritative
	// afterwards).
	if n, err := catalog.CountDevices(ctx0); err != nil {
		return fmt.Errorf("count devices: %w", err)
	} else if n == 0 && len(cfg.Devices) > 0 {
		if err := catalog.ImportCatalog(ctx0, cfg.Admin.ID, cfg.Devices); err != nil {
			return fmt.Errorf("seed catalog: %w", err)
		}
		logger.Info("seeded catalog from file", "devices", len(cfg.Devices))
	}

	// Validate the DB catalog against the Yandex reference schema: warnings are
	// logged, structural errors abort startup.
	defs, err := catalog.LoadAll(ctx0)
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}
	catErrs, catWarns := device.ValidateCatalog(defs)
	for _, w := range catWarns {
		logger.Warn("catalog", "issue", w.Error())
	}
	if len(catErrs) > 0 {
		for _, e := range catErrs {
			logger.Error("catalog", "error", e.Error())
		}
		return fmt.Errorf("%d catalog error(s)", len(catErrs))
	}

	// Server connection config: env is the base; the DB (edited by an admin in the
	// UI) overrides it. Keep the env values so a cleared DB value falls back.
	configRepo := store.NewConfigRepo(db)
	envMQTT, envOpenHAB := cfg.MQTT, cfg.OpenHAB
	if all, err := configRepo.All(ctx0); err == nil {
		overlayServerCfg(&cfg.MQTT, &cfg.OpenHAB, all)
	}

	// Yandex state-change notifier (nil when not configured).
	notifier := yandex.NewNotifier(cfg.Yandex, logger)

	// MQTT bridge + dynamic device manager (hot-reloadable registry). Reload
	// builds the registry and wires the publisher; Connect subscribes.
	bridge := mqtt.New(cfg.MQTT, logger, notifier.OnUpdate)
	connectors := map[string]device.Connector{bridge.Transport(): bridge}

	// openHAB connector (REST/SSE). Always created so it can be enabled/changed at
	// runtime from the settings UI; it stays idle until a URL+token are set.
	ohConn := openhab.NewConnector(cfg.OpenHAB, logger, notifier.OnUpdate)
	connectors[ohConn.Transport()] = ohConn
	defer ohConn.Close()
	if cfg.OpenHAB.Enabled() {
		logger.Info("openhab connector enabled", "url", cfg.OpenHAB.URL)
	}

	manager := device.NewManager(catalog, connectors, logger)
	if err := manager.Reload(ctx0); err != nil {
		return fmt.Errorf("build registry: %w", err)
	}
	if err := bridge.Connect(); err != nil {
		return fmt.Errorf("mqtt connect: %w", err)
	}
	defer bridge.Disconnect()

	sessions := auth.NewSessionManager(cfg.Session.Secret, users)
	oauth := auth.NewOAuth(cfg, tokenStore, sessions, logger)

	// Provider API, now guarded by real bearer-token verification.
	api := yandex.New(manager, oauth.Bearer, logger)
	api.SetUnlinkHook(func(userID string) {
		if err := tokenStore.RemoveByUser(context.Background(), userID); err != nil {
			logger.Error("unlink revoke", "err", err)
		}
	})

	// HLS proxy: get_stream returns a signed public URL routed through /stream,
	// so Alice's player reaches a camera's local HLS (CORS + reachability) with
	// no transcoding on our side.
	streamProxy := stream.New(cfg.Session.Secret, time.Hour)
	api.SetStreamRewriter(streamProxy.PublicURL)

	board := web.New(store.NewRoomRepo(db), catalog, manager, logger)
	board.SetDiscovery(ohConn, store.NewSettingsRepo(db), store.NewIgnoreRepo(db))
	if notifier != nil {
		// After a catalog change, ask Yandex to re-discover the user's devices.
		board.SetDiscoveryNotifier(notifier.NotifyDiscovery)
	}

	// Admin-editable server config: effective values (env overlaid with DB) for
	// display, and an apply hook that reconnects MQTT/openHAB at runtime.
	effective := func() (config.MQTT, config.OpenHAB) {
		m, o := envMQTT, envOpenHAB
		if all, err := configRepo.All(ctx0); err == nil {
			overlayServerCfg(&m, &o, all)
		}
		return m, o
	}
	board.SetServerConfig(configRepo, effective, func() error {
		m, o := effective()
		if err := bridge.Reconfigure(m); err != nil {
			return err
		}
		ohConn.Reconfigure(o)
		return nil
	})

	root := chi.NewRouter()
	// Log every request with the real client IP (from the reverse proxy).
	root.Use(httplog.Middleware(logger))
	root.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app", http.StatusFound)
	})
	root.Get("/version", board.Version)
	root.Get("/login", sessions.LoginForm)
	root.Post("/login", sessions.Login)
	root.Get("/logout", sessions.Logout)
	root.Get("/account", sessions.Account)
	root.Get("/dialog/authorize", oauth.Authorize)
	root.Post("/oauth/token", oauth.Token)

	// Board (per-user devices + rooms).
	root.With(sessions.RequireLogin).Get("/app", board.Board)
	root.With(sessions.RequireLogin).Post("/app/rooms", board.CreateRoom)
	root.With(sessions.RequireLogin).Post("/app/rooms/{id}/rename", board.RenameRoom)
	root.With(sessions.RequireLogin).Post("/app/rooms/{id}/delete", board.DeleteRoom)
	root.With(sessions.RequireLogin).Get("/app/schema", board.Schema)
	root.With(sessions.RequireLogin).Get("/app/devices/new", board.NewDevice)
	root.With(sessions.RequireLogin).Get("/app/devices/{id}/edit", board.EditDevice)
	root.With(sessions.RequireLogin).Post("/app/devices", board.CreateDevice)
	root.With(sessions.RequireLogin).Post("/app/devices/{id}", board.UpdateDevice)
	root.With(sessions.RequireLogin).Post("/app/devices/{id}/move", board.MoveDevice)
	root.With(sessions.RequireLogin).Post("/app/devices/{id}/delete", board.DeleteDevice)
	root.With(sessions.RequireLogin).Get("/app/settings", board.Settings)
	root.With(sessions.RequireLogin).Post("/app/settings/tag", board.SetDiscoveryTagSettings)
	root.With(sessions.RequireLogin).Get("/app/settings/export", board.ExportConfig)
	root.With(sessions.RequireLogin).Post("/app/settings/import", board.ImportConfig)
	root.With(sessions.RequireLogin).Post("/app/settings/reset", board.ResetConfig)
	root.With(sessions.RequireLogin).Post("/app/settings/servers", board.ServerConfig)
	root.With(sessions.RequireLogin).Get("/app/openhab/items", board.OpenHABItems)
	root.With(sessions.RequireLogin).Get("/app/discover/vacuum", board.VacuumSetupPage)
	root.With(sessions.RequireLogin).Post("/app/discover/vacuum", board.CreateVacuum)
	root.With(sessions.RequireLogin).Get("/app/discover", board.Discover)
	root.With(sessions.RequireLogin).Post("/app/discover/add", board.AddDiscovered)
	root.With(sessions.RequireLogin).Post("/app/discover/ignore", board.IgnoreDiscovered)
	root.With(sessions.RequireLogin).Get("/app/discover/ignored", board.IgnoredList)
	root.With(sessions.RequireLogin).Post("/app/discover/unignore", board.Unignore)
	root.With(sessions.RequireLogin).Post("/app/discover/tag", board.SetDiscoveryTag)
	root.With(sessions.RequireLogin).Post("/app/discover/mode", board.SetDiscoveryMode)
	root.With(sessions.RequireLogin).Post("/app/discover/clear-ignore", board.ClearIgnore)

	// Admin: user management (admin-only).
	root.With(sessions.RequireAdmin).Get("/app/users", sessions.UsersPage)
	root.With(sessions.RequireAdmin).Post("/app/users", sessions.CreateUser)
	root.With(sessions.RequireAdmin).Post("/app/users/{id}/delete", sessions.DeleteUser)

	// Public, tokenized HLS proxy (no login: Alice's player calls it directly).
	root.HandleFunc("/stream/{token}", streamProxy.Handler())

	root.Mount("/provider", api.Routes())

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Web.Port),
		Handler: root,
	}
	go func() {
		logger.Info("http server listening", "addr", srv.Addr, "behind_proxy", cfg.Web.BehindProxy)
		var err error
		if cfg.Web.BehindProxy {
			err = srv.ListenAndServe()
		} else {
			err = srv.ListenAndServeTLS(cfg.Web.TLSCert, cfg.Web.TLSKey)
		}
		if err != nil && err != http.ErrServerClosed {
			logger.Error("http server", "err", err)
		}
	}()

	// Block until interrupted, then shut the server down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("running; press Ctrl+C to stop")
	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// overlayServerCfg applies non-empty app_config values over the env-derived MQTT
// and openHAB config. Empty/absent keys keep the env value.
func overlayServerCfg(m *config.MQTT, o *config.OpenHAB, all map[string]string) {
	if v := all[store.CfgMQTTHost]; v != "" {
		m.Host = v
	}
	if v := all[store.CfgMQTTPort]; v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			m.Port = p
		}
	}
	if v := all[store.CfgMQTTUser]; v != "" {
		m.User = v
	}
	if v := all[store.CfgMQTTPassword]; v != "" {
		m.Password = v
	}
	if v := all[store.CfgOpenHABURL]; v != "" {
		o.URL = v
	}
	if v := all[store.CfgOpenHABToken]; v != "" {
		o.Token = v
	}
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
