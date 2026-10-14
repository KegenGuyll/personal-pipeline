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
	AdminToken           string
	HookTimeout          time.Duration
	NotifyWebhookURLs    []string
	NotifyTemplate       string
	NotifyContentType    string

	// ImagePrune controls automatic cleanup of unused Docker images after a
	// successful deploy. Values: "off" to disable, "all" to prune every image
	// no container uses regardless of age, or a Go duration (e.g. "24h") to
	// prune only images created before that cutoff. Default "24h" — old tags
	// are still re-pullable from the registry, so pruning never loses the
	// ability to roll back.
	ImagePrune string

	// GitHub App (project onboarding). Empty GITHUB_APP_ID / private key
	// disable the POST /onboard endpoint.
	GithubAppID             int64
	GithubAppPrivateKeyB64  string
	GithubAppInstallationID int64
	PipelineOwner           string
	PipelineRef             string

	// DeployWebhookURL is the public URL of this agent's /hooks/deploy
	// endpoint (e.g. https://deploy.example.com/hooks/deploy). When set,
	// onboarding also creates DEPLOY_WEBHOOK_URL + DEPLOY_WEBHOOK_SECRET (=
	// WEBHOOK_SECRET) in each project repo, so every onboarded repo can
	// notify the agent with zero manual secret setup.
	DeployWebhookURL string

	// TailscaleAuthKey is the shared tailnet auth key injected into every
	// onboarded service's SERVICE_ENV (the sidecar requires TS_AUTHKEY).
	// Optional: if unset, onboarding requires the caller to provide
	// TS_AUTHKEY explicitly.
	TailscaleAuthKey string
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
		AdminToken:        os.Getenv("ADMIN_TOKEN"),
		HookTimeout:       time.Duration(envIntOr("HOOK_TIMEOUT", 60)) * time.Second,
		NotifyWebhookURLs: splitNonEmpty(os.Getenv("NOTIFY_WEBHOOK_URLS")),
		NotifyTemplate:    os.Getenv("NOTIFY_TEMPLATE"),
		NotifyContentType: envOr("NOTIFY_CONTENT_TYPE", "application/json"),
		ImagePrune:        envOr("IMAGE_PRUNE", "24h"),

		GithubAppID:             envInt64Or("GITHUB_APP_ID", 0),
		GithubAppPrivateKeyB64:  os.Getenv("GITHUB_APP_PRIVATE_KEY_B64"),
		GithubAppInstallationID: envInt64Or("GITHUB_APP_INSTALLATION_ID", 0),
		PipelineOwner:           envOr("PIPELINE_OWNER", os.Getenv("GHCR_OWNER")),
		PipelineRef:             envOr("PIPELINE_REF", "main"),
		DeployWebhookURL:        os.Getenv("DEPLOY_WEBHOOK_URL"),
		TailscaleAuthKey:        os.Getenv("TS_AUTHKEY"),
	}

	prefixes := splitNonEmpty(os.Getenv("ALLOWED_IMAGE_PREFIXES"))
	if len(prefixes) == 0 {
		prefixes = []string{"ghcr.io/"}
	}
	cfg.AllowedImagePrefixes = prefixes

	// Normalize IMAGE_PRUNE: off/false/disabled/never => "", all/always =>
	// "all", anything else must parse as a POSITIVE Go duration (kept as
	// written). Fail fast at startup so a typo doesn't silently disable or
	// break cleanup — and so 0/negatives can't make the until filter
	// silently prune everything.
	switch pr := strings.ToLower(strings.TrimSpace(cfg.ImagePrune)); pr {
	case "", "off", "false", "disabled", "never":
		cfg.ImagePrune = ""
	case "all", "always":
		cfg.ImagePrune = "all"
	default:
		d, err := time.ParseDuration(pr)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("IMAGE_PRUNE: invalid value %q (want off, all, or a positive duration like 24h)", cfg.ImagePrune)
		}
		cfg.ImagePrune = pr
	}

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

func envInt64Or(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
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
