package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration, sourced from environment variables.
type Config struct {
	WebhookSecret        string
	ServicesDir          string
	AllowedImagePrefixes []string
	GHCRUser             string
	GHCRToken            string
	Port                 string
	DataDir              string
	LogRetention         int
	ReadToken            string
	HookTimeout          time.Duration
	NotifyWebhookURLs    []string
	NotifyTemplate       string
	NotifyContentType    string
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		WebhookSecret:     os.Getenv("WEBHOOK_SECRET"),
		ServicesDir:       envOr("SERVICES_DIR", "/services"),
		GHCRUser:          os.Getenv("GHCR_USER"),
		GHCRToken:         os.Getenv("GHCR_TOKEN"),
		Port:              envOr("PORT", "8080"),
		DataDir:           envOr("DATA_DIR", "/data"),
		LogRetention:      envIntOr("LOG_RETENTION", 100),
		ReadToken:         os.Getenv("READ_TOKEN"),
		HookTimeout:       time.Duration(envIntOr("HOOK_TIMEOUT", 60)) * time.Second,
		NotifyWebhookURLs: splitNonEmpty(os.Getenv("NOTIFY_WEBHOOK_URLS")),
		NotifyTemplate:    os.Getenv("NOTIFY_TEMPLATE"),
		NotifyContentType: envOr("NOTIFY_CONTENT_TYPE", "application/json"),
	}

	prefixes := splitNonEmpty(os.Getenv("ALLOWED_IMAGE_PREFIXES"))
	if len(prefixes) == 0 {
		prefixes = []string{"ghcr.io/"}
	}
	cfg.AllowedImagePrefixes = prefixes

	if cfg.WebhookSecret == "" {
		return nil, fmt.Errorf("WEBHOOK_SECRET is required")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func splitNonEmpty(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
