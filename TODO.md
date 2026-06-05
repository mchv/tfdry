# TODO

## Checks

- **E001 terraform fmt** — check HCL formatting using the HCL library's canonical formatter (equivalent to `terraform fmt -check`). Since tfdry already parses HCL, this is a pure in-process check with no Docker or provider downloads needed — much faster than running `terraform fmt`.

- **E006 remote modules** — extend module input type checking to Spacelift registry modules and other sources where the module is cached locally. Currently only `./` and `../` relative paths are supported.

- **E007 OAC + AllViewer incompatibility** — detect when a CloudFront distribution uses an S3 origin with OAC (`origin_access_control_id` set) AND `Managed-AllViewer` as the origin request policy. This combination causes `SignatureDoesNotMatch` errors because `AllViewer` forwards the browser's `Authorization` header, conflicting with CloudFront's own SigV4 signing. The correct policy is `Managed-AllViewerExceptHostHeader`.

- **E006 variables in subdirectories** — currently only reads `variables.tf`; some modules split variables across multiple files (e.g. `variables_network.tf`). Consider reading all `*.tf` files in the module directory for variable declarations.

## Tests

- **CLI integration tests** (`main_test.go`) — subprocess tests via `os/exec` covering:
  - `--json` output is valid JSON with correct shape
  - exit code 0 on clean directory
  - exit code 1 on violations
  - exit code 2 on bad args (`--checks=INVALID`, `--checks=`)
  - `describe` and `describe --json` (flag before and after subcommand)
  - `version` output
  - `--checks=E003,E004` filters output correctly at CLI level

- **E005 module blocks** — document that `module {}` blocks with both `count` and `for_each` are intentionally not flagged (out of scope without init)

- **Additional benchmark coverage**
  - Small-scale benchmark (2–5 files) to measure goroutine overhead vs. sequential
  - Violation-heavy benchmark to stress the `append` path in `Run`
  - Isolated `walkExpressions` benchmark

## Performance

- **HCL AST cache for watch mode** — profiling shows ~75% of allocations
  inside `hclsyntax.ParseConfig`. A single-shot CLI run amortises this
  fine, but a future watch mode (e.g. LSP integration, file-system
  watchers, or pre-commit hooks running over many candidate diffs) would
  benefit hugely from `(content_hash → ParsedFile)` caching keyed by
  file mtime+size or SHA256. Big design points: invalidation rules,
  schema versioning, interaction with `--fix`. Skip until a watch-mode
  use case lands; right now the AST is the cost of doing business.

- **`context.Context` for cancellation/timeouts** — the public API has
  no `ctx` parameter today because tfdry runs in a few milliseconds and
  exits. If/when LSP integration, watch mode, or huge-monorepo support
  lands, every entry point (`ParseDir`, `Run`, `RunModuleChecks`,
  `CheckFormat`, etc) should grow `ctx context.Context` as the first
  argument. Doing this earlier costs nothing operationally; doing it
  later is a breaking API change. Worth one preventive sweep before the
  first stable release.

## Ideas from other tools

Notes on patterns observed in other CLI/perf-oriented projects. None are committed; flagged here for future consideration.

### From [nanobrew](https://github.com/justrach/nanobrew) (Zig package manager)

- **Per-phase tracing via env var** — `NB_BENCH=1 nb install wget` prints `[nb-bench] dl <hash>: 647ms` to stderr from inside the binary itself. A `TFDRY_BENCH=1` flag could log Parse / Locals / each Check / Format / Output durations. Useful for "where does the time go" rather than wall-clock measurement.

- **Auto-updated benchmark numbers in README** — weekly cron (`.github/workflows/benchmark.yml`) runs the bench suite and uses a Python `re.sub` against marker comments in README to commit fresh numbers + a `> Auto-updated weekly` footer. Removes the staleness problem of hand-maintained benchmark tables.

- **Median-of-3 for low-N runs** — `sort -n | head -2 | tail -1` over 3 samples. Robust to a single outlier, much cheaper than full statistical sampling, useful when hyperfine is unavailable or overkill.

- **Explicit cold/warm cache discipline** — each bench function names what it preserves vs clears (`bench_nb_warm` keeps blobs but wipes DB+store). Less guessing than relying on `--warmup`.

- **Content-addressable result cache** — nanobrew's "recall cache" took openssl@3 reinstall from 1508ms to 129ms (11.7×) by caching the relocated keg by SHA256. tfdry analog: cache `(file_hash → []Violation)` in `~/.cache/tfdry/`. Big potential win for pre-commit-hook usage where most files are unchanged between runs. Needs a design pass — invalidation, schema versioning, and how it interacts with `--fix`.

- **`sync.Pool` for parser state** — Go equivalent of their arena allocator. `BenchmarkParseDir` currently shows ~107 KB/file allocation, mostly inside `hclsyntax.ParseConfig`. Worth profiling with pprof before committing to this.

- **Suite parameterisation as parallel arrays in shell** — minor stylistic improvement to `bench/run.sh`; cleaner than the current per-section duplication when adding new sizes or commands.



## JSON output

### `encoding/json/v2` — measured, not adopted

Investigated `GOEXPERIMENT=jsonv2` (Go 1.27 candidate, accepted proposal
[golang/go#71497][gh-71497]). When the experiment is set, v1 `encoding/json`
delegates internally to `encoding/json/v2`, so existing v1 callers can opt in
without code changes.

We measured this against tfdry using `make bench-jsonv2`, which builds two
binaries from the same source — one default, one with `GOEXPERIMENT=jsonv2` —
and runs the standard hyperfine suite plus targeted Go benchmarks
(`output.BenchmarkWriteJSON`, `output.BenchmarkDescribeJSON`).

**Results (Apple M4 Pro, Go 1.26.3, count=20, benchstat):**

| signal | jsonv1 | jsonv2 | delta |
|---|---|---|---|
| Binary size | 4.15 MB | 4.61 MB | **+454 KB / +10.94%** |
| WriteJSON CPU (geomean) | — | — | **−28.7% faster** |
| WriteJSON memory (geomean) | — | — | +45% |
| WriteJSON allocs (geomean) | — | — | +33% |
| End-to-end CLI (small/medium/large) | — | — | within ±20% noise band |

**Why we are not adopting it now.**

1. **Binary cost without offset.** `github.com/zclconf/go-cty` (a HCL
   transitive dep) imports `encoding/json` v1 unconditionally, so opting in
   links **both** implementations. The +454 KB is real and unrecoverable
   until v2 lands stable in Go 1.27.

2. **Known allocation regression for our shape.**
   [golang/go#77642][gh-77642] (open) documents v1-wrap-v2 doing +50%
   allocations vs pure v1 for some shapes — exactly the pattern we observed.
   [golang/go#74538][gh-74538] (closed) fixed one source of this for
   `Encoder.Reset`-heavy callers, but tfdry creates a fresh encoder per call.

3. **Memory regression at large payloads is by design.**
   [`jsontext/pools.go`][jsontext-pools] hard-codes a 64 KiB cutoff above
   which buffers are not pinned in the encoder pool. Combined with
   power-of-two allocation rounding, this creates the non-monotonic memory
   profile we measured (1000 violations costs 1.3 MiB on v2 vs 888 KiB on
   v1). This is a deliberate trade-off ([golang/go#23199][gh-23199]) and
   won't change.

4. **CPU win is invisible at CLI scale.** The 28% speedup is real for
   isolated marshalling, but JSON output is sub-millisecond inside an
   8-15 ms CLI run dominated by HCL parsing.

**Revisit when.** Go 1.27 ships v2 stable. At that point v1 is reimplemented
on top of v2 in the standard distribution (no `GOEXPERIMENT` flag, no
double-linked implementations), and the open allocation regressions tracked
by the working group should be resolved. We get whatever wins remain real
without paying the binary or experiment-flag cost.

**Mechanically what we kept from this exercise:**
- `bench/baseline.sh` generalised — `EXPERIMENT=foo LABEL=… ARGS=…
  bench/baseline.sh` works for any future Go toolchain experiment, no need
  for a sibling script.
- `make bench-jsonv2` target stays in place so a future revisit is one
  command.
- `output/bench_test.go` — `BenchmarkWriteJSON` and `BenchmarkDescribeJSON`
  give us isolated JSON-path measurement, which we lacked before.
- Makefile `bench` / `bench-save` now target `./...` instead of
  `./checker/...` so future package additions are auto-picked up.

[gh-71497]: https://github.com/golang/go/issues/71497
[gh-77642]: https://github.com/golang/go/issues/77642
[gh-74538]: https://github.com/golang/go/issues/74538
[gh-23199]: https://github.com/golang/go/issues/23199
[jsontext-pools]: https://go.dev/src/encoding/json/jsontext/pools.go

## Distribution

### UPX compression for Linux/Windows release artifacts

Currently the macOS dev build is `~4.15 MB`. UPX (ultimate packer for
executables) typically reduces Go binaries by ~50–60%, which would land
the Linux/Windows artifacts at ~1.5–2 MB.

**Why deferred.** There is no CI/release pipeline yet (no
`.github/workflows/`, no `.goreleaser.yaml`, no cross-platform Make
targets). UPX has to live in whatever release pipeline we eventually
introduce; standalone `make` targets for it would have nothing to attach
to.

**Apply to.**
- Linux: `upx --best --lzma tfdry-linux-amd64 tfdry-linux-arm64`.
- Windows: same but on `tfdry-windows-amd64.exe`.

**Skip on macOS.** UPX-packed binaries break Apple's code-signing /
notarisation flow and are flagged by Gatekeeper. Distribute macOS
unpacked.

**Cost.** Cold-start CPU adds ~5–15 ms on Linux for self-decompression.
For a CLI that already runs in 10–15 ms this is significant
proportionally — measure before committing to it. The 50% binary win is
mostly relevant for download-on-demand workflows (homebrew formula,
`go install`-style download); for normal package distribution the
compression saves bytes once and pays decompression cost on every run.

**When to revisit.** Once a release pipeline lands, add UPX as an
optional step gated behind a `RELEASE_COMPRESS=1` env var so the
trade-off is explicit per-platform.

## Known limitations

### Deeply nested HCL input can overflow the parser stack

`hclsyntax.ParseConfig` is a recursive-descent parser. Pathologically
nested input (thousands of levels of `(((((expr)))))` or
`[[[[[[…]]]]]]`) can grow the goroutine stack until Go's 1 GB ceiling
is hit, at which point the parser panics and tfdry exits non-zero with
a runtime fatal error.

**Practical risk.** Real Terraform codebases never approach this.
Triggering it requires hand-crafted input designed to be malicious —
which only matters in scenarios where attacker-controlled `.tf` files
reach a tfdry run (e.g. running tfdry against a public PR's diff
without sandboxing).

**Why we don't wrap parsing in `recover()`.** It would catch the
overflow but also swallow real bugs from inside HCL or our own check
code, converting them to silent diagnostics. The visibility cost is
not worth the marginal robustness gain.

**If this becomes a real problem.** A pre-parse depth check that
counts unmatched `(`/`[`/`{` and rejects input above a threshold (say,
1000) would defuse it without a `recover()`. That's a few lines but
adds a full byte-scan over input — only worth it once the threat is
real.
