package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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
	// GITHUB_APP_PRIVATE_KEY_B64; nil leaves POST /onboard disabled (404).
	gh, err := newGithubAppClient(cfg)
	if err != nil {
		log.Printf("onboarding disabled: %v", err)
		gh = nil
	}
	d.gh = gh

	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/deploy", d.handleDeploy)
	mux.HandleFunc("POST /onboard", d.handleOnboard)
	mux.HandleFunc("GET /onboard/repos", d.handleListOnboardRepos)
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
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
