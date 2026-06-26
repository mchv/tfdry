# Security Policy

## Supported versions

tfdry is at v0.1.0 (first public release). Only the most recent
minor-version line receives security updates while the project is
pre-1.0.

| Version | Supported          |
|---------|--------------------|
| 0.1.x   | :white_check_mark: |

Once a v1.0.0 ships, this policy will be updated to cover at least the
last two minor lines.

## Reporting a vulnerability

**Do not open a public GitHub issue for security reports.** A public
issue is visible to anyone watching the repository and gives an
attacker a head start before a fix lands.

Instead, use **[GitHub Security Advisories][advisories]**:

1. Open the link above.
2. Click **Report a vulnerability**.
3. Fill in the form. The repository maintainers will see the report
   and your messages; nothing is visible to the public until the
   advisory is published.

If you can't use the Security Advisories form (e.g. you don't have a
GitHub account), email
[mariot.chauvin@gmail.com](mailto:mariot.chauvin@gmail.com) with the
subject prefix `[tfdry security]`. Include the same information that
you would put in an advisory:

- A description of the issue and its impact.
- Reproduction steps (a minimal `.tf` fixture is ideal).
- The tfdry version (`tfdry --version`) and the OS / architecture.
- Whether the issue is already public or known elsewhere.
- A proposed fix if you have one — not required, but helpful.

[advisories]: https://github.com/mchv/tfdry/security/advisories/new

## What to expect

- **Acknowledgement**: within 72 hours of submission. If you don't
  hear back, please re-open the advisory with a follow-up comment
  (or re-send the email).
- **Triage**: within 7 days. The maintainer will respond with one of:
  (a) confirmation + a fix timeline, (b) request for more information,
  (c) a "won't fix" with an explanation if the issue is out of scope
  or already mitigated by existing tfdry defences.
- **Fix and disclosure**: for confirmed vulnerabilities, tfdry follows
  a coordinated disclosure model. The patched version is released
  together with the public advisory; the reporter is credited unless
  they explicitly opt out.

## Scope

In scope:

- The tfdry CLI binary and Go library API (`checker/...`, `output/...`).
- Build artifacts shipped via official channels (GitHub Releases,
  Homebrew tap once PR B1 lands).
- Documentation that, if followed, would lead to a security weakness
  (e.g. a CI recipe that disables verification).

Out of scope:

- Third-party Go module dependencies in `go.mod`. Report those
  upstream; `govulncheck` will surface them in `make verify`.
- Misconfiguration of Terraform files that tfdry merely lints. If
  there's a Terraform CVE, report it to HashiCorp.
- The tfdry GitHub repository's branch-protection settings, secrets
  management, or CI configuration. Report those to the repository
  owner via the same email channel.

## tfdry's existing security defences

For context — these are the security-shaped properties that the
test suite already exercises, so a security report should ideally
demonstrate that one of these is bypassed:

- **Symlink rejection** on `.tf` reads and writes. Both `--fix` and
  `tfdry fmt` reject symlinked input paths before opening them
  (atomic via `O_NOFOLLOW` on Unix, `Lstat`-then-check on Windows
  where `O_NOFOLLOW` isn't available).
- **TOCTOU defense-in-depth** on the atomic `--fix` rewrite path: a
  final `Lstat` immediately before `Rename` fails the operation if
  the target was swapped to a symlink between the initial check and
  the rename.
- **Trojan Source / terminal-injection** sanitization: filenames and
  HCL diagnostic text are stripped of ANSI escapes, Bidi-override /
  isolate-control characters (Unicode Cf category), and embedded
  newlines / tabs before reaching stdout, stderr, or the JSON
  output's `directory` field. Mitigates CVE-2021-42574-class
  attacks via malicious `.tf` file names or content.
- **File-size cap** at 10 MiB per `.tf` file to prevent unbounded
  reads from amplifying a malicious or accidental large input into
  excessive memory or CPU.
- **Module containment** for relative-path module references:
  `EvalSymlinks` + parent-prefix check rejects module sources that
  would escape the directory tree being linted.

If your report bypasses one of these, please mention which one in
the advisory body — it speeds triage.
