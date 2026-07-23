# Performance snapshot — 23 July 2026

This snapshot records the pinned end-to-end benchmark suite for source commit
`edf88b917d3cb7dcdc798d9042bb86fcead7848d` (`v0.1.1-64-gedf88b9`).
The worktree was clean when the image was built.

## Environment

| Item | Value |
|---|---|
| Host | Mac16,8; Apple M4 Pro; 24 GiB RAM |
| Host OS | macOS 15.6 (24G84), arm64 |
| Container runtime | Docker server 29.5.2 |
| Container image | `sha256:90b5262551624edfebc26b9c09b79536c46c812a6be922e38fe78ac19645b0d1` |
| Container platform | Linux/aarch64 |
| Go | 1.26.3 |
| Hyperfine | 1.18.0 |
| GNU time | 1.9 |
| Reference CLI | 1.9.8 |
| tfdry | `v0.1.1-64-gedf88b9` |

The container pins the toolchain and fixture construction. Absolute values
remain specific to this host, architecture, container runtime, and load.

## Fixtures

| Fixture | Terraform files | Construction |
|---|---:|---|
| small | 2 | Committed project (`main.tf`, `resources.tf`) |
| medium | 22 | 20 generated resource files plus `providers.tf` and `locals.tf` |
| large | 102 | 100 generated resource files plus `providers.tf` and `locals.tf` |

All commands use fresh processes. Timing runs use three warm-ups and either
20 or 30 measured samples, as recorded in the raw Hyperfine JSON. The suite
reported statistical outliers for several commands; the standard deviation
and complete sample arrays are retained below rather than hidden.

## Read-only timing

| Workload | Fixture | tfdry mean | Reference mean | Relative |
|---|---|---:|---:|---:|
| Full check | small (2 files) | 0.916 ± 0.076 ms | 49.0 ± 1.6 ms | 53.5× faster |
| Full check | large (102 files) | 7.03 ± 1.28 ms | 102.8 ± 5.5 ms | 14.6× faster |
| Format check | small (2 files) | 1.42 ± 1.22 ms | 11.1 ± 1.6 ms | 7.84× faster |
| Format check | large (102 files) | 7.18 ± 1.56 ms | 21.9 ± 3.9 ms | 3.05× faster |

The full-check rows compare different validation scopes. They answer the
workflow-level question “how long does each normal check command take?”;
they do not establish equal diagnostic coverage. Initialisation occurs at
image-build time and is outside the measured window, while the reference
command's normal provider-loading work remains inside it. This suite does
not isolate provider loading as the sole cause of the difference.

## Write mode and output

| Workload | Fixture | tfdry mean | Reference/alternative mean | Interpretation |
|---|---|---:|---:|---|
| Format write | small | 1.08 ± 0.16 ms | 10.5 ± 0.8 ms | 9.73× faster |
| Format write | large | 11.4 ± 1.5 ms | 23.6 ± 2.1 ms | 2.06× faster |
| Human output | large | 7.03 ± 0.90 ms | — | Baseline output mode |
| JSON output | large | 6.99 ± 1.06 ms | — | No measurable overhead in this run |

Write-mode fixtures are restored from dirty source before every sample;
that copy is outside the timed window.

## Peak resident memory

GNU time 1.9 measured 11 fresh processes after three warm-ups per fixture.
The table reports the median of each process's maximum resident set size,
with the observed minimum and maximum.

| Fixture | Median peak RSS | Min | Max |
|---|---:|---:|---:|
| small (2 files) | 7,888 KiB (7.70 MiB) | 7,868 KiB | 7,956 KiB |
| medium (22 files) | 10,276 KiB (10.04 MiB) | 8,312 KiB | 10,468 KiB |
| large (102 files) | 13,392 KiB (13.08 MiB) | 11,388 KiB | 15,544 KiB |

Peak RSS includes the process image, Go runtime, stacks, parsed source, and
heap retained at the measurement peak. It is not equivalent to Go
microbenchmark `B/op`, and values should only be compared under the same
container architecture and fixture.

## Reproduction

```sh
make bench-e2e
```

Generated reports are written to the gitignored `bench/results/` directory.
The raw JSON retained with this snapshot is:

- [`fmt-small.json`](fmt-small.json)
- [`fmt-large.json`](fmt-large.json)
- [`validate-small.json`](validate-small.json)
- [`validate-large.json`](validate-large.json)
- [`scaling-fmt.json`](scaling-fmt.json)
- [`json-overhead.json`](json-overhead.json)
- [`fmt-write-small.json`](fmt-write-small.json)
- [`fmt-write-large.json`](fmt-write-large.json)
- [`memory.json`](memory.json)
