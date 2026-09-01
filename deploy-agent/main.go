package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("mkdir data dir: %v", err)
	}

	hist := newHistory(filepath.Join(cfg.DataDir, "deployments.jsonl"), cfg.LogRetention)
	d := &deployer{
		cfg:      cfg,
		hist:     hist,
		notifier: newNotifier(cfg),
		locks:    map[string]*sync.Mutex{},
	}

	// GitHub App client for project onboarding. Configured via GITHUB_APP_ID +
	// GITHUB_APP_PRIVATE_KEY_B64; leaving d.gh nil keeps onboarding disabled
	// (404). Only assign a non-nil client — assigning a nil *githubAppClient
	// into the interface would make `d.gh == nil` false and panic on use.
	gh, err := newGithubAppClient(cfg)
	switch {
	case err != nil:
		log.Printf("onboarding disabled: %v", err)
	case gh == nil:
		log.Printf("onboarding disabled: GITHUB_APP_ID / GITHUB_APP_PRIVATE_KEY_B64 not configured")
	default:
		d.gh = gh
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/deploy", d.handleDeploy)
	mux.HandleFunc("POST /onboard", d.handleOnboard)
	mux.HandleFunc("GET /onboard/repos", d.handleListOnboardRepos)
	mux.HandleFunc("GET /onboard/diagnostics", d.handleOnboardDiagnostics)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /deployments", d.handleListDeployments)
	mux.HandleFunc("GET /deployments/{id}", d.handleGetDeployment)
	mux.HandleFunc("GET /services", d.handleListServices)
	mux.HandleFunc("POST /services", d.handleCreateService)
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", webHandler()))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})

	addr := ":" + cfg.Port
	logEvent("server.start", map[string]any{"addr": addr, "services_dir": cfg.ServicesDir})
	if err := http.ListenAndServe(addr, logRequests(mux)); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// statusRecorder captures the response status so the access log can include it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logRequests emits one http.request event per inbound request that returned
// 4xx/5xx (method, path, status, duration) — a quiet access log limited to
// errors, so what happened behind a proxy (e.g. a Cloudflare 502 page hiding
// the body) is always visible here without 2xx/healthz noise.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status >= 400 {
			logEvent("http.request", map[string]any{
				"method": r.Method, "path": r.URL.Path, "status": rec.status,
				"duration_ms": time.Since(start).Milliseconds(),
			})
		}
	})
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
