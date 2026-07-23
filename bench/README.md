# bench/

End-to-end benchmarks for tfdry formatting and full-check workflows against a pinned reference CLI.

The container pins tool versions and fixture construction so the environment
is repeatable. Absolute timings and RSS remain hardware- and load-dependent;
compare numbers only with their recorded platform and source provenance.

## What's measured

| Comparison | Why |
|------------|-----|
| `tfdry fmt -check` vs `terraform fmt -check -recursive` | Closest apples-to-apples for read-only formatting check; both exit 3 on dirt |
| `tfdry fmt` vs `terraform fmt -recursive` (write mode, dirty input) | Apples-to-apples for the rewrite path; refreshes input per run |
| `tfdry` (all checks) vs `terraform validate` | Different scopes but both are "is this code OK?" tools |
| Scaling across small/medium/large | How each tool scales with directory size |
| `tfdry` human vs JSON output on a clean module | Cost of JSON encoding when there are no findings |
| `tfdry --json` with 1 and 10 diagnostics | Cost of the temporary-broken-state edit–check–repair path |
| `tfdry --json --recursive` over 10 workspaces | Agent feedback across a representative multi-workspace repository |
| `tfdry` peak RSS | Fresh-process resident-memory scaling across fixtures |

## Layout

```
bench/
├── Dockerfile          # pinned terraform + hyperfine + tfdry build
├── gen-testdata.sh     # generates synthetic .tf dirs at image build time
├── memory.sh           # GNU-time peak-RSS reporter
├── run.sh              # timing commands + RSS reporter; container entry point
├── baseline.sh         # A/B compare current vs baseline ref (host-side)
├── snapshots/          # reviewed, committed timing/RSS evidence
├── testdata/
│   └── small/          # committed: realistic 2-file project
├── attr-corpus/        # attribute-value corpus for microbenchmarks
│   ├── repos.txt       # pinned Terraform repos
│   ├── fetch.sh        # downloads pinned tarballs
│   ├── extract.sh      # runs the Go extractor
│   ├── cmd/extract/    # hclsyntax-based extractor
│   └── values/         # extracted values (committed)
└── README.md
```

The committed `testdata/small/` fixture contains 2 Terraform files.
`gen-testdata.sh` treats its numeric argument as the number of resource
files, then adds `providers.tf` and `locals.tf`; the generated `medium`
fixture therefore contains 22 files (20+2), and `large` contains 102 files
(100+2). The image also constructs two diagnostic fixtures with exactly 1
and 10 undefined-local findings, plus a recursive fixture containing 10
independent copies of the clean two-file workspace. All test data uses only
the `null` provider, so no network access is needed during measured commands.

`attr-corpus/values/` seeds microbenchmarks for the format-validation check family (CIDR, ARN, region, account-ID). Its `files/` sub-directory (gitignored) is populated by `make bench-corpus-fetch`. See `attr-corpus/README.md` for details.

## Running

```sh
make bench-e2e
```

Equivalent to:

```sh
docker build -f bench/Dockerfile -t tfdry-bench .
docker run --rm -v "$(pwd)/bench/results:/out" tfdry-bench
```

Generated reports are written to the gitignored `bench/results/` directory as
both `.md` (human-readable) and `.json` (for further analysis). After a run
is reviewed, selected timing and RSS evidence can be copied into
`bench/snapshots/` with its source commit and environment provenance.

To run only the peak-RSS reporter:

```sh
make bench-memory
```

The reporter uses GNU time 1.9 inside the pinned container and writes
`memory.md` plus `memory.json`.

## A/B comparing the current branch vs a baseline

`bench/baseline.sh` builds the current HEAD and a baseline ref into separate binaries, then runs `hyperfine -L variant current,baseline` against the same testdata. Both commands run on the same host and staged input in one invocation, but their samples are sequential groups rather than statistically paired or interleaved. This is qualitatively different from saving two `bench.txt` files and running `benchstat` after the fact.

```sh
make bench-baseline                  # baseline = git merge-base HEAD origin/main
make bench-baseline BASELINE=v0.1.0  # vs a specific tag
make bench-baseline BASELINE=HEAD~1  # vs the previous commit
```

Output: `bench/results/baseline-{small,medium,large}.{md,json}` — each report is a pairwise current-vs-baseline comparison at one input size.

### Measuring write-mode formatting performance (`DIRTY=1`)

By default `bench-baseline` operates on already-formatted fixtures, so `ARGS=fmt` no-ops on every run. To measure the rewrite path (e.g. comparing two refs of `runFmt`/`FormatFile`), set `DIRTY=1`:

```sh
ARGS=fmt DIRTY=1 make bench-baseline BASELINE=HEAD~1
```

In this mode `gen-testdata.sh --dirty` produces unformatted (still-valid) HCL into a per-size source dir, and hyperfine `--prepare` resets a working copy from it before every measured run. The `cp -R` cost is outside the timed window. Reports are named `baseline-dirty-{small,medium,large}.{md,json}` to keep them separate from the read-only baseline runs.

How it works:

1. Resolves `BASELINE` (CLI arg, env var, or `git merge-base HEAD origin/main`)
2. Builds current HEAD into `.bench-current` (gitignored)
3. Creates a fresh `git worktree` under `/tmp/tfdry-baseline-XXXXXX/` (via `mktemp -d`) and builds the baseline binary into it
4. Stages testdata (small copied, medium/large generated) into a temp dir
5. Runs hyperfine three times, once per size

Each invocation gets its own worktree, so concurrent runs and stale state are not a concern. Temporary testdata, the worktree, and both benchmark binaries are removed automatically on exit (success or failure). This means every run pays the ~1-2s cost of building the baseline binary fresh.

Requires: `git`, `go`, `hyperfine` (all on the host — this script does not use Docker).
Optional: `gum` (charm.sh) for styled output — `brew install gum`. Without it, the script falls back to plain ASCII / `echo`.

## Pinned versions

| Tool | Version | Why pinned |
|------|---------|------------|
| Reference CLI | 1.15.8 | public comparisons use a current stable, exact pin |
| hyperfine | 1.18.0 | output format / statistical methodology stability |
| go | 1.26.3 | matches `go.mod` |

Bump these in `bench/Dockerfile` when intentionally refreshing the baseline.

## Methodology

- `--warmup 3` — fills OS page cache and reduces cold-start noise
- `--runs 20–30` — timing sample count depends on workload
- Peak RSS uses 3 warm-ups and 11 fresh-process measurements per fixture
- The reference validation command uses its native working-directory option and runs directly under Hyperfine `-N`; neither measured validation command pays shell-startup cost
- Reference initialisation runs once at image build time (not measured)
- All terraform commands use `-backend=false` to avoid state setup
- Each container run is ephemeral — no cross-run state

## What this doesn't measure

- **Real-world repos** with cloud providers (AWS/GCP/Azure) — these need real provider downloads at init time. Running tfdry against such a repo on the host is the real test; this container measures the *tool overhead* on equivalent input.
- **Watch-mode performance** — both tools are invoked fresh each time. Process startup is part of the measured cost.
- **Network-bound operations** — explicitly excluded.
