package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	secret := "s3cret"
	body := []byte(`{"project":"x"}`)
	good := sign(secret, body)

	if !verifySignature(secret, body, good) {
		t.Fatal("expected valid signature")
	}
	if verifySignature("wrong", body, good) {
		t.Fatal("expected invalid with wrong secret")
	}
	if verifySignature(secret, []byte(`{"project":"y"}`), good) {
		t.Fatal("expected invalid with tampered body")
	}
	if verifySignature(secret, body, "md5=abc") {
		t.Fatal("expected invalid with wrong scheme")
	}
	if verifySignature(secret, body, "") {
		t.Fatal("expected invalid with empty header")
	}
}

func TestImageAllowed(t *testing.T) {
	prefixes := []string{"ghcr.io/alice/", "registry.example.com/"}
	if !imageAllowed("ghcr.io/alice/web", prefixes) {
		t.Fatal("expected allowed")
	}
	if imageAllowed("ghcr.io/bob/web", prefixes) {
		t.Fatal("expected disallowed")
	}
	if !imageAllowed("registry.example.com/x", prefixes) {
		t.Fatal("expected allowed")
	}
}

func newTestDeployer(t *testing.T) *deployer {
	t.Helper()
	servicesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(servicesDir, "web"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(servicesDir, "web", "docker-compose.yml"), []byte("services:\n  app:\n    image: x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		WebhookSecret:        "s3cret",
		ServicesDir:          servicesDir,
		AllowedImagePrefixes: []string{"ghcr.io/alice/"},
		HookTimeout:          time.Second,
		LogRetention:         10,
		DataDir:              t.TempDir(),
	}
	return &deployer{
		cfg:      cfg,
		hist:     newHistory(filepath.Join(cfg.DataDir, "deployments.jsonl"), cfg.LogRetention),
		notifier: newNotifier(cfg),
		locks:    map[string]*sync.Mutex{},
	}
}

func TestValidate(t *testing.T) {
	d := newTestDeployer(t)
	cases := []struct {
		name string
		req  DeployRequest
		code int
	}{
		{"missing project", DeployRequest{Image: "ghcr.io/alice/web", Tag: "sha-1"}, http.StatusBadRequest},
		{"missing image", DeployRequest{Project: "web", Tag: "sha-1"}, http.StatusBadRequest},
		{"missing tag", DeployRequest{Project: "web", Image: "ghcr.io/alice/web"}, http.StatusBadRequest},
		{"unknown project", DeployRequest{Project: "nope", Image: "ghcr.io/alice/nope", Tag: "sha-1"}, http.StatusNotFound},
		{"disallowed image", DeployRequest{Project: "web", Image: "ghcr.io/bob/web", Tag: "sha-1"}, http.StatusForbidden},
		{"invalid env key", DeployRequest{Project: "web", Image: "ghcr.io/alice/web", Tag: "sha-1", Env: map[string]string{"BAD KEY": "x"}}, http.StatusBadRequest},
		{"valid", DeployRequest{Project: "web", Image: "ghcr.io/alice/web", Tag: "sha-1", Env: map[string]string{"API_KEY": "x"}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _ := d.validate(&c.req)
			if code != c.code {
				t.Fatalf("code = %d, want %d", code, c.code)
			}
		})
	}
}

func TestWriteEnvFile(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"API_KEY": "abc123",
		"QUOTED":  "has \"quote\" and\nnewline",
	}
	if err := writeEnvFile(dir, "sha-abc", env); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "TAG=sha-abc\n") {
		t.Fatalf("missing TAG line: %q", s)
	}
	if !strings.Contains(s, "API_KEY=abc123\n") {
		t.Fatalf("missing API_KEY: %q", s)
	}
	if !strings.Contains(s, "QUOTED=") {
		t.Fatalf("missing QUOTED: %q", s)
	}

	info, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}

	// Atomic replace: a second write removes keys absent from the new env.
	if err := writeEnvFile(dir, "sha-def", map[string]string{"ONLY": "1"}); err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Contains(string(data2), "QUOTED=") {
		t.Fatal("expected prior content replaced")
	}
	if !strings.Contains(string(data2), "ONLY=1") {
		t.Fatal("expected new content")
	}
}

func TestQuoteDotenvValue(t *testing.T) {
	if quoteDotenvValue("abc") != "abc" {
		t.Fatal("safe value should not be quoted")
	}
	if quoteDotenvValue("") != "" {
		t.Fatal("empty should be empty")
	}
	got := quoteDotenvValue("a\"b\nc")
	if !strings.HasPrefix(got, "\"") || !strings.Contains(got, `\"`) || !strings.Contains(got, `\n`) {
		t.Fatalf("unexpected quoting: %q", got)
	}
}

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(filepath.Join(dir, "deployments.jsonl"), 3)
	for i := 0; i < 5; i++ {
		if err := h.Append(DeployRecord{ID: fmt.Sprintf("id-%d", i), Project: "web", Status: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := h.List("", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("len = %d, want 3", len(recs))
	}
	if recs[0].ID != "id-2" {
		t.Fatalf("first = %s, want id-2", recs[0].ID)
	}
	if _, err := h.Get("id-3"); err != nil {
		t.Fatalf("expected to get id-3: %v", err)
	}
	if _, err := h.Get("id-0"); err == nil {
		t.Fatal("expected not found for trimmed id")
	}
}

func TestNotifyRender(t *testing.T) {
	data := NotifyData{Event: "deploy.succeeded", Project: "web", Status: "success"}

	n := newNotifier(&Config{NotifyContentType: "application/json"})
	body, err := n.render(data)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("default is not JSON: %v", err)
	}
	if m["event"] != "deploy.succeeded" {
		t.Fatalf("event = %v", m["event"])
	}

	n2 := newNotifier(&Config{NotifyTemplate: `{"text":"{{.Project}} {{.Status}}"}`})
	body2, err := n2.render(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(body2) != `{"text":"web success"}` {
		t.Fatalf("template body = %s", body2)
	}
}

func TestRunHook(t *testing.T) {
	dir := t.TempDir()

	ok := filepath.Join(dir, "ok.sh")
	if err := os.WriteFile(ok, []byte("#!/bin/sh\necho hello\n"), 0755); err != nil {
		t.Fatal(err)
	}
	res := runHook(ok, dir, map[string]string{"DEPLOY_STATUS": "success"}, time.Second)
	if res.Err != nil {
		t.Fatalf("expected success, got %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if !strings.Contains(res.OutputTail, "hello") {
		t.Fatalf("output = %q", res.OutputTail)
	}

	fail := filepath.Join(dir, "fail.sh")
	if err := os.WriteFile(fail, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0755); err != nil {
		t.Fatal(err)
	}
	res2 := runHook(fail, dir, nil, time.Second)
	if res2.Err == nil {
		t.Fatal("expected failure")
	}
	if res2.ExitCode != 3 {
		t.Fatalf("exit = %d", res2.ExitCode)
	}

	slow := filepath.Join(dir, "slow.sh")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0755); err != nil {
		t.Fatal(err)
	}
	res3 := runHook(slow, dir, nil, 50*time.Millisecond)
	if res3.Err == nil {
		t.Fatal("expected timeout")
	}
}

func TestReadAuthorization(t *testing.T) {
	d := newTestDeployer(t)
	d.cfg.ReadToken = "tok"

	req := httptest.NewRequest("GET", "/deployments", nil)
	req.Header.Set("Authorization", "Bearer tok")
	if !d.authorizeRead(httptest.NewRecorder(), req) {
		t.Fatal("expected authorized")
	}

	req2 := httptest.NewRequest("GET", "/deployments", nil)
	if d.authorizeRead(httptest.NewRecorder(), req2) {
		t.Fatal("expected unauthorized without header")
	}

	d.cfg.ReadToken = ""
	req3 := httptest.NewRequest("GET", "/deployments", nil)
	req3.Header.Set("Authorization", "Bearer tok")
	if d.authorizeRead(httptest.NewRecorder(), req3) {
		t.Fatal("expected disabled when ReadToken empty")
	}
}

func TestHandleDeployRejectsBadSignature(t *testing.T) {
	d := newTestDeployer(t)
	body := []byte(`{"project":"web","image":"ghcr.io/alice/web","tag":"sha-1"}`)
	req := httptest.NewRequest("POST", "/hooks/deploy", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	w := httptest.NewRecorder()
	d.handleDeploy(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestLogRequestsPassesThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	req := httptest.NewRequest("POST", "/x", nil)
	w := httptest.NewRecorder()
	logRequests(inner).ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", w.Code)
	}
}
