package scan

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIsEnvFile(t *testing.T) {
	for p, want := range map[string]bool{
		".env":                  true,
		"apps/web/.env.local":   true,
		".env.production":       true,
		"deploy/prod.env":       true,
		".env.example":          false,
		"config/.env.sample":    false,
		".env.template":         false,
		".env.dist":             false,
		"environment.ts":        false,
		"docs/envelope.md":      false,
		"src/.environment.json": false,
	} {
		if got := IsEnvFile(p); got != want {
			t.Errorf("IsEnvFile(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestTrackedEnvFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := TrackedEnvFiles(dir); !errors.Is(err, ErrNotGitRepo) {
		t.Fatalf("outside a repo: got %v", err)
	}
	run("init", "-q")
	for _, f := range []string{".env", ".env.example", "api/.env.local", "README.md", "untracked.env"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("A=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".env", ".env.example", "api/.env.local", "README.md")

	got, err := TrackedEnvFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".env", "api/.env.local"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
