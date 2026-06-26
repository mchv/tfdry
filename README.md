# tfdry

Fast Terraform validator that works without `terraform init` or `terraform validate`.

Catches a focused set of errors by statically analysing `.tf` files in a directory — no provider downloads, no state, no network.

## Checks

| Code | Description |
|------|-------------|
| E001 | Invalid HCL syntax |
| E002 | Duplicate local definition |
| E003 | Reference to undefined local |
| E004 | Non-scalar local used in string interpolation |
| E005 | `count` and `for_each` used together on same resource/data/module block |
| E006 | Local module input type mismatch |
| E007 | Unknown local module input key |
| E008 | File not formatted (auto-fixable with `--fix`) |
| W001 | Local defined but never used (warning) |

E006 and E007 only apply to local modules (`source = "./..."` or `"../..."`) — remote modules are skipped because tfdry doesn't fetch them.

Scope: only `local.*` values defined in the same directory are resolved. `var.*`, `module.*`, and `data.*` references are skipped when their type is ambiguous — no false positives.

## Install

```sh
go install github.com/mchv/tfdry@latest
```

Or build from source:

```sh
git clone https://github.com/mchv/tfdry
cd tfdry
go build -o tfdry .
```

## Usage

```sh
# Check current directory
tfdry

# Check a specific directory
tfdry ./infra/prod

# JSON output (for CI / agents)
tfdry --json ./infra/prod

# Run specific checks only
tfdry --checks=E003,E004

# Auto-fix formatting violations (E008) — rewrites files in place
tfdry --fix

# Format a directory (drop-in replacement for `terraform fmt`)
tfdry fmt ./infra

# Format a single file
tfdry fmt ./infra/main.tf

# Check formatting without rewriting (exits 3 if any file is unformatted)
tfdry fmt -check ./infra
tfdry fmt -check ./infra/main.tf

# Format recursively, skipping hidden dirs (.terraform, .git, ...)
tfdry fmt -recursive ./infra

# List all checks
tfdry describe

# List checks as JSON
tfdry describe --json
```

`--fix` runs all checks and rewrites E008 violations as a side effect (lint-and-fix pass — like `eslint --fix`).

`fmt` is a focused, terraform-fmt-compatible alias that only formats. Use `fmt -check` in pre-commit hooks (exit 3 = needs formatting, exit 0 = clean).

## Exit codes

| Code | Meaning |
|------|---------|
| 0    | No violations |
| 1    | One or more lint violations found (E001-E008, excluding E000) |
| 2    | Tool error: bad arguments, unreadable directory, oversize file, write failure during `--fix` (E000 violations route here) |
| 3    | `tfdry fmt -check` found unformatted files |
| 130  | Interrupted by SIGINT / SIGTERM, or a context deadline expired |

Warnings (W001) do not affect the exit code. When both E000 (tool error) and other error-severity violations are present, exit 2 takes precedence — the tool couldn't run cleanly on all input, so the user needs the loud signal rather than the routine "lint found issues" code.

## JSON output

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
  "summary": { "errors": 1, "warnings": 0 }
}
```

## Pre-commit hook

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

## CI (GitHub Actions)

```yaml
- name: tfdry
  run: tfdry --json . | tee tfdry.json && jq -e '.summary.errors == 0' tfdry.json
```
