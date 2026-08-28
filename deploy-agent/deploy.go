package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// DeployRequest is the signed webhook payload sent by GitHub Actions.
type DeployRequest struct {
	Project string            `json:"project"`
	Image   string            `json:"image"`
	Tag     string            `json:"tag"`
	Repo    string            `json:"repo"`
	Sha     string            `json:"sha"`
	Env     map[string]string `json:"env"`
}

type deployer struct {
	cfg      *Config
	hist     *history
	notifier *notifier

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex

	loginMu   sync.Mutex
	loginDone bool
	loginErr  error
}

func (d *deployer) projectLock(name string) *sync.Mutex {
	d.locksMu.Lock()
	defer d.locksMu.Unlock()
	mu, ok := d.locks[name]
	if !ok {
		mu = &sync.Mutex{}
		d.locks[name] = mu
	}
	return mu
}

func (d *deployer) record(rec DeployRecord) {
	if err := d.hist.Append(rec); err != nil {
		logEvent("history.append_error", map[string]any{"error": err.Error()})
	}
}

func (d *deployer) handleDeploy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	if !verifySignature(d.cfg.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		logEvent("webhook.rejected", map[string]any{"reason": "invalid_signature"})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
		return
	}

	var req DeployRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if code, msg := d.validate(&req); code != 0 {
		logEvent("webhook.rejected", map[string]any{"reason": msg, "project": req.Project, "image": req.Image})
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}

	rec, deployErr := d.doDeploy(&req)
	if deployErr != nil {
		logEvent("deploy.failed", map[string]any{"project": req.Project, "tag": req.Tag, "error": deployErr.Error()})
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "failed",
			"error":  deployErr.Error(),
			"id":     rec.ID,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "deployed",
		"project": req.Project,
		"tag":     req.Tag,
		"id":      rec.ID,
	})
}

func (d *deployer) validate(req *DeployRequest) (int, string) {
	if req.Project == "" {
		return http.StatusBadRequest, "project is required"
	}
	if req.Image == "" {
		return http.StatusBadRequest, "image is required"
	}
	if req.Tag == "" {
		return http.StatusBadRequest, "tag is required"
	}
	if _, err := os.Stat(filepath.Join(d.cfg.ServicesDir, req.Project, "docker-compose.yml")); err != nil {
		return http.StatusNotFound, "unknown project"
	}
	if !imageAllowed(req.Image, d.cfg.AllowedImagePrefixes) {
		return http.StatusForbidden, "image not in allowlist"
	}
	for k := range req.Env {
		if !envKeyRe.MatchString(k) {
			return http.StatusBadRequest, "invalid env key: " + k
		}
	}
	return 0, ""
}

func (d *deployer) doDeploy(req *DeployRequest) (DeployRecord, error) {
	start := time.Now()
	rec := DeployRecord{
		ID:      newID(),
		TS:      start.UTC(),
		Project: req.Project,
		Image:   req.Image,
		Tag:     req.Tag,
		Sha:     req.Sha,
		Repo:    req.Repo,
		Status:  "success",
	}

	mu := d.projectLock(req.Project)
	mu.Lock()
	defer mu.Unlock()

	dir := filepath.Join(d.cfg.ServicesDir, req.Project)

	notify := func(event, status, errMsg string) {
		rec.Notifications = append(rec.Notifications, d.notifier.Send(NotifyData{
			Event:      event,
			Project:    req.Project,
			Image:      req.Image,
			Tag:        req.Tag,
			Sha:        req.Sha,
			Repo:       req.Repo,
			Status:     status,
			DurationMs: time.Since(start).Milliseconds(),
			Error:      errMsg,
		})...)
	}

	fail := func(err error) (DeployRecord, error) {
		rec.Status = "failed"
		rec.Error = err.Error()
		rec.DurationMs = time.Since(start).Milliseconds()
		notify("deploy.failed", "failed", err.Error())
		d.record(rec)
		return rec, err
	}

	notify("deploy.started", "started", "")

	if hookPath, ok := hookExists(dir, "pre-deploy"); ok {
		res := runHook(hookPath, dir, hookEnv(req, "started", "", 0), d.cfg.HookTimeout)
		rec.PreHook = &HookResult{ExitCode: res.ExitCode, OutputTail: res.OutputTail, DurationMs: res.DurationMs}
		if res.Err != nil {
			return fail(fmt.Errorf("pre-deploy hook failed: %w", res.Err))
		}
	}

	if err := writeEnvFile(dir, req.Tag, req.Env); err != nil {
		return fail(fmt.Errorf("write env file: %w", err))
	}

	if err := d.ensureLogin(); err != nil {
		return fail(fmt.Errorf("ghcr login: %w", err))
	}

	pullOut, err := runCompose(d.cfg, dir, req.Project, "pull")
	if err != nil {
		return fail(fmt.Errorf("compose pull failed: %s", pullOut))
	}
	upOut, err := runCompose(d.cfg, dir, req.Project, "up", "-d", "--remove-orphans")
	if err != nil {
		return fail(fmt.Errorf("compose up failed: %s", upOut))
	}
	rec.Compose = &ComposeResult{PullOutputTail: pullOut, UpOutputTail: upOut}

	var warning string
	if hookPath, ok := hookExists(dir, "post-deploy"); ok {
		res := runHook(hookPath, dir, hookEnv(req, "success", "", time.Since(start).Milliseconds()), d.cfg.HookTimeout)
		rec.PostHook = &HookResult{ExitCode: res.ExitCode, OutputTail: res.OutputTail, DurationMs: res.DurationMs}
		if res.Err != nil {
			warning = "post-deploy hook failed: " + res.Err.Error()
			logEvent("hook.warning", map[string]any{"project": req.Project, "hook": "post-deploy", "error": res.Err.Error()})
		}
	}

	rec.DurationMs = time.Since(start).Milliseconds()
	notify("deploy.succeeded", "success", warning)
	d.record(rec)
	logEvent("deploy.succeeded", map[string]any{"project": req.Project, "tag": req.Tag, "duration_ms": rec.DurationMs})
	return rec, nil
}

// ---- read API ----

func (d *deployer) authorizeRead(w http.ResponseWriter, r *http.Request) bool {
	if d.cfg.ReadToken == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "read endpoint disabled"})
		return false
	}
	if r.Header.Get("Authorization") != "Bearer "+d.cfg.ReadToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func (d *deployer) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	if !d.authorizeRead(w, r) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	recs, err := d.hist.List(q.Get("project"), q.Get("status"), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if recs == nil {
		recs = []DeployRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": recs})
}

func (d *deployer) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	if !d.authorizeRead(w, r) {
		return
	}
	rec, err := d.hist.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// ---- helpers ----

func verifySignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func imageAllowed(image string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(image, p) {
			return true
		}
	}
	return false
}

func writeEnvFile(dir, tag string, env map[string]string) error {
	var b strings.Builder
	b.WriteString("TAG=" + quoteDotenvValue(tag) + "\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + "=" + quoteDotenvValue(env[k]) + "\n")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".env.tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ".env"))
}

func hookExists(dir, name string) (string, bool) {
	p := filepath.Join(dir, "hooks", name)
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "", false
	}
	return p, true
}

func hookEnv(req *DeployRequest, status, errMsg string, durationMs int64) map[string]string {
	return map[string]string{
		"DEPLOY_PROJECT":     req.Project,
		"DEPLOY_IMAGE":       req.Image,
		"DEPLOY_TAG":         req.Tag,
		"DEPLOY_SHA":         req.Sha,
		"DEPLOY_REPO":        req.Repo,
		"DEPLOY_STATUS":      status,
		"DEPLOY_ERROR":       errMsg,
		"DEPLOY_DURATION_MS": strconv.FormatInt(durationMs, 10),
	}
}
