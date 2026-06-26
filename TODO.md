# TODO

This file tracks two things:

1. **v0.1.0 scope** — work committed for the first public release. Each
   item maps to a PR or a phase of the v0.1.0 plan; entries are removed
   here as each lands.
2. **Post v0.1.0 roadmap** — work we want but won't block the first
   release on. TODO.md is the persistent roadmap; entries here are not
   automatically migrated to GitHub Issues. An individual item becomes
   a GitHub Issue only if and when someone starts work on it (or wants
   to discuss it publicly).

## v0.1.0 scope

- [x] **PR A1 — TODO triage + GitHub templates.** Restructure this file
      and add issue/PR templates. (This PR.)
- [x] **PR A2 — `context.Context` API sweep.** Threaded `ctx` through all
      public API entry points (`ParseDir`, `Run`, `CheckFormat`,
      `FixFormat`, `WriteFormatted`, `FormatFile`) before v0.1.0 to ship
      the canonical Go shape. Added cancellation checkpoints in hot
      loops and wired `signal.NotifyContext` in `main`. Test-first;
      covers SIGINT graceful shutdown. (#3 — 7 review rounds.)
- [x] **PR A2 follow-up — E000 exit-code routing.** Routes E000 violations
      (tool/infrastructure failures: unreadable directories, oversize
      files, write failures during `--fix`) to exit code 2 instead of
      exit 1, matching the documented contract in README and `SKILL.md`.
      Added `Summary.ToolErrors` sub-count; exit 2 takes precedence over
      exit 1 when both are present. Refreshed README, `SKILL.md`, and
      `main.go` godoc to spell out the new routing. (#6.)
- [x] **PR A3 — Lint hardening.** Adopted `gofumpt` and `golangci-lint`
      with 11 linters (`staticcheck`, `errcheck`, `gosec`, `revive`,
      `gocritic`, `unconvert`, `unused`, `ineffassign`, `misspell`,
      `noctx`, `unparam`); added `govulncheck` and `make verify`
      target. Fixed all 25 surfaced findings. (#4 — 1 review round.)
- [x] **PR A4 — Review-marker cleanup.** Scrubbed ~140 `C##` / `G##`
      review-finding markers from inline code comments and t.Errorf
      assertion strings across 17 files. Comment reasoning preserved;
      cross-references rewritten to name the property rather than the
      historical finding. (#5 — 1 review round.)
- [ ] **PR A5 — Public-adoption documentation.** `LICENSE`
      (Apache-2.0), `CONTRIBUTING.md`, `SECURITY.md`,
      `CODE_OF_CONDUCT.md`, `CHANGELOG.md`, README overhaul, SPDX
      headers on `.go` files.
- [ ] **PR B1 — CI + release infrastructure.** GitHub Actions
      workflows: PR validation matrix (Linux + macOS + Windows),
      `codeql.yml`, `release.yml` driven by goreleaser with cosign
      keyless signing, Syft SBOMs, dependabot config.
- [ ] **Phase C — Tag v0.1.0 + go public.** Create
      `mchv/homebrew-tfdry` tap repo, tag `v0.1.0`, verify the release
      workflow produces all artifacts and auto-PRs the tap formula,
      flip `mchv/tfdry` to public visibility.

## Post v0.1.0 roadmap

Items deferred to a later release. Tracked here as the persistent
roadmap; an entry only graduates to a GitHub Issue when someone picks
it up or wants to discuss it.

### Checks

- **E006 remote modules** — extend module input type checking to
  Spacelift registry modules and other sources where the module is
  cached locally. Currently only `./` and `../` relative paths are
  supported.

- **E007 OAC + AllViewer incompatibility** — detect when a CloudFront
  distribution uses an S3 origin with OAC
  (`origin_access_control_id` set) AND `Managed-AllViewer` as the
  origin request policy. This combination causes
  `SignatureDoesNotMatch` errors because `AllViewer` forwards the
  browser's `Authorization` header, conflicting with CloudFront's own
  SigV4 signing. The correct policy is
  `Managed-AllViewerExceptHostHeader`.

### Tests & benchmarks

- **Additional benchmark coverage**
  - Small-scale benchmark (2–5 files) to measure goroutine overhead
    vs. sequential
  - Violation-heavy benchmark to stress the `append` path in `Run`
  - Isolated `walkExpressions` benchmark

- **Move chmod-based E000 tests behind `//go:build unix`.**
  `checker/checks_test.go` has `TestE000_AlwaysEmitted_WhenDirUnreadable`
  and `TestParseDir_UnreadableFile_EmitsE000` that drive `ParseDir` into
  the E000 path via `os.Chmod(dir, 0o000)`. Both currently rely on
  `if err := os.Chmod(...); err != nil { t.Skip(...) }` for platform
  divergence, but Windows `os.Chmod` doesn't actually return an error
  on `0o000` — it just doesn't enforce read permissions the same way
  POSIX does. The tests would silently produce a false negative on
  Windows (skip-but-look-clean rather than skip-with-reason). The PR A2
  follow-up (#6) already factored the same pattern into
  `e000_exit_code_unix_test.go` with a `//go:build unix` constraint
  and a comment explaining the rationale (Plan 9 / js/wasm also lack
  the POSIX permission model and `os.Geteuid()`). These two siblings
  in `checker/checks_test.go` should get the same treatment — split
  into a `checks_unix_test.go` companion file (or comparable name)
  with the build constraint at the top. Small, mechanical, ~30 LOC.

### Performance

- **HCL AST cache for watch mode** — profiling shows ~75% of
  allocations inside `hclsyntax.ParseConfig`. A single-shot CLI run
  amortises this fine, but a future watch mode (e.g. LSP integration,
  file-system watchers, or pre-commit hooks running over many candidate
  diffs) would benefit hugely from `(content_hash → ParsedFile)`
  caching keyed by file mtime+size or SHA256. Big design points:
  invalidation rules, schema versioning, interaction with `--fix`.
  Skip until a watch-mode use case lands; right now the AST is the cost
  of doing business.

- **Per-phase tracing via env var (`TFDRY_BENCH=1`)** — pattern from
  [nanobrew](https://github.com/justrach/nanobrew): a single env-var
  toggle prints `[tfdry-bench] parse <file>: 12ms` etc. to stderr from
  inside the binary itself, covering Parse / Locals / each Check /
  Format / Output durations. Answers "where does the time go" without
  external benchmarking infrastructure.

- **Content-addressable result cache** — cache
  `(file_hash → []Violation)` in `~/.cache/tfdry/`. Big potential win
  for pre-commit-hook usage where most files are unchanged between
  runs. Needs a design pass on invalidation, schema versioning, and
  interaction with `--fix`.

- **`sync.Pool` for parser state** — Go equivalent of an arena
  allocator. `BenchmarkParseDir` currently shows ~107 KB/file
  allocation, mostly inside `hclsyntax.ParseConfig`. Profile with
  pprof before committing.

### Distribution

- **UPX compression for Linux/Windows release artifacts.** Currently
  the macOS dev build is ~4.15 MB. UPX typically reduces Go binaries by
  ~50–60%, landing Linux/Windows artifacts at ~1.5–2 MB.
  - Apply to: `tfdry-linux-{amd64,arm64}`,
    `tfdry-windows-amd64.exe` (Linux: `upx --best --lzma`).
  - Skip on macOS — UPX-packed binaries break code-signing /
    notarisation and are flagged by Gatekeeper.
  - Cost: cold-start CPU adds ~5–15 ms on Linux for self-decompression.
    For a CLI that already runs in 10–15 ms this is significant
    proportionally — measure before committing. The 50% win is mostly
    relevant for download-on-demand workflows (homebrew formula,
    `go install`).
  - Gate behind a `RELEASE_COMPRESS=1` env var in the release workflow
    so the trade-off is explicit per-platform.

### Platform support

- **Proper Windows symlink protection.** The Windows variant of
  `oNoFollow` in `checker/nofollow_windows.go` is currently `0` — a
  no-op. Real symlink rejection on Windows requires `CreateFile` with
  `FILE_FLAG_OPEN_REPARSE_POINT`, accessible via
  `golang.org/x/sys/windows`. Without it the `tfdry`-on-Windows path:
    - correctly rejects symlinks pointing at directories or devices,
      via the post-open `fi.Mode().IsRegular()` check
    - silently follows symlinks pointing at regular files, reading
      the target

  This is a downgrade from Unix (which atomically rejects all symlinks
  at the kernel level via `O_NOFOLLOW`) but no worse than typical
  Windows tooling. The proper fix needs:

  1. A `windows.CreateFile` call in `nofollow_windows.go` with
     `FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS` and
     the appropriate `OPEN_EXISTING` disposition.
  2. Wrapping the resulting handle in `os.NewFile` for the rest of the
     call site.
  3. A Windows-side equivalent of `isSymlinkRejection` that detects
     `ERROR_CANT_ACCESS_FILE` (the reparse-point signal).
  4. Test coverage that actually runs on Windows — depends on PR B1
     adding a Windows CI runner so behaviour is verified, not assumed.

### Hardening

- **Deeply nested HCL input depth check.** `hclsyntax.ParseConfig` is
  a recursive-descent parser. Pathologically nested input (thousands of
  levels of `(((((expr)))))` or `[[[[[[…]]]]]]`) can grow the goroutine
  stack until Go's 1 GB ceiling is hit, at which point the parser
  panics. Real codebases never approach this — triggering requires
  hand-crafted malicious input — but if it becomes a problem, a
  pre-parse depth check that counts unmatched `(` / `[` / `{` and
  rejects input above a threshold (~1000) would defuse it without a
  `recover()`. A few lines, but adds a full byte-scan over input. Only
  worth implementing once the threat is real.

  We deliberately don't wrap parsing in `recover()` — it would catch
  the overflow but also swallow real bugs from inside HCL or our own
  check code, converting them to silent diagnostics.

## Ideas under consideration

Patterns observed in other tools. Not committed; flagged here for future
consideration if the underlying need arises.

### From [nanobrew](https://github.com/justrach/nanobrew) (Zig package manager)

- **Auto-updated benchmark numbers in README.** Weekly cron
  (`.github/workflows/benchmark.yml`) runs the bench suite and uses a
  Python `re.sub` against marker comments in README to commit fresh
  numbers + a `> Auto-updated weekly` footer. Removes the staleness
  problem of hand-maintained benchmark tables.

- **Median-of-3 for low-N runs.** `sort -n | head -2 | tail -1` over 3
  samples. Robust to a single outlier, much cheaper than full
  statistical sampling, useful when hyperfine is unavailable or
  overkill.

- **Explicit cold/warm cache discipline.** Each bench function names
  what it preserves vs clears. Less guessing than relying on
  `--warmup`.

- **Suite parameterisation as parallel arrays in shell.** Minor
  stylistic improvement to `bench/run.sh`; cleaner than the current
  per-section duplication when adding new sizes or commands.

## Research log

Investigations with results worth preserving but no current action item.

### `encoding/json/v2` — measured, not adopted

Investigated `GOEXPERIMENT=jsonv2` (Go 1.27 candidate, accepted proposal
[golang/go#71497][gh-71497]). When the experiment is set, v1
`encoding/json` delegates internally to `encoding/json/v2`, so existing
v1 callers can opt in without code changes.

Measured against tfdry using `make bench-jsonv2`, which builds two
binaries from the same source — one default, one with
`GOEXPERIMENT=jsonv2` — and runs the standard hyperfine suite plus
targeted Go benchmarks (`output.BenchmarkWriteJSON`,
`output.BenchmarkDescribeJSON`).

**Results (Apple M4 Pro, Go 1.26.3, count=20, benchstat):**

| signal | jsonv1 | jsonv2 | delta |
|---|---|---|---|
| Binary size | 4.15 MB | 4.61 MB | **+454 KB / +10.94%** |
| WriteJSON CPU (geomean) | — | — | **−28.7% faster** |
| WriteJSON memory (geomean) | — | — | +45% |
| WriteJSON allocs (geomean) | — | — | +33% |
| End-to-end CLI (small/medium/large) | — | — | within ±20% noise band |

**Why we are not adopting it now.**

1. **Binary cost without offset.** `github.com/zclconf/go-cty` (an HCL
   transitive dep) imports `encoding/json` v1 unconditionally, so
   opting in links **both** implementations. The +454 KB is real and
   unrecoverable until v2 lands stable in Go 1.27.

2. **Known allocation regression for our shape.**
   [golang/go#77642][gh-77642] (open) documents v1-wrap-v2 doing +50%
   allocations vs pure v1 for some shapes — exactly the pattern we
   observed. [golang/go#74538][gh-74538] (closed) fixed one source of
   this for `Encoder.Reset`-heavy callers, but tfdry creates a fresh
   encoder per call.

3. **Memory regression at large payloads is by design.**
   [`jsontext/pools.go`][jsontext-pools] hard-codes a 64 KiB cutoff
   above which buffers are not pinned in the encoder pool. Combined
   with power-of-two allocation rounding, this creates the
   non-monotonic memory profile measured (1000 violations costs 1.3
   MiB on v2 vs 888 KiB on v1). This is a deliberate trade-off
   ([golang/go#23199][gh-23199]) and won't change.

4. **CPU win is invisible at CLI scale.** The 28% speedup is real for
   isolated marshalling, but JSON output is sub-millisecond inside an
   8–15 ms CLI run dominated by HCL parsing.

**Revisit when.** Go 1.27 ships v2 stable. At that point v1 is
reimplemented on top of v2 in the standard distribution (no
`GOEXPERIMENT` flag, no double-linked implementations), and the open
allocation regressions tracked by the working group should be resolved.
We get whatever wins remain real without paying the binary or
experiment-flag cost.

**Mechanically what we kept from this exercise:**
- `bench/baseline.sh` generalised — `EXPERIMENT=foo LABEL=… ARGS=…
  bench/baseline.sh` works for any future Go toolchain experiment.
- `make bench-jsonv2` target stays in place so a future revisit is one
  command.
- `output/bench_test.go` — `BenchmarkWriteJSON` and
  `BenchmarkDescribeJSON` give isolated JSON-path measurement.
- Makefile `bench` / `bench-save` target `./...` instead of
  `./checker/...` so future package additions are auto-picked up.

[gh-71497]: https://github.com/golang/go/issues/71497
[gh-77642]: https://github.com/golang/go/issues/77642
[gh-74538]: https://github.com/golang/go/issues/74538
[gh-23199]: https://github.com/golang/go/issues/23199
[jsontext-pools]: https://go.dev/src/encoding/json/jsontext/pools.go
