package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
