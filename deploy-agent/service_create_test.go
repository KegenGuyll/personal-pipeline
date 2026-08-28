package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newCreateDeployer(t *testing.T) *deployer {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "existing"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "existing", "docker-compose.yml"), []byte("services:\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return &deployer{
		cfg: &Config{
			ServicesDir:          dir,
			AllowedImagePrefixes: []string{"ghcr.io/owner/"},
			AdminToken:           "admintok",
		},
	}
}

func TestValidateServiceSpec(t *testing.T) {
	d := newCreateDeployer(t)
	cases := []struct {
		name string
		spec ServiceSpec
		code int
	}{
		{"bad name", ServiceSpec{Name: "Bad_Name", Image: "ghcr.io/owner/x", Port: 3000}, http.StatusBadRequest},
		{"reserved name", ServiceSpec{Name: "_template", Image: "ghcr.io/owner/x", Port: 3000}, http.StatusBadRequest},
		{"missing image", ServiceSpec{Name: "web", Port: 3000}, http.StatusBadRequest},
		{"disallowed image", ServiceSpec{Name: "web", Image: "ghcr.io/other/x", Port: 3000}, http.StatusForbidden},
		{"bad port", ServiceSpec{Name: "web", Image: "ghcr.io/owner/x", Port: 70000}, http.StatusBadRequest},
		{"bad hostname", ServiceSpec{Name: "web", Image: "ghcr.io/owner/x", Port: 3000, Hostname: "bad_host"}, http.StatusBadRequest},
		{"existing", ServiceSpec{Name: "existing", Image: "ghcr.io/owner/x", Port: 3000}, http.StatusConflict},
		{"valid", ServiceSpec{Name: "web", Image: "ghcr.io/owner/x", Port: 3000, Hostname: "web"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := c.spec
			if spec.Hostname == "" {
				spec.Hostname = spec.Name
			}
			code, _ := d.validateServiceSpec(&spec)
			if code != c.code {
				t.Fatalf("code = %d, want %d", code, c.code)
			}
		})
	}
}

func TestRenderServiceCompose(t *testing.T) {
	s, err := renderServiceCompose(&ServiceSpec{Name: "web", Image: "ghcr.io/o/web", Port: 4242, Hostname: "webtail"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"web:",
		"image: ghcr.io/o/web:${TAG}",
		`- "4242"`,
		"hostname: ${TS_HOSTNAME:-webtail}",
		"tailscale/tailscale:",
		"$${TS_CERT_DOMAIN}",
		`"Proxy":"http://web:4242"`,
		"depends_on:\n      - web",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestHandleCreateService(t *testing.T) {
	d := newCreateDeployer(t)

	body := `{"name":"web","image":"ghcr.io/owner/web","port":8080,"hostname":"web"}`
	req := httptest.NewRequest("POST", "/services", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admintok")
	w := httptest.NewRecorder()
	d.handleCreateService(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	composePath := filepath.Join(d.cfg.ServicesDir, "web", "docker-compose.yml")
	info, err := os.Stat(composePath)
	if err != nil {
		t.Fatalf("compose not written: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("mode = %v, want 0644", info.Mode().Perm())
	}
	data, _ := os.ReadFile(composePath)
	if !strings.Contains(string(data), "image: ghcr.io/owner/web:${TAG}") {
		t.Fatalf("compose content wrong:\n%s", data)
	}

	// Adding again conflicts.
	req2 := httptest.NewRequest("POST", "/services", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer admintok")
	w2 := httptest.NewRecorder()
	d.handleCreateService(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("second code = %d, want 409", w2.Code)
	}
}

func TestAuthorizeWrite(t *testing.T) {
	d := newCreateDeployer(t)

	req := httptest.NewRequest("POST", "/services", nil)
	req.Header.Set("Authorization", "Bearer admintok")
	if !d.authorizeWrite(httptest.NewRecorder(), req) {
		t.Fatal("expected authorized")
	}

	req2 := httptest.NewRequest("POST", "/services", nil)
	w2 := httptest.NewRecorder()
	if d.authorizeWrite(w2, req2) {
		t.Fatal("expected unauthorized without header")
	}
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w2.Code)
	}

	d.cfg.AdminToken = ""
	req3 := httptest.NewRequest("POST", "/services", nil)
	req3.Header.Set("Authorization", "Bearer admintok")
	w3 := httptest.NewRecorder()
	if d.authorizeWrite(w3, req3) {
		t.Fatal("expected disabled when AdminToken empty")
	}
	if w3.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w3.Code)
	}
}
