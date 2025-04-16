package scan

import (
	"bytes"
	"errors"
	"os/exec"
	"path"
	"strings"
)

// ErrNotGitRepo means the directory isn't inside a git repository.
var ErrNotGitRepo = errors.New("not a git repository")

// TrackedEnvFiles lists .env files that git is tracking, which almost always
// means real configuration was committed. Templates (.env.example,
// .env.sample, .env.template, .env.dist) are fine and not listed.
func TrackedEnvFiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return nil, ErrNotGitRepo
		}
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" && IsEnvFile(f) {
			files = append(files, f)
		}
	}
	return files, nil
}

// IsEnvFile reports whether a path looks like real env configuration:
// .env, .env.local, .env.production, prod.env and so on, but not templates.
func IsEnvFile(p string) bool {
	name := strings.ToLower(path.Base(p))
	if name != ".env" && !strings.HasPrefix(name, ".env.") && !strings.HasSuffix(name, ".env") {
		return false
	}
	for _, template := range []string{"example", "sample", "template", "dist", "defaults", "schema"} {
		if strings.Contains(name, template) {
			return false
		}
	}
	return true
}
