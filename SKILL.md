# tfdry SKILL.md

Agent-specific invariants for using tfdry correctly.

## What tfdry does

tfdry validates and optionally formats Terraform `.tf` files in a directory without running `terraform init` or `terraform validate`. It catches a focused set of errors that are statically resolvable from the source files alone.

## Invariants

- **The lint path is purely read-only.** `tfdry [dir]` and `tfdry --json [dir]` never modify files.
- **Only two flows write to disk**, both opt-in and obvious from the command:
  - `tfdry --fix [dir]` — rewrites files to fix formatting (E008) only. Never modifies files for any other check.
  - `tfdry fmt [path]` — rewrites unformatted files in place (default), unless `-check` is passed (read-only, exit 3 on dirt).
- **Exit codes are strict:**
  - `0` — no violations found (or all violations were fixed by `--fix`)
  - `1` — one or more lint violations found (E001-E008, excluding E000)
  - `2` — tool error (bad arguments, unreadable directory, oversize file, write failure during `--fix`); E000 violations route here. Takes precedence over exit 1 when both are present.
  - `3` — `tfdry fmt -check` found unformatted files
  - `130` — interrupted by SIGINT / SIGTERM, or a context deadline expired
- **Always use `--json` for machine consumption.** Human output format is not stable.
- **Use `tfdry describe` to enumerate check codes** before filtering with `--checks`.
- **`--checks` filters are additive.** Passing `--checks=E003,E004` runs only those two checks.
- **Warnings (W001) do not affect exit code.** Only errors (E001-E008) cause exit 1; E000 maps to exit 2 (tool error).

## Checks

| Code | Severity | Description |
|------|----------|-------------|
| E001 | error    | Invalid HCL syntax |
| E002 | error    | Duplicate local definition |
| E003 | error    | Reference to undefined local |
| E004 | error    | Non-scalar local used in string interpolation |
| E005 | error    | `count` and `for_each` used together on same resource/data/module block |
| E006 | error    | Local module input type mismatch |
| E007 | error    | Unknown local module input key |
| E008 | error    | File not formatted (equivalent to `terraform fmt --check`) |
| W001 | warning  | Local defined but never used |

## Scope limitations

tfdry only resolves `local.*` values defined in the same directory. It does **not** resolve:
- `var.*` (unless type is inferrable from a literal default)
- `module.*` outputs
- `data.*` source attributes

When a value's type cannot be resolved statically, the check is **skipped** (no false positives).

## JSON output shape

```json
{
  "tfdry_version": "0.1.0",
  "directory": "/path/to/tf",
  "violations": [
    {
      "code": "E004",
      "severity": "error",
      "file": "main.tf",
      "line": 12,
      "message": "local.tags is object, used where string expected in interpolation"
    }
  ],
  "summary": { "errors": 1, "warnings": 0, "tool_errors": 0 }
}
```

## Usage patterns

```sh
# Check current directory
tfdry

# Check specific directory, JSON output
tfdry --json ./infra/prod

# Run only type-mismatch and undefined-local checks
tfdry --checks=E003,E004 ./infra

# Check formatting only (fast)
tfdry --checks=E008

# Fix formatting in place, report remaining violations
tfdry --fix ./infra

# List all checks
tfdry describe

# List checks as JSON
tfdry describe --json
```

## Security

- tfdry does not execute Terraform code. It parses `.tf` files with hclsyntax/hclwrite — no `terraform validate`, no plan/apply, no module install.
- tfdry makes no network requests.
- Symlinked path arguments to `tfdry fmt` are rejected. On Unix-like systems, symlinked `.tf` files inside a scanned directory are also skipped via `O_NOFOLLOW`. **Windows is best-effort**: without `O_NOFOLLOW` the symlink-to-regular-file case is silently followed (symlinks pointing to directories or devices are still rejected by a post-open `IsRegular` check). Both behaviours aim to prevent surprising file rewrites through symlinks.
- Output fields (filenames, local names, error messages) are sanitized for ANSI escape sequences and Unicode bidi-override / isolate-control characters before writing to stdout/JSON, mitigating terminal-injection attacks via crafted `.tf` content.
- File reads are capped at 10 MiB per `.tf` file; oversized files are skipped with an `E000` violation.
- tfdry does **not** sandbox path arguments. Relative paths (`./infra`, `../shared`), absolute paths, and module `source = "../foo"` references are accepted as given (matching terraform's behaviour). Run tfdry inside the directory or container scope you intend to validate.
