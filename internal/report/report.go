// Package report prints results for people (text), programs (JSON) and
// GitHub Actions (workflow commands that become inline annotations).
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Mdjamilh/envguard-cli/internal/check"
	"github.com/Mdjamilh/envguard-cli/internal/generate"
	"github.com/Mdjamilh/envguard-cli/internal/scan"
)

// Format is how results are written.
type Format string

const (
	Text   Format = "text"
	JSON   Format = "json"
	GitHub Format = "github"
)

// ParseFormat validates a --format value.
func ParseFormat(s string) (Format, error) {
	switch f := Format(s); f {
	case Text, JSON, GitHub:
		return f, nil
	}
	return "", fmt.Errorf("unknown format %q (use text, json or github)", s)
}

// Printer writes in one format, with color only when it helps.
type Printer struct {
	w      io.Writer
	format Format
	color  bool
}

// New picks color automatically: on for terminals, off when NO_COLOR is set
// (https://no-color.org) or output is piped.
func New(w io.Writer, format Format) *Printer {
	return &Printer{w: w, format: format, color: format == Text && isTerminal(w) && os.Getenv("NO_COLOR") == ""}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (p *Printer) paint(code, s string) string {
	if !p.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p *Printer) json(v any) error {
	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// escapeData escapes a GitHub workflow command message.
func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

// escapeProperty escapes a GitHub workflow command property such as file=.
func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// CheckResult is the outcome of `envguard check`.
type CheckResult struct {
	EnvFile    string        `json:"env"`
	SchemaFile string        `json:"schema"`
	Variables  int           `json:"variables"`
	Issues     []check.Issue `json:"issues"`
}

func (r CheckResult) count(s check.Severity) int {
	n := 0
	for _, i := range r.Issues {
		if i.Severity == s {
			n++
		}
	}
	return n
}

// Check prints a check result.
func (p *Printer) Check(r CheckResult) error {
	errors, warnings := r.count(check.Error), r.count(check.Warning)
	switch p.format {
	case JSON:
		if r.Issues == nil {
			r.Issues = []check.Issue{}
		}
		return p.json(struct {
			OK       bool `json:"ok"`
			Errors   int  `json:"errors"`
			Warnings int  `json:"warnings"`
			CheckResult
		}{errors == 0, errors, warnings, r})
	case GitHub:
		for _, i := range r.Issues {
			level := "error"
			if i.Severity == check.Warning {
				level = "warning"
			}
			props := "file=" + escapeProperty(r.SchemaFile)
			if i.Line > 0 {
				props += fmt.Sprintf(",line=%d", i.Line)
			}
			fmt.Fprintf(p.w, "::%s %s,title=envguard::%s\n", level, props, escapeData(i.Key+" "+i.Message))
		}
		fmt.Fprintf(p.w, "envguard: %s, %s\n", plural(errors, "error"), plural(warnings, "warning"))
		return nil
	}

	fmt.Fprintf(p.w, "Checked %s against %s (%s)\n\n", r.EnvFile, r.SchemaFile, plural(r.Variables, "variable"))
	if len(r.Issues) == 0 {
		fmt.Fprintf(p.w, "  %s All variables are set and valid.\n", p.paint("32", "✓"))
		return nil
	}
	width := 0
	for _, i := range r.Issues {
		width = max(width, len(i.Key))
	}
	for _, i := range r.Issues {
		mark := p.paint("31", "✗")
		if i.Severity == check.Warning {
			mark = p.paint("33", "!")
		}
		fmt.Fprintf(p.w, "  %s %s  %s\n", mark, p.paint("1", fmt.Sprintf("%-*s", width, i.Key)), i.Message)
	}
	fmt.Fprintf(p.w, "\n%s, %s\n", plural(errors, "error"), plural(warnings, "warning"))
	return nil
}

// ScanResult is the outcome of `envguard scan`.
type ScanResult struct {
	Root        string         `json:"root"`
	Findings    []scan.Finding `json:"findings"`
	TrackedEnvs []string       `json:"trackedEnvFiles"`
}

// Scan prints secret-scan findings.
func (p *Printer) Scan(r ScanResult) error {
	switch p.format {
	case JSON:
		if r.Findings == nil {
			r.Findings = []scan.Finding{}
		}
		if r.TrackedEnvs == nil {
			r.TrackedEnvs = []string{}
		}
		return p.json(struct {
			OK bool `json:"ok"`
			ScanResult
		}{len(r.Findings) == 0 && len(r.TrackedEnvs) == 0, r})
	case GitHub:
		for _, f := range r.TrackedEnvs {
			fmt.Fprintf(p.w, "::error file=%s,title=envguard::%s\n", escapeProperty(f), escapeData("This env file is committed to git. Remove it with `git rm --cached` and add it to .gitignore."))
		}
		for _, f := range r.Findings {
			fmt.Fprintf(p.w, "::error file=%s,line=%d,col=%d,title=envguard::%s\n", escapeProperty(f.File), f.Line, f.Column, escapeData("Possible secret: "+f.Rule+" ("+f.Match+")"))
		}
		fmt.Fprintf(p.w, "envguard: %s, %s\n", plural(len(r.Findings), "possible secret"), plural(len(r.TrackedEnvs), "committed env file"))
		return nil
	}

	if len(r.Findings) == 0 && len(r.TrackedEnvs) == 0 {
		where := r.Root
		if where == "." {
			where = "the current directory"
		}
		fmt.Fprintf(p.w, "%s No secrets found in %s.\n", p.paint("32", "✓"), where)
		return nil
	}
	for _, f := range r.TrackedEnvs {
		fmt.Fprintf(p.w, "%s %s is committed to git\n    remove it with: git rm --cached %s\n", p.paint("31", "✗"), p.paint("1", f), f)
	}
	for _, f := range r.Findings {
		fmt.Fprintf(p.w, "%s %s  %s  %s\n", p.paint("31", "✗"), p.paint("1", fmt.Sprintf("%s:%d:%d", f.File, f.Line, f.Column)), f.Rule, p.paint("2", f.Match))
	}
	fmt.Fprintf(p.w, "\n%s, %s. Mark a false positive with an envguard:ignore comment.\n", plural(len(r.Findings), "possible secret"), plural(len(r.TrackedEnvs), "committed env file"))
	return nil
}

// Diff prints keys present in only one of the two files.
func (p *Printer) Diff(envFile, exampleFile string, d generate.KeyDiff) error {
	switch p.format {
	case JSON:
		return p.json(struct {
			OK bool `json:"ok"`
			generate.KeyDiff
		}{d.Empty(), d})
	case GitHub:
		for _, k := range d.OnlyInExample {
			fmt.Fprintf(p.w, "::warning file=%s,title=envguard::%s\n", escapeProperty(envFile), escapeData(k+" is in "+exampleFile+" but not in "+envFile))
		}
		for _, k := range d.OnlyInEnv {
			fmt.Fprintf(p.w, "::warning file=%s,title=envguard::%s\n", escapeProperty(exampleFile), escapeData(k+" is in "+envFile+" but not documented in "+exampleFile))
		}
		return nil
	}
	if d.Empty() {
		fmt.Fprintf(p.w, "%s %s and %s define the same variables.\n", p.paint("32", "✓"), envFile, exampleFile)
		return nil
	}
	width := 0
	for _, k := range append(append([]string{}, d.OnlyInExample...), d.OnlyInEnv...) {
		width = max(width, len(k))
	}
	for _, k := range d.OnlyInExample {
		fmt.Fprintf(p.w, "%s %s  missing from %s\n", p.paint("31", "-"), p.paint("1", fmt.Sprintf("%-*s", width, k)), envFile)
	}
	for _, k := range d.OnlyInEnv {
		fmt.Fprintf(p.w, "%s %s  not documented in %s\n", p.paint("33", "+"), p.paint("1", fmt.Sprintf("%-*s", width, k)), exampleFile)
	}
	return nil
}
