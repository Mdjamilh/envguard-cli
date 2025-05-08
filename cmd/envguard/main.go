// Command envguard checks environment variables against an annotated
// .env.example and scans projects for committed secrets.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Mdjamilh/envguard-cli/internal/check"
	"github.com/Mdjamilh/envguard-cli/internal/dotenv"
	"github.com/Mdjamilh/envguard-cli/internal/generate"
	"github.com/Mdjamilh/envguard-cli/internal/report"
	"github.com/Mdjamilh/envguard-cli/internal/scan"
	"github.com/Mdjamilh/envguard-cli/internal/schema"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

// Exit codes: problems found is distinct from envguard itself failing.
const (
	exitOK       = 0
	exitProblems = 1
	exitUsage    = 2
)

const usage = `envguard keeps environment variables honest.

Usage:
  envguard check  [--env .env] [--schema .env.example] [--process] [--strict]
  envguard diff   [--env .env] [--schema .env.example]
  envguard scan   [dir] [--no-git]
  envguard init   [--env .env] [--out .env.example] [--force]
  envguard types  [--schema .env.example] [--out env.d.ts]
  envguard version

Every command accepts --format text|json|github.
Exit codes: 0 all good, 1 problems found, 2 envguard couldn't run.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Environ()))
}

func run(args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	var err error
	code := exitOK
	switch cmd {
	case "check":
		code, err = runCheck(rest, stdout, environ)
	case "diff":
		code, err = runDiff(rest, stdout)
	case "scan":
		code, err = runScan(rest, stdout)
	case "init":
		code, err = runInit(rest, stdout)
	case "types":
		code, err = runTypes(rest, stdout)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "envguard %s\n", version)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
	default:
		err = usageError{fmt.Sprintf("unknown command %q", cmd)}
	}
	if err != nil {
		fmt.Fprintf(stderr, "envguard: %v\n", err)
		var u usageError
		if errors.As(err, &u) || errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stderr, "\n"+usage)
		}
		return exitUsage
	}
	return code
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// files collects repeated --env flags: later files override earlier ones,
// like .env followed by .env.local.
type files []string

func (f *files) String() string     { return strings.Join(*f, ",") }
func (f *files) Set(v string) error { *f = append(*f, v); return nil }

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "output format: text, json or github")
	return fs, format
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	// Allow flags after positional arguments ("scan ./app --no-git").
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return fs.Parse(append([]string{"--"}, positional...))
}

func readEnvFile(path string, lookup dotenv.Lookup) ([]dotenv.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := dotenv.Parse(f, lookup)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return entries, nil
}

func loadSchema(path string) (*schema.Schema, error) {
	entries, err := readEnvFile(path, nil)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("schema file %s not found; create one with `envguard init`", path)
		}
		return nil, err
	}
	s, err := schema.FromEntries(entries)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func environMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			m[k] = v
		}
	}
	return m
}

func runCheck(args []string, stdout io.Writer, environ []string) (int, error) {
	fs, format := newFlags("check")
	var envFiles files
	fs.Var(&envFiles, "env", "env file to check (repeatable; later files win)")
	schemaFile := fs.String("schema", ".env.example", "annotated example file")
	useProcess := fs.Bool("process", false, "also check the process environment (it overrides files)")
	strict := fs.Bool("strict", false, "treat warnings as errors")
	ignoreUnknown := fs.Bool("ignore-unknown", false, "don't warn about variables missing from the schema")
	if err := parseFlags(fs, args); err != nil {
		return 0, err
	}
	f, err := report.ParseFormat(*format)
	if err != nil {
		return 0, usageError{err.Error()}
	}
	s, err := loadSchema(*schemaFile)
	if err != nil {
		return 0, err
	}

	process := environMap(environ)
	lookup := func(k string) (string, bool) { v, ok := process[k]; return v, ok }
	values := map[string]string{}
	if len(envFiles) == 0 && !*useProcess {
		envFiles = files{".env"}
	}
	for _, path := range envFiles {
		entries, err := readEnvFile(path, lookup)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return 0, fmt.Errorf("%s not found (use --process to check the current environment instead)", path)
			}
			return 0, err
		}
		for k, v := range dotenv.Map(entries) {
			values[k] = v
		}
	}
	label := strings.Join(envFiles, " + ")
	if *useProcess {
		// The process environment has hundreds of unrelated variables, so
		// only the ones the schema knows about are taken from it.
		for _, v := range s.Vars {
			if value, ok := process[v.Name]; ok {
				values[v.Name] = value
			}
		}
		label = strings.TrimPrefix(label+" + environment", " + ")
	}

	issues := check.Run(s, values, check.Options{IgnoreUnknown: *ignoreUnknown})
	if err := report.New(stdout, f).Check(report.CheckResult{EnvFile: label, SchemaFile: *schemaFile, Variables: len(s.Vars), Issues: issues}); err != nil {
		return 0, err
	}
	for _, i := range issues {
		if i.Severity == check.Error || *strict {
			return exitProblems, nil
		}
	}
	return exitOK, nil
}

func runDiff(args []string, stdout io.Writer) (int, error) {
	fs, format := newFlags("diff")
	envFile := fs.String("env", ".env", "env file")
	schemaFile := fs.String("schema", ".env.example", "example file")
	if err := parseFlags(fs, args); err != nil {
		return 0, err
	}
	f, err := report.ParseFormat(*format)
	if err != nil {
		return 0, usageError{err.Error()}
	}
	env, err := readEnvFile(*envFile, nil)
	if err != nil {
		return 0, err
	}
	example, err := readEnvFile(*schemaFile, nil)
	if err != nil {
		return 0, err
	}
	d := generate.Diff(dotenv.Map(env), dotenv.Map(example))
	if err := report.New(stdout, f).Diff(*envFile, *schemaFile, d); err != nil {
		return 0, err
	}
	if !d.Empty() {
		return exitProblems, nil
	}
	return exitOK, nil
}

func runScan(args []string, stdout io.Writer) (int, error) {
	fs, format := newFlags("scan")
	noGit := fs.Bool("no-git", false, "skip checking for .env files committed to git")
	if err := parseFlags(fs, args); err != nil {
		return 0, err
	}
	f, err := report.ParseFormat(*format)
	if err != nil {
		return 0, usageError{err.Error()}
	}
	root := "."
	if fs.NArg() > 1 {
		return 0, usageError{"scan takes at most one directory"}
	} else if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", root)
	}

	findings, err := scan.Tree(root)
	if err != nil {
		return 0, err
	}
	var tracked []string
	if !*noGit {
		tracked, err = scan.TrackedEnvFiles(root)
		if err != nil && !errors.Is(err, scan.ErrNotGitRepo) {
			return 0, fmt.Errorf("checking git: %w (use --no-git to skip)", err)
		}
	}
	if err := report.New(stdout, f).Scan(report.ScanResult{Root: root, Findings: findings, TrackedEnvs: tracked}); err != nil {
		return 0, err
	}
	if len(findings) > 0 || len(tracked) > 0 {
		return exitProblems, nil
	}
	return exitOK, nil
}

func runInit(args []string, stdout io.Writer) (int, error) {
	fs, _ := newFlags("init")
	envFile := fs.String("env", ".env", "existing env file to document")
	out := fs.String("out", ".env.example", "file to write")
	force := fs.Bool("force", false, "overwrite the output file if it exists")
	if err := parseFlags(fs, args); err != nil {
		return 0, err
	}
	entries, err := readEnvFile(*envFile, nil)
	if err != nil {
		return 0, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if *force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	file, err := os.OpenFile(*out, flags, 0o644)
	if errors.Is(err, os.ErrExist) {
		return 0, fmt.Errorf("%s already exists (use --force to overwrite)", *out)
	} else if err != nil {
		return 0, err
	}
	defer file.Close()
	if _, err := io.WriteString(file, generate.Example(entries)); err != nil {
		return 0, err
	}
	fmt.Fprintf(stdout, "Wrote %s with %d variables. Review the inferred @type annotations, then commit it.\n", *out, len(entries))
	return exitOK, nil
}

func runTypes(args []string, stdout io.Writer) (int, error) {
	fs, _ := newFlags("types")
	schemaFile := fs.String("schema", ".env.example", "annotated example file")
	out := fs.String("out", "", "file to write (default: print to stdout)")
	if err := parseFlags(fs, args); err != nil {
		return 0, err
	}
	s, err := loadSchema(*schemaFile)
	if err != nil {
		return 0, err
	}
	ts := generate.TypeScript(s)
	if *out == "" {
		_, err = io.WriteString(stdout, ts)
		return exitOK, err
	}
	if err := os.WriteFile(*out, []byte(ts), 0o644); err != nil {
		return 0, err
	}
	fmt.Fprintf(stdout, "Wrote %s with %d variables.\n", *out, len(s.Vars))
	return exitOK, nil
}
