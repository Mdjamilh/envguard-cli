package schema

import (
	"errors"
	"strings"
	"testing"

	"github.com/Mdjamilh/envguard-cli/internal/dotenv"
)

func load(t *testing.T, src string) (*Schema, error) {
	t.Helper()
	entries, err := dotenv.Parse(strings.NewReader(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	return FromEntries(entries)
}

const example = `# Where the app connects to Postgres.
# @type url @secret
DATABASE_URL=postgres://localhost:5432/app

# @type port @default 3000
PORT=3000

# @enum development, production, test
NODE_ENV=development

# @type int @min 1 @max 64
WORKERS=4

# @optional
# @pattern [A-Z]{2}-\d{4}
REGION_CODE=EU-0001

# @deprecated use DATABASE_URL instead
DB_HOST=
`

func TestAnnotations(t *testing.T) {
	s, err := load(t, example)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Vars) != 6 {
		t.Fatalf("got %d vars", len(s.Vars))
	}

	db := s.Lookup("DATABASE_URL")
	if db.Type != URL || !db.Secret || !db.Required || db.Description != "Where the app connects to Postgres." || db.Example != "postgres://localhost:5432/app" {
		t.Errorf("DATABASE_URL = %+v", db)
	}
	port := s.Lookup("PORT")
	if port.Type != Port || port.Required || port.Default == nil || *port.Default != "3000" {
		t.Errorf("PORT = %+v", port)
	}
	if env := s.Lookup("NODE_ENV"); env.Type != Enum || strings.Join(env.Enum, "|") != "development|production|test" {
		t.Errorf("NODE_ENV = %+v", env)
	}
	if w := s.Lookup("WORKERS"); *w.Min != 1 || *w.Max != 64 || w.Type != Int {
		t.Errorf("WORKERS = %+v", w)
	}
	region := s.Lookup("REGION_CODE")
	if region.Required || !region.Pattern.MatchString("EU-0001") || region.Pattern.MatchString("EU-0001x") {
		t.Errorf("REGION_CODE pattern should be anchored: %+v", region)
	}
	if old := s.Lookup("DB_HOST"); old.Deprecated != "use DATABASE_URL instead" || old.Required {
		t.Errorf("DB_HOST = %+v", old)
	}
	if s.Lookup("NOPE") != nil {
		t.Error("Lookup of unknown key should be nil")
	}
}

func TestPatternMayContainSpacesAndAt(t *testing.T) {
	s, err := load(t, "# @pattern [a-z]+@example\\.com @x\nEMAIL=a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Vars[0].Pattern.String(); got != `^(?:[a-z]+@example\.com @x)$` {
		t.Errorf("pattern = %s", got)
	}
}

func TestSchemaErrors(t *testing.T) {
	tests := []struct{ src, msg string }{
		{"# @type number\nA=1", `unknown type "number"`},
		{"# @colour red\nA=1", "unknown annotation @colour"},
		{"# @min ten\nA=1", "@min needs a number"},
		{"# @min 5 @max 1\nA=1", "@min is greater than @max"},
		{"# @pattern (unclosed\nA=1", "not a valid regular expression"},
		{"# @enum\nA=1", "@enum needs"},
		{"A=1\nA=2", "defined twice"},
	}
	for _, tt := range tests {
		_, err := load(t, tt.src)
		var se *Error
		if !errors.As(err, &se) || !strings.Contains(se.Msg, tt.msg) {
			t.Errorf("%q: got %v, want error containing %q", tt.src, err, tt.msg)
		}
	}
}
