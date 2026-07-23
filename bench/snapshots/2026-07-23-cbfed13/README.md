# Benchmark snapshot: 23 July 2026 (`cbfed13`)

This snapshot records the pinned end-to-end benchmark suite for source commit
`cbfed13fdf1d83095775cb5ff123e5ab38b2718e`
(`v0.1.1-67-gcbfed13`). The detached worktree was clean when the image was
built.

## Environment

| Item | Value |
|---|---|
| Host | Mac16,8; Apple M4 Pro; 24 GiB RAM |
| Host OS | macOS 15.6 (24G84), arm64 |
| Container runtime | Docker server 29.5.2 |
| Container image | `sha256:b76fc6e198160d9402b891eb2c352281efe9d4c948c40b9a9202f9c32e5870c3` |
| Container platform | Linux/aarch64 |
| Go | 1.26.3 |
| Hyperfine | 1.18.0 |
| GNU time | 1.9 (`1.9-0.2`) |
| Reference CLI | 1.15.8 |
| Fixture provider | 3.3.0 |
| tfdry | `v0.1.1-67-gcbfed13` |

The container pins the toolchain, fixture provider, and fixture construction.
Absolute values remain specific to this host, architecture, container runtime,
and load.

## Fixtures

| Fixture | Terraform files | Construction |
|---|---:|---|
| small | 2 | Committed project (`main.tf`, `resources.tf`) |
| medium | 22 | 20 generated resource files plus `providers.tf` and `locals.tf` |
| large | 102 | 100 generated resource files plus `providers.tf` and `locals.tf` |
| one diagnostic | 1 | One output referencing one undefined local |
| ten diagnostics | 1 | Ten outputs referencing ten undefined locals |
| recursive | 20 | Ten independent copies of the clean two-file workspace |

The image build asserts that the diagnostic fixtures exit 1 with exactly 1
and 10 findings, and that the recursive fixture exits 0 without findings.

## Method

All measured commands launch fresh processes. Timing runs use three warm-ups
and either 20 or 30 measured samples, as retained in the raw Hyperfine JSON.
Hyperfine `-N` executes measured commands directly without shell-startup cost.
The full-check reference commands use the CLI's native working-directory
option; initialisation happens while building the image and remains outside
the measured window.

The suite was run with:

```sh
make bench-e2e
```

The principal measured command strings were:

```sh
tfdry /testdata/{small,large}
terraform -chdir=/testdata/{small,large} validate

tfdry fmt -check /testdata/{small,large}
terraform fmt -check -recursive /testdata/{small,large}

tfdry /testdata/large
tfdry --json /testdata/large
tfdry --json /testdata/agent/broken-1
tfdry --json /testdata/agent/broken-10
tfdry --json --recursive /testdata/agent/recursive
```

The two diagnostic commands intentionally exit 1. Hyperfine's
`--ignore-failure` accepts that asserted product result without adding a shell
wrapper. Clean agent commands run in a separate strict invocation, so an
unexpected clean-command failure still stops the suite.

## Read-only timing

| Workload | Fixture | tfdry mean | Reference CLI 1.15.8 mean | Relative |
|---|---:|---:|---:|---:|
| Full check | small (2 files) | 1.08 ± 0.12 ms | 49.9 ± 1.9 ms | 46.4× faster |
| Full check | large (102 files) | 7.15 ± 1.08 ms | 101.4 ± 9.3 ms | 14.18× faster |
| Format check | small (2 files) | 1.01 ± 0.19 ms | 10.4 ± 1.2 ms | 10.24× faster |
| Format check | large (102 files) | 6.69 ± 0.96 ms | 20.5 ± 1.7 ms | 3.07× faster |

The full-check rows compare different validation scopes. They answer the
workflow-level question “how long does each normal check command take?”;
they do not establish equal diagnostic coverage. Initialisation is outside
the measured window, while the reference command's normal provider, plugin,
and schema path remains inside it. This suite does not isolate any one part
of that path as the sole cause of the difference.

## Agent-oriented fresh-process timing

| Workload | Fixture | Mean |
|---|---:|---:|
| Human output, clean | 102 files | 8.49 ± 2.94 ms |
| JSON output, clean | 102 files | 7.06 ± 0.86 ms |
| JSON output, one diagnostic | 1 file | 0.897 ± 0.152 ms |
| JSON output, ten diagnostics | 1 file | 0.991 ± 0.118 ms |
| JSON output, recursive clean check | 10 workspaces / 20 files | 2.75 ± 0.30 ms |

These rows model clean and temporarily broken edit–check–repair paths. Their
fixture sizes differ deliberately, so they are absolute workload timings and
not ratios against one another. The clean human-output samples include a
21.9 ms maximum and correspondingly wider variance; the complete samples are
retained rather than filtered.

## Write mode

| Workload | Fixture | tfdry mean | Reference CLI 1.15.8 mean | Relative |
|---|---:|---:|---:|---:|
| Format write | small | 1.30 ± 0.25 ms | 10.7 ± 1.7 ms | 8.20× faster |
| Format write | large | 11.99 ± 1.22 ms | 24.6 ± 1.9 ms | 2.05× faster |

Write-mode fixtures are restored from dirty source before every sample; that
copy is outside the timed window.

## Peak resident memory

GNU time 1.9 measured 11 fresh processes after three warm-ups per fixture.
The table reports the median of each process's maximum resident set size,
with the observed minimum and maximum.

| Fixture | Median peak RSS | Min | Max |
|---|---:|---:|---:|
| small (2 files) | 7,884 KiB (7.70 MiB) | 5,772 KiB | 10,060 KiB |
| medium (22 files) | 10,304 KiB (10.06 MiB) | 8,120 KiB | 10,468 KiB |
| large (102 files) | 13,444 KiB (13.13 MiB) | 13,288 KiB | 13,548 KiB |

Peak RSS includes the process image, Go runtime, stacks, parsed source, and
heap retained at the measurement peak. It is not equivalent to Go
microbenchmark `B/op`, and values should only be compared under the same
container architecture and fixture.

## Raw reports

Generated reports are written to the gitignored `bench/results/` directory.
The raw JSON retained with this snapshot is:

- [`fmt-small.json`](fmt-small.json)
- [`fmt-large.json`](fmt-large.json)
- [`validate-small.json`](validate-small.json)
- [`validate-large.json`](validate-large.json)
- [`scaling-fmt.json`](scaling-fmt.json)
- [`agent-clean.json`](agent-clean.json)
- [`agent-diagnostics.json`](agent-diagnostics.json)
- [`fmt-write-small.json`](fmt-write-small.json)
- [`fmt-write-large.json`](fmt-write-large.json)
- [`memory.json`](memory.json)
