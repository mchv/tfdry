<!--
Thanks for contributing to tfdry!

Please make sure your PR:
- Has a clear title that describes the change in <70 characters.
- Builds on a feature branch (not main).
- Includes tests written before the fix/feature (test-first protocol).
- Passes `make verify` locally.

For security-sensitive changes, please coordinate via the security
disclosure flow first (see SECURITY.md).
-->

## Summary

<!-- One paragraph: what does this PR do, and why? -->

## Motivation

<!-- What problem does this solve? Link to the issue if one exists. -->

Closes #

## Changes

<!-- Bullet list of the concrete changes. Be specific about behaviour shifts. -->

-
-
-

## Testing

<!-- How was this verified? -->

- [ ] New or updated tests added (test-first — failing test written before the fix)
- [ ] `make verify` passes locally (`gofumpt`-clean, `golangci-lint` clean, `go vet` clean, all tests pass with `-race`, cross-builds for darwin-arm64 / linux-amd64 / windows-amd64)
- [ ] Documentation updated if behaviour or public API changed

## Risk assessment

<!--
What could break? Any backwards-incompatible changes? Migration story?
For low-risk fixes, write "Low — confined to ...".
-->

## Checklist for reviewer

- [ ] Test-first protocol followed (new tests would fail without the change)
- [ ] No new dependencies added without justification
- [ ] No regression in binary size (target: ≤4.5 MB stripped)
- [ ] Cross-platform behaviour considered (especially symlink / path handling)
- [ ] Public API additions take `ctx context.Context` as first parameter
