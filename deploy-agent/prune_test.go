package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImagePruneArgs(t *testing.T) {
	cases := []struct {
		setting string
		want    []string
	}{
		{"24h", []string{"image", "prune", "-a", "-f", "--filter", "until=24h"}},
		{"1h30m", []string{"image", "prune", "-a", "-f", "--filter", "until=1h30m"}},
		{"all", []string{"image", "prune", "-a", "-f"}},
	}
	for _, c := range cases {
		got := imagePruneArgs(c.setting)
		if len(got) != len(c.want) {
			t.Fatalf("imagePruneArgs(%q) = %v, want %v", c.setting, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("imagePruneArgs(%q) = %v, want %v", c.setting, got, c.want)
			}
		}
	}
}

func TestParsePruneOutput(t *testing.T) {
	// Real CLI shape: "Deleted Images:" header, lowercase "untagged:" /
	// "deleted:" lines (one per image AND shared parent layer), then the
	// reclaimed total. Untagged lines must not be counted.
	out := `Deleted Images:
untagged: ghcr.io/you/my-service@sha256:aaa
deleted: sha256:aaa
deleted: sha256:bbb
deleted: sha256:ccc
untagged: ghcr.io/you/other@sha256:ddd
Deleted: sha256:ddd

Total reclaimed space: 1.234GB
`
	deleted, reclaimed := parsePruneOutput(out)
	if deleted != 4 {
		t.Fatalf("deleted = %d, want 4 (3 lowercase + 1 capitalized; untagged excluded)", deleted)
	}
	if reclaimed != "1.234GB" {
		t.Fatalf("reclaimed = %q, want 1.234GB", reclaimed)
	}

	deleted, reclaimed = parsePruneOutput("Total reclaimed space: 0B")
	if deleted != 0 || reclaimed != "0B" {
		t.Fatalf("deleted = %d, reclaimed = %q, want 0 / 0B", deleted, reclaimed)
	}
}

func TestAutoPruneDisabled(t *testing.T) {
	d := newTestDeployer(t) // cfg.ImagePrune unset => disabled
	if got := d.autoPrune("web"); got != nil {
		t.Fatalf("expected nil result, got %+v", got)
	}
}

// TestAutoPruneRunsDocker uses a fake `docker` on PATH to assert the prune
// command the agent runs and how its output is parsed into the record.
func TestAutoPruneRunsDocker(t *testing.T) {
	d := newTestDeployer(t)
	d.cfg.ImagePrune = "all"

	argsLog := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_LOG", argsLog)
	fakeDockerPath(t, `printf '%s\n' "$*" >> "$FAKE_ARGS_LOG"
echo "Deleted Images:"
echo "untagged: ghcr.io/you/web@sha256:aaa"
echo "deleted: sha256:aaa"
echo "deleted: sha256:bbb"
echo "Total reclaimed space: 1.234GB"
`)

	res := d.autoPrune("web")
	if res == nil {
		t.Fatal("expected a result")
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Deleted != 2 {
		t.Fatalf("deleted = %d, want 2", res.Deleted)
	}
	if res.Reclaimed != "1.234GB" {
		t.Fatalf("reclaimed = %q, want 1.234GB", res.Reclaimed)
	}
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "image prune -a -f\n" {
		t.Fatalf("args = %q", string(data))
	}
}

// TestAutoPruneWarning ensures a prune failure is captured as an error in the
// result (deploy.go turns it into a warning, not a failed deploy).
func TestAutoPruneWarning(t *testing.T) {
	d := newTestDeployer(t)
	d.cfg.ImagePrune = "24h"

	fakeDockerPath(t, "echo boom >&2\nexit 1\n")

	res := d.autoPrune("web")
	if res == nil {
		t.Fatal("expected a result")
	}
	if res.Error == "" {
		t.Fatal("expected error captured in result")
	}
}

// TestAutoPruneTimesOut ensures a hung prune is killed instead of holding
// dockerMu (and the webhook response) forever.
func TestAutoPruneTimesOut(t *testing.T) {
	d := newTestDeployer(t)
	d.cfg.ImagePrune = "all"

	fakeDockerPath(t, "sleep 30\n")

	old := pruneTimeout
	pruneTimeout = 50 * time.Millisecond
	defer func() { pruneTimeout = old }()

	res := d.autoPrune("web")
	if res == nil {
		t.Fatal("expected a result")
	}
	if res.Error == "" || !strings.Contains(res.Error, "timed out") {
		t.Fatalf("expected timeout error, got %q", res.Error)
	}
}

// fakeDockerPath prepends a fake `docker` (built from script) to PATH for the
// test and returns the bin dir.
func fakeDockerPath(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// TestDoDeployPruneSuccess runs doDeploy end-to-end against a fake docker:
// compose pull/up succeed, image prune succeeds, the record keeps status
// success with the prune result, and the history entry carries it.
func TestDoDeployPruneSuccess(t *testing.T) {
	d := newTestDeployer(t)
	d.cfg.ImagePrune = "all"
	fakeDockerPath(t, `case "$1" in
  compose) echo "compose ok: $*";;
  image)   echo "Deleted Images:"; echo "deleted: sha256:aaa"; echo "Total reclaimed space: 0B";;
esac
`)

	rec, err := d.doDeploy(&DeployRequest{Project: "web", Image: "ghcr.io/alice/web", Tag: "sha-1"})
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if rec.Status != "success" {
		t.Fatalf("status = %q, want success", rec.Status)
	}
	if rec.Prune == nil {
		t.Fatal("expected prune recorded")
	}
	if rec.Prune.Deleted != 1 || rec.Prune.Reclaimed != "0B" || rec.Prune.Error != "" {
		t.Fatalf("prune = %+v", rec.Prune)
	}

	recs, err := d.hist.List("web", "success", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Prune == nil || recs[0].Prune.Deleted != 1 {
		t.Fatalf("history record = %+v", recs)
	}
}

// TestDoDeployPruneFailureKeepsSuccess pins the contract Copilot's first
// review hinged on: a failing prune is recorded (with its error) and turned
// into a warning, but the deploy itself stays successful.
func TestDoDeployPruneFailureKeepsSuccess(t *testing.T) {
	d := newTestDeployer(t)
	d.cfg.ImagePrune = "all"
	fakeDockerPath(t, `case "$1" in
  compose) echo "compose ok: $*";;
  image)   echo boom >&2; exit 1;;
esac
`)

	rec, err := d.doDeploy(&DeployRequest{Project: "web", Image: "ghcr.io/alice/web", Tag: "sha-1"})
	if err != nil {
		t.Fatalf("prune failure must not fail the deploy: %v", err)
	}
	if rec.Status != "success" || rec.Error != "" {
		t.Fatalf("status = %q error = %q, want success/empty", rec.Status, rec.Error)
	}
	if rec.Prune == nil || rec.Prune.Error == "" {
		t.Fatalf("expected prune error recorded, got %+v", rec.Prune)
	}

	recs, err := d.hist.List("web", "success", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Prune == nil || recs[0].Prune.Error == "" {
		t.Fatalf("history record = %+v", recs)
	}
}

// TestAutoPruneSkipsWhileHooksActive: a hook's pulled images sit outside
// dockerMu's protection, so the prune is refused admission and the next
// deploy catches up.
func TestAutoPruneSkipsWhileHooksActive(t *testing.T) {
	d := newTestDeployer(t)
	d.cfg.ImagePrune = "all"
	fakeDockerPath(t, `echo "deleted: sha256:aaa"`)

	d.gate.hookBegin()
	if got := d.autoPrune("web"); got != nil {
		t.Fatalf("expected skip (nil), got %+v", got)
	}

	d.gate.hookEnd()
	if got := d.autoPrune("web"); got == nil {
		t.Fatal("expected prune to run once hooks are done")
	}
}

// TestRunHookTrackedWaitsForPrune pins the other half of the gate: a hook
// admitted after a prune started waits (bounded by the prune's timeout cap)
// instead of running docker commands inside the prune's window.
func TestRunHookTrackedWaitsForPrune(t *testing.T) {
	d := newTestDeployer(t)
	script := filepath.Join(t.TempDir(), "post-deploy")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hook-ran\n"), 0755); err != nil {
		t.Fatal(err)
	}

	if !d.gate.pruneBegin() {
		t.Fatal("expected prune admission with no hooks active")
	}
	done := make(chan HookResult, 1)
	go func() { done <- d.runHookTracked(script, t.TempDir(), nil) }()

	select {
	case <-done:
		t.Fatal("hook ran while prune active — it must wait")
	case <-time.After(100 * time.Millisecond):
	}

	d.gate.pruneEnd()
	select {
	case res := <-done:
		if res.Err != nil {
			t.Fatalf("hook error: %v", res.Err)
		}
		if !strings.Contains(res.OutputTail, "hook-ran") {
			t.Fatalf("hook output = %q", res.OutputTail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook never ran after the prune ended")
	}
}

// TestRunComposeTimesOut: compose runs under dockerMu, so a hung invocation
// must be killed at the cap instead of blocking every deploy forever.
func TestRunComposeTimesOut(t *testing.T) {
	fakeDockerPath(t, "sleep 30\n")

	old := composeTimeout
	composeTimeout = 50 * time.Millisecond
	defer func() { composeTimeout = old }()

	_, err := runCompose(&Config{DataDir: t.TempDir()}, t.TempDir(), "web", "pull")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

// TestHookTimeoutKillsChildren: a hook that spawns children (backgrounded or
// foreground) must be fully dead at the deadline — the output pipes close and
// runHook returns — so the docker gate never releases while hook-spawned
// docker work (a backgrounded docker pull) can still be running.
func TestHookTimeoutKillsChildren(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	// A single-process kill would leave the backgrounded `sleep 30` running
	// while the foreground one holds the output pipes; both are in the
	// hook's process group, so the group kill takes out both.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 &\nsleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res := runHook(script, dir, nil, 100*time.Millisecond)
	if res.Err == nil {
		t.Fatal("expected timeout")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runHook returned after %s — hook children outlived the timeout", elapsed)
	}
}

func TestLoadConfigImagePrune(t *testing.T) {
	// Unset falls back to the default (skip if the sandbox has it set).
	if os.Getenv("IMAGE_PRUNE") == "" {
		t.Setenv("WEBHOOK_SECRET", "s3cret")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ImagePrune != "24h" {
			t.Fatalf("default ImagePrune = %q, want 24h", cfg.ImagePrune)
		}
	}

	for _, c := range []struct{ in, want string }{
		{"24h", "24h"},
		{"1h30m", "1h30m"},
		{"all", "all"},
		{"ALL", "all"},
		{"always", "all"},
		{"off", ""},
		{"OFF", ""},
		{"disabled", ""},
	} {
		t.Setenv("WEBHOOK_SECRET", "s3cret")
		t.Setenv("IMAGE_PRUNE", c.in)
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("IMAGE_PRUNE=%s: %v", c.in, err)
		}
		if cfg.ImagePrune != c.want {
			t.Fatalf("IMAGE_PRUNE=%s: got %q, want %q", c.in, cfg.ImagePrune, c.want)
		}
	}

	// Invalid values (including non-positive durations, which would make the
	// until filter silently behave like "all") fail at startup.
	for _, bad := range []string{"yesterday", "0s", "-1h", "0"} {
		t.Setenv("WEBHOOK_SECRET", "s3cret")
		t.Setenv("IMAGE_PRUNE", bad)
		if _, err := loadConfig(); err == nil {
			t.Fatalf("expected error for invalid IMAGE_PRUNE=%s", bad)
		}
	}
}
