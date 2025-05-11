package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test tokens are assembled at runtime so this file doesn't trip secret
// scanners itself (including GitHub's push protection).
func fake(prefix string, n int) string { return prefix + strings.Repeat("A1b2C3", n)[:n] }

func TestKnownTokenFormats(t *testing.T) {
	tests := []struct {
		line, rule string
	}{
		{"aws_key = " + "AKIA" + "IOSFODNN7EXAMPLE", "AWS access key"},
		{"token: " + fake("ghp_", 36), "GitHub token"},
		{"SLACK=" + fake("xoxb-", 24), "Slack token"},
		{"stripe " + fake("sk_live_", 24), "Stripe live key"},
		{"key=" + fake("AIza", 35), "Google API key"},
		{"ANTHROPIC=" + fake("sk-ant-", 40), "Anthropic API key"},
		{"OPENAI=" + fake("sk-proj-", 40), "OpenAI API key"},
		{"-----BEGIN RSA " + "PRIVATE KEY-----", "Private key"},
	}
	for _, tt := range tests {
		found := Line("f", 1, tt.line)
		if len(found) != 1 || found[0].Rule != tt.rule {
			t.Errorf("%q: got %+v, want one %q", tt.line, found, tt.rule)
			continue
		}
		if strings.Contains(found[0].Match, "A1b2C3A1b2") || !strings.Contains(found[0].Match, "*") {
			t.Errorf("%q: match should be redacted, got %q", tt.line, found[0].Match)
		}
	}
}

func TestHighEntropyAssignments(t *testing.T) {
	secret := "q8Zr3Kp1Lw9Xv2Ty7Nb4"
	if got := Line("app.env", 3, "SESSION_SECRET="+secret); len(got) != 1 || got[0].Rule != "High-entropy value for SESSION_SECRET" || got[0].Column != 16 || got[0].Line != 3 {
		t.Errorf("got %+v", got)
	}
	if got := Line("config.yml", 1, `  db_password: "`+secret+`"`); len(got) != 1 {
		t.Errorf("yaml style: got %+v", got)
	}
	// Inside a Go string, "\n" before the name isn't part of it.
	if got := Line("main_test.go", 1, `".env": "PORT=1\nAPI_TOKEN=`+secret+`"`); len(got) != 1 || got[0].Rule != "High-entropy value for API_TOKEN" {
		t.Errorf("escaped newline: got %+v", got)
	}
}

func TestPasswordsInConnectionStrings(t *testing.T) {
	got := Line(".env", 1, "DATABASE_URL=postgres://app:"+"s3cr3t-pa55"+"@db.internal:5432/app")
	if len(got) != 1 || got[0].Rule != "Password in URL" || got[0].Match != "s3cr*******" {
		t.Errorf("got %+v", got)
	}
	for _, line := range []string{
		"DATABASE_URL=postgres://app:${DB_PASSWORD}@db/app",
		"URL=postgres://app@db/app",
		"URL=https://user:changeme@example.com",
		"URL=postgres://postgres:postgres@localhost:5432/dev", // password equals user
		"URL=mysql://root:root@127.0.0.1/app",
		"URL=postgres://app:password@localhost/app", // common dev default
		"URL=redis://u:pw@cache:6379",               // too short to be a real secret
	} {
		if found := Line(".env", 1, line); len(found) != 0 {
			t.Errorf("%q: unexpected %+v", line, found)
		}
	}
}

func TestNoFalsePositivesOnPlaceholdersAndPlainText(t *testing.T) {
	for _, line := range []string{
		"API_KEY=",
		"API_KEY=changeme",
		"SECRET_KEY=your-secret-key-here",
		"API_TOKEN=${API_TOKEN}",
		"PASSWORD=<password>",
		"PASSWORD=aaaaaaaaaaaaaaaaaaaa", // long but not random
		"TOKEN_URL=https://example.com/oauth/token",
		"// The token is refreshed every hour",
		"const key = process.env.API_KEY;",
		"var secretName = regexp.MustCompile(`(?i)(SECRET|TOKEN)`)", // code, not a value
		`token := getToken(ctx, "refresh")`,
		"AWS_SECRET=" + "AKIA" + "IOSFODNN7EXAMPLE # envguard:ignore",
	} {
		if got := Line("f", 1, line); len(got) != 0 {
			t.Errorf("%q: unexpected %+v", line, got)
		}
	}
}

func TestTreeSkipsDependenciesAndBinaries(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	token := fake("ghp_", 36)
	write("src/config.ts", "export const ok = 1;\nconst t = '"+token+"';\n")
	write("node_modules/pkg/index.js", "x='"+token+"'")
	write(".git/config", token)
	write("image.png", "\x89PNG\x00\x00"+token)

	found, err := Tree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].File != "src/config.ts" || found[0].Line != 2 || found[0].Column != 12 {
		t.Errorf("got %+v", found)
	}
}

func TestEntropy(t *testing.T) {
	if e := entropy("aaaa"); e != 0 {
		t.Errorf("entropy(aaaa) = %v", e)
	}
	if e := entropy("abcd"); e != 2 {
		t.Errorf("entropy(abcd) = %v", e)
	}
}
