package config

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

type Config struct {
	HTTPAddress    string
	DatabaseURL    string
	SecretKey      string
	SecretKeyBytes []byte
	LogLevel       string
	OIDC           OIDCConfig
}

type OIDCConfig struct {
	ClientID       string
	ClientSecret   string
	RedirectURL    string
	AllowedDomains []string
	AutoProvision  bool
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddress: getEnv("MIGRATEHUB_HTTP_ADDR", ":8080"),
		LogLevel:    getEnv("MIGRATEHUB_LOG_LEVEL", "info"),
		OIDC: OIDCConfig{
			ClientID:       os.Getenv("MIGRATEHUB_OIDC_GOOGLE_CLIENT_ID"),
			ClientSecret:   os.Getenv("MIGRATEHUB_OIDC_GOOGLE_CLIENT_SECRET"),
			RedirectURL:    os.Getenv("MIGRATEHUB_OIDC_GOOGLE_REDIRECT_URL"),
			AllowedDomains: splitAndTrim(os.Getenv("MIGRATEHUB_OIDC_ALLOWED_DOMAINS")),
			AutoProvision:  strings.EqualFold(os.Getenv("MIGRATEHUB_OIDC_AUTO_PROVISION"), "true"),
		},
	}

	cfg.DatabaseURL = os.Getenv("MIGRATEHUB_DB_DSN")
	cfg.SecretKey = os.Getenv("MIGRATEHUB_SECRET_KEY")

	if cfg.SecretKey != "" {
		keyBytes, err := base64.StdEncoding.DecodeString(cfg.SecretKey)
		if err != nil {
			return Config{}, errors.New("MIGRATEHUB_SECRET_KEY must be base64")
		}
		cfg.SecretKeyBytes = keyBytes
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("MIGRATEHUB_DB_DSN is required")
	}
	if c.SecretKey == "" || len(c.SecretKeyBytes) < 32 {
		return errors.New("MIGRATEHUB_SECRET_KEY is required (base64, >=32 bytes)")
	}
	if c.OIDC.ClientID == "" {
		return errors.New("MIGRATEHUB_OIDC_GOOGLE_CLIENT_ID is required")
	}
	if c.OIDC.ClientSecret == "" {
		return errors.New("MIGRATEHUB_OIDC_GOOGLE_CLIENT_SECRET is required")
	}
	if c.OIDC.RedirectURL == "" {
		return errors.New("MIGRATEHUB_OIDC_GOOGLE_REDIRECT_URL is required")
	}
	return nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func splitAndTrim(input string) []string {
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
