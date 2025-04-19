package generate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Mdjamilh/envguard-cli/internal/check"
	"github.com/Mdjamilh/envguard-cli/internal/dotenv"
	"github.com/Mdjamilh/envguard-cli/internal/schema"
)

func TestDiff(t *testing.T) {
	d := Diff(map[string]string{"A": "1", "B": "2", "TYPO_KEY": "x"}, map[string]string{"A": "", "B": "", "C": ""})
	want := KeyDiff{OnlyInEnv: []string{"TYPO_KEY"}, OnlyInExample: []string{"C"}}
	if !reflect.DeepEqual(d, want) || d.Empty() {
		t.Errorf("got %+v", d)
	}
	if !Diff(map[string]string{"A": "1"}, map[string]string{"A": ""}).Empty() {
		t.Error("same keys should be empty")
	}
}

const realEnv = `# App settings
PORT=8080
DEBUG=true
RATE=0.5
TIMEOUT=30s
PUBLIC_URL=https://app.example.com
DATABASE_URL=postgres://app:` + `hunter2-but-longer` + `@db:5432/app
STRIPE_SECRET_KEY=sk_test_123
GREETING='hello world'
# @optional @enum a,b
MODE=a
`

func TestExample(t *testing.T) {
	entries, err := dotenv.Parse(strings.NewReader(realEnv), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := Example(entries)

	for _, want := range []string{
		"# App settings\n# @type int\nPORT=8080\n",
		"# @type bool\nDEBUG=true\n",
		"# @type float\nRATE=0.5\n",
		"# @type duration\nTIMEOUT=30s\n",
		"# @type url\nPUBLIC_URL=https://app.example.com\n",
		"# @type url @secret\nDATABASE_URL=\n",
		"# @secret\nSTRIPE_SECRET_KEY=\n",
		"GREETING='hello world'\n",
		"# @optional @enum a,b\nMODE=a\n", // existing annotations are kept as written
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "sk_test_123") {
		t.Errorf("secrets leaked into the example:\n%s", got)
	}
}

// The generated example must parse back and work as a schema.
func TestExampleRoundTrips(t *testing.T) {
	entries, _ := dotenv.Parse(strings.NewReader(realEnv), nil)
	parsed, err := dotenv.Parse(strings.NewReader(Example(entries)), nil)
	if err != nil {
		t.Fatalf("generated example doesn't parse: %v", err)
	}
	s, err := schema.FromEntries(parsed)
	if err != nil {
		t.Fatalf("generated example isn't a valid schema: %v", err)
	}
	// The original env (with its real secrets) passes the generated schema.
	if issues := check.Run(s, dotenv.Map(entries), check.Options{}); len(issues) != 0 {
		t.Errorf("original env fails its own schema: %+v", issues)
	}
	if got := dotenv.Map(parsed)["GREETING"]; got != "hello world" {
		t.Errorf("quoted value changed: %q", got)
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       "plain",
		"two words":   "'two words'",
		"it's here":   `"it's here"`,
		"cost $5":     "'cost $5'",
		"line\nbreak": `"line\nbreak"`,
		"":            "",
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
		parsed, err := dotenv.Parse(strings.NewReader("K="+quote(in)), nil)
		if err != nil || dotenv.Map(parsed)["K"] != in {
			t.Errorf("quote(%q) doesn't round-trip: %v %q", in, err, dotenv.Map(parsed)["K"])
		}
	}
}
