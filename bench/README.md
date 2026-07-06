# bench/

End-to-end benchmarks comparing `tfdry` against `terraform fmt --check` and `terraform validate`.

Everything runs inside a container with pinned versions so results are reproducible across machines.

## What's measured

| Comparison | Why |
|------------|-----|
| `tfdry fmt -check` vs `terraform fmt -check -recursive` | Closest apples-to-apples for read-only formatting check; both exit 3 on dirt |
| `tfdry fmt` vs `terraform fmt -recursive` (write mode, dirty input) | Apples-to-apples for the rewrite path; refreshes input per run |
| `tfdry` (all checks) vs `terraform validate` | Different scopes but both are "is this code OK?" tools |
| Scaling across small/medium/large | How each tool scales with directory size |
| `tfdry` human vs JSON output | Cost of JSON encoding |

## Layout

```
bench/
├── Dockerfile          # pinned terraform + hyperfine + tfdry build
├── gen-testdata.sh     # generates synthetic .tf dirs at image build time
├── run.sh              # hyperfine commands; entry point of the container
├── baseline.sh         # A/B compare current vs baseline ref (host-side)
├── testdata/
│   └── small/          # committed: realistic 5-file project
├── attr-corpus/        # attribute-value corpus for microbenchmarks
│   ├── repos.txt       # pinned Terraform repos
│   ├── fetch.sh        # downloads pinned tarballs
│   ├── extract.sh      # runs the Go extractor
│   ├── cmd/extract/    # hclsyntax-based extractor
│   └── values/         # extracted values (committed)
└── README.md
```

`testdata/medium/` (20 files) and `testdata/large/` (100 files) are generated at image build time by `gen-testdata.sh`. All test data uses only the `null` provider so no network access is needed at benchmark time.

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

Reports are written to `bench/results/` as both `.md` (human-readable) and `.json` (for further analysis).

## A/B comparing the current branch vs a baseline

`bench/baseline.sh` builds the current HEAD and a baseline ref into separate binaries, then runs `hyperfine -L variant current,baseline` against the same testdata. Both binaries run on the same machine state in one invocation — qualitatively different from saving two `bench.txt` files and running `benchstat` after the fact.

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

Each invocation gets its own worktree, so concurrent runs and stale state are not a concern. The worktree is removed automatically on exit (success or failure). This means every run pays the ~1-2s cost of building the baseline binary fresh.

Requires: `git`, `go`, `hyperfine` (all on the host — this script does not use Docker).
Optional: `gum` (charm.sh) for styled output — `brew install gum`. Without it, the script falls back to plain ASCII / `echo`.

## Pinned versions

| Tool | Version | Why pinned |
|------|---------|------------|
| terraform | 1.9.8 | comparison must use a stable terraform |
| hyperfine | 1.18.0 | output format / statistical methodology stability |
| go | 1.26.3 | matches `go.mod` |

Bump these in `bench/Dockerfile` when intentionally refreshing the baseline.

## Methodology

- `--warmup 3` — fills OS page cache, eliminates cold-start noise
- `--runs 10–20` — enough samples for hyperfine's outlier detection
- `terraform init` runs once at image build time (not measured)
- All terraform commands use `-backend=false` to avoid state setup
- Each container run is ephemeral — no cross-run state

## What this doesn't measure

- **Real-world repos** with cloud providers (AWS/GCP/Azure) — these need real provider downloads at init time. Running tfdry against such a repo on the host is the real test; this container measures the *tool overhead* on equivalent input.
- **Watch-mode performance** — both tools are invoked fresh each time. Process startup is part of the measured cost.
- **Network-bound operations** — explicitly excluded.
