package main

import (
	"net/http"
	"os"
	"path/filepath"
)

// serviceActionCompose maps a dashboard lifecycle action to the docker compose
// subcommand that performs it. "start" mirrors what a webhook deploy does to
// bring a service up (idempotent: creates containers if missing, or restarts
// them if merely stopped). "stop" halts the service's containers without
// removing them; "restart" restarts them in place.
func serviceActionCompose(action string) ([]string, bool) {
	switch action {
	case "start":
		return []string{"up", "-d", "--remove-orphans"}, true
	case "stop":
		return []string{"stop"}, true
	case "restart":
		return []string{"restart"}, true
	}
	return nil, false
}

// handleServiceAction runs a lifecycle command (start/stop/restart) against a
// service's compose project. Admin-gated, like the other mutation endpoints.
// The action is baked into the handler because mux patterns like
// "POST /services/{name}/start" disambiguate it in the request path.
func (d *deployer) handleServiceAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		compArgs, ok := serviceActionCompose(action)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action"})
			return
		}

		out, err := runCompose(d.cfg, dir, name, compArgs...)
		if err != nil {
			logEvent("service.action_failed", map[string]any{
				"project": name, "action": action, "error": err.Error(), "output": tail(out, 200),
			})
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "action failed",
				"action":  action,
				"project": name,
				"output":  tail(out, 400),
			})
			return
		}

		logEvent("service.action", map[string]any{"project": name, "action": action})
		writeJSON(w, http.StatusOK, map[string]any{
			"action":  action,
			"project": name,
			"output":  tail(out, 400),
		})
	}
}
