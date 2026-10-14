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
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_ARGS_LOG"
echo "Deleted Images:"
echo "untagged: ghcr.io/you/web@sha256:aaa"
echo "deleted: sha256:aaa"
echo "deleted: sha256:bbb"
echo "Total reclaimed space: 1.234GB"
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_ARGS_LOG", argsLog)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

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

	bin := t.TempDir()
	script := "#!/bin/sh\necho boom >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

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

	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

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
