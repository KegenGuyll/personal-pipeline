package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ComposeResult holds truncated output tails from the pull/up steps.
type ComposeResult struct {
	PullOutputTail string `json:"pull_output_tail,omitempty"`
	UpOutputTail   string `json:"up_output_tail,omitempty"`
}

// DeployRecord is one append-only deployment-history entry. The `env` map is
// deliberately absent — secrets are never written to history.
type DeployRecord struct {
	ID            string         `json:"id"`
	TS            time.Time      `json:"ts"`
	Project       string         `json:"project"`
	Image         string         `json:"image"`
	Tag           string         `json:"tag"`
	Sha           string         `json:"sha"`
	Repo          string         `json:"repo"`
	Status        string         `json:"status"`
	DurationMs    int64          `json:"duration_ms"`
	PreHook       *HookResult    `json:"pre_hook,omitempty"`
	PostHook      *HookResult    `json:"post_hook,omitempty"`
	Compose       *ComposeResult `json:"compose,omitempty"`
	Notifications []string       `json:"notifications,omitempty"`
	Error         string         `json:"error,omitempty"`
}

var errNotFound = errors.New("deployment not found")

type history struct {
	mu        sync.Mutex
	path      string
	retention int
}

func newHistory(path string, retention int) *history {
	if retention <= 0 {
		retention = 100
	}
	return &history{path: path, retention: retention}
}

func (h *history) Append(r DeployRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(h.path), 0755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return h.trimLocked()
}

func (h *history) readLocked() ([]DeployRecord, error) {
	data, err := os.ReadFile(h.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var recs []DeployRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r DeployRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		recs = append(recs, r)
	}
	return recs, nil
}

func (h *history) trimLocked() error {
	recs, err := h.readLocked()
	if err != nil {
		return err
	}
	if len(recs) <= h.retention {
		return nil
	}
	recs = recs[len(recs)-h.retention:]
	return h.writeLocked(recs)
}

func (h *history) writeLocked(recs []DeployRecord) error {
	var buf []byte
	for _, r := range recs {
		b, _ := json.Marshal(r)
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	return os.WriteFile(h.path, buf, 0600)
}

func (h *history) List(project, status string, limit int) ([]DeployRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	recs, err := h.readLocked()
	if err != nil {
		return nil, err
	}
	filtered := make([]DeployRecord, 0, len(recs))
	for _, r := range recs {
		if project != "" && r.Project != project {
			continue
		}
		if status != "" && r.Status != status {
			continue
		}
		filtered = append(filtered, r)
	}
	if limit > 0 && limit < len(filtered) {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered, nil
}

func (h *history) Get(id string) (*DeployRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	recs, err := h.readLocked()
	if err != nil {
		return nil, err
	}
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].ID == id {
			r := recs[i]
			return &r, nil
		}
	}
	return nil, errNotFound
}
