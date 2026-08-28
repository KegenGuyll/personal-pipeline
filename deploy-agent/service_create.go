package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

var (
	serviceNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	hostnameRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$`)
)

// ServiceSpec is the operator-supplied request body for adding a service. It is
// deliberately small: the agent renders the compose file from a fixed template,
// so the webhook surface is unchanged (no arbitrary compose over the wire).
type ServiceSpec struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Port     int    `json:"port"`
	Hostname string `json:"hostname"`
}

// serviceComposeTemplate is the private (Tailscale sidecar) compose file the
// agent writes, mirroring services/_template/docker-compose.yml. Only the
// operator-supplied name/image/port/hostname are interpolated; everything else
// is fixed, including `${TS_CERT_DOMAIN}` (rendered as `$${...}` so compose
// passes it through to the tailscale container).
const serviceComposeTemplate = `services:
  {{.Name}}:
    image: {{.Image}}:${TAG}
    restart: unless-stopped
    env_file: .env
    networks:
      - proxy
    expose:
      - "{{.Port}}"

  tailscale:
    image: tailscale/tailscale:latest
    hostname: ${TS_HOSTNAME:-{{.Hostname}}}
    restart: unless-stopped
    environment:
      - TS_AUTHKEY=${TS_AUTHKEY}
      - TS_STATE_DIR=/var/lib/tailscale
      - TS_SERVE_CONFIG=/config/serve.json
      - TS_USERSPACE=false
      - TS_ENABLE_HEALTH_CHECK=true
      - TS_LOCAL_ADDR_PORT=127.0.0.1:41234
      - TS_AUTH_ONCE=true
    configs:
      - source: ts-serve
        target: /config/serve.json
    volumes:
      - ts-state:/var/lib/tailscale
    devices:
      - /dev/net/tun:/dev/net/tun
    cap_add:
      - net_admin
    networks:
      - proxy
    depends_on:
      - {{.Name}}
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://127.0.0.1:41234/healthz"]
      interval: 1m
      timeout: 10s
      retries: 3
      start_period: 10s

configs:
  ts-serve:
    content: |
      {"TCP":{"443":{"HTTPS":true}},
       "Web":{"$${TS_CERT_DOMAIN}:443":{"Handlers":{"/":{"Proxy":"http://{{.Name}}:{{.Port}}"}}}},
       "AllowFunnel":{"$${TS_CERT_DOMAIN}:443":false}}

networks:
  proxy:
    external: true

volumes:
  ts-state:
`

var serviceComposeTpl = template.Must(template.New("compose").Parse(serviceComposeTemplate))

func (d *deployer) handleCreateService(w http.ResponseWriter, r *http.Request) {
	if !d.authorizeWrite(w, r) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	var spec ServiceSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if spec.Port == 0 {
		spec.Port = 3000
	}
	if spec.Hostname == "" {
		spec.Hostname = spec.Name
	}

	if code, msg := d.validateServiceSpec(&spec); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}

	content, err := renderServiceCompose(&spec)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	dir := filepath.Join(d.cfg.ServicesDir, spec.Name)
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	composePath := filepath.Join(dir, "docker-compose.yml")
	tmp := composePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.Rename(tmp, composePath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	logEvent("service.added", map[string]any{"project": spec.Name, "image": spec.Image, "access": "private"})

	writeJSON(w, http.StatusCreated, ServiceInfo{
		Name:     spec.Name,
		Image:    spec.Image,
		Tag:      "",
		Hostname: spec.Hostname,
		Access:   "private",
		Port:     spec.Port,
	})
}

func (d *deployer) validateServiceSpec(spec *ServiceSpec) (int, string) {
	if !serviceNameRe.MatchString(spec.Name) || spec.Name == "_template" {
		return http.StatusBadRequest, "invalid service name (lowercase letters, digits, hyphens)"
	}
	if spec.Image == "" {
		return http.StatusBadRequest, "image is required"
	}
	if !imageAllowed(spec.Image, d.cfg.AllowedImagePrefixes) {
		return http.StatusForbidden, "image not in allowlist"
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return http.StatusBadRequest, "port must be between 1 and 65535"
	}
	if !hostnameRe.MatchString(spec.Hostname) {
		return http.StatusBadRequest, "invalid hostname (letters, digits, hyphens)"
	}
	if _, err := os.Stat(filepath.Join(d.cfg.ServicesDir, spec.Name)); err == nil {
		return http.StatusConflict, "service already exists"
	}
	return 0, ""
}

func renderServiceCompose(spec *ServiceSpec) (string, error) {
	var b strings.Builder
	if err := serviceComposeTpl.Execute(&b, spec); err != nil {
		return "", err
	}
	return b.String(), nil
}
