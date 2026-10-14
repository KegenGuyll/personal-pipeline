package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// dockerEnv points the docker CLI at a writable DOCKER_CONFIG under the data
// dir so `docker login` credentials persist across restarts.
func dockerEnv(cfg *Config) []string {
	return append(os.Environ(), "DOCKER_CONFIG="+filepath.Join(cfg.DataDir, ".docker"))
}

func runCompose(cfg *Config, dir, project string, args ...string) (string, error) {
	base := []string{"compose", "-p", project, "--project-directory", dir, "-f", filepath.Join(dir, "docker-compose.yml")}
	cmd := exec.Command("docker", append(base, args...)...)
	cmd.Env = dockerEnv(cfg)
	out, err := cmd.CombinedOutput()
	return tail(string(out), 4000), err
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

// pruneImages removes images no container is using (old SHA tags left behind
// by previous deploys), returning the full command output.
func (d *deployer) pruneImages() (string, error) {
	cmd := exec.Command("docker", imagePruneArgs(d.cfg.ImagePrune)...)
	cmd.Env = dockerEnv(d.cfg)
	out, err := cmd.CombinedOutput()
	return string(out), err
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
