# tfdry

> Fast, focused Terraform linting — no `terraform init`, no state, no network.

[![Go Reference](https://pkg.go.dev/badge/github.com/mchv/tfdry.svg)](https://pkg.go.dev/github.com/mchv/tfdry)
[![Go Report Card](https://goreportcard.com/badge/github.com/mchv/tfdry)](https://goreportcard.com/report/github.com/mchv/tfdry)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mchv/tfdry)](go.mod)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)
[![Latest Release](https://img.shields.io/github/v/release/mchv/tfdry?include_prereleases&sort=semver)](https://github.com/mchv/tfdry/releases)
[![CI](https://github.com/mchv/tfdry/actions/workflows/ci.yml/badge.svg)](https://github.com/mchv/tfdry/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/mchv/tfdry/graph/badge.svg)](https://codecov.io/gh/mchv/tfdry)
[![govulncheck](https://github.com/mchv/tfdry/actions/workflows/govulncheck.yml/badge.svg)](https://github.com/mchv/tfdry/actions/workflows/govulncheck.yml)
[![Conventional Commits](https://img.shields.io/badge/conventional%20commits-1.0.0-orange)](https://www.conventionalcommits.org)
[![Contributor Covenant](https://img.shields.io/badge/contributor%20covenant-2.1-blueviolet)](CODE_OF_CONDUCT.md)
[![Terraform 1.x](https://img.shields.io/badge/terraform-1.x-purple)](https://www.terraform.io)
[![Skill](https://img.shields.io/badge/skill-SKILL.md-darkgreen)](SKILL.md)

`tfdry` catches a focused set of errors by statically analysing `.tf` files
in a directory. No provider downloads, no state, no network — runs in
milliseconds on a typical Terraform module and integrates cleanly into
pre-commit hooks, CI pipelines, and editor integrations.

---

## Why tfdry?

- **Fast.** Pure static analysis: ~10× quicker than `terraform validate`
  on a typical module because there's no provider download or state lookup.
- **Focused.** Nine deterministic checks (E001–E008 + W001) that catch
  bugs Terraform's own validator misses (undefined locals, type-mismatched
  interpolations, circular references) plus `terraform fmt`-parity
  formatting. No opinionated style nags.
- **Agent-friendly.** Ships with a [`SKILL.md`](SKILL.md) describing the
  CLI surface, exit-code contract, and JSON schema in the convention AI
  coding agents expect. `--json` output is the stable machine-consumption
  contract.

## Quick start

```sh
# Install
go install github.com/mchv/tfdry@latest

# Lint the current directory
tfdry .

# Auto-fix formatting violations (E008 only — every other check stays read-only)
tfdry --fix .

# Format like `terraform fmt`
tfdry fmt .

# Machine-readable output for CI / agents
tfdry --json . | jq '.violations[] | select(.severity == "error")'

# Run only specific checks
tfdry --checks=E003,E004 .

# List available checks
tfdry describe
```

Sample output on a directory with one undefined local reference:

```text
infra/main.tf:12  error  E003  reference to undefined local "tags"

1 error(s) found.
```

Same input with `--json`:

```json
{
  "tfdry_version": "0.1.0",
  "directory": "./infra",
  "violations": [
    {
      "code": "E003",
      "severity": "error",
      "file": "main.tf",
      "line": 12,
      "message": "reference to undefined local \"tags\""
    }
  ],
  "summary": { "errors": 1, "warnings": 0, "tool_errors": 0 }
}
```

## Checks

| Code  | Severity | Description |
|-------|----------|-------------|
| E000  | error    | Tool/infrastructure failure: unreadable directory, oversize file (>10 MiB), write failure during `--fix`. Routes to exit 2. |
| E001  | error    | Invalid HCL syntax. |
| E002  | error    | Duplicate `locals` definition within the same directory. |
| E003  | error    | Reference to an undefined local. |
| E004  | error    | Non-scalar local (object, list, map, set) used in a string interpolation context. |
| E005  | error    | `count` and `for_each` used together on the same `resource` / `data` / `module` block. |
| E006  | error    | Module input type mismatch (relative-path modules only — remote modules aren't fetched). |
| E007  | error    | Unknown input key for a relative-path module. |
| E008  | error    | File is not formatted (`terraform fmt` parity, auto-fixable with `--fix`). |
| W001  | warning  | Local defined but never referenced. |

**Scope limits:**

- E006 and E007 apply only to relative-path modules (`source = "./..."` or `"../..."`). Remote modules and registry modules are deliberately skipped — tfdry doesn't fetch them.
- Only `local.*` values defined in the same directory are resolved. `var.*`, `module.*`, and `data.*` references are skipped when their type is ambiguous — chosen as a no-false-positives default.

Run `tfdry describe` for the canonical list at runtime; the table above mirrors what the CLI reports.

## Usage reference

### CLI flags

```text
tfdry [flags] [directory]
tfdry fmt [-check] [-recursive] [path]
tfdry describe [--json]

Flags:
  --checks=CODES   Comma-separated allow-list of check codes (e.g. E003,E004).
                   Repeatable; flags accumulate. Default: all checks.
  --fix            Rewrite files in place to fix E008 (formatting). Every
                   other check stays read-only.
  --json           Machine-readable JSON output. Schema below.
```

The `fmt` subcommand is a drop-in `terraform fmt` replacement:
- Takes either a directory or a single file path.
- `-check` reads only; exits 3 if any file would be rewritten.
- `-recursive` walks subdirectories, skipping hidden dirs (`.terraform`, `.git`, …).

The `describe` subcommand prints the check table to stdout (or JSON with `--json`) and exits 0 — useful for building IDE integrations or `--checks=` allow-lists.

### Exit codes

| Code  | Meaning |
|-------|---------|
| `0`   | No violations (or all violations fixed by `--fix`). |
| `1`   | One or more lint violations found (E001–E008, excluding E000). |
| `2`   | Tool error: bad arguments, unreadable directory, oversize file, write failure during `--fix`. **E000 violations route here, taking precedence over exit 1 when both are present** — the tool couldn't run cleanly on all input, so the loud signal is more useful than the routine "lint found issues" code. |
| `3`   | `tfdry fmt -check` found unformatted files. |
| `130` | Interrupted by SIGINT / SIGTERM, or a context deadline expired. |

Warnings (W001) do not affect the exit code.

### JSON output schema

The `--json` flag produces a single JSON object — the **stable machine-consumption contract**. Human output may change between minor versions; JSON is versioned with the CLI.

```json
{
  "tfdry_version": "0.1.0",
  "directory": "./infra",
  "violations": [
    {
      "code": "E004",
      "severity": "error",
      "file": "main.tf",
      "line": 12,
      "message": "local.tags is object, used where string expected in interpolation"
    }
  ],
  "summary": {
    "errors": 1,
    "warnings": 0,
    "tool_errors": 0
  }
}
```

| Field | Type | Notes |
|-------|------|-------|
| `tfdry_version` | string | Semver of the binary that produced the output. |
| `directory` | string | The directory tfdry analysed (sanitised — control characters, ANSI escapes, and Bidi-override codepoints are stripped). |
| `violations[]` | array | One object per violation, ordered by `file` then `line`. |
| `violations[].code` | string | E000–E008 or W001. |
| `violations[].severity` | string | `"error"` or `"warning"`. |
| `violations[].file` | string | Filename relative to `directory` (sanitised). |
| `violations[].line` | integer | 1-based line number, or `0` if the violation is file-level. |
| `violations[].message` | string | Human-readable detail (sanitised). |
| `summary.errors` | integer | Count of `severity == "error"` violations (includes E000 for back-compat). |
| `summary.warnings` | integer | Count of `severity == "warning"` violations. |
| `summary.tool_errors` | integer | Count of E000 violations specifically. Drives exit code 2 when `> 0`. |

## Integrations

### Pre-commit hook

```yaml
# .pre-commit-config.yaml
repos:
  - repo: local
    hooks:
      - id: tfdry
        name: tfdry
        entry: tfdry --json
        language: system
        files: \.tf$
        pass_filenames: false
```

### GitHub Actions

```yaml
- name: tfdry
  run: |
    tfdry --json . | tee tfdry.json
    jq -e '.summary.errors == 0' tfdry.json
```

### Other CI

Any pipeline runner can consume tfdry's exit codes:

```sh
tfdry --json infra/ > tfdry.json
case $? in
  0) echo "✓ clean" ;;
  1) echo "✗ lint violations found"; jq '.violations[]' tfdry.json; exit 1 ;;
  2) echo "✗ tool error — check stderr"; exit 2 ;;
  3) echo "(not reachable without -check)"; exit 3 ;;
  *) echo "✗ unexpected exit $?"; exit 1 ;;
esac
```

### AI coding agents

Agents that read [`SKILL.md`](SKILL.md) get the CLI surface, exit-code contract, and JSON schema in a structured format. The `--json` output is designed to be consumed without further parsing — `severity`, `code`, `file`, `line`, and `message` are all separately indexable.

## Project status

tfdry is at **v0.1.0** — the first public release. The API and CLI surface are stable enough for production use, but pre-1.0 means breaking changes can land in a minor version if the rationale is documented in [`CHANGELOG.md`](CHANGELOG.md).

Supported platforms: `darwin-arm64`, `linux-amd64`, `linux-arm64`, `windows-amd64`.

## Documentation

- [`SKILL.md`](SKILL.md) — concise machine-consumable reference for AI agents and integrations.
- [`CHANGELOG.md`](CHANGELOG.md) — per-version history of additions, changes, fixes, and security advisories.
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — how to set up a dev environment, the test-first protocol, code-style conventions.
- [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) — Contributor Covenant 2.1.
- [`SECURITY.md`](SECURITY.md) — how to report a vulnerability privately.

## License

[Apache License 2.0](LICENSE). © 2026 Mariot Chauvin.
