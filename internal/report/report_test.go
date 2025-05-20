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
	if diff != "- MISSING  missing from .env\n+ EXTRA    not documented in .env.example\n" {
		t.Errorf("got %q", diff)
	}
}

func TestScanAndDiffMachineFormats(t *testing.T) {
	findings := ScanResult{Root: ".", TrackedEnvs: []string{"api/.env"}, Findings: []scan.Finding{{File: "a.go", Line: 3, Column: 9, Rule: "Slack token", Match: "xoxb****"}}}

	gh := render(t, GitHub, func(p *Printer) error { return p.Scan(findings) })
	for _, want := range []string{
		"::error file=api/.env,title=envguard::This env file is committed to git.",
		"::error file=a.go,line=3,col=9,title=envguard::Possible secret: Slack token (xoxb****)",
		"envguard: 1 possible secret, 1 committed env file",
	} {
		if !strings.Contains(gh, want) {
			t.Errorf("missing %q in:\n%s", want, gh)
		}
	}

	var parsed struct {
		OK          bool           `json:"ok"`
		Findings    []scan.Finding `json:"findings"`
		TrackedEnvs []string       `json:"trackedEnvFiles"`
	}
	if err := json.Unmarshal([]byte(render(t, JSON, func(p *Printer) error { return p.Scan(ScanResult{Root: "."}) })), &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.OK || parsed.Findings == nil || parsed.TrackedEnvs == nil {
		t.Errorf("clean scan JSON should be ok with empty lists: %+v", parsed)
	}

	d := generate.KeyDiff{OnlyInEnv: []string{"EXTRA"}, OnlyInExample: []string{"MISSING"}}
	ghDiff := render(t, GitHub, func(p *Printer) error { return p.Diff(".env", ".env.example", d) })
	if !strings.Contains(ghDiff, "::warning file=.env,title=envguard::MISSING is in .env.example but not in .env") ||
		!strings.Contains(ghDiff, "::warning file=.env.example,title=envguard::EXTRA is in .env but not documented in .env.example") {
		t.Errorf("got:\n%s", ghDiff)
	}
	jsonDiff := render(t, JSON, func(p *Printer) error { return p.Diff(".env", ".env.example", d) })
	if !strings.Contains(jsonDiff, `"ok": false`) || !strings.Contains(jsonDiff, `"onlyInExample": [`) {
		t.Errorf("got %s", jsonDiff)
	}
	if same := render(t, Text, func(p *Printer) error { return p.Diff("a", "b", generate.KeyDiff{}) }); same != "✓ a and b define the same variables.\n" {
		t.Errorf("got %q", same)
	}
}

func TestColorOnlyForTerminals(t *testing.T) {
	p := &Printer{format: Text, color: true}
	if got := p.paint("31", "x"); got != "\x1b[31mx\x1b[0m" {
		t.Errorf("got %q", got)
	}
	var buf bytes.Buffer
	if New(&buf, Text).color {
		t.Error("a buffer is not a terminal")
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
