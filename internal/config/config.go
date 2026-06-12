package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr        string
	HTTPAddr          string
	DataDir           string
	CertDir           string
	ControlHost       string
	AdminBearerSecret string
	DeviceTokenSecret string
	CodeHashPepper    string
	LEEmail           string
	PublicBaseURL     string
	Dev               bool
	LogLevel          string
}

func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:    getEnv("LISTEN_ADDR", ":443"),
		HTTPAddr:      getEnv("HTTP_ADDR", ":80"),
		DataDir:       getEnv("DATA_DIR", "/data"),
		CertDir:       getEnv("CERT_DIR", "/certs"),
		ControlHost:   getEnv("CONTROL_HOST", "tunnel.mate.iwwwan.com"),
		LogLevel:      getEnv("LOG_LEVEL", "info"),
		PublicBaseURL: getEnv("PUBLIC_BASE_URL", "https://tunnel.mate.iwwwan.com"),
	}
	cfg.Dev, _ = strconv.ParseBool(getEnv("DEV", "false"))
	cfg.AdminBearerSecret = os.Getenv("ADMIN_BEARER_SECRET")
	cfg.DeviceTokenSecret = os.Getenv("DEVICE_TOKEN_SECRET")
	cfg.CodeHashPepper = os.Getenv("CODE_HASH_PEPPER")
	cfg.LEEmail = os.Getenv("LETSENCRYPT_EMAIL")

	var missing []string
	if cfg.AdminBearerSecret == "" {
		missing = append(missing, "ADMIN_BEARER_SECRET")
	}
	if cfg.DeviceTokenSecret == "" {
		missing = append(missing, "DEVICE_TOKEN_SECRET")
	}
	if cfg.CodeHashPepper == "" {
		missing = append(missing, "CODE_HASH_PEPPER")
	}
	if !cfg.Dev && cfg.LEEmail == "" {
		missing = append(missing, "LETSENCRYPT_EMAIL")
	}
	if len(missing) > 0 {
		return nil, errors.New("missing required env vars: " + strings.Join(missing, ", "))
	}
	return cfg, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
