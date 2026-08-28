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

	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/deploy", d.handleDeploy)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /deployments", d.handleListDeployments)
	mux.HandleFunc("GET /deployments/{id}", d.handleGetDeployment)

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
