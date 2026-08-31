package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- fake github client ----

type fakeGithub struct {
	defaultBranch string
	files         map[string]bool // path -> exists (on any ref)
	repoErr       error
	fileErr       error
	secretErr     error
	prErr         error
	repos         []githubRepo
	reposErr      error
	secretValues  map[string]string
	prParams      *workflowPRParams
}

func (f *fakeGithub) repoInfo(_ context.Context, owner, repo string) (string, error) {
	if f.repoErr != nil {
		return "", f.repoErr
	}
	return f.defaultBranch, nil
}

func (f *fakeGithub) hasFile(_ context.Context, _, _, _, path string) (bool, error) {
	if f.fileErr != nil {
		return false, f.fileErr
	}
	return f.files[path], nil
}

func (f *fakeGithub) setSecret(_ context.Context, _, _, name, value string) error {
	if f.secretErr != nil {
		return f.secretErr
	}
	if f.secretValues == nil {
		f.secretValues = map[string]string{}
	}
	f.secretValues[name] = value
	return nil
}

func (f *fakeGithub) listRepos(_ context.Context) ([]githubRepo, error) {
	if f.reposErr != nil {
		return nil, f.reposErr
	}
	return f.repos, nil
}

func (f *fakeGithub) openWorkflowPR(_ context.Context, _, _ string, p workflowPRParams) (workflowPRResult, error) {
	if f.prErr != nil {
		return workflowPRResult{}, f.prErr
	}
	f.prParams = &p
	return workflowPRResult{Number: 11, URL: "https://github.com/o/r/pull/11", Branch: "pipeline/onboard-" + p.Service}, nil
}

func newOnboardDeployer(t *testing.T, gh githubClient) *deployer {
	t.Helper()
	dir := t.TempDir()
	return &deployer{
		cfg: &Config{
			ServicesDir:          dir,
			AllowedImagePrefixes: []string{"ghcr.io/"},
			AdminToken:           "admintok",
			PipelineOwner:        "alice",
			PipelineRef:          "main",
		},
		gh: gh,
	}
}

func onboardRequest(t *testing.T, d *deployer, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admintok")
	w := httptest.NewRecorder()
	d.handleOnboard(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestRenderDeployWorkflow(t *testing.T) {
	basic, err := renderDeployWorkflow(workflowData{
		PipelineOwner: "alice", PipelineRef: "main", DefaultBranch: "main", BranchList: "[main]", Service: "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `name: Deploy
on:
  push:
    branches: [main]
permissions:
  contents: read
  packages: write
jobs:
  deploy:
    uses: alice/personal-pipeline/.github/workflows/deploy-service.yml@main
    with:
      service: web
    secrets:
      deploy_webhook_url: ${{ secrets.DEPLOY_WEBHOOK_URL }}
      deploy_webhook_secret: ${{ secrets.DEPLOY_WEBHOOK_SECRET }}
      service_env: ${{ secrets.SERVICE_ENV }}
`
	if basic != want {
		t.Fatalf("basic template:\n--- got ---\n%s\n--- want ---\n%s", basic, want)
	}

	full, err := renderDeployWorkflow(workflowData{
		PipelineOwner: "alice", PipelineRef: "v2", DefaultBranch: "master", BranchList: "[master]", Service: "web",
		Image: "ghcr.io/alice/custom", Context: "app", Dockerfile: "Dockerfile.prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"branches: [master]",
		"@v2",
		"service: web",
		"image: ghcr.io/alice/custom",
		"context: app",
		"dockerfile: Dockerfile.prod",
		"${{ secrets.DEPLOY_WEBHOOK_URL }}",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("missing %q in:\n%s", want, full)
		}
	}
}

func TestDefaultServiceFromRepo(t *testing.T) {
	cases := map[string]string{
		"my_app":   "my-app",
		"My_App":   "my-app",
		"a.b.c":    "a-b-c",
		"simple":   "simple",
		"---":      "",
		"my--repo": "my-repo",
		"a___b":    "a-b",
		"UPPER":    "upper",
		"x":        "x",
		"1234567890123456789012345678901234567890123456789012345678901234567890": "123456789012345678901234567890123456789012345678901234567890123", // truncated to 63
	}
	for in, want := range cases {
		if got := defaultServiceFromRepo(in); got != want {
			t.Fatalf("defaultServiceFromRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitRepo(t *testing.T) {
	if o, r, err := splitRepo("Alice/My-Repo"); err != nil || o != "Alice" || r != "My-Repo" {
		t.Fatalf("got %q %q %v", o, r, err)
	}
	for _, bad := range []string{"", "foo", "foo/", "/bar", "foo/bar/baz", "foo bar/x", "a/../b"} {
		if _, _, err := splitRepo(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestHandleOnboardDisabled(t *testing.T) {
	d := newOnboardDeployer(t, nil)
	w, out := onboardRequest(t, d, `{"repo":"alice/web"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
	if !strings.Contains(out["error"].(string), "disabled") {
		t.Fatalf("error = %v", out["error"])
	}
}

// Regression: a TYPED nil (*fakeGithub)(nil) stored in the interface must not
// pass the plain `d.gh == nil` check and panic on method call (the original
// 502-bad-gateway bug: a nil *githubAppClient inside the githubClient
// interface). Both onboarding endpoints must return a clean 404 instead.
func TestHandleOnboardTypedNilClient(t *testing.T) {
	d := newOnboardDeployer(t, (*fakeGithub)(nil))

	w, _ := onboardRequest(t, d, `{"repo":"alice/web"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("onboard code = %d, want 404 (no panic)", w.Code)
	}

	d.cfg.ReadToken = "readtok"
	req := httptest.NewRequest("GET", "/onboard/repos", nil)
	req.Header.Set("Authorization", "Bearer readtok")
	w2 := httptest.NewRecorder()
	d.handleListOnboardRepos(w2, req)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("repos code = %d, want 404 (no panic)", w2.Code)
	}
}

func TestHandleListOnboardRepos(t *testing.T) {
	gh := &fakeGithub{repos: []githubRepo{
		{Name: "web", FullName: "alice/web"},
		{Name: "finance", FullName: "alice/finance"},
	}}
	d := newOnboardDeployer(t, gh)
	d.cfg.ReadToken = "readtok"

	req := httptest.NewRequest("GET", "/onboard/repos", nil)
	req.Header.Set("Authorization", "Bearer readtok")
	w := httptest.NewRecorder()
	d.handleListOnboardRepos(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	var out struct {
		Repos []githubRepo `json:"repos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Repos) != 2 || out.Repos[0].FullName != "alice/finance" {
		t.Fatalf("repos = %+v", out.Repos)
	}

	// Unauthorized without a read token.
	req2 := httptest.NewRequest("GET", "/onboard/repos", nil)
	w2 := httptest.NewRecorder()
	d.handleListOnboardRepos(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w2.Code)
	}

	// Disabled when the app client is missing.
	d.gh = nil
	req3 := httptest.NewRequest("GET", "/onboard/repos", nil)
	req3.Header.Set("Authorization", "Bearer readtok")
	w3 := httptest.NewRecorder()
	d.handleListOnboardRepos(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w3.Code)
	}

	// Upstream failure -> 502.
	d.gh = &fakeGithub{reposErr: errors.New("boom")}
	req4 := httptest.NewRequest("GET", "/onboard/repos", nil)
	req4.Header.Set("Authorization", "Bearer readtok")
	w4 := httptest.NewRecorder()
	d.handleListOnboardRepos(w4, req4)
	if w4.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", w4.Code)
	}
}

func TestHandleOnboardSuccess(t *testing.T) {
	gh := &fakeGithub{defaultBranch: "main", files: map[string]bool{"Dockerfile": true}}
	d := newOnboardDeployer(t, gh)

	w, out := onboardRequest(t, d, `{"repo":"alice/web","env":{"API_KEY":"x"},"service":"web"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	if out["compose"] != "created" {
		t.Fatalf("compose = %v", out["compose"])
	}
	if out["secret"] != "set" {
		t.Fatalf("secret = %v", out["secret"])
	}
	pr := out["pr"].(map[string]any)
	if pr["state"] != "open" || pr["number"].(float64) != 11 {
		t.Fatalf("pr = %v", pr)
	}
	if gh.secretValues["SERVICE_ENV"] != `{"API_KEY":"x"}` {
		t.Fatalf("secret value = %q", gh.secretValues["SERVICE_ENV"])
	}
	if gh.prParams == nil || gh.prParams.Content == "" || !strings.Contains(gh.prParams.Content, "service: web") {
		t.Fatalf("pr params = %+v", gh.prParams)
	}
	if _, err := os.Stat(filepath.Join(d.cfg.ServicesDir, "web", "docker-compose.yml")); err != nil {
		t.Fatalf("compose file not written: %v", err)
	}
	// Warnings must be absent/empty when the Dockerfile exists.
	if w, ok := out["warnings"].([]any); ok && len(w) != 0 {
		t.Fatalf("warnings = %v", out["warnings"])
	}
}

func TestHandleOnboardDockerfileWarning(t *testing.T) {
	gh := &fakeGithub{defaultBranch: "main", files: map[string]bool{}}
	d := newOnboardDeployer(t, gh)

	w, out := onboardRequest(t, d, `{"repo":"alice/web"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	warns := out["warnings"].([]any)
	if len(warns) != 1 || !strings.Contains(warns[0].(string), "no Dockerfile found at Dockerfile on main") {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestHandleOnboardContextDockerfileWarning(t *testing.T) {
	gh := &fakeGithub{defaultBranch: "main", files: map[string]bool{}}
	d := newOnboardDeployer(t, gh)

	_, out := onboardRequest(t, d, `{"repo":"alice/web","context":"app","dockerfile":"Dockerfile.dev"}`)
	warns := out["warnings"].([]any)
	if len(warns) != 1 || !strings.Contains(warns[0].(string), "app/Dockerfile.dev") {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestHandleOnboardWorkflowConflict(t *testing.T) {
	gh := &fakeGithub{
		defaultBranch: "main",
		files:         map[string]bool{"Dockerfile": true, ".github/workflows/deploy.yml": true},
	}
	d := newOnboardDeployer(t, gh)

	w, out := onboardRequest(t, d, `{"repo":"alice/web"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(out["error"].(string), "already exists") {
		t.Fatalf("error = %v", out["error"])
	}

	// overwrite_workflow: true proceeds.
	gh.files[".github/workflows/deploy.yml"] = true
	w2, _ := onboardRequest(t, d, `{"repo":"alice/web","overwrite_workflow":true}`)
	if w2.Code != http.StatusCreated {
		t.Fatalf("overwrite code = %d, body = %s", w2.Code, w2.Body.String())
	}
}

func TestHandleOnboardSecretFailureIsPartial(t *testing.T) {
	gh := &fakeGithub{
		defaultBranch: "main",
		files:         map[string]bool{"Dockerfile": true},
		secretErr:     errors.New("boom"),
	}
	d := newOnboardDeployer(t, gh)

	w, out := onboardRequest(t, d, `{"repo":"alice/web"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", w.Code)
	}
	res := out["results"].(map[string]any)
	if res["compose"] != "created" {
		t.Fatalf("compose should be created before secret step: %v", res["compose"])
	}
	if res["secret"] != nil {
		t.Fatalf("secret should be unset: %v", res["secret"])
	}
	if !strings.Contains(out["error"].(string), "boom") {
		t.Fatalf("error = %v", out["error"])
	}
}

func TestHandleOnboardExistingService(t *testing.T) {
	gh := &fakeGithub{defaultBranch: "main", files: map[string]bool{"Dockerfile": true}}
	d := newOnboardDeployer(t, gh)
	if err := createServiceOnDisk(d.cfg, &ServiceSpec{Name: "web", Image: "ghcr.io/alice/web", Port: 3000, Hostname: "web"}); err != nil {
		t.Fatal(err)
	}

	w, out := onboardRequest(t, d, `{"repo":"alice/web"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	if out["compose"] != "existing" {
		t.Fatalf("compose = %v", out["compose"])
	}
}

func TestHandleOnboardValidation(t *testing.T) {
	gh := &fakeGithub{defaultBranch: "main"}
	d := newOnboardDeployer(t, gh)

	for _, body := range []string{
		`{"repo":"not-a-repo"}`,
		`{"repo":"alice/web","service":"Bad_Name"}`,
		`{"repo":"alice/web","service":"web","image":"docker.io/other/web"}`,
		`{"repo":"alice/web","env":{"BAD KEY":"x"}}`,
		`{"repo":"alice/web","context":"../etc"}`,
		`{"repo":"alice/web","port":99999}`,
	} {
		w, _ := onboardRequest(t, d, body)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
			t.Fatalf("body %s: code = %d, want 4xx", body, w.Code)
		}
	}
}

func TestHandleOnboardExistingServiceStillValidatesImage(t *testing.T) {
	gh := &fakeGithub{defaultBranch: "main", files: map[string]bool{"Dockerfile": true}}
	d := newOnboardDeployer(t, gh)
	if err := createServiceOnDisk(d.cfg, &ServiceSpec{Name: "web", Image: "ghcr.io/alice/web", Port: 3000, Hostname: "web"}); err != nil {
		t.Fatal(err)
	}

	w, _ := onboardRequest(t, d, `{"repo":"alice/web","service":"web","image":"docker.io/other/web"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (image allowlist must hold even when the service exists)", w.Code)
	}
}

func TestHandleOnboardDerivesServiceFromRepo(t *testing.T) {
	gh := &fakeGithub{defaultBranch: "main", files: map[string]bool{"Dockerfile": true}}
	d := newOnboardDeployer(t, gh)

	w, out := onboardRequest(t, d, `{"repo":"alice/My_App"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	if out["service"] != "my-app" {
		t.Fatalf("service = %v", out["service"])
	}
	if out["image"] != "ghcr.io/alice/my-app" {
		t.Fatalf("image = %v", out["image"])
	}
}

func TestHandleOnboardUnauthorized(t *testing.T) {
	d := newOnboardDeployer(t, &fakeGithub{defaultBranch: "main"})
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(`{"repo":"alice/web"}`))
	w := httptest.NewRecorder()
	d.handleOnboard(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}
