package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// dockerEnv points the docker CLI at a writable DOCKER_CONFIG under the data
// dir so `docker login` credentials persist across restarts.
func dockerEnv(cfg *Config) []string {
	return append(os.Environ(), "DOCKER_CONFIG="+filepath.Join(cfg.DataDir, ".docker"))
}

// composeTimeout bounds each compose invocation (pull can legitimately take
// minutes on large images; 10m is generous). Compose commands run under
// dockerMu, so without a cap a hung one blocks every deploy, the dashboard
// start action, and the webhook HTTP response. var so tests can shorten it.
var composeTimeout = 10 * time.Minute

func runCompose(cfg *Config, dir, project string, args ...string) (string, error) {
	base := []string{"compose", "-p", project, "--project-directory", dir, "-f", filepath.Join(dir, "docker-compose.yml")}
	out, err := runDocker(cfg, composeTimeout, append(base, args...)...)
	return tail(out, 4000), err
}

// runDocker runs one docker command with a hard cap: the command (and any
// children it spawns) runs in its own process group and the whole group is
// SIGKILLed at the deadline, so a hung command can't hold dockerMu — and the
// webhook HTTP response — forever. cmd.Cancel is invoked by os/exec only
// while the command may still be alive (never after a successful Wait), so
// the group kill can't land on a reused pid's group.
func runDocker(cfg *Config, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnv(cfg)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// Reject the already-completed case so the group kill can't land on
		// a reused pid (Process.Signal(0) fails once the process is gone).
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("timed out after %s", timeout)
	}
	return out, err
}

func (d *deployer) ensureLogin() error {
	d.loginMu.Lock()
	defer d.loginMu.Unlock()
	if d.loginDone {
		return d.loginErr
	}
	d.loginDone = true
	if d.cfg.GHCRUser == "" || d.cfg.GHCRToken == "" {
		return nil
	}
	cmd := exec.Command("docker", "login", "ghcr.io", "-u", d.cfg.GHCRUser, "--password-stdin")
	cmd.Env = dockerEnv(d.cfg)
	cmd.Stdin = strings.NewReader(d.cfg.GHCRToken)
	out, err := cmd.CombinedOutput()
	if err != nil {
		d.loginErr = fmt.Errorf("%v: %s", err, tail(string(out), 500))
		return d.loginErr
	}
	return nil
}

// imagePruneArgs builds the `docker image prune` argument list for an
// ImagePrune setting (validated at config load: "all" or a Go duration).
// Pruning with -a removes every image NO container uses — running and stopped
// containers always protect their images — so an in-use service is never
// affected. The until cutoff (image creation time) only bounds how far back
// unused tags are kept.
func imagePruneArgs(setting string) []string {
	args := []string{"image", "prune", "-a", "-f"}
	if setting != "all" {
		args = append(args, "--filter", "until="+setting)
	}
	return args
}

// dockerGate makes image pruning and deploy hooks mutually exclusive no
// matter which starts first. Hooks may run docker commands (custom hooks
// commonly pull images via `docker run`) but sit outside dockerMu — the
// deploy's pull→up→prune section — so a running prune could otherwise delete
// an image a hook just pulled. First one in wins: prune skips when hooks are
// active (pruning is best-effort and catches up on the next successful
// deploy), and hooks wait while a prune runs (bounded by pruneTimeout, so
// the wait cannot hang). It coordinates only this agent's operations —
// external docker clients (e.g. host-side compose runs during a stack
// rollout) can't acquire a process-local lock; the daemon itself protects
// images referenced by containers, and a pruned-then-repulled image
// self-heals on the next command.
type dockerGate struct {
	mu      sync.Mutex
	hooks   int
	pruning bool
}

// hookBegin blocks while a prune is running, then registers the hook.
func (g *dockerGate) hookBegin() {
	for {
		g.mu.Lock()
		if !g.pruning {
			g.hooks++
			g.mu.Unlock()
			return
		}
		g.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
}

func (g *dockerGate) hookEnd() {
	g.mu.Lock()
	g.hooks--
	g.mu.Unlock()
}

// pruneBegin admits the prune: false => hooks are active, skip this round.
func (g *dockerGate) pruneBegin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hooks > 0 {
		return false
	}
	g.pruning = true
	return true
}

func (g *dockerGate) pruneEnd() {
	g.mu.Lock()
	g.pruning = false
	g.mu.Unlock()
}

// pruneTimeout caps a single prune run: a hung or enormous prune would hold
// dockerMu — stalling every deploy, not just its own project — and the
// webhook HTTP response indefinitely. var so tests can shorten it.
var pruneTimeout = 5 * time.Minute

// pruneImages removes images no container is using (old SHA tags left behind
// by previous deploys), returning the full command output.
func (d *deployer) pruneImages() (string, error) {
	return runDocker(d.cfg, pruneTimeout, imagePruneArgs(d.cfg.ImagePrune)...)
}

// The image-prune CLI prints a "Deleted Images:" header with lowercase
// "untagged:" / "deleted:" lines, followed by "Total reclaimed space:".
var (
	pruneReclaimedRe = regexp.MustCompile(`(?i)Total reclaimed space: (.*)`)
	pruneDeletedRe   = regexp.MustCompile(`(?im)^deleted: `)
)

// parsePruneOutput extracts the reclaimed-space total and the number of
// deletion entries from `docker image prune` output. Deletion is matched
// case-insensitively across CLI versions; each `deleted:` line is one image
// or shared parent layer, so the count is deletion entries, not distinct
// images — reclaimed is the meaningful number.
func parsePruneOutput(out string) (deleted int, reclaimed string) {
	if m := pruneReclaimedRe.FindStringSubmatch(out); m != nil {
		reclaimed = strings.TrimSpace(m[1])
	}
	deleted = len(pruneDeletedRe.FindAllString(out, -1))
	return deleted, reclaimed
}
