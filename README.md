# envguard

[![CI](https://github.com/Mdjamilh/envguard-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/Mdjamilh/envguard-cli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Mdjamilh/envguard-cli)](https://github.com/Mdjamilh/envguard-cli/releases)

Catch missing and broken environment variables before they reach production, and stop secrets from being committed.

envguard reads the `.env.example` your project probably already has. A few comments turn it into a schema:

```bash
# Where the app connects to Postgres.
# @type url @secret
DATABASE_URL=postgres://localhost:5432/app

# @type port @default 3000
PORT=3000

# @enum development, production, test
NODE_ENV=development
```

Then a single command tells you what's wrong:

```
$ envguard check
Checked .env against .env.example (10 variables)

  ✗ DATABASE_URL    is required but not set (Where the app connects to Postgres.)
  ✗ PORT            must be a port between 1 and 65535, got 80800
  ✗ NODE_ENV        must be one of development, production, test, got "Production" (values are case-sensitive: use "production")
  ✗ SESSION_SECRET  must be at least 32 characters long
  ! DATABSE_URL     is not in the schema; did you mean DATABASE_URL?

4 errors, 1 warning
```

It's a single binary with no dependencies, and it never prints the value of a variable marked `@secret`.

## Install

```bash
go install github.com/Mdjamilh/envguard-cli/cmd/envguard@latest
```

Or download a binary for Linux, macOS or Windows from the [releases page](https://github.com/Mdjamilh/envguard-cli/releases).

## Quick start

```bash
envguard init          # writes .env.example from your .env, with secrets blanked
# review the inferred @type lines, add @enum / @optional where needed
envguard check         # validate .env against it
envguard scan          # look for committed secrets
```

`init` infers types from your current values (`8080` becomes `@type int`, `https://...` becomes `@type url`). It blanks anything that looks secret, judging by the variable name or by matching a known token format. Its output always passes `envguard check` against the `.env` it came from.

## Annotations

Annotations go in the comment lines directly above a variable. Several can share a line. Every variable is **required** unless it's marked `@optional` or has a `@default`.

| Annotation | Meaning |
|---|---|
| `@type <t>` | `string` (default), `int`, `float`, `bool`, `url`, `email`, `port`, `duration` (`30s`, `5m`), `json` |
| `@enum a, b, c` | One of these exact values (case-sensitive) |
| `@min n` / `@max n` | Range for numbers, length for strings |
| `@pattern <regex>` | Must match the whole value; takes the rest of the line |
| `@optional` | May be unset or empty |
| `@default <value>` | Documents the fallback; implies optional |
| `@secret` | Never shown in output; blanked by `init` |
| `@deprecated <message>` | Warns when the variable is still set |

Any other comment lines become the variable's description, which is included in error messages and in generated types.

`url` means a full `scheme://host` URL. Go's own parser accepts `localhost:5432` as a URL with the scheme "localhost", which is a common mistake in `DATABASE_URL`, so envguard rejects it.

## Commands

| Command | What it does | Exit code 1 when |
|---|---|---|
| `check` | Validates values against the schema | Any error (or warning, with `--strict`) |
| `diff` | Lists keys that are in only one of `.env` and `.env.example` | The files differ |
| `scan [dir]` | Looks for secrets in files, and for `.env` files committed to git | Anything is found |
| `init` | Writes `.env.example` from `.env`. Won't overwrite without `--force` | Never |
| `types` | Prints or writes a TypeScript `env.d.ts` for `process.env` | Never |

Exit code 2 means envguard itself couldn't run: a bad flag, a missing file, or a schema error.

**`check` options**
- `--env` sets which file to check. It can be repeated (`--env .env --env .env.local`); later files win.
- `--process` checks the real environment instead of, or on top of, the files. Only variables named in the schema are read from it.
- `--strict` makes warnings fail the check.
- `--ignore-unknown` turns off the "not in the schema" warnings.

**Typed `process.env`**

`envguard types --out env.d.ts` writes a declaration file, so TypeScript knows which variables exist:

```ts
declare global {
  namespace NodeJS {
    interface ProcessEnv {
      /** Where the app connects to Postgres. Validated as url. */
      DATABASE_URL: string;
      /** Port the HTTP server listens on. Validated as port. Defaults to "3000". */
      PORT?: string;
      NODE_ENV: "development" | "production" | "test";
    }
  }
}
```

Enums become literal unions, so `process.env.NODE_ENV === "staging"` is a type error.

## In CI

Every command takes `--format json` for scripts, or `--format github` for annotations that appear on the right line of a pull request.

With GitHub Actions:

```yaml
- uses: Mdjamilh/envguard-cli@v0.1.0
  with:
    command: check
    args: --env .env.ci --strict

- uses: Mdjamilh/envguard-cli@v0.1.0
  with:
    command: scan
```

Before every commit, with [pre-commit](https://pre-commit.com):

```yaml
# .pre-commit-config.yaml
repos:
  - repo: https://github.com/Mdjamilh/envguard-cli
    rev: v0.1.0
    hooks:
      - id: envguard-scan
      - id: envguard-diff
```

Elsewhere, run the binary and rely on the exit code:

```bash
envguard check --process --format json   # in a container, before the app starts
```

## What `scan` looks for

- **Known token formats:** AWS access keys, GitHub, Slack, Stripe live keys, Google, OpenAI and Anthropic API keys, and private key blocks.
- **Passwords inside connection strings** (`postgres://app:...@db`). Local defaults such as `postgres:postgres` are skipped.
- **High-entropy values** assigned to names like `*_SECRET`, `*_TOKEN` and `*_PASSWORD`. Only token-like characters count, so code such as `token = getToken()` doesn't match.
- **`.env` files tracked by git.** Templates like `.env.example` and `.env.sample` are fine.

Matches are always shown redacted, for example `ghp_************`. To mark a false positive, add `envguard:ignore` on the same line. `node_modules`, `.git`, `vendor`, build output, binary files and files over 1 MB are skipped.

envguard runs `scan` on its own repository in CI. The first run flagged six lines: regular expressions, development connection strings, and fake tokens in tests. That shaped the rules above. The fake tokens now carry `envguard:ignore`.

## How it reads .env files

The parser follows the conventions most dotenv libraries share:

- an optional `export` prefix
- `#` comments, including after a value (`a#b` stays intact)
- single quotes are literal
- double quotes support `\n`, `\t`, `\"`, `\$` and values spanning several lines
- `${VAR}`, `$VAR` and `${VAR:-default}` expand from earlier lines, then from the environment

Errors give the line number, for example `line 12: unterminated " quote`.

## Development

```bash
go test ./...                                              # unit and end-to-end tests
go test ./internal/dotenv -run '^$' -fuzz FuzzParse        # fuzz the parser
golangci-lint run
```

Test coverage is about 91%. The end-to-end tests run the real commands in temporary projects, including a git repository with a committed `.env`. CI runs them with the race detector on Linux, macOS and Windows with two Go versions, and fuzzes the parser on every push.

```
cmd/envguard/        CLI: flags, commands, exit codes
internal/dotenv/     .env parser
internal/schema/     annotations → rules
internal/check/      validation, typo suggestions
internal/scan/       secret detection, git check
internal/generate/   init, diff and TypeScript output
internal/report/     text, JSON and GitHub output
```

## License

[MIT](LICENSE)
