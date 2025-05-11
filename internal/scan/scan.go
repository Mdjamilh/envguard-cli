// Package scan looks for secrets that shouldn't be committed: known token
// formats, private keys, and high-entropy values assigned to names like
// *_SECRET or *_TOKEN.
package scan

import (
	"bufio"
	"bytes"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is one likely secret. Match is redacted; the full secret is never kept.
type Finding struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Rule   string `json:"rule"`
	Match  string `json:"match"`
}

type rule struct {
	name    string
	pattern *regexp.Regexp
}

// Known formats, specific enough to report without further checks.
var rules = []rule{
	{"AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{"Stripe live key", regexp.MustCompile(`\b(sk|rk)_live_[A-Za-z0-9]{20,}\b`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`)},
	{"OpenAI API key", regexp.MustCompile(`\bsk-(proj-)?[A-Za-z0-9_-]{32,}\b`)},
	{"Private key", regexp.MustCompile(`-----BEGIN ([A-Z]+ )?PRIVATE KEY-----`)},
}

// A password inside a connection string: scheme://user:password@host.
var urlCredentials = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://([^/\s:@'"]+):([^/\s@'"]+)@[^\s'"]+`) // envguard:ignore

// Throwaway passwords in local development URLs (postgres://postgres:postgres@localhost).
var devPassword = regexp.MustCompile(`(?i)^(postgres|password|pass|secret|root|admin|example|test|dev|local|guest)$`)

// NAME=value or name: "value", where the name suggests a secret.
var assignment = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(SECRET|TOKEN|PASSWORD|PASSWD|API_?KEY|PRIVATE_KEY|ACCESS_KEY)[A-Z0-9_]*)\b\s*[:=]\s*["']?([^\s"'#,;]+)`)

// Escape sequences in source strings ("PORT=1\nAPI_TOKEN=...") become spaces
// of the same width, so names aren't glued to the "n" and columns still line up.
var sourceEscapes = strings.NewReplacer(`\n`, "  ", `\t`, "  ", `\r`, "  ")

// Generated secrets use these characters (base64, hex, URL-safe tokens,
// JWTs). Values with parentheses, backticks or the like are code.
var tokenChars = regexp.MustCompile(`^[A-Za-z0-9+/=_.~:-]+$`)

// Values that are obviously placeholders, not real secrets.
var placeholder = regexp.MustCompile(`(?i)^(changeme|change-me|example|placeholder|dummy|test|xxx+|\*+|<.*>|\$\{.*\}|\$[A-Z_]+|your[-_].*|.*[-_]here|todo|none|null|true|false)$`)

const ignoreMarker = "envguard:ignore"

// Line scans one line, returning findings with 1-based columns.
func Line(file string, lineNo int, line string) []Finding {
	if strings.Contains(line, ignoreMarker) {
		return nil
	}
	var out []Finding
	matched := false
	for _, r := range rules {
		for _, loc := range r.pattern.FindAllStringIndex(line, -1) {
			if r.name == "OpenAI API key" && strings.HasPrefix(line[loc[0]:], "sk-ant-") {
				continue // reported as an Anthropic key
			}
			out = append(out, Finding{file, lineNo, loc[0] + 1, r.name, redact(line[loc[0]:loc[1]])})
			matched = true
		}
	}
	for _, m := range urlCredentials.FindAllStringSubmatchIndex(line, -1) {
		user, password := line[m[2]:m[3]], line[m[4]:m[5]]
		if len(password) < 4 || password == user || placeholder.MatchString(password) || devPassword.MatchString(password) {
			continue
		}
		out = append(out, Finding{file, lineNo, m[4] + 1, "Password in URL", redact(password)})
		matched = true
	}
	if matched {
		return out
	}
	line = sourceEscapes.Replace(line)
	for _, m := range assignment.FindAllStringSubmatchIndex(line, -1) {
		value := line[m[6]:m[7]]
		// URLs aren't random strings; passwords inside them are caught above.
		if strings.Contains(value, "://") || !tokenChars.MatchString(value) {
			continue
		}
		if len(value) >= 16 && !placeholder.MatchString(value) && entropy(value) >= 3.5 {
			out = append(out, Finding{file, lineNo, m[6] + 1, "High-entropy value for " + line[m[2]:m[3]], redact(value)})
		}
	}
	return out
}

// Reader scans a whole file.
func Reader(file string, r io.Reader) ([]Finding, error) {
	var out []Finding
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		out = append(out, Line(file, n, sc.Text())...)
	}
	return out, sc.Err()
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, ".next": true, "coverage": true}

const maxFileSize = 1 << 20

// Tree scans files under root, skipping dependency and build folders,
// binaries and files over 1 MB.
func Tree(root string) ([]Finding, error) {
	var out []Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// Files that vanish or can't be read are skipped rather than ending the scan.
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entries
		}
		if !info.Mode().IsRegular() || info.Size() > maxFileSize {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil //nolint:nilerr // skip unreadable files
		}
		if isBinary(data) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		found, err := Reader(filepath.ToSlash(rel), bytes.NewReader(data))
		out = append(out, found...)
		return err
	})
	return out, err
}

func isBinary(data []byte) bool {
	head := data[:min(len(data), 8000)]
	return bytes.IndexByte(head, 0) >= 0
}

// entropy is the Shannon entropy in bits per character. Random tokens score
// around 4 to 6; words and repeated characters score much lower.
func entropy(s string) float64 {
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	n := float64(len([]rune(s)))
	var h float64
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// redact keeps just enough to recognize the secret: "ghp_abcd…".
func redact(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", min(len(s)-4, 12))
}
