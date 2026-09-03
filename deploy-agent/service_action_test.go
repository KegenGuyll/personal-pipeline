package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceActionCompose(t *testing.T) {
	cases := []struct {
		action string
		args   []string
		ok     bool
	}{
		{"start", []string{"up", "-d", "--remove-orphans"}, true},
		{"stop", []string{"stop"}, true},
		{"restart", []string{"restart"}, true},
		{"down", nil, false},
		{"", nil, false},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			args, ok := serviceActionCompose(c.action)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				if args != nil {
					t.Fatalf("args = %v, want nil for unknown action", args)
				}
				return
			}
			if strings.Join(args, " ") != strings.Join(c.args, " ") {
				t.Fatalf("args = %v, want %v", args, c.args)
			}
		})
	}
}

// fakeDocker installs a `docker` on PATH that records its argv (one arg per
// line) to recordFile and exits 0, so runCompose can be exercised without a
// real Docker daemon. It returns the recorded file path.
func fakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "args.txt")
	binary := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\n"
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+old)
	t.Cleanup(func() { os.Setenv("PATH", old) })
	return record
}

func TestHandleServiceActionRunsCompose(t *testing.T) {
	record := fakeDocker(t)
	d := newTestDeployer(t)
	d.cfg.AdminToken = "admintok"

	req := httptest.NewRequest("POST", "/services/web/start", nil)
	req.SetPathValue("name", "web")
	req.Header.Set("Authorization", "Bearer admintok")
	w := httptest.NewRecorder()

	d.handleServiceAction("start")(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	got := string(args)
	for _, want := range []string{
		"compose\n", "-p\n", "web\n", "--project-directory\n",
		"up\n", "-d\n", "--remove-orphans\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("recorded argv missing %q:\n%s", want, got)
		}
	}
}

func TestHandleServiceActionAuth(t *testing.T) {
	d := newTestDeployer(t)

	// No admin token configured -> write endpoint disabled (404).
	req := httptest.NewRequest("POST", "/services/web/stop", nil)
	req.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	d.handleServiceAction("stop")(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("no-admin code = %d, want 404", w.Code)
	}

	// Wrong token -> 401.
	d.cfg.AdminToken = "admintok"
	req2 := httptest.NewRequest("POST", "/services/web/stop", nil)
	req2.SetPathValue("name", "web")
	req2.Header.Set("Authorization", "Bearer nope")
	w2 := httptest.NewRecorder()
	d.handleServiceAction("stop")(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("bad-token code = %d, want 401", w2.Code)
	}
}

func TestHandleServiceActionValidation(t *testing.T) {
	d := newTestDeployer(t)
	d.cfg.AdminToken = "admintok"
	h := d.handleServiceAction("restart")
	auth := func(req *http.Request) { req.Header.Set("Authorization", "Bearer admintok") }

	// Invalid service name.
	req := httptest.NewRequest("POST", "/services/BA_D/restart", nil)
	req.SetPathValue("name", "BA_D")
	auth(req)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad-name code = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	// Unknown service.
	req2 := httptest.NewRequest("POST", "/services/missing/restart", nil)
	req2.SetPathValue("name", "missing")
	auth(req2)
	w2 := httptest.NewRecorder()
	h(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("missing code = %d, want 404; body=%s", w2.Code, w2.Body.String())
	}
}
