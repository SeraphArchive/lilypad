// Package config loads LilyPad configuration from YAML with environment
// overrides. Secrets (DB DSN, signing key) are expected via local config or
// environment and are never committed.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"lilypad/internal/protocol"
	"net/url"
	"os"

	"gopkg.in/yaml.v3"
)

type TLS struct {
	Enabled bool   `yaml:"enabled"`
	Cert    string `yaml:"cert"`
	Key     string `yaml:"key"`
}

type DB struct {
	DSN string `yaml:"dsn"`
}

type Signing struct {
	Mode          string `yaml:"mode"` // noop | rsa
	PrivateKeyPEM string `yaml:"private_key_pem"`
}

type SMTP struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
	From string `yaml:"from"`
}

type Portal struct {
	BaseURL                  string `yaml:"base_url"`
	RequireEmailVerification bool   `yaml:"require_email_verification"`
	SMTP                     SMTP   `yaml:"smtp"`
	// CountryCode/CurrencyCode are reported by the platform shim's
	// payment/purchase/steam/userinfo (the real platform derives them from the
	// Steam store region). Empty = the shim defaults (CN/CNY).
	CountryCode  string `yaml:"country_code"`
	CurrencyCode string `yaml:"currency_code"`
}

type Asset struct {
	Version string `yaml:"version"`
	Hash    string `yaml:"hash"`
}

type Constants struct {
	SystemLock  []any `yaml:"systemLock"`
	LotteryShop []any `yaml:"lotteryShop"`
	// WebShopProducts is the platform-catalog product list served by
	// /api/web/shop/product/list as user_web_shop_product rows. The row fields
	// (_masterGamelibProductId, _campaignType, _campaignMode, _priority,
	// _consumable, _endDatetime, _limitedDatetime, _limitedCount) come from the
	// platform's product metadata, not the game master data, so they are
	// configured here. Empty = no web-shop sale running.
	WebShopProducts []any `yaml:"web_shop_products"`
}

type Gem struct {
	// ValidateCost prefers a known master quartz cost for gem-paid requests.
	// Missing/non-quartz master entries fall back to the declared cost; zero
	// declared cost stays zero. False always trusts declared cost. Defaults true.
	ValidateCost *bool `yaml:"validate_cost"`
}

type Config struct {
	Listen   string `yaml:"listen"`
	TLS      TLS    `yaml:"tls"`
	DB       DB     `yaml:"db"`
	DataDir  string `yaml:"data_dir"`
	Fixtures string `yaml:"fixtures"`
	// Deprecated compatibility field: client push baselines are always checked.
	EnforcePushBaseline bool      `yaml:"enforce_push_baseline"`
	Signing             Signing   `yaml:"signing"`
	Portal              Portal    `yaml:"portal"`
	Asset               Asset     `yaml:"asset"`
	Constants           Constants `yaml:"constants"`
	Gem                 Gem       `yaml:"gem"`
}

// GemValidateCost reports whether gacha costs are taken from master data
// (server-authoritative). Defaults to true when unset.
func (c *Config) GemValidateCost() bool {
	return c.Gem.ValidateCost == nil || *c.Gem.ValidateCost
}

// Load reads YAML from path, applies defaults, then environment overrides.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("config: expected exactly one YAML document")
	}
	applyDefaults(c)
	overrideEnv(c)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Validate() error {
	if c.Signing.Mode != "noop" && c.Signing.Mode != "rsa" {
		return fmt.Errorf("config: signing.mode must be noop or rsa")
	}
	if c.Signing.Mode == "rsa" && c.Signing.PrivateKeyPEM == "" {
		return fmt.Errorf("config: rsa requires private_key_pem")
	}
	if c.TLS.Enabled && (c.TLS.Cert == "" || c.TLS.Key == "") {
		return fmt.Errorf("config: TLS requires cert and key")
	}
	u, err := url.Parse(c.Portal.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("config: portal.base_url must be an absolute HTTP(S) URL")
	}
	if c.Portal.SMTP.Port < 0 || c.Portal.SMTP.Port > 65535 {
		return fmt.Errorf("config: invalid SMTP port")
	}
	if c.Portal.RequireEmailVerification && (c.Portal.SMTP.Host == "" || c.Portal.SMTP.From == "") {
		return fmt.Errorf("config: email verification requires SMTP host and from")
	}
	for _, entry := range []struct{ src, dst any }{{c.Constants.SystemLock, &[]protocol.SystemLock{}}, {c.Constants.LotteryShop, &[]int{}}, {c.Constants.WebShopProducts, &[]map[string]any{}}} {
		raw, err := json.Marshal(entry.src)
		if err != nil {
			return fmt.Errorf("config: invalid constants: %w", err)
		}
		if err := json.Unmarshal(raw, entry.dst); err != nil {
			return fmt.Errorf("config: invalid constants: %w", err)
		}
	}
	return nil
}

func applyDefaults(c *Config) {
	if c.Listen == "" {
		c.Listen = ":8443"
	}
	if c.Signing.Mode == "" {
		c.Signing.Mode = "noop"
	}
	if c.Portal.BaseURL == "" {
		c.Portal.BaseURL = "http://localhost:8443"
	}
}

func overrideEnv(c *Config) {
	if v := os.Getenv("LILYPAD_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("LILYPAD_DB_DSN"); v != "" {
		c.DB.DSN = v
	}
	if v, present := os.LookupEnv("LILYPAD_DATA_DIR"); present {
		c.DataDir = v
	}
	if v := os.Getenv("LILYPAD_FIXTURES"); v != "" {
		c.Fixtures = v
	}
	if v := os.Getenv("LILYPAD_SIGNING_PRIVATE_KEY_PEM"); v != "" {
		c.Signing.PrivateKeyPEM = v
	}
}
