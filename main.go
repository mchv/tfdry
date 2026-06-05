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
			checksFilter = make(checker.CheckSet)
			for _, c := range codes {
				checksFilter[c] = struct{}{}
			}
		case arg == "describe" || arg == "version" || arg == "fmt":
			subcmd = arg
		case !strings.HasPrefix(arg, "-"):
			dir = arg
		}
	}

	switch subcmd {
	case "describe":
		runDescribe(stdout, jsonFlag)
		return 0
	case "version":
		fmt.Fprintln(stdout, "tfdry", output.Version)
		return 0
	case "fmt":
		return runFmt(stdout, stderr, dir, fmtCheck, fmtRecursive)
	}

	files, parseViolations := checker.ParseDir(dir)

	// Parse violations (E000, E001) are always emitted — not subject to --checks filtering.
	violations := append([]checker.Violation{}, parseViolations...)
	violations = append(violations, checker.Run(files, checksFilter, dir)...)

	// --fix: rewrite unformatted files when E008 is enabled.
	// Only removes E008 from output for files that were successfully written.
	if fixFlag && checksFilter.Enabled("E008") {
		fixed, fixViolations := checker.FixFormat(files, dir)
		violations = append(violations, fixViolations...)
		var filtered []checker.Violation
		for _, v := range violations {
			if v.Code == "E008" && fixed[v.File] {
				continue
			}
			filtered = append(filtered, v)
		}
		violations = filtered
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

func runDescribe(stdout io.Writer, asJSON bool) {
	checks := checker.AllChecks()
	if asJSON {
		_ = output.WriteChecksJSON(stdout, checks)
		return
	}
	fmt.Fprintln(stdout, "tfdry checks:")
	fmt.Fprintln(stdout)
	for _, c := range checks {
		fmt.Fprintf(stdout, "  %-6s  %-8s  %s\n", c.Code, c.Severity, c.Summary)
	}
}

// runFmt implements `tfdry fmt`, modelled on `terraform fmt`:
//   - default: rewrite unformatted .tf files in dir, print filenames changed
//   - -check: don't rewrite, print filenames that would change, exit 3 if any
//   - -recursive: walk subdirs (skip hidden ones, e.g. .terraform/.git)
//
// Exit codes match terraform fmt:
//   - 0 = success (clean, or successfully rewrote)
//   - 2 = parse / write error
//   - 3 = -check found unformatted files
func runFmt(stdout, stderr io.Writer, dir string, check, recursive bool) int {
	dirs, err := collectFmtDirs(dir, recursive)
	if err != nil {
		fmt.Fprintln(stderr, "tfdry fmt:", err)
		return 2
	}

	anyDirty := false
	anyError := false

	for _, d := range dirs {
		files, parseViolations := checker.ParseDir(d)
		for _, v := range parseViolations {
			fmt.Fprintf(stderr, "Error: %s: %s\n", v.File, v.Message)
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
			relPath, relErr := filepath.Rel(dir, absFile)
			if relErr != nil {
				relPath = absFile
			}
			fmt.Fprintln(stdout, relPath)
			if !check {
				if err := checker.FormatFile(absFile, f.Src); err != nil {
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
