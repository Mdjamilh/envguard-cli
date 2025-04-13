// Package schema reads variable rules from the comments in a .env.example:
//
//	# Where the app connects to Postgres.
//	# @type url @secret
//	DATABASE_URL=postgres://localhost:5432/app
//
// Every variable is required unless marked @optional or given a @default.
package schema

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Mdjamilh/envguard-cli/internal/dotenv"
)

// Type is what a value must parse as.
type Type string

const (
	String   Type = "string"
	Int      Type = "int"
	Float    Type = "float"
	Bool     Type = "bool"
	URL      Type = "url"
	Email    Type = "email"
	Port     Type = "port"
	Duration Type = "duration"
	JSON     Type = "json"
	Enum     Type = "enum"
)

var knownTypes = map[Type]bool{String: true, Int: true, Float: true, Bool: true, URL: true, Email: true, Port: true, Duration: true, JSON: true, Enum: true}

// Var is one variable's rules.
type Var struct {
	Name        string
	Line        int
	Description string
	Example     string
	Type        Type
	Required    bool
	Secret      bool
	Default     *string
	Min, Max    *float64
	Pattern     *regexp.Regexp
	Enum        []string
	Deprecated  string
}

// Schema lists variables in file order.
type Schema struct {
	Vars []*Var
}

// Lookup finds a variable by name.
func (s *Schema) Lookup(name string) *Var {
	for _, v := range s.Vars {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// Error is a problem with an annotation, reported against its key's line.
type Error struct {
	Line int
	Key  string
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d (%s): %s", e.Line, e.Key, e.Msg) }

// FromEntries builds a schema from a parsed .env.example.
func FromEntries(entries []dotenv.Entry) (*Schema, error) {
	s := &Schema{}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Key] {
			return nil, &Error{e.Line, e.Key, "defined twice in the schema"}
		}
		seen[e.Key] = true
		v, err := parseVar(e)
		if err != nil {
			return nil, err
		}
		s.Vars = append(s.Vars, v)
	}
	return s, nil
}

func parseVar(e dotenv.Entry) (*Var, error) {
	v := &Var{Name: e.Key, Line: e.Line, Example: e.Value, Type: String, Required: true}
	fail := func(format string, args ...any) error { return &Error{e.Line, e.Key, fmt.Sprintf(format, args...)} }
	var description []string

	for _, comment := range e.Comments {
		if !strings.HasPrefix(comment, "@") {
			description = append(description, comment)
			continue
		}
		for _, a := range splitAnnotations(comment) {
			switch a.name {
			case "required":
				v.Required = true
			case "optional":
				v.Required = false
			case "secret":
				v.Secret = true
			case "type":
				t := Type(strings.ToLower(a.arg))
				if !knownTypes[t] || t == Enum {
					return nil, fail("unknown type %q (use string, int, float, bool, url, email, port, duration, json, or @enum)", a.arg)
				}
				v.Type = t
			case "enum":
				for _, option := range strings.Split(a.arg, ",") {
					if option = strings.TrimSpace(option); option != "" {
						v.Enum = append(v.Enum, option)
					}
				}
				if len(v.Enum) == 0 {
					return nil, fail("@enum needs a comma-separated list of values")
				}
				v.Type = Enum
			case "default":
				d := a.arg
				v.Default = &d
				v.Required = false
			case "min", "max":
				n, err := strconv.ParseFloat(a.arg, 64)
				if err != nil {
					return nil, fail("@%s needs a number, got %q", a.name, a.arg)
				}
				if a.name == "min" {
					v.Min = &n
				} else {
					v.Max = &n
				}
			case "pattern":
				re, err := regexp.Compile("^(?:" + a.arg + ")$")
				if err != nil {
					return nil, fail("@pattern is not a valid regular expression: %v", err)
				}
				v.Pattern = re
			case "deprecated":
				v.Deprecated = a.arg
				if v.Deprecated == "" {
					v.Deprecated = "this variable is deprecated"
				}
				v.Required = false
			default:
				return nil, fail("unknown annotation @%s", a.name)
			}
		}
	}
	if v.Min != nil && v.Max != nil && *v.Min > *v.Max {
		return nil, fail("@min is greater than @max")
	}
	v.Description = strings.Join(description, " ")
	return v, nil
}

type annotation struct{ name, arg string }

// splitAnnotations reads "@type int @min 1" as two annotations. @pattern
// and @deprecated take the rest of the line, since regular expressions and
// messages can contain spaces and @.
func splitAnnotations(line string) []annotation {
	var out []annotation
	rest := line
	for rest != "" {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "@") {
			break
		}
		name, after, _ := strings.Cut(rest[1:], " ")
		name = strings.ToLower(name)
		if name == "pattern" || name == "deprecated" {
			out = append(out, annotation{name, strings.TrimSpace(after)})
			break
		}
		arg := after
		switch i := strings.Index(after, " @"); {
		case strings.HasPrefix(after, "@"):
			// A flag with no argument, followed directly by the next annotation.
			arg, rest = "", after
		case i >= 0:
			arg, rest = after[:i], after[i:]
		default:
			rest = ""
		}
		out = append(out, annotation{name, strings.TrimSpace(arg)})
	}
	return out
}
