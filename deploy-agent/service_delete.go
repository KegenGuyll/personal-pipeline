package main

import (
	"net/http"
	"os"
	"path/filepath"
)

// handleDeleteService removes a service from the server: it stops and removes
// the service's containers (docker compose down), then deletes the service
// directory (compose file, hooks, .env). Volumes are kept unless ?purge=true.
// Admin-gated, like POST /services.
func (d *deployer) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	if !d.authorizeWrite(w, r) {
		return
	}
	name := r.PathValue("name")
	if !serviceNameRe.MatchString(name) || name == "_template" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid service name"})
		return
	}
	dir := filepath.Join(d.cfg.ServicesDir, name)
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown service"})
		return
	}

	purge := r.URL.Query().Get("purge") == "1" || r.URL.Query().Get("purge") == "true"
	warnings := []string{}
	args := []string{"down"}
	if purge {
		args = append(args, "-v")
	}
	if out, err := runCompose(d.cfg, dir, name, args...); err != nil {
		warnings = append(warnings, "compose down failed (containers may still be running): "+tail(out, 200))
	}
	if err := os.RemoveAll(dir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	logEvent("service.deleted", map[string]any{"project": name, "purge": purge, "compose_down_ok": len(warnings) == 0})
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":  name,
		"purge":    purge,
		"warnings": warnings,
	})
}
