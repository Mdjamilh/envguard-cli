// Package dotenv parses .env files the way most tools read them: KEY=value
// lines, optional "export", single quotes (literal), double quotes (escapes
// and ${VAR} expansion, may span lines) and # comments.
package dotenv

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Entry is one assignment. Comments holds the comment lines directly above
// it (without the leading #), which is where schema annotations live.
type Entry struct {
	Key      string
	Value    string
	Line     int
	Comments []string
}

// ParseError points at the line that couldn't be read.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// Lookup resolves variables that aren't defined earlier in the file, such as
// the process environment. It may be nil.
type Lookup func(key string) (string, bool)

// Parse reads every entry in order. Later duplicates are kept so callers can
// report them; Map gives the usual last-one-wins view.
func Parse(r io.Reader, lookup Lookup) ([]Entry, error) {
	lines, err := readLines(r)
	if err != nil {
		return nil, err
	}
	p := &parser{lines: lines, lookup: lookup, defined: map[string]string{}}
	return p.parse()
}

// Map turns entries into key/value pairs, the last definition winning.
func Map(entries []Entry) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.Key] = e.Value
	}
	return m
}

func readLines(r io.Reader) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, strings.TrimSuffix(sc.Text(), "\r"))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) > 0 {
		lines[0] = strings.TrimPrefix(lines[0], "\ufeff")
	}
	return lines, nil
}

type parser struct {
	lines   []string
	i       int
	lookup  Lookup
	defined map[string]string
}

func (p *parser) parse() ([]Entry, error) {
	var entries []Entry
	var comments []string
	for ; p.i < len(p.lines); p.i++ {
		line := strings.TrimSpace(p.lines[p.i])
		switch {
		case line == "":
			comments = nil // a blank line separates a comment block from the next key
			continue
		case strings.HasPrefix(line, "#"):
			comments = append(comments, strings.TrimSpace(strings.TrimPrefix(line, "#")))
			continue
		}

		start := p.i + 1
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, &ParseError{start, fmt.Sprintf("expected KEY=value, got %q", truncate(line, 40))}
		}
		key := strings.TrimSpace(line[:eq])
		if !validKey(key) {
			return nil, &ParseError{start, fmt.Sprintf("%q is not a valid variable name", key)}
		}
		value, err := p.value(strings.TrimLeft(line[eq+1:], " \t"), start)
		if err != nil {
			return nil, err
		}
		p.defined[key] = value
		entries = append(entries, Entry{Key: key, Value: value, Line: start, Comments: comments})
		comments = nil
	}
	return entries, nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		letter := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		digit := r >= '0' && r <= '9'
		if !letter && !(digit && i > 0) {
			return false
		}
	}
	return true
}

// value reads the right-hand side, consuming more lines for multi-line quotes.
func (p *parser) value(raw string, start int) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '\'':
		body, err := p.quoted(raw[1:], '\'', start)
		return body, err
	case '"':
		body, err := p.quoted(raw[1:], '"', start)
		if err != nil {
			return "", err
		}
		return p.expand(unescape(body)), nil
	default:
		// Unquoted: a # starts a comment only after whitespace ("a#b" stays intact).
		if i := strings.Index(raw, " #"); i >= 0 {
			raw = raw[:i]
		} else if i := strings.Index(raw, "\t#"); i >= 0 {
			raw = raw[:i]
		}
		return p.expand(strings.TrimSpace(raw)), nil
	}
}

// quoted collects text up to the closing quote, across lines if needed.
// Anything after the closing quote must be blank or a comment.
func (p *parser) quoted(rest string, quote byte, start int) (string, error) {
	var b strings.Builder
	for {
		if end := closingQuote(rest, quote); end >= 0 {
			b.WriteString(rest[:end])
			tail := strings.TrimSpace(rest[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return "", &ParseError{p.i + 1, fmt.Sprintf("unexpected %q after closing quote", truncate(tail, 20))}
			}
			return b.String(), nil
		}
		b.WriteString(rest)
		p.i++
		if p.i >= len(p.lines) {
			return "", &ParseError{start, fmt.Sprintf("unterminated %c quote", quote)}
		}
		b.WriteByte('\n')
		rest = p.lines[p.i]
	}
}

// closingQuote finds the closing quote; inside double quotes a backslash escapes it.
func closingQuote(s string, quote byte) int {
	for i := 0; i < len(s); i++ {
		if quote == '"' && s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			return i
		}
	}
	return -1
}

func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i == len(s)-1 {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"', '\\':
			b.WriteByte(s[i])
		case '$':
			// Keep a marker so expand leaves this dollar alone.
			b.WriteString("\x00$")
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// expand replaces ${VAR} and $VAR with earlier definitions or the lookup.
func (p *parser) expand(s string) string {
	if !strings.ContainsRune(s, '$') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == 0 && i+1 < len(s) && s[i+1] == '$':
			b.WriteByte('$') // escaped with \$
			i++
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteString(s[i:])
				return b.String()
			}
			name, fallback, hasDefault := strings.Cut(s[i+2:i+2+end], ":-")
			v, ok := p.resolve(name)
			if (!ok || v == "") && hasDefault {
				v = fallback
			}
			b.WriteString(v)
			i += end + 2
		case s[i] == '$' && i+1 < len(s) && (s[i+1] == '_' || isLetter(s[i+1])):
			j := i + 1
			for j < len(s) && (s[j] == '_' || isLetter(s[j]) || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			v, _ := p.resolve(s[i+1 : j])
			b.WriteString(v)
			i = j - 1
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func (p *parser) resolve(name string) (string, bool) {
	if v, ok := p.defined[name]; ok {
		return v, true
	}
	if p.lookup != nil {
		return p.lookup(name)
	}
	return "", false
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
