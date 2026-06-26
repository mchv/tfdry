# Contributing to tfdry

Thanks for considering a contribution. This document captures the
working conventions of the project so a first-time contribution has
the same shape as work that lands on `main`. Every convention here
is enforced by `make verify` (locally) and by CI (PR B1 onward), so
following these rules means your PR is more likely to land on the
first review pass.

## Quick start

```sh
git clone https://github.com/mchv/tfdry.git
cd tfdry
make tools     # one-shot install of dev tools (gofumpt, golangci-lint, govulncheck)
make verify    # runs the full pre-PR pipeline
```

If `make verify` passes locally, the same checks will pass in CI.

## How to file an issue

Use the [Issues tab][issues]. There are two templates:

- **Bug report** — please include the tfdry version, the OS/arch, a
  minimal `.tf` fixture that reproduces the issue, and the actual vs.
  expected output. The fixture matters more than prose; "tfdry crashes
  on my real codebase" is hard to triage without a minimal repro.
- **Feature request** — describe the use case, not just the
  implementation. A check code idea like "we should add E009 for X"
  is easier to evaluate when it includes a Terraform pattern that
  E009 would catch.

**Security issues do NOT go through the public issue tracker** —
see [`SECURITY.md`](SECURITY.md) for the private disclosure flow.

[issues]: https://github.com/mchv/tfdry/issues

## Pull request workflow

### Test-first protocol

Every behaviour change must be accompanied by a test that **fails on
the current code and passes on your change**. The full sequence:

1. Write the failing test first. Verify it actually fails by running
   `go test ./...` on `main`. The error message should describe the
   bug or missing feature in user-facing terms.
2. Apply the fix.
3. Verify the test now passes.
4. Run `make verify` end-to-end.

For pure refactors (no observable behaviour change), the existing
test suite is the regression guard — explicitly call this out in the
commit message and PR description rather than skipping the
test-first step silently. Reviewers expect to see either a new test
or a "no behaviour change" justification.

### Branch + commit conventions

- **Branch name**: `feat/`, `fix/`, `docs/`, `build/`, `refactor/`,
  or `chore/` prefix, then a short kebab-case description. Example:
  `feat/context-api-sweep`, `fix/e000-exit-code-routing`.
- **Commit message subject**: ≤72 characters, lowercase verb prefix
  (the same prefixes as the branch). Examples from this repository:
  - `feat: thread context.Context through public API for v0.1.0`
  - `fix: route E000 to exit 2 (tool error) per documented CLI contract`
  - `docs: scrub C##/G## review markers from production code`
- **Commit message body**: hard-wrap at 72 columns. Explain *why*,
  not *what* — the diff already shows what. Reference the PR's
  test plan, similar-code audit, and any decisions you flagged for
  the user.
- **Atomic commits**: each commit should leave the tree in a state
  where `make verify` passes. Don't squash multiple logical changes
  into one commit; don't split a single logical change across two
  commits that each fail on their own.

### Reply convention on review

When a bot or human leaves a review comment:

- **Assess validity** first: is the finding accurate? Sometimes a bot
  reasons from outdated documentation (e.g. older Go versions' build
  constraint rules); call those out empirically rather than applying
  the suggestion blindly.
- **Reply on the thread** with: (a) verdict, (b) commit SHA where the
  fix landed (if valid), (c) a similar-code audit summary if the same
  pattern exists elsewhere.
- **One commit per review round** consolidating all the round's
  fixes is preferable to one commit per finding, unless the findings
  are genuinely independent.

## Style and tooling

### Code style

- **`gofumpt`** is the source of truth for formatting. `make fmt`
  applies it; `make fmt-check` verifies it (used by `make verify`).
- **`golangci-lint`** with the 11 linters in `.golangci.yml` runs as
  part of `make verify`. Don't disable rules in `.golangci.yml` to
  silence a finding — fix the finding, or add an inline
  `//nolint:linterName // rationale` if the finding is a genuine
  false positive (the rationale comment is mandatory and enforced by
  `gocritic`'s `whyNoLint`).
- **British English** in prose comments (cancelled, behaviour, honour,
  recognise, neighbour). The `misspell` linter is configured with
  `locale: UK` to enforce this. US-spelled Go identifiers we don't
  control (e.g. `context.Canceled`, `output.Sanitize`) stay as-is
  via an `ignore-rules` list.
- **No C##/G## review markers in `.go` source.** The marker policy
  is enforced by `make check-no-markers` (part of `make verify`). If
  you need to reference a historical review finding, put it in the
  commit message body or PR description, not in a code comment.

### Test conventions

- All tests use `t.Parallel()` unless there's a documented reason
  not to (resource contention, environment mutation).
- Test fixture files live in the test temp dir from `t.TempDir()`,
  which Go auto-cleans up. Avoid writing to the repository tree.
- Subprocess tests that need a built binary use the `tfdryBin`
  helper in `sigint_test.go` — `sync.Once` builds the binary once
  per test-process invocation.
- Tests that depend on POSIX file permissions (e.g. `os.Chmod(0o000)`
  to force a read failure) go in a `*_unix_test.go` file with a
  `//go:build unix` constraint. Don't rely on runtime `t.Skip` for
  platform-conditional compilation — the file is compiled regardless,
  so a `syscall.SysProcAttr.Setpgid` etc. needs to be excluded at
  compile time.

### Cross-platform

tfdry targets darwin-arm64, linux-amd64, linux-arm64, and
windows-amd64. `make verify` cross-builds for all four. Don't import
packages that aren't available on one of these targets without a
build constraint.

## Decision flagging

If you're unsure about a design choice mid-PR (e.g. should this
return a slice or a channel? should the new flag be `--foo` or
`-f`?), surface the decision in the PR description rather than
making the call silently. Bot reviewers (Gemini, Copilot) and human
reviewers will spend less time on a PR that includes a "Decisions I
made and would like your view on" section.

## Code of conduct

This project follows the [Contributor Covenant 2.1][CoC]. By
participating you agree to abide by its terms.

[CoC]: CODE_OF_CONDUCT.md
