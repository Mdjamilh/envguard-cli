package dotenv

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, src string, lookup Lookup) map[string]string {
	t.Helper()
	entries, err := Parse(strings.NewReader(src), lookup)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return Map(entries)
}

func TestValues(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want map[string]string
	}{
		{"plain", "A=1\nB = two \n", map[string]string{"A": "1", "B": "two"}},
		{"export prefix", "export TOKEN=abc", map[string]string{"TOKEN": "abc"}},
		{"empty value", "EMPTY=\nALSO_EMPTY=''", map[string]string{"EMPTY": "", "ALSO_EMPTY": ""}},
		{"inline comment", "A=value # note\nB=a#b", map[string]string{"A": "value", "B": "a#b"}},
		{"single quotes are literal", `A='$HOME \n # not a comment'`, map[string]string{"A": `$HOME \n # not a comment`}},
		{"double quote escapes", `A="line1\nline2\t\"q\" \\"`, map[string]string{"A": "line1\nline2\t\"q\" \\"}},
		{"comment after quotes", `A="x y" # trailing`, map[string]string{"A": "x y"}},
		{"multi-line double quotes", "KEY=\"-----BEGIN\nabc\n-----END\"\nNEXT=1", map[string]string{"KEY": "-----BEGIN\nabc\n-----END", "NEXT": "1"}},
		{"expansion", "HOST=db\nURL=postgres://${HOST}:5432/$NAME\nNAME=late", map[string]string{"HOST": "db", "URL": "postgres://db:5432/", "NAME": "late"}},
		{"expansion default", "A=${MISSING:-fallback}\nB=\"${MISSING:-x}y\"", map[string]string{"A": "fallback", "B": "xy"}},
		{"escaped dollar", `PRICE="\$5"`, map[string]string{"PRICE": "$5"}},
		{"no expansion in single quotes", "H=x\nA='${H}'", map[string]string{"H": "x", "A": "${H}"}},
		{"windows line endings and BOM", "\xef\xbb\xbfA=1\r\nB=2\r\n", map[string]string{"A": "1", "B": "2"}},
		{"last definition wins", "A=1\nA=2", map[string]string{"A": "2"}},
		{"lone dollar", "A=$ 5\nB=cost$", map[string]string{"A": "$ 5", "B": "cost$"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parse(t, tt.src, nil); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLookupFallsBackToEnvironment(t *testing.T) {
	env := map[string]string{"USER": "ada"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	got := parse(t, "GREETING=hello $USER\nUSER_OVERRIDE=${USER}!", lookup)
	if got["GREETING"] != "hello ada" || got["USER_OVERRIDE"] != "ada!" {
		t.Errorf("got %q", got)
	}
}

func TestCommentsAttachToTheNextKey(t *testing.T) {
	src := "# file header\n\n# Database URL\n# @required @type url\nDATABASE_URL=x\nPORT=1\n"
	entries, err := Parse(strings.NewReader(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Database URL", "@required @type url"}; !reflect.DeepEqual(entries[0].Comments, want) {
		t.Errorf("comments = %q, want %q", entries[0].Comments, want)
	}
	if entries[1].Comments != nil {
		t.Errorf("PORT should have no comments, got %q", entries[1].Comments)
	}
	if entries[0].Line != 5 || entries[1].Line != 6 {
		t.Errorf("lines = %d, %d", entries[0].Line, entries[1].Line)
	}
}

func TestErrorsPointAtTheLine(t *testing.T) {
	tests := []struct {
		src  string
		line int
		msg  string
	}{
		{"A=1\nnot an assignment", 2, "expected KEY=value"},
		{"1BAD=x", 1, "not a valid variable name"},
		{"MY-KEY=x", 1, "not a valid variable name"},
		{"A=1\nB=\"never closed\nC=3", 2, "unterminated \" quote"},
		{"A='x' junk", 1, "after closing quote"},
	}
	for _, tt := range tests {
		_, err := Parse(strings.NewReader(tt.src), nil)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("%q: expected ParseError, got %v", tt.src, err)
		}
		if pe.Line != tt.line || !strings.Contains(pe.Msg, tt.msg) {
			t.Errorf("%q: got line %d %q, want line %d containing %q", tt.src, pe.Line, pe.Msg, tt.line, tt.msg)
		}
	}
}

// FuzzParse checks the parser never panics and that every key it returns is valid.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"A=1", "export B='x'", "C=\"multi\nline\"", "D=${E:-f}", "# c\nG=h # i", "J=\"\\$k\"", "K=\"unterminated", "=x", "\xef\xbb\xbfL=1\r\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		entries, err := Parse(strings.NewReader(src), nil)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !validKey(e.Key) {
				t.Fatalf("invalid key %q from %q", e.Key, src)
			}
			if e.Line < 1 {
				t.Fatalf("line %d for %q", e.Line, e.Key)
			}
		}
	})
}
