package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Mdjamilh/envguard-cli/internal/check"
	"github.com/Mdjamilh/envguard-cli/internal/generate"
	"github.com/Mdjamilh/envguard-cli/internal/scan"
)

var result = CheckResult{
	EnvFile:    ".env",
	SchemaFile: ".env.example",
	Variables:  4,
	Issues: []check.Issue{
		{Key: "DATABASE_URL", Severity: check.Error, Message: "is required but not set", Line: 3},
		{Key: "PORT", Severity: check.Error, Message: "must be a port between 1 and 65535, got 99999", Line: 6},
		{Key: "DATABSE_URL", Severity: check.Warning, Message: "is not in the schema; did you mean DATABASE_URL?"},
	},
}

func render(t *testing.T, format Format, fn func(*Printer) error) string {
	t.Helper()
	var buf bytes.Buffer
	if err := fn(New(&buf, format)); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestTextHasNoColorWhenPiped(t *testing.T) {
	got := render(t, Text, func(p *Printer) error { return p.Check(result) })
	want := `Checked .env against .env.example (4 variables)

  ✗ DATABASE_URL  is required but not set
  ✗ PORT          must be a port between 1 and 65535, got 99999
  ! DATABSE_URL   is not in the schema; did you mean DATABASE_URL?

2 errors, 1 warning
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "\x1b[") {
		t.Error("escape codes in piped output")
	}
}

func TestJSON(t *testing.T) {
	var parsed struct {
		OK       bool
		Errors   int
		Warnings int
		Issues   []check.Issue
	}
	if err := json.Unmarshal([]byte(render(t, JSON, func(p *Printer) error { return p.Check(result) })), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OK || parsed.Errors != 2 || parsed.Warnings != 1 || len(parsed.Issues) != 3 {
		t.Errorf("got %+v", parsed)
	}

	clean := render(t, JSON, func(p *Printer) error { return p.Check(CheckResult{EnvFile: ".env", SchemaFile: "s"}) })
	if !strings.Contains(clean, `"ok": true`) || !strings.Contains(clean, `"issues": []`) {
		t.Errorf("clean JSON should be ok with an empty list: %s", clean)
	}
}

func TestGitHubAnnotations(t *testing.T) {
	got := render(t, GitHub, func(p *Printer) error { return p.Check(result) })
	for _, want := range []string{
		"::error file=.env.example,line=3,title=envguard::DATABASE_URL is required but not set\n",
		"::warning file=.env.example,title=envguard::DATABSE_URL is not in the schema; did you mean DATABASE_URL?\n",
		"envguard: 2 errors, 1 warning\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if escapeData("50%\nnext") != "50%25%0Anext" || escapeProperty("a:b,c") != "a%3Ab%2Cc" {
		t.Error("workflow command escaping is wrong")
	}
}

func TestScanAndDiffOutput(t *testing.T) {
	scanText := render(t, Text, func(p *Printer) error {
		return p.Scan(ScanResult{Root: ".", TrackedEnvs: []string{".env"}, Findings: []scan.Finding{{File: "src/a.ts", Line: 2, Column: 7, Rule: "GitHub token", Match: "ghp_****"}}})
	})
	for _, want := range []string{".env is committed to git", "git rm --cached .env", "src/a.ts:2:7  GitHub token  ghp_****", "1 possible secret, 1 committed env file"} {
		if !strings.Contains(scanText, want) {
			t.Errorf("missing %q in:\n%s", want, scanText)
		}
	}
	if got := render(t, Text, func(p *Printer) error { return p.Scan(ScanResult{Root: "repo"}) }); got != "✓ No secrets found in repo.\n" {
		t.Errorf("got %q", got)
	}

	diff := render(t, Text, func(p *Printer) error {
		return p.Diff(".env", ".env.example", generate.KeyDiff{OnlyInEnv: []string{"EXTRA"}, OnlyInExample: []string{"MISSING"}})
	})
	if diff != "- MISSING  missing from .env\n+ EXTRA  not documented in .env.example\n" {
		t.Errorf("got %q", diff)
	}
}

func TestParseFormat(t *testing.T) {
	if _, err := ParseFormat("yaml"); err == nil {
		t.Error("yaml should be rejected")
	}
	if f, err := ParseFormat("github"); err != nil || f != GitHub {
		t.Errorf("got %v %v", f, err)
	}
}
