package check

import (
	"strings"
	"testing"

	"github.com/Mdjamilh/envguard-cli/internal/dotenv"
	"github.com/Mdjamilh/envguard-cli/internal/schema"
)

const example = `# Where the app connects to Postgres.
# @type url @secret
DATABASE_URL=postgres://localhost/app

# @type port @default 3000
PORT=3000

# @enum development, production, test
NODE_ENV=development

# @type int @min 1 @max 64
WORKERS=4

# @type bool @optional
DEBUG=false

# @type email @optional
SUPPORT_EMAIL=help@example.com

# @type duration @optional
TIMEOUT=30s

# @type json @optional
FEATURES={}

# @optional @min 32
# @secret
SESSION_SECRET=

# @optional @pattern sk_(test|live)_[a-zA-Z0-9]+
# @secret
STRIPE_KEY=

# @deprecated use DATABASE_URL instead
DB_HOST=
`

func mustSchema(t *testing.T) *schema.Schema {
	t.Helper()
	entries, err := dotenv.Parse(strings.NewReader(example), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.FromEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func messages(issues []Issue) []string {
	var out []string
	for _, i := range issues {
		out = append(out, string(i.Severity)+" "+i.Key+" "+i.Message)
	}
	return out
}

func TestValidEnvironmentHasNoIssues(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":   "postgres://user:pw@db:5432/app",
		"NODE_ENV":       "production",
		"WORKERS":        "8",
		"DEBUG":          "yes",
		"SUPPORT_EMAIL":  "ops@example.org",
		"TIMEOUT":        "1m30s",
		"FEATURES":       `{"beta":true}`,
		"SESSION_SECRET": strings.Repeat("x", 40),
		"STRIPE_KEY":     "sk_test_abc123",
	}
	if issues := Run(mustSchema(t), values, Options{}); len(issues) != 0 {
		t.Errorf("unexpected issues: %q", messages(issues))
	}
}

func TestMissingAndInvalidValues(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":   "localhost:5432",
		"PORT":           "99999",
		"NODE_ENV":       "Production",
		"WORKERS":        "0",
		"DEBUG":          "maybe",
		"SUPPORT_EMAIL":  "Help <help@example.com>",
		"TIMEOUT":        "30",
		"FEATURES":       "{broken",
		"SESSION_SECRET": "short-secret-value",
		"STRIPE_KEY":     "pk_live_123",
		"DB_HOST":        "db",
	}
	got := messages(Run(mustSchema(t), values, Options{}))
	want := []string{
		"error DATABASE_URL must be a full URL such as https://example.com, got the value",
		"error PORT must be a port between 1 and 65535, got 99999",
		`error NODE_ENV must be one of development, production, test, got "Production" (values are case-sensitive: use "production")`,
		"error WORKERS must be at least 1, got 0",
		`error DEBUG must be true or false, got "maybe"`,
		`error SUPPORT_EMAIL must be an email address, got "Help <help@example.com>"`,
		`error TIMEOUT must be a duration such as 30s or 5m, got "30"`,
		"error FEATURES must be valid JSON",
		"error SESSION_SECRET must be at least 32 characters long",
		"error STRIPE_KEY doesn't match the required pattern",
		"warning DB_HOST is deprecated: use DATABASE_URL instead",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestURLs(t *testing.T) {
	v := &schema.Var{Name: "U", Type: schema.URL}
	for _, ok := range []string{"https://example.com", "postgres://u:p@db:5432/app?sslmode=require", "redis://localhost:6379/0", "file:///var/data.db"} {
		if msg := validate(v, ok); msg != "" {
			t.Errorf("%q: %s", ok, msg)
		}
	}
	for _, bad := range []string{"localhost:5432", "example.com", "/just/a/path", "https://", "mailto:a@b.c"} {
		if validate(v, bad) == "" {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestSecretsNeverAppearInMessages(t *testing.T) {
	secret := "pk_live_SUPERSECRET"
	for _, issue := range Run(mustSchema(t), map[string]string{"STRIPE_KEY": secret, "DATABASE_URL": secret}, Options{}) {
		if strings.Contains(issue.Message, "SUPERSECRET") {
			t.Errorf("secret leaked in %q", issue.Message)
		}
	}
}

func TestRequiredMessagesIncludeTheDescription(t *testing.T) {
	got := messages(Run(mustSchema(t), map[string]string{"NODE_ENV": "test", "WORKERS": "1"}, Options{}))
	if got[0] != "error DATABASE_URL is required but not set (Where the app connects to Postgres.)" {
		t.Errorf("got %q", got)
	}
	// PORT has a default, so it's not required.
	for _, m := range got {
		if strings.Contains(m, "PORT") {
			t.Errorf("PORT has a default and shouldn't be reported: %q", m)
		}
	}
}

func TestUnknownKeysSuggestTheIntendedName(t *testing.T) {
	values := map[string]string{"DATABASE_URL": "https://db.example.com", "NODE_ENV": "test", "WORKERS": "2", "DATABSE_URL": "x", "WORKRES": "1", "TOTALLY_NEW": "1"}
	got := messages(Run(mustSchema(t), values, Options{}))
	want := []string{
		"warning DATABSE_URL is not in the schema; did you mean DATABASE_URL?",
		"warning TOTALLY_NEW is not in the schema",
		"warning WORKRES is not in the schema; did you mean WORKERS?",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q", got)
	}
	if n := len(Run(mustSchema(t), values, Options{IgnoreUnknown: true})); n != 0 {
		t.Errorf("IgnoreUnknown should hide them, got %d issues", n)
	}
}

func TestDistance(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{{"KITTEN", "SITTING", 3}, {"AB", "BA", 1}, {"", "ABC", 3}, {"SAME", "SAME", 0}} {
		if got := distance(tt.a, tt.b); got != tt.want {
			t.Errorf("distance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
