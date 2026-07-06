// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

// Command tfdry validates Terraform files without requiring terraform init or validate.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/mchv/tfdry/checker"
	"github.com/mchv/tfdry/output"
)

func main() {
	// signal.NotifyContext gives us a cancellable ctx that fires on
	// SIGINT (Ctrl-C) and SIGTERM. The derived ctx flows into run() and
	// onwards into every long-running checker entry point, so an
	// interrupted tfdry run cleanly stops at the next per-file
	// checkpoint instead of being torn down mid-write or mid-parse.
	//
	// stop() must run before os.Exit because os.Exit terminates the
	// process immediately and skips deferred functions — `defer stop()`
	// here would never fire. Capture run()'s exit code, call stop()
	// explicitly to unregister the signal handlers (restoring default
	// signal behaviour as documented on signal.NotifyContext), then
	// exit with the captured code.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// Test-only ready signal: when TFDRY_TEST_READY=1 is set, emit a
	// stable marker to stderr so a parent process can deterministically
	// wait for full startup — signal handlers armed and Go runtime
	// past initialisation — rather than guessing with a time.Sleep.
	// Production users don't set TFDRY_TEST_READY, so this is a no-op
	// for them (one getenv call at startup, ~microseconds).
	//
	// The marker line ("tfdry: test-ready\n") is a stable contract
	// consumed by sigint_test.go's TestRunCLI_SIGINT_HandlesGracefully.
	// Do not change the wording without updating that test.
	if os.Getenv("TFDRY_TEST_READY") == "1" {
		_, _ = fmt.Fprintln(os.Stderr, "tfdry: test-ready")
	}

	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// handleCtxErr is the cancellation-only branch of run()'s error handling.
// It writes a brief "interrupted" message to stderr and returns the
// canonical interrupted-execution exit code (130) when err is a context
// cancellation or timeout. Returns (0, false) for nil or any
// non-cancellation error, letting the caller fall through to its own
// error path (per-file accumulation, custom prefixes, etc.).
//
// Exit code 130 is the canonical signal-driven exit (128 + SIGINT) and
// is reused here for any cancellation observed by the helper — SIGINT,
// SIGTERM (both wired via signal.NotifyContext in main()), and explicit
// context.DeadlineExceeded from a timeout context. The mapping treats
// any interrupted-execution path as "exit 130" for CLI-facing
// simplicity, rather than trying to recover the original signal
// (which signal.NotifyContext doesn't expose downstream).
func handleCtxErr(err error, stderr io.Writer) (int, bool) {
	if err == nil {
		return 0, false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(stderr, "tfdry: interrupted")
		return 130, true
	}
	return 0, false
}

// handleFatalErr is the "any error is fatal to the run" companion to
// handleCtxErr. It categorizes an error from a top-level orchestration
// call (one that can't continue past an error) into a process exit
// code, writes a user-facing message to stderr, and reports whether
// the caller should return that code immediately.
//
// Three outcomes:
//
//	nil                          -> (0, false), no stderr — caller proceeds.
//	context.Canceled / Deadline  -> (130, true) + "tfdry: interrupted"
//	any other non-nil error      -> (2, true)  + "<prefix>: <err>"
//
// prefix is the subcommand label that scopes the error message
// (e.g., "tfdry" for the main path, "tfdry fmt" for the fmt
// subcommand). The cancellation message stays uniform across
// subcommands because "tfdry: interrupted" is the existing
// user-facing contract for exit code 130; only the non-cancellation
// branch uses the prefix.
//
// Use handleFatalErr at call sites where any error from a checker
// orchestration call (ParseDir/Run/FixFormat/ctx.Err()) is fatal —
// there's nothing useful to continue with after the failure. Use
// handleCtxErr directly (cancel-only) at call sites that accumulate
// per-file errors and want to continue past non-cancellation
// failures, like the WriteFormatted loops in runFmt/runFmtFile
// where one unwriteable file shouldn't abort the rest of the batch.
func handleFatalErr(err error, stderr io.Writer, prefix string) (int, bool) {
	if err == nil {
		return 0, false
	}
	if code, ok := handleCtxErr(err, stderr); ok {
		return code, true
	}
	fmt.Fprintln(stderr, prefix+":", err)
	return 2, true
}

// run executes the CLI with the given args, writing user output to stdout and
// errors/diagnostics to stderr. Returns the exit code:
//   - 0 = clean (no violations found, or all fixed)
//   - 1 = one or more lint violations found by the lint pass
//     (E001-E008, excluding E000 — see exit 2)
//   - 2 = tool error — covers usage mistakes (unknown flags, misplaced
//     subcommand args), I/O failures (unreadable directories, oversize
//     files, write failures during --fix or fmt), stdout broken-pipe /
//     short-write failures, parse errors in fmt subcommand, AND any
//     E000 violation emitted by the checker package (E000 represents a
//     tool-side "couldn't process this input" condition, semantically
//     distinct from "lint found issues in user code"). Exit 2 takes
//     precedence over exit 1 when both are present — a tool failure
//     means some files weren't actually checked.
//   - 3 = `fmt -check` found unformatted files
//   - 130 = interrupted execution. Set whenever a checker call returns
//     context.Canceled or context.DeadlineExceeded — i.e. SIGINT or
//     SIGTERM (both wired via signal.NotifyContext in main()), or an
//     explicit context.WithTimeout from a future caller. 130 is the
//     canonical exit code for SIGINT (128 + 2); the helper reuses it
//     for SIGTERM and deadlines too so the CLI's "tool was
//     interrupted" semantics are uniform across cancellation sources.
//
// ctx is the cancellation token created by [main] via signal.NotifyContext.
// Pure of os.Args / os.Exit / os.Stdout for testability.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// True early-exit pre-scan for --help/-h and --version/-v. These
	// flags must succeed regardless of any other arguments in argv —
	// the universal CLI convention is that help and version output
	// take precedence over validation errors. Handled here, before
	// the main parsing loop, so that later validation (extra
	// positional args, subcommand conflicts, unknown flags) can't
	// short-circuit them.
	for _, arg := range args {
		switch arg {
		case "--version", "-v":
			return runWrite(printVersion, stdout, stderr)
		case "--help", "-h":
			return runWrite(printUsage, stdout, stderr)
		}
	}

	// Main parse: collect flags, subcommand, and directory. Order of
	// flags relative to subcommand name doesn't matter — everything
	// is accumulated in one pass, then dispatched below.
	jsonFlag := false
	fixFlag := false
	fmtCheck := false
	recursive := false
	var checksFilter checker.CheckSet
	dir := "."
	dirSet := false
	subcmd := ""

	for _, arg := range args {
		switch {
		case arg == "--json":
			jsonFlag = true
		case arg == "--fix":
			fixFlag = true
		case arg == "-check" || arg == "--check":
			fmtCheck = true
		case arg == "-recursive" || arg == "--recursive" || arg == "-r":
			recursive = true
		case strings.HasPrefix(arg, "--checks="):
			rawCodes := strings.Split(strings.TrimPrefix(arg, "--checks="), ",")
			var codes []string
			for _, c := range rawCodes {
				c = strings.TrimSpace(c)
				if c != "" {
					codes = append(codes, c)
				}
			}
			// Empty --checks= (no codes after splitting) disables all checks —
			// treat as an error so the user doesn't silently get no output.
			if len(codes) == 0 {
				fmt.Fprintln(stderr, "tfdry: --checks= requires at least one check code")
				return 2
			}
			if err := checker.ValidateCheckCodes(codes); err != nil {
				fmt.Fprintln(stderr, "tfdry: "+err.Error())
				return 2
			}
			// Accumulate when --checks= is repeated: `--checks=E001
			// --checks=E002` is equivalent to `--checks=E001,E002`. Initialise
			// only on first use; reuse the existing set on subsequent flags.
			if checksFilter == nil {
				checksFilter = make(checker.CheckSet)
			}
			for _, c := range codes {
				checksFilter[c] = struct{}{}
			}
		case arg == "describe" || arg == "version" || arg == "fmt" || arg == "help":
			if subcmd != "" {
				fmt.Fprintf(stderr, "tfdry: unexpected subcommand %q after %q\n", arg, subcmd)
				return 2
			}
			subcmd = arg
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "tfdry: unrecognized flag %q\n", arg)
			return 2
		default:
			if dirSet {
				fmt.Fprintf(stderr, "tfdry: unexpected extra argument %q\n", arg)
				return 2
			}
			dir = arg
			dirSet = true
		}
	}
	// describe / version / help do not take a positional argument.
	if (subcmd == "describe" || subcmd == "version" || subcmd == "help") && dirSet {
		fmt.Fprintf(stderr, "tfdry: %s does not accept a positional argument\n", subcmd)
		return 2
	}
	// -check only applies to the `fmt` subcommand. Reject early so a
	// user who types `tfdry -check ./infra` (expecting a format check)
	// gets a clear error instead of a silent lint pass with the flag
	// ignored.
	if fmtCheck && subcmd != "fmt" {
		fmt.Fprintln(stderr, "tfdry: -check is only valid with the fmt subcommand")
		return 2
	}
	// -recursive applies to the lint path (empty subcmd) and the `fmt`
	// subcommand. On other subcommands (describe / version / help) it
	// has no meaning, so reject to surface user mistakes rather than
	// silently ignore the flag.
	if recursive && subcmd != "" && subcmd != "fmt" {
		fmt.Fprintln(stderr, "tfdry: -recursive is only valid with the lint and fmt commands")
		return 2
	}
	// Symmetric to the -check/-recursive guards above — --json / --fix
	// / --checks= are lint-path
	// flags and don't apply to the `fmt` subcommand. fmt has its own
	// stdout contract (prints filenames) and exit codes (3 for -check),
	// always rewrites in non-check mode, and only does formatting (no
	// individual check filtering). Silently ignoring these flags would
	// leave the user thinking they took effect.
	if subcmd == "fmt" {
		if jsonFlag {
			fmt.Fprintln(stderr, "tfdry: --json is not valid with the fmt subcommand")
			return 2
		}
		if fixFlag {
			fmt.Fprintln(stderr, "tfdry: --fix is not valid with the fmt subcommand")
			return 2
		}
		if checksFilter != nil {
			fmt.Fprintln(stderr, "tfdry: --checks= is not valid with the fmt subcommand")
			return 2
		}
	}

	switch subcmd {
	case "describe":
		return runDescribe(stdout, stderr, jsonFlag)
	case "version":
		return runWrite(printVersion, stdout, stderr)
	case "help":
		return runWrite(printUsage, stdout, stderr)
	case "fmt":
		return runFmt(ctx, stdout, stderr, dir, fmtCheck, recursive)
	}

	// When --fix is enabled, skip E008 in the initial Run pass.
	// `checker.Run` would otherwise format every file just to emit E008,
	// and `FixFormat` formats them again to write — doubling the
	// hclwrite.Format work per dirty file. By disabling E008 here,
	// FixFormat becomes the single emitter of E008 (for files it can't
	// write — see FixFormat in checker/format.go which appends E008 alongside
	// E000 on write failure so the actionable signal is preserved).
	runFilter := checksFilter
	shouldFix := fixFlag && checksFilter.Enabled("E008")
	if shouldFix {
		runFilter = checksFilterWithout(checksFilter, "E008")
	}
	// CheckSet uses an empty/nil map as the implicit "all enabled"
	// sentinel. If the user passed `--checks=E008 --fix`, removing E008
	// from a single-element filter yields an empty CheckSet — which Run
	// would interpret as "run everything", silently subverting the
	// user's filter. Detect that case (originally non-empty filter that
	// emptied out via exclusion) and skip Run entirely.
	skipRun := shouldFix && len(checksFilter) > 0 && len(runFilter) == 0

	// Clean the root once so path comparisons downstream are
	// consistent. checker.ParseDir applies filepath.Clean internally,
	// so v.File from a directory-level E000 (v.File == dir) comes back
	// in cleaned form. Without matching that at the caller, a
	// dot-prefixed CLI arg like `./infra` yields WalkDir results
	// still bearing the `./` while ParseDir's E000 emissions carry the
	// cleaned `infra` — displayPath's `vFile == dir` guard then fails
	// string comparison and the fallback join produces duplicated
	// path segments. `dir` itself is preserved unchanged for
	// report.directory (user-visible field). rootClean drives the walk
	// and displayPath so the whole per-directory pipeline sees a
	// single consistent representation.
	//
	// Cleaning also has to happen BEFORE the symlink/file validation
	// below because of a POSIX quirk: os.Lstat on a symlink path with
	// a trailing slash (e.g. `link/`) resolves the symlink to the
	// target directory rather than returning symlink info. Without
	// cleaning first, `tfdry --recursive link/` would pass the
	// symlink check, then filepath.WalkDir would (correctly) see the
	// cleaned form as a symlink and refuse to recurse — producing an
	// empty walk and a silent exit-0 no-op. Cleaning the input into
	// rootClean means both os.Lstat here and WalkDir inside collectDirs
	// see the same normalised form, and the symlink guard fires.
	rootClean := filepath.Clean(dir)

	// Root validation for --recursive. filepath.WalkDir is Lstat-based:
	// given a file-path or symlink-to-dir root it invokes the walkFn
	// once with (path, non-dir DirEntry, nil), collectDirs skips it
	// (!IsDir or the symlink-hidden-name check), and the walk returns
	// (empty, nil) — producing an empty report and exit 0, a silent
	// no-op. Reject both cases up-front so misuse surfaces with a
	// clear error, mirroring runFmt's symlink/file-with-recursive
	// discipline. Only applied when recursive is set; non-recursive
	// lint's behaviour on file paths is unchanged (ParseDir surfaces
	// the error naturally).
	if recursive {
		li, err := os.Lstat(rootClean)
		if err != nil {
			fmt.Fprintln(stderr, "tfdry:", err)
			return 2
		}
		if li.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(stderr, "tfdry: refusing to operate on symlinked path: %s\n", dir)
			return 2
		}
		if !li.IsDir() {
			fmt.Fprintf(stderr, "tfdry: --recursive cannot be used with a file path: %s\n", dir)
			return 2
		}
	}

	// Directory list to lint. Non-recursive: exactly rootClean.
	// Recursive: full walk, skipping hidden dirs and node_modules.
	// Shares collectDirs with fmt -recursive for parity of skip logic.
	dirs, err := collectDirs(rootClean, recursive)
	if code, ok := handleFatalErr(err, stderr, "tfdry"); ok {
		return code
	}

	var violations []checker.Violation
	for _, d := range dirs {
		// Per-directory cancel checkpoint. On large monorepos the
		// walker may cover hundreds of subdirs; a SIGINT mid-walk
		// should bail before parsing the next directory. Mirrors the
		// runFmt loop's checkpoint discipline.
		if code, ok := handleFatalErr(ctx.Err(), stderr, "tfdry"); ok {
			return code
		}

		files, parseViolations, err := checker.ParseDir(ctx, d)
		if code, ok := handleFatalErr(err, stderr, "tfdry"); ok {
			return code
		}

		// Parse violations (E000, E001) are always emitted — not
		// subject to --checks filtering.
		dirViolations := append([]checker.Violation{}, parseViolations...)

		// Guard the Run and FixFormat calls behind len(files) > 0.
		// Both are safe no-ops on empty input, but for large
		// monorepos with many empty-of-.tf directories the
		// function-call overhead adds up. The guard is around
		// these two calls specifically (not the whole iteration)
		// because parseViolations still needs to surface: a
		// subdirectory where every .tf file failed to parse gives
		// (files empty, parseViolations non-empty), and a directory-
		// level E000 (unreadable dir) does the same. Dropping the
		// iteration wholesale would swallow those signals.
		if len(files) > 0 {
			if !skipRun {
				runViolations, err := checker.Run(ctx, files, runFilter, d)
				if code, ok := handleFatalErr(err, stderr, "tfdry"); ok {
					return code
				}
				dirViolations = append(dirViolations, runViolations...)
			}

			if shouldFix {
				_, fixViolations, err := checker.FixFormat(ctx, files, d)
				if code, ok := handleFatalErr(err, stderr, "tfdry"); ok {
					return code
				}
				dirViolations = append(dirViolations, fixViolations...)
			}
		}

		// Prefix v.File with the sub-path relative to the CLI arg,
		// but ONLY for the recursive case. In non-recursive mode
		// (dirs == [rootClean], one iteration), rewriting v.File is
		// not a no-op for directory-level E000 violations: when
		// ParseDir emits v.File == d, displayPath returns "." (the
		// self-relative sub-path), which is a schema regression from
		// v0.1.1 where those violations carried the directory path.
		// Bare-filename violations (E001, E006, etc.) are unaffected
		// either way (displayPath("dir", "dir", "main.tf") returns
		// "main.tf" identically to v0.1.1). Gating on `recursive`
		// keeps the non-recursive path byte-for-byte compatible.
		//
		// For --recursive, this is what turns "main.tf" into
		// "staging/main.tf" so consumers can attribute violations to
		// a specific workspace directory. Uses rootClean (not the
		// raw CLI arg) so ParseDir's internal filepath.Clean matches
		// what displayPath compares against — see the rootClean
		// comment above.
		if recursive {
			for i := range dirViolations {
				dirViolations[i].File = displayPath(rootClean, d, dirViolations[i].File)
			}
		}
		violations = append(violations, dirViolations...)
	}

	report := output.NewReport(dir, violations)

	if jsonFlag {
		if err := output.WriteJSON(stdout, report); err != nil {
			fmt.Fprintln(stderr, "error writing output:", err)
			return 2
		}
	} else {
		if err := output.WriteHuman(stdout, report); err != nil {
			fmt.Fprintln(stderr, "error writing output:", err)
			return 2
		}
	}

	// Exit-code routing for violation-bearing runs:
	//
	//   - ToolErrors > 0  → exit 2 (E000: unreadable dir, oversize file,
	//                       write failure). The tool itself couldn't run
	//                       cleanly on the input — that's semantically
	//                       different from "lint found issues in user
	//                       code", and the CLI contract in README /
	//                       SKILL.md documents exit 2 for this class.
	//                       Takes precedence over Errors > 0 because a
	//                       tool failure means some files weren't
	//                       actually checked; the user needs to see the
	//                       loud signal rather than the routine exit-1.
	//   - Errors > 0      → exit 1 (lint found issues in user code).
	//   - else            → exit 0.
	//
	// Note: Summary.Errors counts ALL error-severity violations including
	// E000, so the human-output "X error(s) found" line and JSON
	// `summary.errors` stay backwards-compatible. ToolErrors is the
	// E000-only sub-count we route on.
	if report.Summary.ToolErrors > 0 {
		return 2
	}
	if report.Summary.Errors > 0 {
		return 1
	}
	return 0
}

func runDescribe(stdout, stderr io.Writer, asJSON bool) int {
	checks := checker.AllChecks()
	if asJSON {
		if err := output.WriteChecksJSON(stdout, checks); err != nil {
			fmt.Fprintln(stderr, "tfdry: error writing output:", err)
			return 2
		}
		return 0
	}
	// Mirror the JSON path's write-error
	// propagation. Build into a buffer first so a single Write either
	// fully succeeds or fully fails — keeps "describe" output atomic
	// from a stdout consumer's perspective and lets us detect the
	// failure with one error check.
	//
	// Use bytes.Buffer.WriteTo rather than stdout.Write(b.Bytes())
	// so a spec-violating Writer that silently short-writes (returns
	// n < len(p) with nil error) still surfaces io.ErrShortWrite.
	var b bytes.Buffer
	fmt.Fprintln(&b, "tfdry checks:")
	fmt.Fprintln(&b)
	for _, c := range checks {
		fmt.Fprintf(&b, "  %-6s  %-8s  %s\n", c.Code, c.Severity, c.Summary)
	}
	if _, err := b.WriteTo(stdout); err != nil {
		fmt.Fprintln(stderr, "tfdry: error writing output:", err)
		return 2
	}
	return 0
}

// runWrite executes fn to produce output on stdout and returns the exit
// code: 0 if the write succeeded, 2 (with a stderr message) if it failed.
// Encapsulates the exit-2-on-write-failure contract used by --version,
// --help, and their subcommand forms so all four entry points map write
// failures uniformly per run()'s documented exit-code contract.
func runWrite(fn func(io.Writer) error, stdout, stderr io.Writer) int {
	if err := fn(stdout); err != nil {
		fmt.Fprintln(stderr, "tfdry: error writing output:", err)
		return 2
	}
	return 0
}

// printVersion writes the version line to the given writer. Used by
// --version, -v, and the 'version' subcommand. Returns any write error
// so callers can map stdout failures (broken pipe, short write, etc.)
// to exit 2 per run()'s documented contract. Buffered write via
// bytes.Buffer.WriteTo detects both real errors and spec-violating
// short-writes-without-error uniformly.
func printVersion(w io.Writer) error {
	var b bytes.Buffer
	fmt.Fprintln(&b, "tfdry", output.Version)
	_, err := b.WriteTo(w)
	return err
}

// printUsage writes top-level help text to the given writer. Used by
// --help, -h, and the 'help' subcommand. Returns any write error so
// callers can map stdout failures to exit 2 (same contract as
// printVersion above).
func printUsage(w io.Writer) error {
	var b bytes.Buffer
	fmt.Fprintln(&b, "Usage: tfdry [flags] [-recursive] [directory]")
	fmt.Fprintln(&b, "       tfdry fmt [-check] [-recursive] [path]")
	fmt.Fprintln(&b, "       tfdry describe [--json]")
	fmt.Fprintln(&b, "       tfdry version")
	fmt.Fprintln(&b, "       tfdry help")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Fast, focused Terraform linting — no init, no state, no network.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Flags:")
	fmt.Fprintln(&b, "  --checks=CODES                 Comma-separated allow-list of check codes (e.g. E003,E004).")
	fmt.Fprintln(&b, "  --fix                          Rewrite files in place to fix E008 (formatting).")
	fmt.Fprintln(&b, "  --json                         Machine-readable JSON output.")
	fmt.Fprintln(&b, "  -recursive, --recursive, -r    Recurse into subdirectories (lint and fmt).")
	fmt.Fprintln(&b, "  --help, -h                     Show this help and exit.")
	fmt.Fprintln(&b, "  --version, -v                  Print version and exit.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "fmt subcommand flags:")
	fmt.Fprintln(&b, "  -check, --check                Report files that would be reformatted; exit 3 if any (no changes made).")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Exit codes:")
	fmt.Fprintln(&b, "  0   No violations (or all fixed by --fix).")
	fmt.Fprintln(&b, "  1   Lint violations found.")
	fmt.Fprintln(&b, "  2   Tool error (bad arguments, unreadable input, write failure).")
	fmt.Fprintln(&b, "  3   tfdry fmt -check found unformatted files.")
	_, err := b.WriteTo(w)
	return err
}

// runFmt implements `tfdry fmt`, modelled on `terraform fmt`:
//   - default: rewrite unformatted .tf files in dir, print filenames changed
//   - -check: don't rewrite, print filenames that would change, exit 3 if any
//   - -recursive: walk subdirs (skip hidden ones, e.g. .terraform/.git)
//
// `path` may be either a directory or a single file (terraform fmt
// parity). With a single file, `-recursive` is rejected as nonsensical.
//
// Exit codes match terraform fmt:
//   - 0 = success (clean, or successfully rewrote)
//   - 2 = parse / write error / bad usage
//   - 3 = -check found unformatted files
func runFmt(ctx context.Context, stdout, stderr io.Writer, path string, check, recursive bool) int {
	// Entry-level cancel checkpoint. Without this, a pre-cancelled ctx
	// still pays for os.Lstat on the supplied path plus a potentially
	// deep collectDirs walk in -recursive mode before the per-dir
	// check (below) fires. Mirror the runFmtFile pattern (PR A2 round 1)
	// so both fmt entry points behave identically on entry-cancel.
	if code, ok := handleFatalErr(ctx.Err(), stderr, "tfdry fmt"); ok {
		return code
	}
	// Reject symlinked roots up front (consistent with file-mode symlink
	// rejection in runFmtFile, round 4). Without this, a symlinked-dir
	// root produces inconsistent behaviour: ParseDir / os.ReadDir follows
	// symlinks but filepath.WalkDir is Lstat-based and silently does
	// nothing for `fmt -recursive`, exiting 0 with no output.
	// Reject in both modes so the security/atomicity contract of the path
	// argument is uniform regardless of -recursive.
	//
	// filepath.Clean before Lstat handles the POSIX trailing-slash
	// quirk: os.Lstat on a symlink with `/` suffix (e.g. `link/`)
	// resolves the symlink to the target directory rather than
	// returning symlink info, so `Mode & ModeSymlink` is 0 and the
	// guard silently passes. filepath.WalkDir then sees the cleaned
	// form as a symlink and refuses to recurse, producing an empty
	// walk and a silent exit-0 no-op. Cleaning first means the Lstat
	// here and the Lstat inside WalkDir see the same normalised
	// shape. Error messages still use the user-supplied `path` so
	// the reported path matches what the caller typed.
	pathClean := filepath.Clean(path)
	if li, err := os.Lstat(pathClean); err == nil && li.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintf(stderr, "tfdry fmt: refusing to operate on symlinked path: %s\n", path)
		return 2
	}
	fi, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(stderr, "tfdry fmt:", err)
		return 2
	}
	if !fi.IsDir() {
		if recursive {
			fmt.Fprintf(stderr, "tfdry fmt: -recursive cannot be used with a file path: %s\n", path)
			return 2
		}
		return runFmtFile(ctx, stdout, stderr, path, check)
	}

	dirs, err := collectDirs(pathClean, recursive)
	if err != nil {
		fmt.Fprintln(stderr, "tfdry fmt:", err)
		return 2
	}

	anyDirty := false
	anyError := false

	for _, d := range dirs {
		// Per-directory cancel checkpoint. The runFmt walker may cover
		// large monorepos with hundreds of subdirs; a SIGINT mid-walk
		// should bail before parsing the next directory. handleFatalErr
		// covers both cancellation (exit 130) and the defensive
		// non-cancellation branch (exit 2 with "tfdry fmt:" prefix).
		if code, ok := handleFatalErr(ctx.Err(), stderr, "tfdry fmt"); ok {
			return code
		}
		files, parseViolations, err := checker.ParseDir(ctx, d)
		if code, ok := handleFatalErr(err, stderr, "tfdry fmt"); ok {
			return code
		}
		for _, v := range parseViolations {
			// Show the path relative to the user-supplied root so a
			// recursive run reports the subdir, not just a bare filename
			// that may exist under several subdirs. The helper
			// guards against the dir-level case where v.File == d, which
			// would otherwise duplicate the path.
			//
			// Filenames and HCL diagnostic text can contain ANSI
			// escapes / Bidi-override / newline characters from
			// attacker-controlled .tf content. Sanitize before printing
			// to prevent terminal-injection / line-injection in fmt output.
			fmt.Fprintf(stderr, "Error: %s: %s\n",
				output.Sanitize(displayPath(pathClean, d, v.File)),
				output.Sanitize(v.Message))
			anyError = true
		}
		for _, f := range files {
			// Per-file cancel checkpoint. Without this, SIGINT
			// during the format-and-emit loop is ignored for the rest
			// of the current directory — particularly noticeable in
			// -check mode where WriteFormatted (which has its own
			// ctx check at entry) is never reached, so cancellation
			// would only land at the NEXT directory's outer check.
			// Uses handleFatalErr for consistency with the outer
			// per-directory check at the top of this loop.
			if code, ok := handleFatalErr(ctx.Err(), stderr, "tfdry fmt"); ok {
				return code
			}
			if f.Src == nil {
				continue
			}
			formatted := hclwrite.Format(f.Src)
			if bytes.Equal(f.Src, formatted) {
				continue
			}
			anyDirty = true
			absFile := filepath.Join(d, f.Name)
			// Same sanitisation for the dirty-file path printed to
			// stdout (the user-facing list of formatted files).
			relPath := output.Sanitize(displayPath(pathClean, d, f.Name))
			fmt.Fprintln(stdout, relPath)
			if !check {
				if err := checker.WriteFormatted(ctx, absFile, formatted); err != nil {
					if code, ok := handleCtxErr(err, stderr); ok {
						return code
					}
					fmt.Fprintln(stderr, "Error formatting", relPath+":", err)
					anyError = true
				}
			}
		}
	}

	if anyError {
		return 2
	}
	if check && anyDirty {
		return 3
	}
	return 0
}

// runFmtFile formats a single file path, the file-mode counterpart of the
// directory-walking branch in runFmt. Mirrors terraform fmt's behaviour for
// individual files: prints the path on stdout when dirty, rewrites in-place
// unless `check` is set, and uses exit code 3 only when -check finds dirt.
//
// Symlinks are rejected: without Lstat here, `-check` would follow the
// symlink at os.ReadFile and exit 3 if the target was dirty, while a write
// pass would later destroy the symlink on Windows (oNoFollow=0). Reject
// upfront so the failure mode is identical across read/write/platforms.
func runFmtFile(ctx context.Context, stdout, stderr io.Writer, path string, check bool) int {
	if code, ok := handleFatalErr(ctx.Err(), stderr, "tfdry fmt"); ok {
		return code
	}
	if li, err := os.Lstat(path); err == nil && li.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintf(stderr, "tfdry fmt: %s: not a regular file (symlinks are not supported)\n", path)
		return 2
	}
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "tfdry fmt:", err)
		return 2
	}
	// Parse for syntax errors before formatting. Directory mode
	// surfaces parse errors via E001/exit 2; without this check, single-
	// file mode would silently format invalid HCL (best-effort token
	// reshuffling), exit 0, and leave the user thinking the file is
	// fine. Parse failure → exit 2 with a stderr message identifying
	// the file and the diagnostic.
	if _, diags := hclsyntax.ParseConfig(src, filepath.Base(path), hcl.Pos{Line: 1, Column: 1}); diags.HasErrors() {
		// Sanitize path and message before printing — both can
		// carry attacker-controlled control / Bidi characters from the
		// caller-supplied path or HCL diagnostic text.
		safePath := output.Sanitize(path)
		for _, d := range diags {
			if d.Severity != hcl.DiagError {
				continue
			}
			line := 0
			if d.Subject != nil {
				line = d.Subject.Start.Line
			}
			msg := d.Detail
			if msg == "" {
				msg = d.Summary
			}
			if msg == "" {
				msg = "parse error"
			}
			msg = output.Sanitize(msg)
			if line > 0 {
				fmt.Fprintf(stderr, "Error: %s:%d: %s\n", safePath, line, msg)
			} else {
				fmt.Fprintf(stderr, "Error: %s: %s\n", safePath, msg)
			}
		}
		return 2
	}
	formatted := hclwrite.Format(src)
	if bytes.Equal(src, formatted) {
		return 0
	}
	// Sanitize the file path before printing to stdout — the path
	// came from the user's argv but could legitimately contain control
	// chars on Unix (filenames are byte strings).
	safePath := output.Sanitize(path)
	fmt.Fprintln(stdout, safePath)
	if check {
		return 3
	}
	if err := checker.WriteFormatted(ctx, path, formatted); err != nil {
		if code, ok := handleCtxErr(err, stderr); ok {
			return code
		}
		fmt.Fprintln(stderr, "Error formatting", safePath+":", err)
		return 2
	}
	return 0
}

// checksFilterWithout returns a CheckSet equivalent to filter but with code
// disabled. Used by --fix to skip E008 in the initial checker.Run pass:
// since FixFormat will compute the formatted bytes itself, having Run also
// format every file just to emit E008 is wasted work. When filter is nil/
// empty (the implicit "all enabled" sentinel), this expands the AllChecks()
// list and removes the named code so the result is "all except code".
func checksFilterWithout(filter checker.CheckSet, code string) checker.CheckSet {
	out := make(checker.CheckSet)
	if len(filter) == 0 {
		for _, c := range checker.AllChecks() {
			if c.Code != code {
				out[c.Code] = struct{}{}
			}
		}
		return out
	}
	for k := range filter {
		if k != code {
			out[k] = struct{}{}
		}
	}
	return out
}

// displayPath formats the path embedded in a violation for output,
// relative to the user-supplied root when possible. Used by both the
// fmt subcommand's stderr/stdout output and the lint --recursive path
// (where it prefixes bare filenames with the sub-path relative to
// the CLI arg, so consumers can attribute violations to a specific
// workspace directory).
//
// vFile is normally a basename (file-level violations like E001 carry just
// the .tf filename), in which case we join it under dir and relativize.
// However, ParseDir can also emit a directory-level E000 where vFile == dir
// (the directory path itself, not a filename) — e.g. a TOCTOU race where
// a recursively-walked subdir becomes unreadable between WalkDir scheduling
// and ParseDir reading it. Naively joining dir + vFile in that case yields
// "<dir>/<dir>". We detect that and absolute-path cases and treat
// vFile as already-a-path. Falls back to the absolute path when filepath.Rel
// can't compute one (e.g. different drives on Windows).
//
// The returned path always uses forward slashes regardless of host OS, so
// output (stderr for fmt, stdout for both, plus JSON `Violation.File`)
// stays stable across platforms and integration tests don't have to pivot
// on the runtime separator. Windows handles `/` everywhere in modern
// shells and standard library APIs, so normalising to `/` is a UX win
// (consistent across platforms) and a testing win (no `filepath.Join`
// dance in every assertion).
func displayPath(rootArg, dir, vFile string) string {
	var abs string
	switch {
	case vFile == "" || vFile == dir:
		abs = dir
	case filepath.IsAbs(vFile):
		abs = vFile
	default:
		abs = filepath.Join(dir, vFile)
	}
	if rel, err := filepath.Rel(rootArg, abs); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(abs)
}

// collectDirs walks the tree from root and returns the directories to
// operate on. Non-recursive mode returns exactly the root. Recursive
// mode returns every directory under root, skipping directories that
// never contain Terraform in practice:
//
//   - Dot-prefixed hidden directories (.terraform, .git, .hidden, etc.)
//   - node_modules (polyglot-monorepo staple, no Terraform inside)
//
// Both skips are applied only to descendants of root — root itself is
// always included regardless of its name. Shared by fmt -recursive
// and lint --recursive.
//
// Per-directory readdir errors during the walk are non-fatal: the
// affected directory has already been added to `dirs` on the first
// walkFn invocation (with walkErr=nil), so downstream ParseDir/format
// calls will re-hit the same failure and surface it through the
// normal violation path (E000 for lint, stderr diagnostic for fmt).
// Aborting the whole walk on a single dir's permission error would
// otherwise skip peer directories that were perfectly readable, and
// break the "aggregated Report even on tool errors" contract that
// non-recursive lint honours for `--json` consumers.
//
// The only walkErr case treated as fatal is the initial Lstat on
// `root` failing (d == nil). That's a race window with any caller-side
// pre-validation, and there are no dirs yet to surface the error
// through the aggregation path, so we let WalkDir return it.
func collectDirs(root string, recursive bool) ([]string, error) {
	if !recursive {
		return []string{root}, nil
	}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d == nil {
				// Initial Lstat on root failed. No dir has been
				// added yet; nothing downstream can surface this.
				return walkErr
			}
			// Per-directory readdir failure. `path` was already
			// added to dirs on the first walkFn call (before
			// readdir was attempted). Return nil so WalkDir
			// continues walking siblings; the downstream call
			// site will hit the same error via os.ReadDir and
			// emit a proper violation.
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	return dirs, err
}
