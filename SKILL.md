# tfdry SKILL.md

Agent-specific invariants for using tfdry correctly.

## What tfdry does

tfdry validates and optionally formats Terraform `.tf` files in a directory without running `terraform init` or `terraform validate`. It catches a focused set of errors that are statically resolvable from the source files alone.

## Invariants

- **tfdry never modifies files unless `--fix` is passed.** Without `--fix` it is purely read-only.
- **`--fix` only rewrites files to fix formatting (E008).** It never modifies files for any other check.
- **Exit codes are strict:**
  - `0` — no violations found (or all violations were fixed by `--fix`)
  - `1` — one or more violations found
  - `2` — tool error (bad arguments, unreadable directory, etc.)
  - `3` — `tfdry fmt -check` found unformatted files
- **Always use `--json` for machine consumption.** Human output format is not stable.
- **Use `tfdry describe` to enumerate check codes** before filtering with `--checks`.
- **`--checks` filters are additive.** Passing `--checks=E003,E004` runs only those two checks.
- **Warnings (W001) do not affect exit code.** Only errors (E0xx) cause exit 1.

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
  "summary": { "errors": 1, "warnings": 0 }
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

- All path arguments are validated. Path traversal attempts are rejected.
- tfdry does not make network requests.
- tfdry does not execute any Terraform code.
