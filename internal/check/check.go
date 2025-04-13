// Package check compares actual environment values with a schema.
package check

import (
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mdjamilh/envguard-cli/internal/schema"
)

// Severity of a finding: errors fail the check, warnings don't (unless --strict).
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Issue is one problem with one variable. Messages never contain the value
// of a secret.
type Issue struct {
	Key      string   `json:"key"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	// Line in the schema file, so CI annotations can point at it.
	Line int `json:"line,omitempty"`
}

// Options tune what counts as a problem.
type Options struct {
	// Unknown keys (not in the schema) are reported as warnings unless ignored.
	IgnoreUnknown bool
}

// Run checks values against the schema. Results are in schema order, then
// unknown keys alphabetically, so output is stable.
func Run(s *schema.Schema, values map[string]string, opts Options) []Issue {
	var issues []Issue
	for _, v := range s.Vars {
		value, set := values[v.Name]
		if v.Deprecated != "" && set {
			issues = append(issues, Issue{v.Name, Warning, "is deprecated: " + v.Deprecated, v.Line})
		}
		if !set || value == "" {
			if v.Required {
				msg := "is required but not set"
				if v.Description != "" {
					msg += " (" + v.Description + ")"
				}
				issues = append(issues, Issue{v.Name, Error, msg, v.Line})
			}
			continue
		}
		if msg := validate(v, value); msg != "" {
			issues = append(issues, Issue{v.Name, Error, msg, v.Line})
		}
	}

	if !opts.IgnoreUnknown {
		var unknown []string
		for key := range values {
			if s.Lookup(key) == nil {
				unknown = append(unknown, key)
			}
		}
		slices.Sort(unknown)
		for _, key := range unknown {
			msg := "is not in the schema"
			if near := closest(key, s); near != "" {
				msg += fmt.Sprintf("; did you mean %s?", near)
			}
			issues = append(issues, Issue{Key: key, Severity: Warning, Message: msg})
		}
	}
	return issues
}

// validate returns a problem description, or "" if the value is fine.
func validate(v *schema.Var, value string) string {
	shown := func() string {
		if v.Secret {
			return "the value"
		}
		return strconv.Quote(truncate(value, 60))
	}

	switch v.Type {
	case schema.Int, schema.Port:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Sprintf("must be a whole number, got %s", shown())
		}
		if v.Type == schema.Port && (n < 1 || n > 65535) {
			return fmt.Sprintf("must be a port between 1 and 65535, got %d", n)
		}
		if msg := inRange(v, float64(n), "be"); msg != "" {
			return msg
		}
	case schema.Float:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Sprintf("must be a number, got %s", shown())
		}
		if msg := inRange(v, n, "be"); msg != "" {
			return msg
		}
	case schema.Bool:
		switch strings.ToLower(value) {
		case "true", "false", "1", "0", "yes", "no", "on", "off":
		default:
			return fmt.Sprintf("must be true or false, got %s", shown())
		}
	case schema.URL:
		// url.Parse accepts "localhost:5432" (scheme "localhost"), a common
		// mistake in DATABASE_URL, so require scheme://host, or a file:// path.
		u, err := url.Parse(value)
		hasHost := err == nil && strings.Contains(value, "://") && u.Host != ""
		isFile := err == nil && u.Scheme == "file" && u.Path != ""
		if !hasHost && !isFile {
			return fmt.Sprintf("must be a full URL such as https://example.com, got %s", shown())
		}
	case schema.Email:
		addr, err := mail.ParseAddress(value)
		if err != nil || addr.Address != value {
			return fmt.Sprintf("must be an email address, got %s", shown())
		}
	case schema.Duration:
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Sprintf("must be a duration such as 30s or 5m, got %s", shown())
		}
	case schema.JSON:
		if !json.Valid([]byte(value)) {
			return "must be valid JSON"
		}
	case schema.Enum:
		if !slices.Contains(v.Enum, value) {
			msg := fmt.Sprintf("must be one of %s, got %s", strings.Join(v.Enum, ", "), shown())
			for _, option := range v.Enum {
				if strings.EqualFold(option, value) && !v.Secret {
					msg += fmt.Sprintf(" (values are case-sensitive: use %q)", option)
				}
			}
			return msg
		}
	case schema.String:
		length := float64(utf8.RuneCountInString(value))
		if v.Min != nil && length < *v.Min {
			return fmt.Sprintf("must be at least %s characters long", formatNumber(*v.Min))
		}
		if v.Max != nil && length > *v.Max {
			return fmt.Sprintf("must be at most %s characters long", formatNumber(*v.Max))
		}
	}

	if v.Pattern != nil && !v.Pattern.MatchString(value) {
		if v.Secret {
			return "doesn't match the required pattern"
		}
		return fmt.Sprintf("must match %s, got %s", v.Pattern.String(), shown())
	}
	return ""
}

func inRange(v *schema.Var, n float64, verb string) string {
	switch {
	case v.Min != nil && n < *v.Min:
		return fmt.Sprintf("must %s at least %s, got %s", verb, formatNumber(*v.Min), formatNumber(n))
	case v.Max != nil && n > *v.Max:
		return fmt.Sprintf("must %s at most %s, got %s", verb, formatNumber(*v.Max), formatNumber(n))
	}
	return ""
}

func formatNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// closest suggests a schema key within two edits, for typos like DATABSE_URL.
func closest(key string, s *schema.Schema) string {
	best, bestDistance := "", 3
	for _, v := range s.Vars {
		if d := distance(strings.ToUpper(key), strings.ToUpper(v.Name)); d < bestDistance {
			best, bestDistance = v.Name, d
		}
	}
	return best
}

// distance is the Damerau–Levenshtein (optimal string alignment) distance,
// so swapped letters count as one edit.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range rb {
		d[0][j+1] = j + 1
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}
