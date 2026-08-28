package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DeploySummary is the non-secret subset of a DeployRecord shown in the
// service list — just enough to display "what version is running now".
type DeploySummary struct {
	ID     string    `json:"id"`
	Tag    string    `json:"tag"`
	Sha    string    `json:"sha"`
	Status string    `json:"status"`
	TS     time.Time `json:"ts"`
}

// ServiceInfo is the dashboard's view of one deployable service. Secrets from
// the service's `.env` are deliberately absent — only the `Tag` is surfaced.
type ServiceInfo struct {
	Name       string         `json:"name"`
	Image      string         `json:"image,omitempty"`
	Tag        string         `json:"tag"`
	Hostname   string         `json:"hostname,omitempty"`
	Access     string         `json:"access,omitempty"` // "private" (Tailscale) or "public"
	Port       int            `json:"port,omitempty"`
	LastDeploy *DeploySummary `json:"last_deploy,omitempty"`
}

// listServices enumerates every deployable service under ServicesDir. A service
// is a directory (excluding `_template`, dotfiles, and anything without a
// docker-compose.yml) — the same "known project" check the webhook uses.
func (d *deployer) listServices() ([]ServiceInfo, error) {
	entries, err := os.ReadDir(d.cfg.ServicesDir)
	if err != nil {
		return nil, err
	}

	out := make([]ServiceInfo, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == "_template" || strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(d.cfg.ServicesDir, name)
		composePath := filepath.Join(dir, "docker-compose.yml")
		if _, err := os.Stat(composePath); err != nil {
			continue
		}

		info := ServiceInfo{Name: name}
		if b, err := os.ReadFile(composePath); err == nil {
			s := string(b)
			info.Image = composeImage(s)
			info.Hostname = composeHostname(s)
			info.Port = composePort(s)
			if strings.Contains(s, "tailscale/tailscale:") {
				info.Access = "private"
			} else {
				info.Access = "public"
			}
		}

		if env, err := readDotenv(filepath.Join(dir, ".env")); err == nil {
			info.Tag = env["TAG"]
		}

		if recs, err := d.hist.List(name, "", 1); err == nil && len(recs) > 0 {
			r := recs[0]
			info.LastDeploy = &DeploySummary{ID: r.ID, Tag: r.Tag, Sha: r.Sha, Status: r.Status, TS: r.TS}
		}

		out = append(out, info)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (d *deployer) handleListServices(w http.ResponseWriter, r *http.Request) {
	if !d.authorizeRead(w, r) {
		return
	}
	services, err := d.listServices()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if services == nil {
		services = []ServiceInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": services})
}

// ---- dotenv parsing (reads only the TAG we write; never re-exposes secrets) ----

func readDotenv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDotenv(string(data)), nil
}

func parseDotenv(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		m[key] = unquoteDotenv(line[eq+1:])
	}
	return m
}

// unquoteDotenv reverses the quoting writeEnvFile's quoteDotenvValue applies.
func unquoteDotenv(v string) string {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return v
	}
	v = v[1 : len(v)-1]
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
			switch v[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				b.WriteByte('\\')
				b.WriteByte(v[i])
			}
			continue
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// ---- lenient compose metadata extraction (best-effort; unknown => zero) ----

var (
	imageRe           = regexp.MustCompile(`(?m)^\s*image:\s*(.+?)\s*$`)
	tagSuffixRe       = regexp.MustCompile(`:\$\{?TAG\}?$`)
	composeHostnameRe = regexp.MustCompile(`hostname:\s*\$\{TS_HOSTNAME:-([^}]+)\}`)
	exposePortRe      = regexp.MustCompile(`(?s)expose:\s*\n\s*-\s*["']?(\d+)["']?`)
)

func composeImage(s string) string {
	m := imageRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return tagSuffixRe.ReplaceAllString(strings.TrimSpace(m[1]), "")
}

func composeHostname(s string) string {
	if m := composeHostnameRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func composePort(s string) int {
	if m := exposePortRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}
