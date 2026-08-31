package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// OnboardRequest is the operator-supplied body for POST /onboard. It is the
// union of "create the service compose file" (ServiceSpec) and "wire the
// project repo" (repo + env + workflow options).
type OnboardRequest struct {
	Repo              string            `json:"repo"`               // "owner/repo"
	Service           string            `json:"service"`            // default: repo name, lowercased/sanitized
	Image             string            `json:"image"`              // default: ghcr.io/<owner-lower>/<service>
	Port              int               `json:"port"`               // default 3000
	Hostname          string            `json:"hostname"`           // default: service name
	Context           string            `json:"context"`            // build context, default "."
	Dockerfile        string            `json:"dockerfile"`         // Dockerfile path rel. to context, default "Dockerfile"
	Env               map[string]string `json:"env"`                // runtime env -> SERVICE_ENV secret
	OverwriteWorkflow bool              `json:"overwrite_workflow"` // allow updating an existing deploy.yml
}

// onboardPRResult describes the opened (never auto-merged) PR.
type onboardPRResult struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
	State  string `json:"state"` // always "open" — awaiting human review
}

// onboardResults is the structured, idempotent-friendly response.
type onboardResults struct {
	Repo       string           `json:"repo"`
	Service    string           `json:"service"`
	Image      string           `json:"image"`
	BaseBranch string           `json:"base_branch"`
	Secret     string           `json:"secret,omitempty"` // "set" once the SERVICE_ENV secret exists
	Compose    string           `json:"compose"`          // "created" | "existing"
	Warnings   []string         `json:"warnings,omitempty"`
	PR         *onboardPRResult `json:"pr,omitempty"`
	Error      string           `json:"error,omitempty"`
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?/[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// deployWorkflowTemplate is rendered with [[ ]] delimiters so the literal
// ${{ ... }} GitHub expression syntax passes through untouched.
const deployWorkflowTemplate = `name: Deploy
on:
  push:
    branches: [[.BranchList]]
permissions:
  contents: read
  packages: write
jobs:
  deploy:
    uses: [[.PipelineOwner]]/personal-pipeline/.github/workflows/deploy-service.yml@[[.PipelineRef]]
    with:
      service: [[.Service]][[if .Image]]
      image: [[.Image]][[end]][[if .Context]]
      context: [[.Context]][[end]][[if .Dockerfile]]
      dockerfile: [[.Dockerfile]][[end]]
    secrets:
      deploy_webhook_url: ${{ secrets.DEPLOY_WEBHOOK_URL }}
      deploy_webhook_secret: ${{ secrets.DEPLOY_WEBHOOK_SECRET }}
      service_env: ${{ secrets.SERVICE_ENV }}
`

type workflowData struct {
	PipelineOwner string
	PipelineRef   string
	DefaultBranch string
	BranchList    string // "[<branch>]" — rendered as the push filter
	Service       string
	Image         string // "" => omit
	Context       string // "" => omit
	Dockerfile    string // "" => omit
}

var deployWorkflowTpl = template.Must(template.New("deploy").Delims("[[", "]]").Parse(deployWorkflowTemplate))

func renderDeployWorkflow(d workflowData) (string, error) {
	var b strings.Builder
	if err := deployWorkflowTpl.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// handleListOnboardRepos returns every repo the onboarding GitHub App can see,
// for the dashboard's repository dropdown. Read-gated like GET /services.
func (d *deployer) handleListOnboardRepos(w http.ResponseWriter, r *http.Request) {
	if !d.authorizeRead(w, r) {
		return
	}
	if d.gh == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "onboarding disabled — set GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_B64 on the server",
		})
		return
	}
	repos, err := d.gh.listRepos(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if repos == nil {
		repos = []githubRepo{}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].FullName < repos[j].FullName })
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

func (d *deployer) handleOnboard(w http.ResponseWriter, r *http.Request) {
	if !d.authorizeWrite(w, r) {
		return
	}
	if d.gh == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "onboarding disabled — set GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_B64 on the server",
		})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	var req OnboardRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	owner, repo, err := splitRepo(req.Repo)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// ---- defaults ----
	if req.Service == "" {
		req.Service = defaultServiceFromRepo(repo)
	}
	if req.Port == 0 {
		req.Port = 3000
	}
	if req.Hostname == "" {
		req.Hostname = req.Service
	}
	if req.Context == "" {
		req.Context = "."
	}
	if req.Dockerfile == "" {
		req.Dockerfile = "Dockerfile"
	}
	if req.Image == "" {
		req.Image = "ghcr.io/" + strings.ToLower(owner) + "/" + req.Service
	}
	if req.Env == nil {
		req.Env = map[string]string{}
	}

	// ---- validation (no side effects yet) ----
	if req.Service == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not derive a service name from repo — pass service explicitly"})
		return
	}
	// These always apply — they feed the compose file, the branch name, and the
	// generated workflow, so they must hold even when the service dir exists.
	if !serviceNameRe.MatchString(req.Service) || req.Service == "_template" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid service name (lowercase letters, digits, hyphens)"})
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "port must be between 1 and 65535"})
		return
	}
	if !hostnameRe.MatchString(req.Hostname) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid hostname (letters, digits, hyphens)"})
		return
	}
	if !imageAllowed(req.Image, d.cfg.AllowedImagePrefixes) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "image not in allowlist"})
		return
	}
	if !validRelPath(req.Context) || !validRelPath(req.Dockerfile) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context/dockerfile must be relative paths without '..'"})
		return
	}
	for k := range req.Env {
		if !envKeyRe.MatchString(k) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid env key: " + k})
			return
		}
	}

	ctx := r.Context()
	res := &onboardResults{
		Repo:     req.Repo,
		Service:  req.Service,
		Image:    req.Image,
		Warnings: []string{},
	}

	// 1. Resolve the repo's default branch (also proves the app can see it).
	baseBranch, err := d.gh.repoInfo(ctx, owner, repo)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "results": res})
		return
	}
	res.BaseBranch = baseBranch

	// 2. Workflow file must not already exist unless overwriting.
	exists, err := d.gh.hasFile(ctx, owner, repo, baseBranch, ".github/workflows/deploy.yml")
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "results": res})
		return
	}
	if exists && !req.OverwriteWorkflow {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": ".github/workflows/deploy.yml already exists — re-run with overwrite_workflow: true to replace it",
		})
		return
	}

	// 3. Compose file on the server (idempotent).
	spec := &ServiceSpec{Name: req.Service, Image: req.Image, Port: req.Port, Hostname: req.Hostname}
	if serviceExists(d.cfg.ServicesDir, req.Service) {
		res.Compose = "existing"
		if cur := composeImageFromDir(d.cfg.ServicesDir, req.Service); cur != "" && cur != req.Image {
			res.Warnings = append(res.Warnings, "service already exists with image "+cur+"; leaving it unchanged (requested "+req.Image+")")
		}
	} else {
		if code, msg := d.validateServiceSpec(spec); code != 0 {
			writeJSON(w, code, map[string]string{"error": msg})
			return
		}
		if err := createServiceOnDisk(d.cfg, spec); err != nil {
			res.Error = err.Error()
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "results": res})
			return
		}
		res.Compose = "created"
	}
	// 4. Dockerfile pre-flight: warn (never block) if it is missing.
	dockerfilePath := path.Join(req.Context, req.Dockerfile)
	if dockerfilePath == "." {
		dockerfilePath = "Dockerfile"
	}
	hasDockerfile, err := d.gh.hasFile(ctx, owner, repo, baseBranch, dockerfilePath)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "results": res})
		return
	}
	if !hasDockerfile {
		res.Warnings = append(res.Warnings, "no Dockerfile found at "+dockerfilePath+" on "+baseBranch+" — the first build will fail until one is added")
	}

	// 5. SERVICE_ENV secret (repo-level; independent of PR state).
	envJSON, err := json.Marshal(req.Env)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "results": res})
		return
	}
	if err := d.gh.setSecret(ctx, owner, repo, "SERVICE_ENV", string(envJSON)); err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "results": res})
		return
	}
	res.Secret = "set"

	// 6. Open the review PR (never merged by the agent).
	wd := workflowData{
		PipelineOwner: d.cfg.PipelineOwner,
		PipelineRef:   d.cfg.PipelineRef,
		DefaultBranch: baseBranch,
		BranchList:    "[" + baseBranch + "]",
		Service:       req.Service,
	}
	if req.Image != wdImageDefault(owner, req.Service) {
		wd.Image = req.Image
	}
	if req.Context != "." {
		wd.Context = req.Context
	}
	if req.Dockerfile != "Dockerfile" {
		wd.Dockerfile = req.Dockerfile
	}
	content, err := renderDeployWorkflow(wd)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "results": res})
		return
	}
	bodyText := "Onboarding service `" + req.Service + "` into the personal pipeline.\n\n" +
		"- Service: " + req.Service + "\n" +
		"- Image: " + req.Image + "\n" +
		"- Base branch: " + baseBranch + "\n\n" +
		"This adds the reusable deploy workflow. Review the snippet, then merge to enable " +
		"deployments from pushes to `" + baseBranch + "`. The pipeline agent never merges this PR."
	pr, err := d.gh.openWorkflowPR(ctx, owner, repo, workflowPRParams{
		Service:    req.Service,
		BaseBranch: baseBranch,
		Content:    content,
		Title:      "Add pipeline deploy workflow for " + req.Service,
		Body:       bodyText,
	})
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "results": res})
		return
	}
	res.PR = &onboardPRResult{Number: pr.Number, URL: pr.URL, Branch: pr.Branch, State: "open"}

	logEvent("onboard.completed", map[string]any{
		"repo": req.Repo, "service": req.Service, "pr": pr.Number,
		"compose": res.Compose, "dockerfile_present": hasDockerfile,
	})
	writeJSON(w, http.StatusCreated, res)
}

// wdImageDefault mirrors the reusable workflow's default image name
// (ghcr.io/<owner-lower>/<service>), used to decide whether deploy.yml needs an
// explicit `image:` input.
func wdImageDefault(owner, service string) string {
	return "ghcr.io/" + strings.ToLower(owner) + "/" + service
}

// ---- helpers ----

func splitRepo(s string) (owner, repo string, err error) {
	s = strings.TrimSpace(s)
	if !repoRe.MatchString(s) {
		return "", "", errors.New("repo must be owner/repo (letters, digits, '.', '_', '-')")
	}
	owner, repo, _ = strings.Cut(s, "/")
	return owner, repo, nil
}

func serviceExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func composeImageFromDir(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name, "docker-compose.yml"))
	if err != nil {
		return ""
	}
	return composeImage(string(b))
}

// defaultServiceFromRepo lowercases the repo name and maps any character that
// is not [a-z0-9-] to '-', collapsing repeats and trimming edges — the closest
// valid service name to the repo name.
func defaultServiceFromRepo(repo string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(repo) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			r = '-'
		}
		if r == '-' && prevDash {
			continue
		}
		prevDash = r == '-'
		b.WriteRune(r)
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

// validRelPath rejects absolute paths and anything containing ".." or backslash.
func validRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Contains(p, "\\") {
		return false
	}
	return true
}

// createServiceOnDisk writes the rendered compose file for a validated spec
// (the same output as the dashboard "Add service"). Returns the error, if any.
func createServiceOnDisk(cfg *Config, spec *ServiceSpec) error {
	content, err := renderServiceCompose(spec)
	if err != nil {
		return err
	}
	dir := filepath.Join(cfg.ServicesDir, spec.Name)
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0755); err != nil {
		return err
	}
	composePath := filepath.Join(dir, "docker-compose.yml")
	tmp := composePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, composePath); err != nil {
		return err
	}
	logEvent("service.added", map[string]any{"project": spec.Name, "image": spec.Image, "access": "private"})
	return nil
}
