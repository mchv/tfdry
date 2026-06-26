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
//   - 1 = one or more violations found by the lint pass
//   - 2 = tool error — covers usage mistakes (unknown flags, misplaced
//     subcommand args), I/O failures (unreadable directories, write
//     failures during --fix or fmt), stdout broken-pipe / short-write
//     failures, and parse errors in fmt subcommand
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
	// Pre-scan: collect all flags before dispatching subcommands so that
	// flag order relative to subcommand name doesn't matter.
	jsonFlag := false
	fixFlag := false
	fmtCheck := false
	fmtRecursive := false
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
		case arg == "-recursive" || arg == "--recursive":
			fmtRecursive = true
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
		case arg == "describe" || arg == "version" || arg == "fmt":
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
	// describe / version do not take a positional argument.
	if (subcmd == "describe" || subcmd == "version") && dirSet {
		fmt.Fprintf(stderr, "tfdry: %s does not accept a positional argument\n", subcmd)
		return 2
	}
	// -check / -recursive only apply to the `fmt` subcommand. Reject early
	// so a user who types `tfdry -check ./infra` (expecting a format check)
	// gets a clear error instead of a silent lint pass with the flag
	// ignored.
	if subcmd != "fmt" {
		if fmtCheck {
			fmt.Fprintln(stderr, "tfdry: -check is only valid with the fmt subcommand")
			return 2
		}
		if fmtRecursive {
			fmt.Fprintln(stderr, "tfdry: -recursive is only valid with the fmt subcommand")
			return 2
		}
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
		fmt.Fprintln(stdout, "tfdry", output.Version)
		return 0
	case "fmt":
		return runFmt(ctx, stdout, stderr, dir, fmtCheck, fmtRecursive)
	}

	files, parseViolations, err := checker.ParseDir(ctx, dir)
	if code, ok := handleFatalErr(err, stderr, "tfdry"); ok {
		return code
	}

	// Parse violations (E000, E001) are always emitted — not subject to --checks filtering.
	violations := append([]checker.Violation{}, parseViolations...)

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
	if !skipRun {
		runViolations, err := checker.Run(ctx, files, runFilter, dir)
		if code, ok := handleFatalErr(err, stderr, "tfdry"); ok {
			return code
		}
		violations = append(violations, runViolations...)
	}

	if shouldFix {
		_, fixViolations, err := checker.FixFormat(ctx, files, dir)
		if code, ok := handleFatalErr(err, stderr, "tfdry"); ok {
			return code
		}
		violations = append(violations, fixViolations...)
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

// runFmt implements `tfdry fmt`, modelled on `terraform fmt`:
//   - default: rewrite unformatted .tf files in dir, print filenames changed
//   - -check: don't rewrite, print filenames that would change, exit 3 if any
//   - -recursive: walk subdirs (skip hidden ones, e.g. .terraform/.git)
//
// `path` may be either a directory or a single fileterraform fmt
// parity). With a single file, `-recursive` is rejected as nonsensical.
//
// Exit codes match terraform fmt:
//   - 0 = success (clean, or successfully rewrote)
//   - 2 = parse / write error / bad usage
//   - 3 = -check found unformatted files
func runFmt(ctx context.Context, stdout, stderr io.Writer, path string, check, recursive bool) int {
	// Entry-level cancel checkpoint. Without this, a pre-cancelled ctx
	// still pays for os.Lstat on the supplied path plus a potentially
	// deep collectFmtDirs walk in -recursive mode before the per-dir
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
	if li, err := os.Lstat(path); err == nil && li.Mode()&os.ModeSymlink != 0 {
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

	dirs, err := collectFmtDirs(path, recursive)
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
				output.Sanitize(displayFmtPath(path, d, v.File)),
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
			// Same sanitization for the dirty-file path printed to
			// stdout (the user-facing list of formatted files).
			relPath := output.Sanitize(displayFmtPath(path, d, f.Name))
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

// displayFmtPath formats the path embedded in an fmt-subcommand violation
// for human-friendly stderr output, relative to the user-supplied root
// when possible.
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
func displayFmtPath(rootArg, dir, vFile string) string {
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
		return rel
	}
	return abs
}

// collectFmtDirs returns directories to scan. With recursive=false this is
// just [dir]. With recursive=true it walks dir and includes every subdir
// except those whose name starts with '.' (matches `terraform fmt -recursive`,
// which skips .terraform, .git, .hidden, etc.).
func collectFmtDirs(root string, recursive bool) ([]string, error) {
	if !recursive {
		return []string{root}, nil
	}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	return dirs, err
}
