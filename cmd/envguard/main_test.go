package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const example = `# Where the app connects to Postgres.
# @type url @secret
DATABASE_URL=postgres://localhost/app

# @type port @default 3000
PORT=3000

# @enum development, production, test
NODE_ENV=development
`

// project creates files in a fresh directory and makes it the working directory.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

func envguard(t *testing.T, env []string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, env)
	return code, stdout.String(), stderr.String()
}

func TestCheckPassesForAValidEnv(t *testing.T) {
	project(t, map[string]string{".env.example": example, ".env": "DATABASE_URL=postgres://db/app\nNODE_ENV=production\n"})
	code, out, _ := envguard(t, nil, "check")
	if code != 0 || !strings.Contains(out, "All variables are set and valid") {
		t.Errorf("code %d, output:\n%s", code, out)
	}
}

func TestCheckFailsAndNeverPrintsSecrets(t *testing.T) {
	project(t, map[string]string{".env.example": example, ".env": "DATABASE_URL=not-a-url-SECRET\nNODE_ENV=staging\n"})
	code, out, _ := envguard(t, nil, "check")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(out, "DATABASE_URL  must be a full URL") || !strings.Contains(out, `NODE_ENV      must be one of development, production, test, got "staging"`) {
		t.Errorf("output:\n%s", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("secret value printed:\n%s", out)
	}
}

func TestLaterEnvFilesAndTheProcessEnvironmentWin(t *testing.T) {
	project(t, map[string]string{
		".env.example": example,
		".env":         "DATABASE_URL=bad\nNODE_ENV=development\n",
		".env.local":   "DATABASE_URL=postgres://local/app\n",
	})
	if code, out, _ := envguard(t, nil, "check", "--env", ".env", "--env", ".env.local"); code != 0 {
		t.Errorf(".env.local should override .env; code %d:\n%s", code, out)
	}

	// Only the environment: unrelated process variables aren't reported as unknown.
	env := []string{"DATABASE_URL=https://db.example.com", "NODE_ENV=test", "PATH=/usr/bin", "HOME=/root"}
	code, out, _ := envguard(t, env, "check", "--process")
	if code != 0 || strings.Contains(out, "PATH") {
		t.Errorf("code %d:\n%s", code, out)
	}
	if !strings.Contains(out, "Checked environment against") {
		t.Errorf("label should mention the environment:\n%s", out)
	}
}

func TestStrictTurnsWarningsIntoFailures(t *testing.T) {
	project(t, map[string]string{".env.example": example, ".env": "DATABASE_URL=https://db\nNODE_ENV=test\nEXTRA=1\n"})
	if code, _, _ := envguard(t, nil, "check"); code != 0 {
		t.Errorf("warnings alone should pass, got %d", code)
	}
	if code, _, _ := envguard(t, nil, "check", "--strict"); code != 1 {
		t.Errorf("--strict should fail on warnings, got %d", code)
	}
	if code, out, _ := envguard(t, nil, "check", "--strict", "--ignore-unknown"); code != 0 {
		t.Errorf("--ignore-unknown should hide EXTRA, got %d:\n%s", code, out)
	}
}

func TestJSONOutput(t *testing.T) {
	project(t, map[string]string{".env.example": example, ".env": "NODE_ENV=test\n"})
	_, out, _ := envguard(t, nil, "check", "--format", "json")
	var parsed struct {
		OK     bool                   `json:"ok"`
		Errors int                    `json:"errors"`
		Issues []struct{ Key string } `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if parsed.OK || parsed.Errors != 1 || parsed.Issues[0].Key != "DATABASE_URL" {
		t.Errorf("got %+v", parsed)
	}
}

func TestHelpfulErrors(t *testing.T) {
	project(t, map[string]string{".env": "A=1\n"})
	tests := []struct {
		args []string
		msg  string
	}{
		{[]string{"check"}, "schema file .env.example not found; create one with `envguard init`"},
		{[]string{"check", "--format", "xml"}, `unknown format "xml"`},
		{[]string{"check", "--bogus"}, "flag provided but not defined: -bogus"},
		{[]string{"frobnicate"}, `unknown command "frobnicate"`},
		{[]string{"scan", "missing-dir"}, "missing-dir is not a directory"},
	}
	for _, tt := range tests {
		code, _, stderr := envguard(t, nil, tt.args...)
		if code != 2 || !strings.Contains(stderr, tt.msg) {
			t.Errorf("%v: code %d, stderr %q, want %q", tt.args, code, stderr, tt.msg)
		}
	}
	if code, _, _ := envguard(t, nil); code != 2 {
		t.Error("no arguments should be a usage error")
	}
}

func TestDiff(t *testing.T) {
	project(t, map[string]string{".env.example": example, ".env": "DATABASE_URL=x\nPORT=1\nNODE_ENV=a\n"})
	if code, _, _ := envguard(t, nil, "diff"); code != 0 {
		t.Errorf("same keys should pass, got %d", code)
	}
	os.WriteFile(".env", []byte("DATABASE_URL=x\nSURPRISE=1\n"), 0o644)
	code, out, _ := envguard(t, nil, "diff")
	if code != 1 || !strings.Contains(out, "PORT  missing from .env") || !strings.Contains(out, "SURPRISE  not documented") {
		t.Errorf("code %d:\n%s", code, out)
	}
}

func TestInitWritesAnExampleThatChecksTheOriginal(t *testing.T) {
	project(t, map[string]string{".env": "PORT=8080\nAPI_TOKEN=abc123def456ghi789\n"})
	code, out, stderr := envguard(t, nil, "init")
	if code != 0 || !strings.Contains(out, "Wrote .env.example with 2 variables") {
		t.Fatalf("code %d: %s %s", code, out, stderr)
	}
	written, _ := os.ReadFile(".env.example")
	if strings.Contains(string(written), "abc123") {
		t.Errorf("token copied into the example:\n%s", written)
	}
	if code, out, _ := envguard(t, nil, "check"); code != 0 {
		t.Errorf("the original .env should pass its generated schema:\n%s", out)
	}
	if code, _, stderr := envguard(t, nil, "init"); code != 2 || !strings.Contains(stderr, "already exists") {
		t.Errorf("init must not overwrite without --force: %d %s", code, stderr)
	}
	if code, _, _ := envguard(t, nil, "init", "--force"); code != 0 {
		t.Error("--force should overwrite")
	}
}

func TestScan(t *testing.T) {
	token := "ghp_" + strings.Repeat("aB3", 12)
	dir := project(t, map[string]string{"src/app.js": "const t = '" + token + "';\n", "README.md": "hello\n"})
	code, out, _ := envguard(t, nil, "scan", dir, "--no-git")
	if code != 1 || !strings.Contains(out, "src/app.js:1:12  GitHub token") || strings.Contains(out, token) {
		t.Errorf("code %d:\n%s", code, out)
	}
	os.Remove("src/app.js")
	if code, out, _ := envguard(t, nil, "scan", "--no-git"); code != 0 {
		t.Errorf("clean project: code %d:\n%s", code, out)
	}
}

func TestScanFindsCommittedEnvFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := project(t, map[string]string{".env": "A=1\n", ".env.example": "A=\n"})
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	code, out, _ := envguard(t, nil, "scan", "--format", "github")
	if code != 1 || !strings.Contains(out, "::error file=.env,title=envguard::This env file is committed to git") {
		t.Errorf("code %d:\n%s", code, out)
	}
}

func TestVersion(t *testing.T) {
	if _, out, _ := envguard(t, nil, "version"); out != "envguard dev\n" {
		t.Errorf("got %q", out)
	}
}
