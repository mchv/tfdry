# Changelog

All notable changes to tfdry are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

Each release entry groups changes under the following headings (omitted if empty):

- **Added** — new features, checks, flags, or output fields.
- **Changed** — behaviour or signature changes for existing features.
- **Deprecated** — features marked for removal in a future release.
- **Removed** — features removed in this release.
- **Fixed** — bug fixes.
- **Security** — vulnerability remediations (mirrored from `SECURITY.md` advisories).

## [Unreleased]

### Added

- `LICENSE` (Apache-2.0), `CONTRIBUTING.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md`, this `CHANGELOG.md`, and SPDX headers on every
  `.go` file in preparation for v0.1.0 public release.
- README badges: Go Reference, Go Report Card, Go version, License,
  Latest Release, CI status, codecov, govulncheck, Conventional
  Commits, Contributor Covenant, Terraform compatibility, and a custom
  `SKILL.md` link.
- **CI workflows** (`.github/workflows/`):
  - `ci.yml` — runs `make verify` on every PR + main push across
    Linux, macOS, and Windows runners with Go 1.26.3.
  - `codeql.yml` — CodeQL security analysis with the
    `security-extended` query pack, on every PR + weekly schedule.
  - `govulncheck.yml` — daily scheduled vulnerability scan against
    `vuln.go.dev` to catch CVE drift in dependencies between PRs.
- **Release workflow** (`.github/workflows/release.yml`):
  - Triggered by `v*.*.*` tag pushes.
  - Uses goreleaser v2 to build `darwin-arm64`, `linux-amd64`,
    `linux-arm64`, and `windows-amd64` binaries with version
    injected via `-ldflags`.
  - Signs every archive and the `checksums.txt` with cosign in
    keyless mode (OIDC identity, no key management).
  - Generates a Syft SBOM (SPDX JSON) per archive.
  - Auto-commits an updated Homebrew cask formula to the
    `mchv/homebrew-tfdry` tap on every release.
- **Dependabot** (`.github/dependabot.yml`) — weekly updates for Go
  module dependencies and GitHub Actions versions. Commits land as
  `build(gomod): bump <pkg> from <a> to <b>` (Go modules ecosystem) or
  `build(github-actions): bump <action> from <a> to <b>` (GitHub
  Actions ecosystem) — the conventional-commit scope makes the source
  ecosystem visible at a glance. Both are excluded from goreleaser's
  release-notes via the `^build(\(.+\))?:` exclude regex.
- **Pinned tool versions** in the Makefile so `make tools` produces
  reproducible builds: `gofumpt@v0.10.0`, `golangci-lint@v2.12.2`,
  `govulncheck@v1.4.0`. Dependabot's `gomod` ecosystem can't track
  Makefile variables (it only watches `go.mod` / `go.sum`), so these
  pins are bumped manually during release-prep — usually a one-line
  edit per tool.

### Changed

- README "Install" section now leads with the Homebrew tap install
  path alongside `go install` and a "download a signed binary"
  pointer to GitHub Releases.

## [0.1.0] — TBD

First public release. The sections below summarise the surface that
shipped; for the per-PR breakdown see the merged PRs in the
`mchv/tfdry` repository (#1 through #6 covered the v0.1.0 prep work).

### Added

- **Lint checks** (`tfdry [dir]`, all toggleable via `--checks=`):
  - `E001` — invalid HCL syntax.
  - `E002` — duplicate local definition.
  - `E003` — reference to an undefined local.
  - `E004` — type-mismatched interpolation
    (e.g. `local.tags` is `object`, used where `string` expected).
  - `E005` — `count` and `for_each` used together on the same
    `resource` / `data` / `module` block.
  - `E006` — module input type mismatch
    (for relative-path modules tfdry can resolve).
  - `E007` — unknown input key for a relative-path module.
  - `E008` — file is not formatted (matches `terraform fmt` parity).
  - `W001` — local declared but never referenced.
- **Tool-error code** — `E000` is emitted by tfdry itself when it
  cannot operate on the input (unreadable directory, oversize file
  >10 MiB, write failure during `--fix`). Always enabled (not
  toggleable via `--checks=`) and routes to exit `2`.
- **`fmt` subcommand** (`tfdry fmt [path]`): drop-in replacement for
  `terraform fmt`. Supports directory and single-file modes,
  `-recursive`, and `-check` (exit 3 on dirt, no rewrite).
- **`--fix` flag**: rewrites unformatted files to fix `E008` while
  leaving every other check read-only.
- **`--checks=` filter**: additive enable list, e.g.
  `--checks=E003,E004`. Disabled checks are skipped at the per-file
  loop level so filtering improves runtime, not just output.
- **`--json` output**: machine-readable JSON with `tfdry_version`,
  `directory`, `violations[]`, and `summary` (errors, warnings,
  `tool_errors`). The JSON shape is the stable machine-consumption
  contract — see `README.md` for the schema.
- **`describe` subcommand**: enumerates all check codes with their
  severities and one-line descriptions. Supports `--json`.
- **`context.Context` API**: every long-running public entry point in
  the `checker` package takes a `ctx` as its first parameter and
  honours cancellation at per-file checkpoints. `main()` wires
  `signal.NotifyContext` so SIGINT / SIGTERM cleanly stops the
  current pass at the next file boundary.
- **Atomic `--fix` rewrites**: uses `CreateTemp` + `Rename` with
  symlink-rejection and TOCTOU defence-in-depth, so a power loss or
  SIGKILL mid-write never leaves a half-written file on disk.
- **Trojan Source / terminal-injection sanitisation**: filenames and
  HCL diagnostic text are stripped of ANSI escapes,
  Bidi-override / isolate-control characters (Unicode Cf category),
  and embedded newlines / tabs before reaching stdout, stderr, or
  the JSON output's `directory` field. Mitigates CVE-2021-42574-class
  attacks via malicious `.tf` file names or content.
- **GitHub issue and PR templates** (`.github/ISSUE_TEMPLATE/`,
  `.github/PULL_REQUEST_TEMPLATE.md`).

### Exit codes

| Code | Meaning |
|------|---------|
| `0`   | No violations (or all violations fixed via `--fix`). |
| `1`   | One or more lint violations found (E001–E008, excluding E000). |
| `2`   | Tool error: bad arguments, unreadable directory, oversize file, write failure during `--fix`. E000 violations route here. Takes precedence over exit 1 when both are present. |
| `3`   | `tfdry fmt -check` found unformatted files. |
| `130` | Interrupted by SIGINT / SIGTERM, or a context deadline expired. |

### Tooling

- **`make verify`** runs the full pre-PR pipeline: `gofumpt -l .`,
  `go mod tidy -diff` (asserts go.mod / go.sum stay canonical),
  `go vet`, `golangci-lint run` (with 11 linters), `make lint-prose`
  (`misspell -locale UK` against `README.md`, `CHANGELOG.md`, the
  other root `.md` docs, `Makefile`, `.github/workflows/*.yml`
  except `codeql.yml`, `.github/dependabot.yml`, and
  `.goreleaser.yaml`), `go test -race`, `govulncheck`, cross-builds
  for `darwin-arm64`, `linux-amd64`, `linux-arm64`, `windows-amd64`,
  plus a marker-policy check that refuses `C##` / `G##`
  review-finding markers in `.go` source.
- **`.golangci.yml`** with `staticcheck`, `errcheck`, `gosec`,
  `revive`, `gocritic`, `unconvert`, `unused`, `ineffassign`,
  `misspell` (UK locale), `noctx`, and `unparam`.

### Supported platforms

- `darwin-arm64` — primary development target.
- `linux-amd64` — primary deployment target.
- `linux-arm64` — secondary deployment target.
- `windows-amd64` — best-effort. The atomic-rewrite symlink rejection
  degrades to "post-open IsRegular check" because Windows doesn't
  honour POSIX `O_NOFOLLOW`. See `checker/nofollow_windows.go`.

[Unreleased]: https://github.com/mchv/tfdry/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/mchv/tfdry/releases/tag/v0.1.0
