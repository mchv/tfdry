// Command tfdry validates Terraform files without requiring terraform init or validate.
package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/mchv/tfdry/checker"
	"github.com/mchv/tfdry/output"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI with the given args, writing user output to stdout and
// errors/diagnostics to stderr. Returns the exit code (0 = clean, 1 = errors
// found, 2 = usage error, 3 = `fmt -check` found unformatted files). Pure of
// os.Args / os.Exit / os.Stdout for testability.
func run(args []string, stdout, stderr io.Writer) int {
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
			// Accumulate when --checks= is repeated (G15): `--checks=E001
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
	// ignored (C19).
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

	switch subcmd {
	case "describe":
		return runDescribe(stdout, stderr, jsonFlag)
	case "version":
		fmt.Fprintln(stdout, "tfdry", output.Version)
		return 0
	case "fmt":
		return runFmt(stdout, stderr, dir, fmtCheck, fmtRecursive)
	}

	files, parseViolations := checker.ParseDir(dir)

	// Parse violations (E000, E001) are always emitted — not subject to --checks filtering.
	violations := append([]checker.Violation{}, parseViolations...)

	// G21: when --fix is enabled, skip E008 in the initial Run pass.
	// `checker.Run` would otherwise format every file just to emit E008,
	// and `FixFormat` formats them again to write — doubling the
	// hclwrite.Format work per dirty file. By disabling E008 here,
	// FixFormat becomes the single emitter of E008 (for files it can't
	// write — see G22 in checker/format.go which appends E008 alongside
	// E000 on write failure so the actionable signal is preserved).
	runFilter := checksFilter
	shouldFix := fixFlag && checksFilter.Enabled("E008")
	if shouldFix {
		runFilter = checksFilterWithout(checksFilter, "E008")
	}
	violations = append(violations, checker.Run(files, runFilter, dir)...)

	if shouldFix {
		_, fixViolations := checker.FixFormat(files, dir)
		violations = append(violations, fixViolations...)
	}

	report := output.NewReport(dir, violations)

	if jsonFlag {
		if err := output.WriteJSON(stdout, report); err != nil {
			fmt.Fprintln(stderr, "error writing output:", err)
			return 2
		}
	} else {
		output.WriteHuman(stdout, report)
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
	fmt.Fprintln(stdout, "tfdry checks:")
	fmt.Fprintln(stdout)
	for _, c := range checks {
		fmt.Fprintf(stdout, "  %-6s  %-8s  %s\n", c.Code, c.Severity, c.Summary)
	}
	return 0
}

// runFmt implements `tfdry fmt`, modelled on `terraform fmt`:
//   - default: rewrite unformatted .tf files in dir, print filenames changed
//   - -check: don't rewrite, print filenames that would change, exit 3 if any
//   - -recursive: walk subdirs (skip hidden ones, e.g. .terraform/.git)
//
// `path` may be either a directory or a single file (G11 — terraform fmt
// parity). With a single file, `-recursive` is rejected as nonsensical.
//
// Exit codes match terraform fmt:
//   - 0 = success (clean, or successfully rewrote)
//   - 2 = parse / write error / bad usage
//   - 3 = -check found unformatted files
func runFmt(stdout, stderr io.Writer, path string, check, recursive bool) int {
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
		return runFmtFile(stdout, stderr, path, check)
	}

	dirs, err := collectFmtDirs(path, recursive)
	if err != nil {
		fmt.Fprintln(stderr, "tfdry fmt:", err)
		return 2
	}

	anyDirty := false
	anyError := false

	for _, d := range dirs {
		files, parseViolations := checker.ParseDir(d)
		for _, v := range parseViolations {
			// Show the path relative to the user-supplied root so a
			// recursive run reports the subdir, not just a bare filename
			// that may exist under several subdirs (G19). Falls back to
			// the absolute path if Rel can't compute one.
			absFile := filepath.Join(d, v.File)
			relPath, relErr := filepath.Rel(path, absFile)
			if relErr != nil {
				relPath = absFile
			}
			fmt.Fprintf(stderr, "Error: %s: %s\n", relPath, v.Message)
			anyError = true
		}
		for _, f := range files {
			if f.Src == nil {
				continue
			}
			formatted := hclwrite.Format(f.Src)
			if bytes.Equal(f.Src, formatted) {
				continue
			}
			anyDirty = true
			absFile := filepath.Join(d, f.Name)
			relPath, relErr := filepath.Rel(path, absFile)
			if relErr != nil {
				relPath = absFile
			}
			fmt.Fprintln(stdout, relPath)
			if !check {
				if err := checker.WriteFormatted(absFile, formatted); err != nil {
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
// Symlinks are rejected (G14): without Lstat here, `-check` would follow the
// symlink at os.ReadFile and exit 3 if the target was dirty, while a write
// pass would later destroy the symlink on Windows (oNoFollow=0). Reject
// upfront so the failure mode is identical across read/write/platforms.
func runFmtFile(stdout, stderr io.Writer, path string, check bool) int {
	if li, err := os.Lstat(path); err == nil && li.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintf(stderr, "tfdry fmt: %s: not a regular file (symlinks are not supported)\n", path)
		return 2
	}
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "tfdry fmt:", err)
		return 2
	}
	formatted := hclwrite.Format(src)
	if bytes.Equal(src, formatted) {
		return 0
	}
	fmt.Fprintln(stdout, path)
	if check {
		return 3
	}
	if err := checker.WriteFormatted(path, formatted); err != nil {
		fmt.Fprintln(stderr, "Error formatting", path+":", err)
		return 2
	}
	return 0
}

// checksFilterWithout returns a CheckSet equivalent to filter but with code
// disabled. Used by --fix (G21) to skip E008 in the initial checker.Run pass:
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
