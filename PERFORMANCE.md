# Performance

`tfdry` treats short feedback loops and restrained memory use as product
requirements. It is intended to run repeatedly during local development,
pre-commit checks, CI, and agentic coding workflows without waiting for
initialisation, remote I/O, or a large supporting process.

This document explains the design choices behind that goal, what is
measured, and how to reproduce or compare results. It complements the
[benchmark harness documentation](bench/README.md), which describes the
end-to-end fixture layout in more detail.

## Performance objectives

A fast result is useful only when it remains predictable and meaningful.
The project therefore optimises for the following properties:

- **Fast local feedback.** A normal lint run reads and statically analyses
  local `.tf` files. It does not download providers, fetch schemas, read
  state, or make network requests.
- **Memory-conscious execution.** The process does not start provider
  processes or maintain provider schemas or state. Source input is capped
  at 10 MiB per file, and common literal-validation paths are designed to
  avoid per-value heap allocation.
- **Proportional work.** Files are parsed once, then checks operate on the
  resulting syntax trees. Individual checks use narrow trigger surfaces
  and skip blocks or attributes outside their scope before doing deeper
  work.
- **Measured changes.** Performance-sensitive code has microbenchmarks
  with allocation reporting, while user-visible command cost is measured
  separately with repeatable end-to-end runs.

These are engineering goals, not a fixed latency or resident-memory
service-level objective. Input shape, file count, filesystem cache,
processor, Go version, and concurrent host activity all affect a run.

## Current reviewed snapshot

The latest reviewed measurements are
[`23 July 2026 at edf88b9`](bench/snapshots/2026-07-23-edf88b9/README.md).
On its recorded Apple M4 Pro host, inside the pinned Linux/arm64 container:

| Workload | Fixture | Result |
|---|---:|---:|
| Full tfdry check | 102 files | 7.03 ± 1.28 ms |
| Read-only tfdry format check | 102 files | 7.18 ± 1.56 ms |
| Full-check median peak RSS | 102 files | 13.08 MiB |

The snapshot records the source commit, architecture, pinned tool versions,
fixture shape, complete timing samples, and peak-RSS range. Generated raw
reports remain under the gitignored `bench/results/` directory; selected
evidence is copied into a snapshot only after review.

The full-check row is a workflow-level comparison, not an equal-scope
validator comparison. Initialisation happens while the container image is
built and is outside the timed window. The reference command's normal
provider-loading work remains inside that window. The harness does not
isolate provider loading as the sole cause of any difference.

Absolute timings and RSS vary with hardware and host load. Treat a snapshot
as evidence for that recorded environment, not as a cross-machine promise.

## Design approach

### No external initialisation path

`tfdry` analyses source syntax directly. Its normal execution path has no
provider download, provider process, schema lookup, state read, or network
round trip. This removes both elapsed time and the external caches and
processes that otherwise increase memory pressure.

### Parse once; check selectively

The parser builds the syntax representation once for each input file. The
checker dispatcher then shares that parsed input across enabled checks.
Checks avoid a generic deep walk where a flatter, scoped scan is sufficient:
for example, grammar checks first reject non-matching block types and
attribute names before inspecting expression values.

A `--checks=` allow-list also constrains work to the requested checks. The
always-enabled E000 tool-error path remains separate so operational
failures are never hidden by check selection.

### Allocation-aware hot paths

Many checks run over every relevant attribute in a module, so small
allocations can compound. Where a check accepts a pure string literal,
`TryLiteralString` provides a fast path that does not construct template
parts. Several grammar validators use fixed-size data, stack-local state,
or byte lookup tables rather than allocation-heavy intermediate objects.

The aim is not to avoid allocation everywhere: parsing necessarily builds
syntax structures, and diagnostics allocate when there is a finding. The
aim is to avoid unnecessary allocation in successful, repeated validation
of ordinary source.

### Bound input and work

`tfdry` reports E000 rather than attempting to parse source files larger
than 10 MiB. This protects the tool from accidental or hostile oversized
input and keeps memory demand bounded per file. Context cancellation is
propagated through the pipeline so callers can stop a run that is no
longer useful.

## What the benchmark suite measures

The suite has five complementary layers. No single layer answers every
performance question.

| Layer | Command | Measures | Best use |
|---|---|---|---|
| Go microbenchmarks | `make bench` | Isolated functions, checker walks, pipeline stages, output, `ns/op`, `B/op`, and `allocs/op` | Detecting a local regression or validating an allocation contract |
| Hermetic attribute corpus | `make bench` | Grammar-check workloads built from committed extracted values | Exercising validators with representative literal shapes |
| End-to-end container runs | `make bench-e2e` | Fresh command timing on 2-, 22-, and 102-file fixtures, including clean, diagnostic JSON, and recursive workloads | User-visible latency, scaling, and agent edit–check–repair feedback |
| Peak-RSS container runs | `make bench-memory` | Median/minimum/maximum process peak RSS on the same fixtures | Checking process-memory scaling without conflating RSS and heap allocation |
| Same-host A/B runs | `make bench-baseline` | Current branch and baseline commands in one Hyperfine invocation | Attributing a command-level change to a branch or commit |

`make bench-jsonv2` is an additional experiment target. It compares the
default build with a Go runtime experiment for output paths; it is not part
of the normal regression workflow.

### Go microbenchmarks

`*_bench_test.go` files cover the checker pipeline, individual check
families, and output encoding. They call `b.ReportAllocs()` and use a sink
where needed so the compiler cannot remove the work being measured.

The standard command runs every benchmark once, for five seconds each,
with allocation statistics:

```sh
make bench
```

For a focused investigation, run only the relevant package and benchmark:

```sh
go test ./checker -run '^$' -bench 'BenchmarkE204' -benchmem -benchtime=2s -count=2
```

Benchmark names distinguish the operation being measured. Typical examples
include a validator-only path, a checker walk with matching triggers, and a
no-trigger walk that measures the cost paid by unrelated configuration.
The latter matters because an inexpensive non-match protects every module
that does not use a particular check.

### Hermetic attribute corpus

`bench/attr-corpus/values/` contains extracted literal values from pinned
open-source Terraform repositories. The small, committed value files make
microbenchmarks available from a normal checkout without a network fetch.
They provide distributions and attribute shapes that hand-written examples
can miss.

The large source snapshots used to create the values are generated and
ignored. Refresh the corpus only when changing its pinned sources or
extractor behaviour:

```sh
make bench-corpus-refresh
```

Review the resulting `values/` diff and keep corpus refreshes separate from
checker changes where practical. This makes performance changes and input
changes independently reviewable. See
[`bench/attr-corpus/README.md`](bench/attr-corpus/README.md) for the pinned
source and extraction rules.

### End-to-end fixtures

`make bench-e2e` builds the benchmark container and runs the end-to-end
harness:

```sh
make bench-e2e
```

The committed `small` fixture contains 2 Terraform files. The generator's
numeric argument counts resource files, then adds `providers.tf` and
`locals.tf`; therefore `medium` contains 22 files (20+2) and `large`
contains 102 files (100+2). The harness performs warm-up runs, takes 20 or
30 measured timing samples depending on the workload, and writes both
human-readable Markdown and machine-readable JSON reports to the gitignored
`bench/results/` directory.

The container pins the harness toolchain and fixture construction. That
makes the method repeatable, but it does not make absolute timings
comparable across processors, container runtimes, or concurrently loaded
hosts.

The end-to-end measurements include process startup because interactive
and CI use invoke a fresh command. They deliberately exclude network-bound
work and do not claim to model every real repository or long-running watch
mode. The full-check comparison also has different validation scope, as
noted in the reviewed-snapshot section.

### Agent-oriented output workloads

The end-to-end suite separately times a clean 102-file check with human and
JSON output, JSON checks that return exactly 1 and 10 diagnostics, and a
recursive JSON check over 10 independent two-file workspaces. The diagnostic
commands intentionally exit 1; Hyperfine accepts that expected result
directly rather than measuring a shell wrapper.

These cases model the edit–check–repair loop more closely than a clean-only
run. They include diagnostic construction and JSON growth while keeping
fixture shape and finding counts explicit. They remain representative cases,
not a claim that every agent workload has the same size or diagnostic mix.

### Peak resident memory

`make bench-memory` runs only the memory reporter. `make bench-e2e` runs it
after the timing suite. For each fixture, the reporter performs three
warm-ups followed by 11 fresh `tfdry` processes under GNU time 1.9. It
publishes the median, minimum, and maximum of each process's maximum
resident set size in KiB:

```sh
make bench-memory
```

The JSON and Markdown reports are written to `bench/results/memory.*`.
Peak RSS includes the process image, Go runtime, stacks, parsed source, and
heap retained at the measurement peak. It is intentionally reported
separately from Go benchmark `B/op` and `allocs/op`.

### Same-host baseline comparison

Absolute timings vary across machines. To compare a performance-sensitive
change, run both versions on the same host, against the same staged input,
in one invocation:

```sh
make bench-baseline                  # merge-base with origin/main
make bench-baseline BASELINE=HEAD~1  # previous commit
make bench-baseline BASELINE=v0.1.0  # named ref
```

The script builds the current and baseline binaries separately, stages
small/medium/large input in a temporary directory, and runs both commands
within one Hyperfine invocation. Samples are sequential command groups,
not statistically paired or interleaved. The script removes temporary
testdata, its worktree, and both temporary binaries on exit. Results are
written as Markdown and JSON under `bench/results/baseline-*`.

For formatting write-path changes, use dirty fixtures. The harness restores
an unformatted copy before every measured run; setup-copy time is outside
the timed command:

```sh
ARGS=fmt DIRTY=1 make bench-baseline BASELINE=HEAD~1
```

This A/B method is stronger evidence than comparing two independently
recorded benchmark files because both commands use the same host and staged
input in one invocation. Samples are still sequential, so load and thermal
conditions can drift during the run.

## Comparing Go benchmarks

`benchstat` is pinned in the Makefile and installed by `make tools`. To
install only that tool:

```sh
make tools-benchstat
```

Then use repeated samples for a code-level comparison:

```sh
make bench-save FILE=before.txt
# make the change
make bench-save FILE=after.txt
make bench-compare OLD=before.txt NEW=after.txt
```

For benchmarks with sub-names such as `files=10`, pivot a result by that
dimension:

```sh
make bench-pivot FILE=after.txt COL=files
```

Keep the toolchain, host, benchmark duration, and input corpus unchanged
between `before` and `after`. Record the command, Go version, operating
system, architecture, and whether the machine was otherwise busy when
sharing results. Use the same-host baseline harness when the question is
command-level performance rather than one Go function.

## Reading the numbers correctly

| Measurement | Meaning | Interpretation |
|---|---|---|
| `ns/op` | Mean time per benchmark iteration | Lower is better; compare only equivalent benchmark workloads. |
| `B/op` | Heap bytes allocated per iteration | Lower reduces garbage-collection pressure, but is not process RSS. |
| `allocs/op` | Heap allocation count per iteration | A zero-allocation hot path is useful only when the measured input actually reaches it. |
| Hyperfine mean/min/max | Fresh-command wall-clock timing | Includes startup and reflects host noise; use several runs and same-invocation A/B comparisons. |
| GNU time maximum RSS | Peak resident set size for one fresh process | Includes runtime and process image; compare only the same container architecture and fixture. |

Allocation statistics are deliberately not presented as a resident-memory
claim. `B/op` and `allocs/op` describe allocations performed during one
benchmark iteration; the Go runtime may retain or reuse heap memory, and
process RSS also includes the binary, runtime, parsed source, stacks, and
operating-system accounting. Use a dedicated profiler or platform memory
tool when diagnosing an RSS issue, and document the measurement method
alongside any reported number.

Similarly, a microbenchmark can prove that a helper is cheap without
proving that a full command is fast. Check both the local benchmark and an
end-to-end or same-host A/B result when a change can affect users.

## Expectations for performance-sensitive changes

When adding or altering a check that runs across source input:

1. Add or update a focused microbenchmark alongside the implementation.
   Include allocation reporting and ensure the benchmark consumes its
   result.
2. Measure both matching and non-matching scope where applicable. A check
   should be cheap when it fires and nearly free when it does not apply.
3. Prefer committed corpus values or deterministic synthetic fixtures over
   live network input.
4. Capture a baseline before making a meaningful optimisation, then compare
   after the change with the same command and environment.
5. Run the normal correctness suite as well. A benchmark is not evidence
   that the check still validates the intended grammar or traversal scope.
6. Describe any material trade-off in the pull request: changed latency,
   allocations, binary size, or code complexity.

Do not optimise for a single number at the expense of representative input,
correctness, or readable code. Performance work should improve the actual
feedback loop, not merely the benchmark harness.

## Limits and follow-up investigation

The checked-in suite intentionally does not provide a universal memory
budget, a cloud-provider simulation, or a long-lived editor-process model.
Those need workload-specific profiling. Start with the smallest relevant
layer above, then collect a CPU or heap profile around the real command if
the evidence points to a problem outside the microbenchmark.

For fixture mechanics and pinned end-to-end tooling, see
[`bench/README.md`](bench/README.md). For normal build and verification
commands, see [`CONTRIBUTING.md`](CONTRIBUTING.md).
