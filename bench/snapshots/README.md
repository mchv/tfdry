# Benchmark snapshots

This directory contains selected, reviewed performance evidence. Raw outputs
from local runs are generated under the gitignored `bench/results/` directory;
they are not evidence for a published claim until their provenance and scope
have been recorded here.

Each snapshot must include:

- source commit and whether the worktree was clean;
- date, operating system, architecture, and relevant host/container details;
- pinned Go, timing-tool, and reference-command versions;
- exact fixture composition;
- commands and sample counts;
- timing and peak-RSS results used by public documentation;
- scope and interpretation caveats.

A snapshot is evidence for its recorded environment, not a performance
service-level objective or a promise that other hardware will produce the
same absolute values. Prefer same-host A/B runs when evaluating a change;
use snapshots for traceability and broad scaling evidence.
