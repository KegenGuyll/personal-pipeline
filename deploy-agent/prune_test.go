package main

import (
	"os"
	"path/filepath"
	"testing"
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
	out := `Deleted: sha256:aaa
Deleted: sha256:bbb

Total reclaimed space: 1.234GB
`
	pruned, reclaimed := parsePruneOutput(out)
	if pruned != 2 {
		t.Fatalf("pruned = %d, want 2", pruned)
	}
	if reclaimed != "1.234GB" {
		t.Fatalf("reclaimed = %q, want 1.234GB", reclaimed)
	}

	pruned, reclaimed = parsePruneOutput("Total reclaimed space: 0B")
	if pruned != 0 || reclaimed != "0B" {
		t.Fatalf("pruned = %d, reclaimed = %q, want 0 / 0B", pruned, reclaimed)
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
echo "Deleted: sha256:aaa"
echo "Deleted: sha256:bbb"
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
	if res.Pruned != 2 {
		t.Fatalf("pruned = %d, want 2", res.Pruned)
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

	t.Setenv("WEBHOOK_SECRET", "s3cret")
	t.Setenv("IMAGE_PRUNE", "yesterday")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for invalid IMAGE_PRUNE")
	}
}
