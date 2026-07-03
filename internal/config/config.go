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

	devices, err := LoadDevices(cfg.DevicesFile)
	if err != nil {
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
