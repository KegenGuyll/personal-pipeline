package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeServiceDir(t *testing.T, dir, name, image string, port int, hostname string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0755); err != nil {
		t.Fatal(err)
	}
	s, err := renderServiceCompose(&ServiceSpec{Name: name, Image: image, Port: port, Hostname: hostname})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte(s), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestListServices(t *testing.T) {
	dir := t.TempDir()

	writeServiceDir(t, dir, "app-a", "ghcr.io/owner/app-a", 3000, "appa")
	if err := os.WriteFile(filepath.Join(dir, "app-a", ".env"), []byte("TAG=sha-1111111\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeServiceDir(t, dir, "app-b", "ghcr.io/owner/app-b", 8080, "appb")

	// Entries that must be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "_template"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".hidden"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "no-compose"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	hist := newHistory(filepath.Join(t.TempDir(), "deployments.jsonl"), 100)
	if err := hist.Append(DeployRecord{
		ID: "id1", TS: time.Now().UTC(), Project: "app-a", Tag: "sha-2222222",
		Sha: "abc123", Status: "success",
	}); err != nil {
		t.Fatal(err)
	}

	d := &deployer{cfg: &Config{ServicesDir: dir}, hist: hist}
	services, err := d.listServices()
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 {
		t.Fatalf("len = %d, want 2", len(services))
	}

	a := services[0]
	if a.Name != "app-a" {
		t.Fatalf("first = %s, want app-a", a.Name)
	}
	if a.Tag != "sha-1111111" {
		t.Fatalf("tag = %q, want sha-1111111", a.Tag)
	}
	if a.Image != "ghcr.io/owner/app-a" {
		t.Fatalf("image = %q", a.Image)
	}
	if a.Access != "private" {
		t.Fatalf("access = %q, want private", a.Access)
	}
	if a.Port != 3000 {
		t.Fatalf("port = %d, want 3000", a.Port)
	}
	if a.Hostname != "appa" {
		t.Fatalf("hostname = %q, want appa", a.Hostname)
	}
	if a.LastDeploy == nil {
		t.Fatal("expected last_deploy for app-a")
	} else if a.LastDeploy.Tag != "sha-2222222" {
		t.Fatalf("last_deploy.tag = %q, want sha-2222222", a.LastDeploy.Tag)
	}

	b := services[1]
	if b.Name != "app-b" {
		t.Fatalf("second = %s, want app-b", b.Name)
	}
	if b.Tag != "" {
		t.Fatalf("app-b tag = %q, want empty (never deployed)", b.Tag)
	}
	if b.LastDeploy != nil {
		t.Fatal("app-b should have no last_deploy")
	}
}

func TestListServicesPublicAccess(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "public-app")
	if err := os.MkdirAll(d, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  public-app:\n    image: ghcr.io/owner/public-app:${TAG}\n    ports:\n      - \"127.0.0.1:3000:3000\"\n"
	if err := os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	deps := &deployer{cfg: &Config{ServicesDir: dir}, hist: newHistory(filepath.Join(t.TempDir(), "h.jsonl"), 100)}
	services, err := deps.listServices()
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("len = %d, want 1", len(services))
	}
	if services[0].Access != "public" {
		t.Fatalf("access = %q, want public", services[0].Access)
	}
}

func TestParseDotenv(t *testing.T) {
	s := `TAG=sha-abc
QUOTED="a\"b\nc"
# comment
EMPTY=
NOEQUALS
`
	m := parseDotenv(s)
	if m["TAG"] != "sha-abc" {
		t.Fatalf("TAG = %q", m["TAG"])
	}
	if m["QUOTED"] != "a\"b\nc" {
		t.Fatalf("QUOTED = %q", m["QUOTED"])
	}
	if v, ok := m["EMPTY"]; !ok || v != "" {
		t.Fatalf("EMPTY = %q, ok=%v", v, ok)
	}
	if _, ok := m["NOEQUALS"]; ok {
		t.Fatal("NOEQUALS should be ignored")
	}
}

func TestUnquoteDotenv(t *testing.T) {
	if got := unquoteDotenv("sha-abc"); got != "sha-abc" {
		t.Fatalf("unquoted = %q", got)
	}
	if got := unquoteDotenv(`"a\"b\nc"`); got != "a\"b\nc" {
		t.Fatalf("quoted = %q", got)
	}
}

func TestComposeHelpers(t *testing.T) {
	s, err := renderServiceCompose(&ServiceSpec{Name: "web", Image: "ghcr.io/o/web", Port: 4242, Hostname: "webtail"})
	if err != nil {
		t.Fatal(err)
	}
	if got := composeImage(s); got != "ghcr.io/o/web" {
		t.Fatalf("image = %q", got)
	}
	if got := composeHostname(s); got != "webtail" {
		t.Fatalf("hostname = %q", got)
	}
	if got := composePort(s); got != 4242 {
		t.Fatalf("port = %d", got)
	}
}
