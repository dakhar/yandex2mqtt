// Package config loads and validates the application configuration.
//
// Secrets and infrastructure settings come exclusively from environment
// variables (12-factor style), which maps cleanly onto Ansible Vault ->
// container environment. The (non-secret) device catalog is loaded from a
// YAML file so it can use anchors and, later, be swapped for a database
// source without touching the rest of the code.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Config is the fully-resolved application configuration.
type Config struct {
	MQTT    MQTT
	Web     Web
	Session Session
	Admin   Admin
	OAuth   OAuthClient
	Yandex  Yandex
	OpenHAB OpenHAB
	Go2RTC  Go2RTC

	DBPath      string `env:"DB_PATH" envDefault:"./data/yandex2mqtt.db"`
	DevicesFile string `env:"DEVICES_FILE" envDefault:"./data/devices.yaml"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`

	// Devices is loaded from the YAML catalog, not from the environment.
	Devices []Device `env:"-"`
}

// MQTT holds broker connection settings.
type MQTT struct {
	Host     string `env:"MQTT_HOST" envDefault:"localhost"`
	Port     int    `env:"MQTT_PORT" envDefault:"1883"`
	User     string `env:"MQTT_USER"`
	Password string `env:"MQTT_PASSWORD"`
}

// Web holds HTTP(S) server settings.
type Web struct {
	Port int `env:"WEB_PORT" envDefault:"80"`
	// TLSKey / TLSCert are required unless BehindProxy is true (TLS is
	// terminated by an upstream reverse proxy).
	TLSKey      string `env:"WEB_TLS_KEY"`
	TLSCert     string `env:"WEB_TLS_CERT"`
	BehindProxy bool   `env:"WEB_BEHIND_PROXY" envDefault:"false"`
	// PublicURL is the externally reachable base (e.g. https://yahome.bels.pw).
	// When set, it's used to build absolute URLs (the video_stream proxy link)
	// instead of the request's Host header — robust to reverse proxies that don't
	// preserve Host.
	PublicURL string `env:"PUBLIC_URL"`
}

// Go2RTC points at a go2rtc instance (github.com/AlexxIT/go2rtc) used as the
// camera relay: it pulls RTSP/ONVIF and serves stable low-latency HLS that the
// video_stream proxy fetches. URL is the internal base (e.g. http://127.0.0.1:1984)
// reachable from this process; empty disables the builder's go2rtc stream picker.
type Go2RTC struct {
	URL string `env:"GO2RTC_URL"`
	// KeepaliveSec is how long (seconds) the video_stream proxy keeps a go2rtc
	// HLS session warm after the player's last request, to bridge the player's
	// fetch gaps past go2rtc's hardcoded 5s session timeout. 0 disables it.
	KeepaliveSec int `env:"GO2RTC_KEEPALIVE_SEC" envDefault:"30"`
}

// Session holds the cookie session secret (replaces the hardcoded value).
type Session struct {
	Secret string `env:"SESSION_SECRET,required"`
}

// Admin is the single local user. Multi-user support will move to the DB
// alongside multitenancy.
type Admin struct {
	ID       string `env:"ADMIN_ID" envDefault:"1"`
	Username string `env:"ADMIN_USERNAME,required"`
	Password string `env:"ADMIN_PASSWORD,required"`
	Name     string `env:"ADMIN_NAME" envDefault:"Administrator"`
}

// OAuthClient is the single registered OAuth client (Yandex).
type OAuthClient struct {
	DBID         string `env:"OAUTH_CLIENT_DBID" envDefault:"1"`
	ClientID     string `env:"OAUTH_CLIENT_ID,required"`
	ClientSecret string `env:"OAUTH_CLIENT_SECRET,required"`
	Name         string `env:"OAUTH_CLIENT_NAME" envDefault:"Yandex"`
	Trusted      bool   `env:"OAUTH_CLIENT_TRUSTED" envDefault:"false"`
}

// Yandex holds the state-notification callback credentials. Optional: when
// SkillID/OAuthToken are empty, state notifications are disabled.
type Yandex struct {
	SkillID    string `env:"YANDEX_SKILL_ID"`
	OAuthToken string `env:"YANDEX_OAUTH_TOKEN"`
	UserID     string `env:"YANDEX_USER_ID"`
}

// NotificationEnabled reports whether Yandex state notifications are configured.
func (c *Config) NotificationEnabled() bool {
	return c.Yandex.SkillID != "" && c.Yandex.OAuthToken != ""
}

// OpenHAB is the openHAB connector configuration. The API token is read from
// TokenFile (preferred, so it can be a mounted secret) or Token directly.
type OpenHAB struct {
	URL       string `env:"OPENHAB_URL"`
	Token     string `env:"OPENHAB_TOKEN"`
	TokenFile string `env:"OPENHAB_TOKEN_FILE"`
}

// Enabled reports whether the openHAB connector should run.
func (o OpenHAB) Enabled() bool { return o.URL != "" && o.Token != "" }

// Load reads configuration from the environment and the device catalog file,
// then validates it. It fails fast so misconfiguration is caught at startup
// rather than at request time.
func Load() (*Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return nil, fmt.Errorf("parse environment: %w", err)
	}

	// Default the notification user to the admin user.
	if cfg.Yandex.UserID == "" {
		cfg.Yandex.UserID = cfg.Admin.ID
	}

	// Load the openHAB API token from its file if given.
	if cfg.OpenHAB.Token == "" && cfg.OpenHAB.TokenFile != "" {
		b, err := os.ReadFile(cfg.OpenHAB.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read openhab token file %q: %w", cfg.OpenHAB.TokenFile, err)
		}
		cfg.OpenHAB.Token = strings.TrimSpace(string(b))
	}

	// The device catalog file is only a seed source; once the DB is populated
	// it becomes authoritative, so a missing file is not fatal.
	devices, err := LoadDevices(cfg.DevicesFile)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load devices %q: %w", cfg.DevicesFile, err)
	}
	cfg.Devices = devices

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.Web.Port < 1 || c.Web.Port > 65535 {
		return fmt.Errorf("web port out of range: %d", c.Web.Port)
	}
	if c.MQTT.Port < 1 || c.MQTT.Port > 65535 {
		return fmt.Errorf("mqtt port out of range: %d", c.MQTT.Port)
	}
	if !c.Web.BehindProxy && (c.Web.TLSKey == "" || c.Web.TLSCert == "") {
		return fmt.Errorf("WEB_TLS_KEY and WEB_TLS_CERT are required unless WEB_BEHIND_PROXY=true")
	}

	seen := make(map[string]struct{}, len(c.Devices))
	for i, d := range c.Devices {
		if d.ID == "" {
			return fmt.Errorf("device #%d: empty id", i)
		}
		if _, dup := seen[d.ID]; dup {
			return fmt.Errorf("duplicate device id: %q", d.ID)
		}
		seen[d.ID] = struct{}{}
	}
	return nil
}
