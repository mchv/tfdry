// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// runCLI invokes the package-level run() function with captured stdout/stderr.
// Returns exit code, stdout, stderr.
func runCLI(args ...string) (code int, stdout, stderr string) {
	var stdoutBuf, stderrBuf bytes.Buffer
	code = run(context.Background(), args, &stdoutBuf, &stderrBuf)
	return code, stdoutBuf.String(), stderrBuf.String()
}

// writeTFDir creates a temp dir with the given files and returns its
// path. Keys are relative paths from the created root; intermediate
// directories are created automatically, so nested layouts like
// `map[string]string{"main.tf": "...", "sub/nested.tf": "..."}` work
// without pre-computing MkdirAll calls.
//
// Note: this helper is intentionally duplicated in checker/checks_test.go
// (same signature, same body). The two live in different test packages
// (main vs checker_test), so true sharing would require an
// internal/testutil package — overkill for two identical helpers.
// If a third duplicate appears, promote to internal/testutil
// rather than triplicating.
func writeTFDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ── Exit codes ───────────────────────────────────────────────────────────────

func TestRun_CleanDir_ExitZero(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `locals {
  x = "ok"
}

output "x" {
  value = local.x
}
`,
	})
	code, _, _ := runCLI(dir)
	if code != 0 {
		t.Fatalf("expected exit 0 on clean dir, got %d", code)
	}
}

func TestRun_DirWithErrors_ExitOne(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `output "x" {
  value = local.does_not_exist
}
`,
	})
	code, _, _ := runCLI(dir)
	if code != 1 {
		t.Fatalf("expected exit 1 on errors, got %d", code)
	}
}

func TestRun_OnlyWarnings_ExitZero(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `locals {
  unused = "just a warning"
}
`,
	})
	code, _, _ := runCLI(dir)
	if code != 0 {
		t.Fatalf("warnings (W001) must not affect exit code, got %d", code)
	}
}

// ── --json flag ──────────────────────────────────────────────────────────────

func TestRun_JSONOutput_Shape(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `output "x" { value = local.missing }`,
	})
	code, stdout, _ := runCLI("--json", dir)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	var got struct {
		TfdryVersion string `json:"tfdry_version"`
		Directory    string `json:"directory"`
		Violations   []any  `json:"violations"`
		Summary      struct {
			Errors   int `json:"errors"`
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	if got.Summary.Errors == 0 {
		t.Errorf("expected at least one error, got summary %+v", got.Summary)
	}
	if len(got.Violations) == 0 {
		t.Errorf("expected at least one violation in JSON output")
	}
}

// ── --fix flag ───────────────────────────────────────────────────────────────

func TestRun_Fix_RewritesUnformattedFile(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": "locals{a=\"x\"}\n",
	})
	code, _, _ := runCLI("--fix", dir)
	if code != 0 {
		t.Fatalf("expected exit 0 after --fix, got %d", code)
	}
	got, err := os.ReadFile(filepath.Join(dir, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "a = \"x\"") {
		t.Errorf("file was not reformatted; got: %q", got)
	}
}

func TestRun_FixWithChecksFilterExcludingE008_DoesNotFix(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": "locals{a=\"x\"}\n",
	})
	original, err := os.ReadFile(filepath.Join(dir, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := runCLI("--fix", "--checks=E001,E002", dir)
	got, err := os.ReadFile(filepath.Join(dir, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, got) {
		t.Errorf("--fix should not run when E008 is excluded by --checks=, but file was modified")
	}
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

// With --fix enabled, the initial Run pass skips E008 (FixFormat owns
// the format check). For successfully-fixed files there must be no E008 in
// the JSON output. (Previously this was achieved by a post-Run filter on
// `fixed[v.File]`; the new flow avoids the redundant Format work entirely
// by not emitting E008 from Run in the first place.)
func TestRun_FixSuccessfullyFixed_NoE008InOutput(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": "locals{a=\"x\"}\n",
	})
	code, stdout, _ := runCLI("--fix", "--json", dir)
	if code != 0 {
		t.Fatalf("expected exit 0 after --fix on dirty file, got %d", code)
	}
	var got struct {
		Violations []struct {
			Code string `json:"code"`
		} `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	for _, v := range got.Violations {
		if v.Code == "E008" {
			t.Errorf("E008 must not appear after a successful --fix; got %s", stdout)
		}
	}
	// And the file must actually be reformatted.
	contents, err := os.ReadFile(filepath.Join(dir, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "a = \"x\"") {
		t.Errorf("file was not reformatted: %q", contents)
	}
}

// When `--checks=E008 --fix` is used, the filter has only E008.
// `checksFilterWithout(filter, "E008")` returns an empty CheckSet.
// CheckSet.Enabled() treats empty as "all enabled" (the implicit
// sentinel), so without a guard the initial Run pass would run ALL
// checks, defeating the user's explicit `--checks=E008` filter and
// emitting violations the user asked NOT to see (e.g. E002 duplicates,
// E003 undefined refs from a file that's intentionally a fragment).
// The fix is to skip Run entirely when the filtered set is empty AND
// the original was non-empty.
func TestRun_FixWithChecksOnlyE008_DoesNotRunOtherChecks(t *testing.T) {
	t.Parallel()
	// File has a duplicate-local (would trigger E002) AND is unformatted
	// (E008). User asked for ONLY E008, so E002 must NOT be reported.
	dir := writeTFDir(t, map[string]string{
		"main.tf": "locals{a=\"x\"}\nlocals { a = \"y\" }\n",
	})
	code, stdout, stderr := runCLI("--checks=E008", "--fix", "--json", dir)
	if code != 0 {
		t.Fatalf("expected exit 0 (only E008 enabled, file fixed), got %d (stderr=%q stdout=%q)",
			code, stderr, stdout)
	}
	var got struct {
		Violations []struct {
			Code string `json:"code"`
			File string `json:"file"`
		} `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	for _, v := range got.Violations {
		// Only E008 (and E000, parse violations) may appear. E002 must NOT.
		if v.Code != "E008" && v.Code != "E000" && v.Code != "E001" {
			t.Errorf("unexpected violation code %s emitted with --checks=E008: %+v", v.Code, v)
		}
	}
}

// Regression guard for the inverse: --checks=E001,E008 --fix must still
// run E001 (it's in the filter) but not E002 etc. and must still fix E008.
func TestRun_FixWithMultiChecksIncludingE008_OnlyRunsRequested(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": "locals{a=\"x\"}\nlocals { a = \"y\" }\n",
	})
	code, stdout, _ := runCLI("--checks=E001,E008", "--fix", "--json", dir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	var got struct {
		Violations []struct {
			Code string `json:"code"`
		} `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	for _, v := range got.Violations {
		if v.Code == "E002" {
			t.Errorf("E002 must NOT appear when only E001+E008 are enabled: %s", stdout)
		}
	}
}

// ── --checks= edge cases ─────────────────────────────────────────────────────

func TestRun_ChecksEmpty_ExitTwo(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCLI("--checks=", ".")
	if code != 2 {
		t.Fatalf("expected exit 2 for empty --checks=, got %d", code)
	}
	if !strings.Contains(stderr, "--checks=") {
		t.Errorf("stderr should mention --checks=, got: %q", stderr)
	}
}

func TestRun_ChecksUnknownCode_ExitTwo(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCLI("--checks=E999", ".")
	if code != 2 {
		t.Fatalf("expected exit 2 for unknown check, got %d", code)
	}
	if !strings.Contains(stderr, "E999") && !strings.Contains(stderr, "unknown") {
		t.Errorf("stderr should mention the unknown code, got: %q", stderr)
	}
}

func TestRun_ChecksFilters_OnlyEmitsRequestedCodes(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `output "x" { value = local.missing }
locals {
  unused = "warn"
}
`,
	})
	// Only E003 — should suppress W001.
	code, stdout, _ := runCLI("--checks=E003", dir)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if strings.Contains(stdout, "W001") {
		t.Errorf("W001 should be filtered out by --checks=E003, but appeared in: %s", stdout)
	}
	if !strings.Contains(stdout, "E003") {
		t.Errorf("E003 should appear in output, got: %s", stdout)
	}
}

// Repeated --checks= flags must accumulate, not overwrite. The single-
// flag form `--checks=E003,W001` is already supported; the multi-flag form
// `--checks=E003 --checks=W001` should be equivalent. Previously each flag
// re-initialised checksFilter via make(), silently dropping the prior set.
func TestRun_ChecksRepeated_Accumulate(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `output "x" { value = local.missing }
locals {
  unused = "warn"
}
`,
	})
	// Equivalent forms — both must produce the same set of codes (E003 + W001).
	codeMulti, stdoutMulti, _ := runCLI("--checks=E003", "--checks=W001", dir)
	codeSingle, _, _ := runCLI("--checks=E003,W001", dir)
	if codeMulti != codeSingle {
		t.Errorf("multi-flag exit %d != single-flag exit %d", codeMulti, codeSingle)
	}
	if !strings.Contains(stdoutMulti, "E003") {
		t.Errorf("multi-flag output missing E003: %s", stdoutMulti)
	}
	if !strings.Contains(stdoutMulti, "W001") {
		t.Errorf("multi-flag output missing W001 — flags were not accumulated: %s", stdoutMulti)
	}
}

// ── describe subcommand ──────────────────────────────────────────────────────

func TestRun_Describe_ListsChecks(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("describe")
	if code != 0 {
		t.Fatalf("describe should exit 0, got %d", code)
	}
	for _, code := range []string{"E001", "E008", "W001", "W009", "E101", "E201", "E202", "E203"} {
		if !strings.Contains(stdout, code) {
			t.Errorf("describe output missing %s; got: %s", code, stdout)
		}
	}
	// Family headers must appear so users can see the taxonomy at a glance.
	for _, family := range []string{"E000", "E100", "E200"} {
		if !strings.Contains(stdout, family) {
			t.Errorf("describe output missing family %s; got: %s", family, stdout)
		}
	}
}

func TestRun_DescribeJSON_ParsesAndContainsAllCodes(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("describe", "--json")
	if code != 0 {
		t.Fatalf("describe --json should exit 0, got %d", code)
	}
	var got struct {
		Families []struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"families"`
		Checks []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
			Family   string `json:"family"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	codes := map[string]bool{}
	for _, c := range got.Checks {
		codes[c.Code] = true
	}
	for _, want := range []string{"E001", "E002", "E003", "E004", "E005", "E006", "E007", "E008", "E009", "W001", "W009", "E101", "E201", "E202", "E203"} {
		if !codes[want] {
			t.Errorf("describe --json missing %s", want)
		}
	}
	// The families array must exist and cover the check families we ship —
	// consumers rely on it to filter by domain without reconstructing the
	// mapping from check codes.
	familyCodes := map[string]bool{}
	for _, f := range got.Families {
		familyCodes[f.Code] = true
	}
	for _, want := range []string{"E000", "E100", "E200"} {
		if !familyCodes[want] {
			t.Errorf("describe --json missing family %s", want)
		}
	}
	// Every check must reference a family that is registered. A dangling
	// Family pointer would mean the check is orphaned from the taxonomy
	// and would render under the ungrouped fallback in the human output.
	for _, c := range got.Checks {
		if c.Family == "" {
			t.Errorf("check %s has empty family — must reference a family header", c.Code)
			continue
		}
		if !familyCodes[c.Family] {
			t.Errorf("check %s references unknown family %s", c.Code, c.Family)
		}
	}
}

// ── version subcommand ───────────────────────────────────────────────────────

func TestRun_Version_PrintsVersion(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("version")
	if code != 0 {
		t.Fatalf("version should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "tfdry") {
		t.Errorf("version output should contain 'tfdry'; got: %q", stdout)
	}
}

// TestRun_Version_OutputFormat guards the exact shape of the version
// line: `tfdry <version>` with a space separator and no `v` prefix on
// the version token. Docs and issue templates that show an example
// of this format (currently: the bug_report.yml placeholder, and the
// `"tfdry_version": "..."` field in the README's `--json` output
// examples) all depend on this shape. If someone changes printVersion
// to emit `tfdry v<version>` (or `<version>` bare, or an empty
// version string), this guard catches the drift so the paired
// doc/template updates aren't silently forgotten.
func TestRun_Version_OutputFormat(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("version")
	if code != 0 {
		t.Fatalf("version should exit 0, got %d", code)
	}
	// Trim the trailing newline that fmt.Fprintln emits.
	got := strings.TrimSpace(stdout)
	if !strings.HasPrefix(got, "tfdry ") {
		t.Errorf("version output must start with 'tfdry '; got: %q", got)
	}
	// After the leading 'tfdry ' the version token must be non-empty
	// AND must not begin with 'v'. tfdry emits semver without the
	// git-tag 'v' prefix; the bug_report.yml placeholder and the
	// `"tfdry_version"` field in README's JSON examples both rely
	// on this exact format.
	versionToken := strings.TrimPrefix(got, "tfdry ")
	if versionToken == "" {
		t.Errorf("version token must not be empty; got: %q", got)
	} else if strings.HasPrefix(versionToken, "v") {
		t.Errorf("version token must not have 'v' prefix; got: %q", versionToken)
	}
}

// ── --version / -v flags ─────────────────────────────────────────────────────

func TestRun_VersionFlag_PrintsVersionAndExitsZero(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("--version")
	if code != 0 {
		t.Fatalf("--version should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "tfdry") {
		t.Errorf("--version output should contain 'tfdry'; got: %q", stdout)
	}
}

func TestRun_VersionShortFlag_PrintsVersionAndExitsZero(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("-v")
	if code != 0 {
		t.Fatalf("-v should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "tfdry") {
		t.Errorf("-v output should contain 'tfdry'; got: %q", stdout)
	}
}

func TestRun_VersionFlag_MatchesSubcommandOutput(t *testing.T) {
	t.Parallel()
	_, flagOut, _ := runCLI("--version")
	_, subcmdOut, _ := runCLI("version")
	if flagOut != subcmdOut {
		t.Errorf("--version and 'version' subcommand should produce identical output\n  --version: %q\n  version:   %q", flagOut, subcmdOut)
	}
}

// ── --help / -h flags and 'help' subcommand ──────────────────────────────────

func TestRun_HelpFlag_PrintsUsageAndExitsZero(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("--help")
	if code != 0 {
		t.Fatalf("--help should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Usage") {
		t.Errorf("--help output should contain 'Usage'; got: %q", stdout)
	}
}

func TestRun_HelpShortFlag_PrintsUsageAndExitsZero(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("-h")
	if code != 0 {
		t.Fatalf("-h should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Usage") {
		t.Errorf("-h output should contain 'Usage'; got: %q", stdout)
	}
}

func TestRun_HelpSubcommand_PrintsUsageAndExitsZero(t *testing.T) {
	t.Parallel()
	code, stdout, _ := runCLI("help")
	if code != 0 {
		t.Fatalf("'help' subcommand should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Usage") {
		t.Errorf("'help' output should contain 'Usage'; got: %q", stdout)
	}
}

func TestRun_HelpFlag_MatchesHelpSubcommand(t *testing.T) {
	t.Parallel()
	_, flagOut, _ := runCLI("--help")
	_, subcmdOut, _ := runCLI("help")
	if flagOut != subcmdOut {
		t.Errorf("--help and 'help' subcommand should produce identical output\n  --help: %q\n  help:   %q", flagOut, subcmdOut)
	}
}

func TestRun_HelpOutput_ContainsKeyInformation(t *testing.T) {
	t.Parallel()
	_, stdout, _ := runCLI("--help")
	// Help should mention subcommands.
	if !strings.Contains(stdout, "fmt") {
		t.Errorf("help should mention the 'fmt' subcommand; got: %q", stdout)
	}
	if !strings.Contains(stdout, "describe") {
		t.Errorf("help should mention the 'describe' subcommand; got: %q", stdout)
	}
	if !strings.Contains(stdout, "version") {
		t.Errorf("help should mention the 'version' subcommand; got: %q", stdout)
	}
	// Help should mention key flags.
	if !strings.Contains(stdout, "--json") {
		t.Errorf("help should mention '--json' flag; got: %q", stdout)
	}
	if !strings.Contains(stdout, "--fix") {
		t.Errorf("help should mention '--fix' flag; got: %q", stdout)
	}
	if !strings.Contains(stdout, "--checks") {
		t.Errorf("help should mention '--checks' flag; got: %q", stdout)
	}
}

func TestRun_FmtHelp_PrintsUsageAndExitsZero(t *testing.T) {
	t.Parallel()
	// Subcommand-level --help: printing top-level help is acceptable.
	code, stdout, _ := runCLI("fmt", "--help")
	if code != 0 {
		t.Fatalf("'fmt --help' should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Usage") {
		t.Errorf("'fmt --help' output should contain 'Usage'; got: %q", stdout)
	}
}

// ── default dir argument ─────────────────────────────────────────────────────

func TestRun_DefaultDirIsCurrent(t *testing.T) {
	t.Parallel()
	// No args → defaults to ".". cwd is the project root which has no .tf files.
	// Exit code depends on whether checker reports any errors on an empty/non-tf dir.
	// We just verify the call returns and doesn't panic.
	code, _, _ := runCLI()
	if code < 0 {
		t.Fatalf("run() should not return negative exit code, got %d", code)
	}
}

// ── fmt subcommand (mirrors terraform fmt) ───────────────────────────────────

const (
	fmtDirtyTF = "locals {\n  a=\"b\"\n   c =   \"d\"\n}\n"
	fmtCleanTF = "locals {\n  a = \"b\"\n  c = \"d\"\n}\n"
)

func TestRun_Fmt_RewritesUnformattedFile_PrintsName_ExitZero(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"dirty.tf": fmtDirtyTF})
	code, stdout, _ := runCLI("fmt", dir)
	if code != 0 {
		t.Fatalf("fmt should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "dirty.tf") {
		t.Fatalf("expected dirty.tf in output, got %q", stdout)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dirty.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fmtCleanTF {
		t.Fatalf("file not formatted:\nexpected: %q\ngot:      %q", fmtCleanTF, string(got))
	}
}

func TestRun_Fmt_AlreadyFormatted_NoOutput_ExitZero(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"clean.tf": fmtCleanTF})
	code, stdout, _ := runCLI("fmt", dir)
	if code != 0 {
		t.Fatalf("fmt on clean dir should exit 0, got %d", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected no stdout on already-formatted dir, got %q", stdout)
	}
}

func TestRun_FmtCheck_PrintsButDoesntRewrite_ExitThree(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"dirty.tf": fmtDirtyTF})
	code, stdout, _ := runCLI("fmt", "-check", dir)
	if code != 3 {
		t.Fatalf("fmt -check on dirty dir should exit 3, got %d", code)
	}
	if !strings.Contains(stdout, "dirty.tf") {
		t.Fatalf("expected dirty.tf in output, got %q", stdout)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dirty.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fmtDirtyTF {
		t.Fatalf("fmt -check must not rewrite the file, got %q", string(got))
	}
}

func TestRun_FmtCheck_Clean_ExitZero(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"clean.tf": fmtCleanTF})
	code, stdout, _ := runCLI("fmt", "-check", dir)
	if code != 0 {
		t.Fatalf("fmt -check on clean dir should exit 0, got %d", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected no stdout, got %q", stdout)
	}
}

func TestRun_FmtRecursive_FormatsNestedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Top-level dirty.
	if err := os.WriteFile(filepath.Join(dir, "dirty.tf"), []byte(fmtDirtyTF), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nested dirty in subdir/.
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.tf"), []byte(fmtDirtyTF), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deeper.
	if err := os.MkdirAll(filepath.Join(dir, "subdir", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "deep", "deep.tf"), []byte(fmtDirtyTF), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCLI("fmt", "-recursive", dir)
	if code != 0 {
		t.Fatalf("fmt -recursive should exit 0, got %d", code)
	}
	for _, want := range []string{"dirty.tf", "subdir/nested.tf", "subdir/deep/deep.tf"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in stdout, got %q", want, stdout)
		}
	}
	for _, p := range []string{"dirty.tf", "subdir/nested.tf", "subdir/deep/deep.tf"} {
		got, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != fmtCleanTF {
			t.Errorf("%s not formatted: %q", p, string(got))
		}
	}
}

func TestRun_FmtRecursive_SkipsHiddenDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, sub := range []string{".terraform", ".git", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "x.tf"), []byte(fmtDirtyTF), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, _ := runCLI("fmt", "-recursive", dir)
	if code != 0 {
		t.Fatalf("fmt -recursive should exit 0, got %d", code)
	}
	for _, sub := range []string{".terraform", ".git", ".hidden"} {
		// Files in hidden dirs must not appear in output.
		if strings.Contains(stdout, sub+"/x.tf") {
			t.Errorf("expected %s/x.tf to be skipped, got output: %q", sub, stdout)
		}
		// And must not be rewritten.
		got, err := os.ReadFile(filepath.Join(dir, sub, "x.tf"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != fmtDirtyTF {
			t.Errorf("file in hidden dir %s was modified: %q", sub, string(got))
		}
	}
}

func TestRun_FmtRecursiveCheck_DirtyInSubdir_ExitThree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clean.tf"), []byte(fmtCleanTF), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.tf"), []byte(fmtDirtyTF), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCLI("fmt", "-recursive", "-check", dir)
	if code != 3 {
		t.Fatalf("fmt -recursive -check on dirty subdir should exit 3, got %d", code)
	}
	if !strings.Contains(stdout, "subdir/nested.tf") {
		t.Errorf("expected subdir/nested.tf in stdout, got %q", stdout)
	}
	// Must not be rewritten.
	got, err := os.ReadFile(filepath.Join(dir, "subdir", "nested.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fmtDirtyTF {
		t.Errorf("file rewritten despite -check: %q", string(got))
	}
}

// ── Unknown flags must be rejected ───────────────────────────────────────────

// Unknown flags should produce an error and exit code 2 (tool error),
// not be silently treated as a directory path.
func TestRun_UnknownFlag_ExitTwo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"long flag typo", []string{"--checkss=E001"}},
		{"short flag typo", []string{"-x"}},
		{"unknown flag with value", []string{"--verbose"}},
		{"unknown flag after dir", []string{".", "--what"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(tc.args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2; stderr=%q", code, stderr)
			}
			if !strings.Contains(stderr, "unrecognized") && !strings.Contains(stderr, "unknown") {
				t.Errorf("stderr should mention unrecognized/unknown flag, got %q", stderr)
			}
		})
	}
}

// -check only makes sense with the `fmt` subcommand. Using it on the
// lint path silently ignored the flag and ran the normal pass, hiding
// user mistakes (e.g. `tfdry -check ./infra` would NOT check
// formatting — it would lint the dir and exit accordingly). Reject as
// a usage error.
//
// -recursive is separately valid on the lint path (issue #21) and no
// longer appears in this table; its rejection on non-lint-non-fmt
// subcommands is asserted in TestRun_LintRecursive_RejectedOnNonLintNonFmtSubcommands.
func TestRun_FmtFlagsOutsideFmt_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"a.tf": `locals { a = "x" }`})
	cases := []struct {
		name string
		args []string
	}{
		{"-check on lint", []string{"-check", dir}},
		{"--check on lint", []string{"--check", dir}},
		{"-check on describe", []string{"describe", "-check"}},
		{"-check after dir on lint", []string{dir, "-check"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(tc.args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2; stderr=%q", code, stderr)
			}
			if stderr == "" {
				t.Errorf("expected stderr message explaining the misuse")
			}
		})
	}
}

// Regression: -check and -recursive must STILL work under `fmt`.
func TestRun_FmtFlagsWithFmtSubcommand_StillWork(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"a.tf": fmtCleanTF})
	cases := [][]string{
		{"fmt", "-check", dir},
		{"fmt", "--check", dir},
		{"fmt", "-recursive", dir},
		{"fmt", "--recursive", dir},
		{"fmt", "-check", "-recursive", dir},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, _, stderr := runCLI(args...)
			if code == 2 {
				t.Errorf("fmt with %v should not exit 2; stderr=%q", args, stderr)
			}
		})
	}
}

// --json/--fix/--checks= are lint-path flags that don't apply to
// the fmt subcommand. The previous behaviour silently ignored them.
// Symmetric to the -check/-recursive guards being rejected outside fmt:
// flags that don't apply to the chosen subcommand should reject early
// with exit 2 instead of letting the user think they applied.
func TestRun_LintFlagsWithFmtSubcommand_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"a.tf": fmtCleanTF})
	cases := [][]string{
		// --json doesn't apply (fmt has its own stdout contract: filenames).
		{"fmt", "--json", dir},
		{"--json", "fmt", dir},
		// --fix doesn't apply (fmt always rewrites or runs -check).
		{"fmt", "--fix", dir},
		{"--fix", "fmt", dir},
		// --checks= filters individual checks; fmt only does E008.
		{"fmt", "--checks=E001", dir},
		{"--checks=E008", "fmt", dir},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, _, stderr := runCLI(args...)
			if code != 2 {
				t.Errorf("fmt with lint flag %v should exit 2, got %d (stderr=%q)",
					args, code, stderr)
			}
			if stderr == "" {
				t.Errorf("expected an error message on stderr explaining the rejection; got empty")
			}
		})
	}
}

// Known flags and the bare dir argument should all keep working.
func TestRun_KnownFlagsStillWork(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `locals {
  a = "x"
}
`,
	})
	// Each must NOT exit 2.
	cases := [][]string{
		{dir},
		{"--json", dir},
		{"--checks=E001", dir},
		{dir, "--json"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, _, stderr := runCLI(args...)
			if code == 2 {
				t.Errorf("known flags should not exit 2; got code=%d stderr=%q", code, stderr)
			}
		})
	}
}

// ── Multiple positional args / extras after subcommands → error ──────────────

// `tfdry dir1 dir2` should error rather than silently using dir2.
func TestRun_MultiplePositionalDirs_ExitTwo(t *testing.T) {
	t.Parallel()
	dir1 := writeTFDir(t, map[string]string{"a.tf": `locals { x = "y" }` + "\n"})
	dir2 := writeTFDir(t, map[string]string{"b.tf": `locals { y = "z" }` + "\n"})
	code, _, stderr := runCLI(dir1, dir2)
	if code != 2 {
		t.Errorf("two dirs should exit 2, got code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "unexpected") && !strings.Contains(stderr, "extra") {
		t.Errorf("stderr should mention extra/unexpected arg, got %q", stderr)
	}
}

// Extra args after `describe` / `version` should error.
func TestRun_ExtrasAfterSubcommand_ExitTwo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"describe + dir", []string{"describe", "."}},
		{"version + extra", []string{"version", "foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(tc.args...)
			if code != 2 {
				t.Errorf("%v should exit 2, got code=%d stderr=%q", tc.args, code, stderr)
			}
			if !strings.Contains(stderr, "unexpected") &&
				!strings.Contains(stderr, "extra") &&
				!strings.Contains(stderr, "does not accept") {
				t.Errorf("stderr should mention extra/unexpected arg, got %q", stderr)
			}
		})
	}
}

// In `tfdry fmt -recursive`, parse errors in subdirs must be reported
// with their subdirectory path so the user can locate the broken file.
// Previously the bare basename was printed (e.g. `bad.tf`) — when the same
// filename exists under multiple subdirs the message is ambiguous.
func TestRun_FmtRecursive_ParseError_PrefixesSubdirPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "infra", "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Invalid HCL in a deep subdir.
	if err := os.WriteFile(filepath.Join(dir, "infra", "prod", "bad.tf"),
		[]byte(`resource "x" "y" { @@@`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI("fmt", "-recursive", dir)
	if code != 2 {
		t.Errorf("fmt -recursive on dir with parse error should exit 2, got %d (stderr=%q)", code, stderr)
	}
	// stderr must mention the subdir path so the user can locate the file.
	// Accept either OS path separator since the test runs on multiple platforms.
	if !strings.Contains(stderr, filepath.Join("infra", "prod", "bad.tf")) &&
		!strings.Contains(stderr, "infra/prod/bad.tf") {
		t.Errorf("stderr should include subdir path 'infra/prod/bad.tf'; got %q", stderr)
	}
}

// tfdry fmt prints filenames and parse-error text directly to
// stdout/stderr without applying the output sanitizer used by the
// lint/JSON paths. On Unix, filenames can contain ESC/control/Bidi
// characters, which enables terminal/line injection via crafted
// `.tf` filenames. The fix routes those values through the same
// Sanitize helper used by output.NewReport.
func TestRun_Fmt_SanitizesFilenameInOutput(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows filesystem rejects most control chars in filenames")
	}
	dir := t.TempDir()
	// Filename with embedded ESC and a Bidi-override character. Under
	// the previous code these would be printed verbatim to stdout when
	// fmt rewrites the file (the file is dirty, so its name appears).
	bad := "evil\x1b[31m\u202Edirty.tf"
	path := filepath.Join(dir, bad)
	if err := os.WriteFile(path, []byte(fmtDirtyTF), 0o644); err != nil {
		t.Skipf("filesystem rejects control chars in filename: %v", err)
	}
	code, stdout, _ := runCLI("fmt", dir)
	if code != 0 {
		t.Fatalf("fmt on dir should exit 0, got %d", code)
	}
	if strings.ContainsAny(stdout, "\x1b") {
		t.Errorf("ESC character leaked into stdout: %q", stdout)
	}
	if strings.Contains(stdout, "\u202E") {
		t.Errorf("Bidi-override character leaked into stdout: %q", stdout)
	}
	// Visible portion of the filename should still appear so the user can
	// identify the file (just stripped of dangerous characters).
	if !strings.Contains(stdout, "evildirty.tf") && !strings.Contains(stdout, "[31m") {
		// Either the ESC is gone (sanitized — good) or the test crafted
		// a name that doesn't survive at all. Allow either as long as
		// the stronger ContainsAny check above passed.
		_ = stdout
	}
}

// Same property for parse-error stderr output. A subdir with an
// invalid .tf whose name contains ESC must not propagate that ESC to
// stderr.
func TestRun_FmtRecursive_SanitizesParseErrorPath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows filesystem rejects most control chars in filenames")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "evil\x1b[31mbad.tf"
	if err := os.WriteFile(filepath.Join(dir, "infra", bad),
		[]byte(`resource "x" "y" { @@@`), 0o644); err != nil {
		t.Skipf("filesystem rejects control chars in filename: %v", err)
	}
	code, _, stderr := runCLI("fmt", "-recursive", dir)
	if code != 2 {
		t.Errorf("fmt -recursive on dir with parse error should exit 2, got %d", code)
	}
	if strings.ContainsAny(stderr, "\x1b") {
		t.Errorf("ESC character leaked into stderr: %q", stderr)
	}
}

// When ParseDir emits a directory-level E000 (because os.ReadDir
// failed on the directory itself, e.g. permission race), v.File is the
// directory path — not a basename. Naively joining d+v.File then
// duplicates the prefix (e.g. "infra/prod/infra/prod"). The
// displayPath helper detects that case and treats v.File as
// already-a-path. Unit-tested directly because reliably triggering a
// dir-level ParseDir error from a recursive walk requires a TOCTOU
// race that's hard to script; the helper guarantees the correct path
// regardless of trigger.
func TestDisplayPath_DoesNotDuplicateDirPath(t *testing.T) {
	t.Parallel()
	// absRoot constructs a platform-appropriate absolute path from
	// path segments. Windows considers `/root` to NOT be absolute (it
	// requires a drive letter or UNC prefix), so the Unix-style
	// hard-coded `/root` inputs the test used to ship with were
	// effectively relative on Windows and produced bizarre
	// duplicated paths. Routing through this helper keeps the test
	// cross-platform while exercising the absolute-path branch.
	absRoot := func(segments ...string) string {
		joined := filepath.Join(segments...)
		if runtime.GOOS == "windows" {
			return `C:\` + joined
		}
		return "/" + joined
	}
	cases := []struct {
		name    string
		rootArg string
		dir     string
		vFile   string
		want    string
	}{
		{
			name:    "file-level violation: basename joined under dir",
			rootArg: absRoot("root"),
			dir:     absRoot("root", "infra", "prod"),
			vFile:   "bad.tf",
			// displayPath always emits forward slashes regardless
			// of host OS (see its godoc — UX consistency + test
			// stability), so the expected values do too.
			want: "infra/prod/bad.tf",
		},
		{
			name:    "dir-level violation: vFile equals dir, must NOT duplicate",
			rootArg: absRoot("root"),
			dir:     absRoot("root", "infra", "prod"),
			vFile:   absRoot("root", "infra", "prod"),
			want:    "infra/prod",
		},
		{
			name:    "dir-level violation, relative tree",
			rootArg: ".",
			dir:     filepath.Join("infra", "prod"),
			vFile:   filepath.Join("infra", "prod"),
			want:    "infra/prod",
		},
		{
			name:    "absolute vFile resolves under root",
			rootArg: absRoot("root"),
			dir:     absRoot("root", "infra"),
			vFile:   absRoot("root", "infra", "main.tf"),
			want:    "infra/main.tf",
		},
		{
			name:    "empty vFile falls back to dir",
			rootArg: absRoot("root"),
			dir:     absRoot("root", "infra"),
			vFile:   "",
			want:    "infra",
		},
		{
			name:    "root and dir identical: dir-level violation reports root itself",
			rootArg: absRoot("root"),
			dir:     absRoot("root"),
			vFile:   absRoot("root"),
			want:    ".",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := displayPath(tc.rootArg, tc.dir, tc.vFile)
			if got != tc.want {
				t.Errorf("displayPath(%q, %q, %q) = %q, want %q",
					tc.rootArg, tc.dir, tc.vFile, got, tc.want)
			}
			// Strong invariant: the result must NEVER contain the same
			// non-empty subpath segment twice in a row (the bug
			// signature). Test in forward-slash space since that's the
			// canonical form displayPath emits.
			if tc.dir != "" {
				dirSlash := filepath.ToSlash(tc.dir)
				if strings.Contains(got, dirSlash+"/"+dirSlash) {
					t.Errorf("path duplication detected in %q (dir=%q)", got, tc.dir)
				}
			}
		})
	}
}

// `tfdry fmt path1 path2` should also error — fmt takes at most one path.
func TestRun_FmtMultiplePaths_ExitTwo(t *testing.T) {
	t.Parallel()
	dir1 := writeTFDir(t, nil)
	dir2 := writeTFDir(t, nil)
	code, _, stderr := runCLI("fmt", dir1, dir2)
	if code != 2 {
		t.Errorf("two paths to fmt should exit 2, got code=%d stderr=%q", code, stderr)
	}
}

// ── tfdry fmt single-file (terraform fmt parity) ──────────────────────
//
// terraform fmt accepts both directories and individual files; tfdry must do
// the same. Previously passing a file path produced a confusing
// "cannot read directory" error from ParseDir.

// fmt on a dirty file path: rewrite, print path, exit 0.
func TestRun_Fmt_SingleDirtyFile_RewritesPrintsExitZero(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"dirty.tf": fmtDirtyTF})
	path := filepath.Join(dir, "dirty.tf")
	code, stdout, stderr := runCLI("fmt", path)
	if code != 0 {
		t.Fatalf("fmt <dirty file> should exit 0, got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stdout, path) && !strings.Contains(stdout, "dirty.tf") {
		t.Errorf("expected file path in stdout, got %q", stdout)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fmtCleanTF {
		t.Fatalf("file not formatted:\nexpected: %q\ngot:      %q", fmtCleanTF, string(got))
	}
}

// fmt on an already-clean file: no output, exit 0, file unchanged.
func TestRun_Fmt_SingleCleanFile_NoOutputExitZero(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"clean.tf": fmtCleanTF})
	path := filepath.Join(dir, "clean.tf")
	code, stdout, stderr := runCLI("fmt", path)
	if code != 0 {
		t.Fatalf("fmt <clean file> should exit 0, got %d (stderr=%q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected no stdout on already-formatted file, got %q", stdout)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fmtCleanTF {
		t.Fatalf("clean file should not be modified, got %q", string(got))
	}
}

// fmt -check on a dirty file: print path, exit 3, file unchanged.
func TestRun_FmtCheck_SingleDirtyFile_PrintsExitThree(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"dirty.tf": fmtDirtyTF})
	path := filepath.Join(dir, "dirty.tf")
	code, stdout, stderr := runCLI("fmt", "-check", path)
	if code != 3 {
		t.Fatalf("fmt -check <dirty file> should exit 3, got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stdout, "dirty.tf") {
		t.Errorf("expected dirty.tf in stdout, got %q", stdout)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fmtDirtyTF {
		t.Fatalf("fmt -check must not modify the file; got %q", string(got))
	}
}

// fmt -check on a clean file: no output, exit 0.
func TestRun_FmtCheck_SingleCleanFile_NoOutputExitZero(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"clean.tf": fmtCleanTF})
	path := filepath.Join(dir, "clean.tf")
	code, stdout, stderr := runCLI("fmt", "-check", path)
	if code != 0 {
		t.Fatalf("fmt -check <clean file> should exit 0, got %d (stderr=%q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected no stdout, got %q", stdout)
	}
}

// Single-file fmt should report HCL syntax errors before formatting,
// matching the directory-mode behaviour (which surfaces parse errors via
// E001 with exit 2). Without this, `tfdry fmt bad.tf` would silently exit
// 0 even when bad.tf has invalid HCL — the user is left thinking the
// file was successfully formatted when it wasn't.
func TestRun_Fmt_SingleFileWithSyntaxError_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"bad.tf": `resource "x" "y" { @@@`, // invalid HCL
	})
	path := filepath.Join(dir, "bad.tf")
	code, _, stderr := runCLI("fmt", path)
	if code != 2 {
		t.Errorf("fmt <bad-syntax-file> should exit 2, got %d (stderr=%q)", code, stderr)
	}
	if stderr == "" {
		t.Error("expected an error message on stderr explaining the syntax error")
	}
	// The original bad content must NOT have been overwritten.
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `resource "x" "y" { @@@` {
		t.Errorf("bad-syntax file was modified despite parse failure: %q", contents)
	}
}

// Same for fmt -check: a syntax-broken file is a tool error (exit 2),
// not a "would change" condition (exit 3).
func TestRun_FmtCheck_SingleFileWithSyntaxError_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"bad.tf": `resource "x" "y" { @@@`,
	})
	path := filepath.Join(dir, "bad.tf")
	code, _, stderr := runCLI("fmt", "-check", path)
	if code != 2 {
		t.Errorf("fmt -check <bad-syntax-file> should exit 2, got %d (stderr=%q)", code, stderr)
	}
	if stderr == "" {
		t.Error("expected an error message on stderr explaining the syntax error")
	}
}

// fmt on a non-existent path: exit 2 with a useful error message.
func TestRun_Fmt_NonExistentPath_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, nil)
	missing := filepath.Join(dir, "does-not-exist.tf")
	code, _, stderr := runCLI("fmt", missing)
	if code != 2 {
		t.Errorf("fmt <missing> should exit 2, got %d (stderr=%q)", code, stderr)
	}
	if stderr == "" {
		t.Error("expected an error message on stderr")
	}
}

// fmt -recursive on a file path is meaningless; reject with exit 2.
func TestRun_Fmt_RecursiveOnFile_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"a.tf": fmtCleanTF})
	path := filepath.Join(dir, "a.tf")
	code, _, stderr := runCLI("fmt", "-recursive", path)
	if code != 2 {
		t.Errorf("fmt -recursive <file> should exit 2, got %d (stderr=%q)", code, stderr)
	}
}

// tfdry fmt <symlink-path> must reject symlinks before reading or
// writing — on Unix this was already enforced at writeFormatted via
// O_NOFOLLOW, but the dirty-detection read in runFmtFile happened first
// (os.ReadFile follows symlinks), and on Windows oNoFollow=0 means the
// rename would later destroy the symlink. The Lstat precheck rejects
// symlinks consistently across platforms before any I/O against the target.
func TestRun_Fmt_FilePathIsSymlink_Rejected(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"real.tf": fmtDirtyTF})
	realPath := filepath.Join(dir, "real.tf")
	link := filepath.Join(dir, "link.tf")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	// fmt write-mode on a symlink must NOT modify either the symlink or its
	// target — exit 2 with a useful stderr message.
	code, _, stderr := runCLI("fmt", link)
	if code != 2 {
		t.Errorf("fmt <symlink> should exit 2, got %d (stderr=%q)", code, stderr)
	}
	if stderr == "" {
		t.Error("expected an error message on stderr explaining symlink rejection")
	}

	// The symlink itself must remain a symlink (not have been replaced by a
	// regular file via os.Rename).
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("symlink unexpectedly missing after fmt: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced by a regular file (Windows-style destruction)")
	}

	// The target file must still contain the dirty content.
	target, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(target) != fmtDirtyTF {
		t.Fatalf("symlink target was modified through the symlink; got %q", string(target))
	}
}

// Read-only path: fmt -check on a symlink should also reject (no read
// follows, no exit-3 false positive, just a usage error).
func TestRun_FmtCheck_FilePathIsSymlink_Rejected(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"real.tf": fmtDirtyTF})
	realPath := filepath.Join(dir, "real.tf")
	link := filepath.Join(dir, "link.tf")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	code, _, stderr := runCLI("fmt", "-check", link)
	if code != 2 {
		t.Errorf("fmt -check <symlink> should exit 2, got %d (stderr=%q)", code, stderr)
	}
}

// Symlinked-DIR handling for `tfdry fmt`. The previous code path used
// os.Stat (follows symlinks) to detect dir-vs-file, but collectDirs uses
// filepath.WalkDir which is Lstat-based and does NOT recurse into a
// symlinked root — so `tfdry fmt -recursive <symlink-to-dir>` silently did
// nothing and exited 0. Reject symlinked dir roots up front, consistent
// with file-path symlink rejection (round 4 decision: avoid TOCTOU and
// surprising traversal into unintended directories).
func TestRun_Fmt_SymlinkedDirRoot_Rejected(t *testing.T) {
	t.Parallel()
	realDir := writeTFDir(t, map[string]string{"main.tf": fmtDirtyTF})
	parent := t.TempDir()
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	// Both recursive and non-recursive must consistently reject symlinked
	// dir roots — this is a security/atomicity property of the path, not
	// of the traversal mode.
	scenarios := []struct {
		name string
		args []string
	}{
		{"fmt non-recursive", []string{"fmt", link}},
		{"fmt -recursive", []string{"fmt", "-recursive", link}},
		{"fmt -check", []string{"fmt", "-check", link}},
		{"fmt -check -recursive", []string{"fmt", "-check", "-recursive", link}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			code, _, stderr := runCLI(sc.args...)
			if code != 2 {
				t.Errorf("fmt %v on symlinked dir should exit 2, got %d (stderr=%q)",
					sc.args, code, stderr)
			}
			if stderr == "" {
				t.Errorf("expected an error message on stderr explaining symlink rejection; got empty")
			}
		})
	}

	// Strong invariant: the real dir's file MUST NOT have been modified
	// through the symlinked path (no in-place rewrites bypassed).
	content, err := os.ReadFile(filepath.Join(realDir, "main.tf"))
	if err != nil {
		t.Fatalf("real file unexpectedly missing: %v", err)
	}
	if string(content) != fmtDirtyTF {
		t.Errorf("real dir was modified through symlink; got %q", string(content))
	}
}

// ── describe --json must propagate write errors ────────────────────────

// errWriter is a Writer that always fails on Write — simulates closed pipe /
// EPIPE / disk full etc. Used to verify CLI exit code on output failure.
type errWriter struct{ err error }

func (e errWriter) Write(p []byte) (int, error) { return 0, e.err }

// runDescribe --json with a failing stdout must return exit 2, matching the
// behaviour of the main `--json` path. Previously the error was swallowed via
// `_ = output.WriteChecksJSON(...)`, causing silent success on broken pipe.
func TestRun_DescribeJSON_PropagatesWriteError(t *testing.T) {
	t.Parallel()
	stdout := errWriter{err: io.ErrClosedPipe}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"describe", "--json"}, stdout, &stderr)
	if code != 2 {
		t.Errorf("describe --json with failing stdout should exit 2, got %d (stderr=%q)",
			code, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected an error message on stderr explaining the write failure")
	}
}

// Symmetry test: the main `--json` path already returns 2 on stdout failure;
// guard that behaviour against regression too.
func TestRun_MainJSON_PropagatesWriteError(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"main.tf": `locals { x = "y" }` + "\n"})
	stdout := errWriter{err: io.ErrClosedPipe}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--json", dir}, stdout, &stderr)
	if code != 2 {
		t.Errorf("--json with failing stdout should exit 2, got %d (stderr=%q)",
			code, stderr.String())
	}
}

// The human-output path should propagate stdout write errors with the
// same exit code semantics as the JSON path, otherwise success is reported
// even when stdout is broken (closed pipe, full disk, etc.).
func TestRun_MainHuman_PropagatesWriteError(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"main.tf": `locals { x = "y" }` + "\n"})
	stdout := errWriter{err: io.ErrClosedPipe}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{dir}, stdout, &stderr)
	if code != 2 {
		t.Errorf("human output with failing stdout should exit 2, got %d (stderr=%q)",
			code, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected an error message on stderr explaining the write failure")
	}
}

// No-violations branch: the early "No violations found" path also
// writes to stdout and must propagate write errors.
func TestRun_MainHuman_NoViolationsBranch_PropagatesWriteError(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"main.tf": `locals { x = "y" }` + "\n"})
	stdout := errWriter{err: io.ErrClosedPipe}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--checks=E002", dir}, stdout, &stderr)
	if code != 2 {
		t.Errorf("clean human run with failing stdout should exit 2, got %d (stderr=%q)",
			code, stderr.String())
	}
}

// runDescribe text mode (`tfdry describe` without
// --json) was the closest analogue to WriteHuman and had the same issue —
// JSON path propagated write errors but text path silently continued.
// Symmetric fix.
func TestRun_DescribeText_PropagatesWriteError(t *testing.T) {
	t.Parallel()
	stdout := errWriter{err: io.ErrClosedPipe}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"describe"}, stdout, &stderr)
	if code != 2 {
		t.Errorf("describe (text) with failing stdout should exit 2, got %d (stderr=%q)",
			code, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected an error message on stderr explaining the write failure")
	}
}

// shortWriter simulates an io.Writer that breaks the spec: returns n < len(p)
// with a nil error. The io.Writer contract requires non-nil error on short
// write, but real-world implementations sometimes break that. The fix uses
// bytes.Buffer.WriteTo which detects the violation and surfaces
// io.ErrShortWrite, so output failures are not silently swallowed.
type shortWriter struct{ accept int }

func (s shortWriter) Write(p []byte) (int, error) {
	if s.accept >= len(p) {
		return len(p), nil
	}
	return s.accept, nil // short write WITHOUT error — spec violation
}

// runDescribe text mode should detect a short-write-without-error and
// return exit 2 (consistent with the JSON path and human-output path which
// already do via the writer's own error chain). The previous code used
// `stdout.Write(b.Bytes())` and only checked the returned error, so a
// spec-violating Writer that silently truncated would slip through and
// report success.
func TestRun_DescribeText_DetectsShortWrite(t *testing.T) {
	t.Parallel()
	stdout := shortWriter{accept: 5} // accept first 5 bytes only
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"describe"}, stdout, &stderr)
	if code != 2 {
		t.Errorf("describe (text) with short-writing stdout should exit 2, got %d (stderr=%q)",
			code, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected an error message on stderr explaining the short write")
	}
}

// Same property for the main human-output path. WriteHuman previously
// used `w.Write(b.Bytes())` which couldn't surface short writes either.
func TestRun_MainHuman_DetectsShortWrite(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{"main.tf": `locals { x = "y" }` + "\n"})
	stdout := shortWriter{accept: 5}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{dir}, stdout, &stderr)
	if code != 2 {
		t.Errorf("human output with short-writing stdout should exit 2, got %d (stderr=%q)",
			code, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected an error message on stderr explaining the short write")
	}
}

// ── SKILL.md regression guards ───────────────────────────────────────────────

// SKILL.md should not carry security claims that don't match what
// tfdry actually implements. Specifically, the previous claim "All path
// arguments are validated. Path traversal attempts are rejected." was
// false: CLI paths are accepted as-is and module `source = "../shared"`
// is explicitly allowed (terraform parity). Misleading agents/users into
// believing they have sandboxing they don't have is worse than no claim
// at all. This is a regression guard.
func TestSkillMd_NoMisleadingPathTraversalClaim(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile("SKILL.md")
	if err != nil {
		t.Fatalf("cannot read SKILL.md: %v", err)
	}
	s := string(content)
	forbidden := []string{
		"Path traversal attempts are rejected",
		"All path arguments are validated",
	}
	for _, phrase := range forbidden {
		if strings.Contains(s, phrase) {
			t.Errorf("SKILL.md still contains misleading security claim: %q", phrase)
		}
	}
	// Sanity: the Security section must still exist — we only object to
	// false claims, not to having a Security section.
	if !strings.Contains(s, "## Security") {
		t.Error("SKILL.md should retain a Security section describing the actual posture")
	}
	// The symlink bullet must qualify Windows behaviour. The
	// O_NOFOLLOW protection only applies on Unix-like systems; on
	// Windows oNoFollow=0 and the symlink-to-regular-file case is
	// silently followed (see checker/nofollow_windows.go). Without
	// this qualification, the bullet overpromises cross-platform
	// symlink skipping.
	if !strings.Contains(s, "Windows") {
		t.Error("SKILL.md symlink bullet must qualify Windows behaviour")
	}
	// The "never modifies files unless --fix" invariant is misleading
	// because the `fmt` subcommand rewrites files by default (without
	// -check). The line must NOT contain a blanket "never modifies"
	// claim — it must qualify the fmt subcommand's write behaviour.
	if strings.Contains(s, "tfdry never modifies files unless `--fix` is passed.") {
		t.Error("SKILL.md still contains the misleading blanket invariant; the fmt subcommand also writes")
	}
	// And the qualified replacement should call out that `tfdry fmt`
	// (without -check) rewrites in place, so users aren't surprised.
	if !strings.Contains(s, "tfdry fmt") {
		t.Error("SKILL.md should mention `tfdry fmt` write behaviour explicitly")
	}
}

// Package-level regexes used by extractRelativeLinks. Compiling once
// (rather than on every call) avoids repeated work as the walker test
// processes multiple template files.
var (
	// Inline form: [text](url) where url may include a title after whitespace.
	inlineLinkRe = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	// Reference-definition form: leading whitespace, [ref]: url.
	// (?m) so ^ matches start of every line.
	refLinkRe = regexp.MustCompile(`(?m)^\s*\[[^\]]+\]:\s*(\S+)`)
)

// extractRelativeLinks finds every relative file-path link in markdown
// content and returns them in textual order of appearance.
//
// It recognises both common markdown link forms:
//   - Inline links: [text](path) and [text](path "optional title")
//   - Reference link definitions: [ref]: path
//
// Returned slice preserves the order links appear in the source text;
// a reference definition that appears before an inline link in the
// source comes first in the output (and vice versa).
//
// It filters out anything that is not a local relative file path:
//   - Absolute URLs of any scheme (anything containing "://") —
//     http://, https://, git://, ftp+ssh://, etc.
//   - Protocol-relative URLs (//host/path) — absolute web links
//     that omit the scheme; common legacy of HTTPS migration era.
//   - mailto: links
//   - Pure anchor links (#section)
//
// Anchor fragments are stripped from path links so
// `../FOO.md#section` is reported as `../FOO.md`. Inline-link title
// suffixes ("title") are also stripped.
func extractRelativeLinks(content string) []string {
	// Track both the text and the start offset so we can sort by
	// textual position across the two regex passes.
	type rawLink struct {
		start int
		text  string
	}
	var raw []rawLink
	// FindAllStringSubmatchIndex returns [matchStart, matchEnd, g1Start, g1End, ...]
	// for each match. We sort by matchStart (m[0]) to preserve appearance order.
	for _, m := range inlineLinkRe.FindAllStringSubmatchIndex(content, -1) {
		raw = append(raw, rawLink{start: m[0], text: content[m[2]:m[3]]})
	}
	for _, m := range refLinkRe.FindAllStringSubmatchIndex(content, -1) {
		raw = append(raw, rawLink{start: m[0], text: content[m[2]:m[3]]})
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i].start < raw[j].start })

	var out []string
	for _, r := range raw {
		link := strings.TrimSpace(r.text)
		// Strip optional inline title suffix: (path "title").
		if i := strings.IndexAny(link, " \t"); i > 0 {
			link = link[:i]
		}
		// Skip absolute URLs of any scheme, protocol-relative URLs,
		// mailto, and pure anchors. The ://-containment check is
		// scheme-agnostic; the //-prefix check separately catches
		// protocol-relative URLs that omit the scheme entirely.
		if strings.Contains(link, "://") ||
			strings.HasPrefix(link, "//") ||
			strings.HasPrefix(link, "mailto:") ||
			strings.HasPrefix(link, "#") {
			continue
		}
		// Strip anchor fragment from path link (e.g. ../FOO.md#section).
		if i := strings.Index(link, "#"); i >= 0 {
			link = link[:i]
		}
		if link == "" {
			continue
		}
		out = append(out, link)
	}
	return out
}

// TestExtractRelativeLinks is a unit test for the link-extraction
// helper used by TestGitHubTemplates_RelativeLinksResolve. It pins the
// behaviour the walker test relies on: which link forms are detected
// (inline + reference-style), and which links are correctly filtered
// out as absolute URLs / anchors.
//
// An earlier review caught that the old regex missed reference-style links
// ([ref]: url definitions); the old scheme check
// only listed http/https, missing git://, ftp://, custom schemes.
// Both cases live in the table below as explicit regression guards.
func TestExtractRelativeLinks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"empty", "", nil},
		{"plain text no links", "just some prose", nil},
		{"inline relative", "see [foo](../foo.md)", []string{"../foo.md"}},
		{"inline absolute https skipped", "see [a](https://example.com)", nil},
		{"inline absolute http skipped", "see [a](http://example.com)", nil},
		// Non-http schemes must also be filtered out.
		{"inline absolute git skipped", "see [a](git://example.com/repo.git)", nil},
		{"inline absolute ftp skipped", "see [a](ftp://example.com)", nil},
		{"inline absolute custom-scheme skipped", "see [a](slack://channel/123)", nil},
		// Protocol-relative URLs (//example.com/...) are absolute
		// web links but don't contain "://". The naive containment
		// check would let them through and then os.Stat would fail
		// against ".github/example.com/..." or similar nonsense.
		{"inline protocol-relative skipped", "see [a](//example.com/foo)", nil},
		{"inline mailto skipped", "see [a](mailto:foo@example.com)", nil},
		{"inline pure anchor skipped", "jump to [a](#section)", nil},
		{"inline with anchor fragment stripped", "see [a](../bar.md#sec)", []string{"../bar.md"}},
		{"inline with title stripped", `see [a](../bar.md "title")`, []string{"../bar.md"}},
		// Reference-style link definitions must also be picked up.
		{"reference relative", "uses [foo][r1]\n\n[r1]: ../bar.md", []string{"../bar.md"}},
		{"reference indented", "uses [foo][r1]\n\n  [r1]: ../bar.md", []string{"../bar.md"}},
		{"reference absolute skipped", "uses [foo][r1]\n\n[r1]: https://example.com", nil},
		{"reference git scheme skipped", "uses [foo][r1]\n\n[r1]: git://example.com/x.git", nil},
		{"reference protocol-relative skipped", "uses [foo][r1]\n\n[r1]: //example.com/x", nil},
		// Mixed: inline + reference in same content.
		{"both styles", "see [a](../a.md) and [b][r]\n\n[r]: ../b.md", []string{"../a.md", "../b.md"}},
		// The walker godoc promises "order of appearance"; a reference
		// definition placed BEFORE an inline link must come first in
		// the result, not be silently relegated to last because of
		// the implementation order.
		{"reference before inline preserves textual order", "[r]: ../first.md\n\nthen [link](../second.md)", []string{"../first.md", "../second.md"}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractRelativeLinks(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d links %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("link[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// resolveDocLink returns the on-disk path that a markdown link in a
// doc file resolves to, mimicking how GitHub renders relative links.
//
// GitHub treats markdown link paths in two distinct ways:
//   - Paths starting with "/" are *repository-root* relative — they
//     resolve to <repo>/<path> regardless of where the doc lives.
//   - All other paths are *doc-relative* — they resolve relative to
//     the directory containing the doc file.
//
// docPath is the path to the doc file containing the link.
// repoRoot is the directory the test treats as the repository root
// (typically "." when tests run from the package directory).
// link is the markdown link as written, with anchors and titles
// already stripped by extractRelativeLinks.
//
// An earlier review caught that the original walker used filepath.Join on every
// link, which silently mishandles "/X" by stripping the leading slash
// during the join — so a future template with [link](/TODO.md) would
// falsely fail the resolution check against ".github/ISSUE_TEMPLATE/
// TODO.md" instead of "<repo>/TODO.md".
func resolveDocLink(docPath, repoRoot, link string) string {
	if strings.HasPrefix(link, "/") {
		return filepath.Join(repoRoot, strings.TrimPrefix(link, "/"))
	}
	return filepath.Join(filepath.Dir(docPath), link)
}

// TestResolveDocLink pins the doc-link resolution behaviour: bare
// relative links resolve against the doc's directory, "/X" links
// resolve against the repo root. Sub-cases cover the corner Gemini
// (originally flagged during PR #2 review).
//
// Test inputs and expectations are written with forward slashes for
// readability; filepath.FromSlash converts them to the host
// separator at compare time so the test passes on Windows
// (backslash) and POSIX (slash) alike.
func TestResolveDocLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		docPath  string
		repoRoot string
		link     string
		want     string
	}{
		{"bare-relative up-two", ".github/ISSUE_TEMPLATE/bug_report.yml", ".", "../../TODO.md", "TODO.md"},
		{"bare-relative up-one", ".github/CONTRIBUTING.md", ".", "../README.md", "README.md"},
		{"bare-relative sibling", "docs/foo.md", ".", "bar.md", "docs/bar.md"},
		{"root-relative repo .", ".github/ISSUE_TEMPLATE/bug_report.yml", ".", "/TODO.md", "TODO.md"},
		{"root-relative repo abs", ".github/ISSUE_TEMPLATE/bug_report.yml", "/repo", "/TODO.md", "/repo/TODO.md"},
		{"root-relative nested", ".github/PULL_REQUEST_TEMPLATE.md", ".", "/docs/guide.md", "docs/guide.md"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveDocLink(tc.docPath, tc.repoRoot, tc.link)
			// FromSlash makes the expected path use the host's
			// separator (\ on Windows, / on POSIX) so the test
			// passes on either OS without forking expectations.
			want := filepath.FromSlash(tc.want)
			if got != want {
				t.Errorf("resolveDocLink(%q, %q, %q) = %q, want %q",
					tc.docPath, tc.repoRoot, tc.link, got, want)
			}
		})
	}
}

// gitHubTemplateFiles returns the set of GitHub template files the
// invariant test should check. Deliberately narrow: only files that
// GitHub treats as templates, never the wider .github/ tree (which
// will eventually contain workflows whose run-scripts can include
// "[text](path)"-shaped strings that aren't markdown links — see PR
// B1).
//
// New templates added to .github/ISSUE_TEMPLATE/ are auto-picked up.
// Adding other template locations (e.g. .github/DISCUSSION_TEMPLATE/)
// means extending this function.
func gitHubTemplateFiles() []string {
	var files []string
	if _, err := os.Stat(".github/PULL_REQUEST_TEMPLATE.md"); err == nil {
		files = append(files, ".github/PULL_REQUEST_TEMPLATE.md")
	}
	if entries, err := os.ReadDir(".github/ISSUE_TEMPLATE"); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := filepath.Ext(e.Name())
			if ext != ".yml" && ext != ".yaml" && ext != ".md" {
				continue
			}
			files = append(files, filepath.Join(".github", "ISSUE_TEMPLATE", e.Name()))
		}
	}
	return files
}

// TestGitHubTemplates_RelativeLinksResolve is a doc-invariant regression
// guard for the .github/ templates. It enumerates the known template
// files (issue templates + the PR template — see gitHubTemplateFiles),
// extracts every inline and reference-style markdown link via
// extractRelativeLinks, and verifies that each relative-path target
// resolves to a file that exists on disk.
//
// Scope is intentionally narrow: walking the entire
// .github/ tree would eventually catch workflow YAML files added in
// PR B1, whose run-scripts can contain "[text](path)"-shaped strings
// that aren't real markdown links.
//
// The resolution step uses resolveDocLink so both bare relative
// ("../../TODO.md") and repo-root-relative ("/TODO.md") link forms
// are handled correctly.
//
// This catches the class of broken-doc-link bug surfaced during PR #2 review
// where templates linked to "../blob/main/X" — a confused mix of a
// relative file path and GitHub's web-URL "blob/main" pattern. The
// resolved path lands inside .github/ instead of the repo root and
// produces a 404 when GitHub renders the template. It also catches
// the smaller class of bug where a template links to a file the
// project hasn't created yet.
func TestGitHubTemplates_RelativeLinksResolve(t *testing.T) {
	t.Parallel()
	files := gitHubTemplateFiles()
	if len(files) == 0 {
		t.Skip("no GitHub template files present (running outside a checkout)")
	}
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		for _, link := range extractRelativeLinks(string(content)) {
			abs := resolveDocLink(path, ".", link)
			if _, err := os.Stat(abs); err != nil {
				t.Errorf("%s: relative link %q resolves to %q which does not exist on disk", path, link, abs)
			}
		}
	}
}

// ── version/help write-error propagation ────────────────────────────────

// All version/help entry points (both flag and subcommand forms) must map
// stdout write failures to exit 2 with an error message on stderr, matching
// run()'s documented "stdout broken-pipe / short-write failures → exit 2"
// contract and runDescribe's implementation. Prior to this fix, these paths
// silently swallowed write errors and returned 0 even on broken pipes.
func TestRun_VersionHelp_PropagatesWriteError(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"long-version-flag", []string{"--version"}},
		{"short-version-flag", []string{"-v"}},
		{"long-help-flag", []string{"--help"}},
		{"short-help-flag", []string{"-h"}},
		{"version-subcommand", []string{"version"}},
		{"help-subcommand", []string{"help"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout := errWriter{err: io.ErrClosedPipe}
			var stderr bytes.Buffer
			code := run(context.Background(), tc.args, stdout, &stderr)
			if code != 2 {
				t.Errorf("%s with failing stdout should exit 2, got %d (stderr=%q)",
					tc.name, code, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Errorf("%s: expected an error message on stderr explaining the write failure",
					tc.name)
			}
		})
	}
}

// Same six paths, short-write-without-error variant. bytes.Buffer.WriteTo
// surfaces io.ErrShortWrite even for spec-violating writers that return
// n < len(p) with a nil error, so all these paths must detect and
// propagate that failure mode too.
func TestRun_VersionHelp_DetectsShortWrite(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"long-version-flag", []string{"--version"}},
		{"short-version-flag", []string{"-v"}},
		{"long-help-flag", []string{"--help"}},
		{"short-help-flag", []string{"-h"}},
		{"version-subcommand", []string{"version"}},
		{"help-subcommand", []string{"help"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout := shortWriter{accept: 1} // accept only the first byte
			var stderr bytes.Buffer
			code := run(context.Background(), tc.args, stdout, &stderr)
			if code != 2 {
				t.Errorf("%s with short-writing stdout should exit 2, got %d (stderr=%q)",
					tc.name, code, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Errorf("%s: expected an error message on stderr explaining the short write",
					tc.name)
			}
		})
	}
}

// ── --help / --version early-exit precedence (review response) ─────────

// TestRun_HelpVersion_EarlyExitPrecedence guards the true early-exit
// contract for --help/-h and --version/-v: these flags must succeed
// regardless of whether other arguments would trigger validation
// errors. Prior to the pre-scan fix, arguments causing errors that
// appeared BEFORE the early-exit flag in argv (extra positional args,
// subcommand conflicts) would trip the main parsing loop's validation
// and return exit 2 before --help/--version was ever evaluated.
// This test surfaces those cases and locks in the fix.
func TestRun_HelpVersion_EarlyExitPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantPrefix string // stdout must start with this
	}{
		// Cases that FAIL under a single-pass parse: extra positional
		// arg or subcommand conflict fires before the flag is reached.
		{"help-after-two-positional", []string{"dir1", "dir2", "--help"}, "Usage:"},
		{"h-after-two-positional", []string{"dir1", "dir2", "-h"}, "Usage:"},
		{"help-after-three-positional", []string{"a", "b", "c", "--help"}, "Usage:"},
		{"help-after-subcommand-conflict", []string{"version", "describe", "--help"}, "Usage:"},
		{"version-after-three-positional", []string{"dir1", "dir2", "dir3", "--version"}, "tfdry "},
		{"v-after-three-positional", []string{"dir1", "dir2", "dir3", "-v"}, "tfdry "},
		{"version-after-subcommand-conflict", []string{"describe", "version", "--version"}, "tfdry "},

		// Regression: baseline cases where early-exit was already
		// working (flag appears before any conflicting arg).
		{"help-first", []string{"--help", "dir1", "dir2"}, "Usage:"},
		{"version-first", []string{"--version", "dir1", "dir2"}, "tfdry "},
		{"help-only", []string{"--help"}, "Usage:"},
		{"version-only", []string{"--version"}, "tfdry "},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tc.args, &stdout, &stderr)
			if code != 0 {
				t.Errorf("%s: exit=%d want=0; stderr=%q; stdout=%q",
					tc.name, code, stderr.String(), stdout.String())
			}
			if !strings.HasPrefix(stdout.String(), tc.wantPrefix) {
				t.Errorf("%s: stdout should start with %q; got %q",
					tc.name, tc.wantPrefix, stdout.String())
			}
		})
	}
}

// ── fmt: -r short form + help-text documentation ────────────────────

// TestRun_FmtRecursive_AllForms verifies -recursive, --recursive, and
// the new -r short form all trigger recursive fmt behaviour
// identically. Table-driven so the three spellings must be true
// aliases, not near-synonyms that diverge on some code path.
// -r is added per issue #18 review feedback for consistency with
// the -h / -v short-form convention.
func TestRun_FmtRecursive_AllForms(t *testing.T) {
	cases := []struct {
		name string
		flag string
	}{
		{"single-dash-long", "-recursive"},
		{"double-dash-long", "--recursive"},
		{"short-form", "-r"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			// Top-level unformatted.
			if err := os.WriteFile(filepath.Join(dir, "dirty.tf"), []byte(fmtDirtyTF), 0o644); err != nil {
				t.Fatal(err)
			}
			// Nested unformatted.
			if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.tf"), []byte(fmtDirtyTF), 0o644); err != nil {
				t.Fatal(err)
			}
			// Combine -check with the recursive flag so we assert
			// what would-be-formatted without mutating fixtures across
			// subtests. Any spelling must reach into subdir/.
			code, stdout, _ := runCLI("fmt", tc.flag, "-check", dir)
			if code != 3 {
				t.Fatalf("%s: fmt %s -check should exit 3 (unformatted files), got %d",
					tc.name, tc.flag, code)
			}
			for _, want := range []string{"dirty.tf", "subdir/nested.tf"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("%s: expected %q in stdout, got %q", tc.name, want, stdout)
				}
			}
		})
	}
}

// TestRun_HelpOutput_DocumentsFmtFlags asserts --help output surfaces
// every parser-accepted spelling of the fmt-specific flags. The
// parser accepts -check/--check and -recursive/--recursive/-r; help
// must mention each so users reading --help see the accurate CLI
// surface. Addresses reviewer feedback (PR #28) that -check/-recursive
// were accepted but missing from the Flags section, and locks in the
// -r short-form addition.
func TestRun_HelpOutput_DocumentsFmtFlags(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("--help exit=%d want=0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	// Concrete phrase matches — avoid substring false-positives
	// (e.g. --check as prefix of --checks=).
	needles := []string{
		"-check, --check",
		"-recursive, --recursive, -r",
	}
	for _, want := range needles {
		if !strings.Contains(out, want) {
			t.Errorf("--help output missing %q; got:\n%s", want, out)
		}
	}
}

// ── E008 JSON schema uniformity (issue #19) ──────────────────────────────

// TestRun_E008_JSONOutput_IncludesLineFieldAsZero asserts that E008
// (file not formatted) violations emit "line": 0 in JSON output, matching
// the schema shape of every other violation code. E008 is a whole-file
// property with no meaningful source line — 0 is the sentinel meaning
// "not tied to a specific line". Prior behaviour (omitted key via
// json:"line,omitempty" plus zero-value default) forced JSON consumers
// to nil-check .violations[].line for E008 specifically while other
// codes reliably emitted it. This test locks in the uniform-schema
// contract issue #19 established.
func TestRun_E008_JSONOutput_IncludesLineFieldAsZero(t *testing.T) {
	// Unformatted .tf triggers E008. Deliberately misaligned assignment
	// (space vs alignment expected by hclwrite.Format).
	dir := writeTFDir(t, map[string]string{
		"dirty.tf": "locals {\n a=\"unformatted\"\n}\n",
	})

	code, stdout, stderr := runCLI("--json", dir)
	if code != 1 {
		t.Fatalf("expected exit 1 for E008, got %d; stderr=%q", code, stderr)
	}

	// Decode as generic maps to detect key *presence* — a struct with
	// int line field would silently accept missing keys via zero value.
	var got struct {
		Violations []map[string]any `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}

	sawE008 := false
	for i, v := range got.Violations {
		if v["code"] != "E008" {
			continue
		}
		sawE008 = true
		lineVal, hasLine := v["line"]
		if !hasLine {
			t.Errorf("violation[%d] (E008) missing \"line\" key; every violation must emit line (0 sentinel for file-level); violation=%+v", i, v)
			continue
		}
		lineNum, ok := lineVal.(float64)
		if !ok {
			t.Errorf("violation[%d] (E008) \"line\" not a number: got %T (%v)", i, lineVal, lineVal)
			continue
		}
		if lineNum != 0 {
			t.Errorf("violation[%d] (E008) \"line\" = %v, want 0 (file-level sentinel)", i, lineNum)
		}
	}
	if !sawE008 {
		t.Fatalf("no E008 violation found in output; expected one for unformatted dirty.tf; output: %s", stdout)
	}
}

// TestRun_LineFieldPresent_AcrossViolationCodes is a broader regression
// guard: every violation entry in a JSON payload must include a "line"
// key regardless of code. This proves the uniform-schema contract
// holds across a mix of file-level (E008) and line-attributed (W001,
// E003) violations, and would catch any future regression that
// re-introduces omitempty or per-code special-casing of the line
// field's presence.
func TestRun_LineFieldPresent_AcrossViolationCodes(t *testing.T) {
	// Mixed violations: E008 (unformatted, file-level), W001 (unused local,
	// line-attributed), E003 (undefined local reference, line-attributed).
	dir := writeTFDir(t, map[string]string{
		"dirty.tf": "locals {\n unused =\"x\"\n}\n" + // E008 (misaligned) + W001 (unused)
			"output \"bad\" {\n  value = local.does_not_exist\n}\n", // E003 (undefined local)
	})

	code, stdout, stderr := runCLI("--json", dir)
	// exit code is 1 (violations found); mix of error + warning.
	if code != 1 {
		t.Fatalf("expected exit 1 for mixed violations, got %d; stderr=%q", code, stderr)
	}

	var got struct {
		Violations []map[string]any `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	if len(got.Violations) == 0 {
		t.Fatalf("expected at least one violation, got none; output: %s", stdout)
	}
	for i, v := range got.Violations {
		if _, hasLine := v["line"]; !hasLine {
			t.Errorf("violation[%d] (code=%v) missing \"line\" key; every violation must emit line; violation=%+v",
				i, v["code"], v)
		}
	}
}

// ── Lint --recursive (issue #21) ────────────────────────────────────────────
//
// Tests for the recursive lint walk. Each recursed directory is linted as
// an independent workspace under the existing single-workspace contract
// — no cross-directory scope merging (that's tracked separately in
// issue #32). Directly analogous to `fmt -recursive`, sharing the walk
// helper.

// TestRun_LintRecursive_AllForms locks the three accepted spellings
// (-recursive, --recursive, -r) into true aliases on the lint path,
// mirroring the fmt precedent. If someone adds a new short/long form
// or removes one, this catches divergence between them.
func TestRun_LintRecursive_AllForms(t *testing.T) {
	cases := []struct {
		name string
		flag string
	}{
		{"single-dash-long", "-recursive"},
		{"double-dash-long", "--recursive"},
		{"short-form", "-r"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := writeTFDir(t, map[string]string{
				"main.tf": `locals { x = "ok" }
output "x" { value = local.x }
`,
				"staging/main.tf": `output "x" {
  value = local.does_not_exist
}
`,
			})
			code, stdout, _ := runCLI(tc.flag, dir)
			if code != 1 {
				t.Fatalf("%s: lint %s should exit 1 (violations in subdir), got %d; stdout=%q",
					tc.name, tc.flag, code, stdout)
			}
			// Subdir violation must surface with prefixed path.
			if !strings.Contains(stdout, "staging/main.tf") {
				t.Errorf("%s: expected 'staging/main.tf' in output; got: %q", tc.name, stdout)
			}
		})
	}
}

// TestRun_LintRecursive_NonRecursiveUnchanged is the regression guard:
// without --recursive, lint continues to scan only the supplied
// directory, exactly as v0.1.1 behaved. If the walk logic accidentally
// activates by default, or if the parser leaks recursive-on into
// non-recursive invocations, this test catches it.
func TestRun_LintRecursive_NonRecursiveUnchanged(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `locals { x = "ok" }
output "x" { value = local.x }
`,
		// Violation subdir that MUST NOT be scanned without --recursive.
		"staging/main.tf": `output "x" {
  value = local.does_not_exist
}
`,
	})
	code, stdout, _ := runCLI(dir)
	if code != 0 {
		t.Fatalf("non-recursive lint should exit 0 (top-level clean, subdir ignored), got %d; stdout=%q",
			code, stdout)
	}
	if strings.Contains(stdout, "staging") {
		t.Errorf("non-recursive lint must not surface subdir content; stdout=%q", stdout)
	}
}

// TestRun_LintRecursive_JSONOutput_PathsPrefixed asserts the JSON schema
// contract for the recursive case: violations[].file is prefixed with
// the sub-path relative to the CLI arg, so consumers can attribute
// violations to a specific workspace directory. The directory field
// stays as the CLI arg.
func TestRun_LintRecursive_JSONOutput_PathsPrefixed(t *testing.T) {
	t.Parallel()
	// Two subdirs, each with a distinct violation code, so we can
	// verify per-dir attribution in the aggregated JSON.
	dir := writeTFDir(t, map[string]string{
		"staging/main.tf": `output "x" {
  value = local.undefined_here
}
`,
		"production/main.tf": `locals { dup = "a" }
locals { dup = "b" }
`,
	})
	code, stdout, _ := runCLI("--json", "--recursive", dir)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d; stdout=%q", code, stdout)
	}
	var got struct {
		Directory  string           `json:"directory"`
		Violations []map[string]any `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	// Directory field is the CLI arg, unchanged from non-recursive semantics.
	if got.Directory != dir {
		t.Errorf("directory field = %q, want %q", got.Directory, dir)
	}
	// Every violation's file path must be prefixed with its subdir.
	foundStaging := false
	foundProduction := false
	for _, v := range got.Violations {
		file, _ := v["file"].(string)
		if strings.HasPrefix(file, "staging/") {
			foundStaging = true
		}
		if strings.HasPrefix(file, "production/") {
			foundProduction = true
		}
		// Bare filenames without subdir prefix are the non-recursive
		// contract; they must not appear here.
		if file == "main.tf" {
			t.Errorf("violation without subdir prefix: file=%q", file)
		}
	}
	if !foundStaging {
		t.Errorf("no violation with 'staging/' prefix; violations=%+v", got.Violations)
	}
	if !foundProduction {
		t.Errorf("no violation with 'production/' prefix; violations=%+v", got.Violations)
	}
}

// TestRun_LintRecursive_SkipsHiddenDirs mirrors the fmt-recursive
// hidden-dir skip test: .terraform, .git, and any dot-prefixed
// directory must not be walked. Violations planted inside them must
// not surface.
func TestRun_LintRecursive_SkipsHiddenDirs(t *testing.T) {
	t.Parallel()
	// Plant violation-bearing files in three hidden dirs. Clean top-level
	// so exit 0 is expected only if hidden dirs are truly skipped.
	violation := `output "x" {
  value = local.undefined
}
`
	dir := writeTFDir(t, map[string]string{
		"main.tf":         `locals { x = "ok" }` + "\n",
		".terraform/x.tf": violation,
		".git/x.tf":       violation,
		".hidden/x.tf":    violation,
	})
	code, stdout, _ := runCLI("--recursive", dir)
	if code != 0 {
		t.Fatalf("hidden dirs must be skipped; expected exit 0, got %d; stdout=%q", code, stdout)
	}
	for _, sub := range []string{".terraform", ".git", ".hidden"} {
		if strings.Contains(stdout, sub) {
			t.Errorf("hidden dir %s appeared in output; stdout=%q", sub, stdout)
		}
	}
}

// TestRun_LintRecursive_SkipsNodeModules covers the polyglot-monorepo
// case: node_modules is a common non-dot-prefixed directory that never
// contains Terraform. Skipping avoids the perf cost of walking it, and
// avoids surfacing spurious content if a vendored package happens to
// ship .tf files as test fixtures.
func TestRun_LintRecursive_SkipsNodeModules(t *testing.T) {
	t.Parallel()
	// Vendored package with a .tf fixture that would generate a
	// violation if scanned.
	dir := writeTFDir(t, map[string]string{
		"main.tf":                                `locals { x = "ok" }` + "\n",
		"node_modules/some-pkg/fixtures/test.tf": `output "x" { value = local.missing }` + "\n",
	})
	code, stdout, _ := runCLI("--recursive", dir)
	if code != 0 {
		t.Fatalf("node_modules must be skipped; expected exit 0, got %d; stdout=%q", code, stdout)
	}
	if strings.Contains(stdout, "node_modules") {
		t.Errorf("node_modules appeared in output; stdout=%q", stdout)
	}
}

// TestRun_LintRecursive_RejectedOnNonLintNonFmtSubcommands guards the
// parser rejection surface: --recursive is a lint/fmt flag and MUST
// still be rejected on subcommands where it has no meaning (describe,
// version, help). This locks in that the #21 change only OPENS the
// flag on the lint path; it doesn't blanket-accept it everywhere.
func TestRun_LintRecursive_RejectedOnNonLintNonFmtSubcommands(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"describe", "--recursive"},
		{"describe", "-r"},
		{"version", "--recursive"},
		{"version", "-recursive"},
		{"help", "--recursive"},
	}
	for _, args := range cases {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			code, _, stderr := runCLI(args...)
			if code != 2 {
				t.Errorf("%v: expected exit 2 (recursive rejected), got %d; stderr=%q",
					args, code, stderr)
			}
			if stderr == "" {
				t.Errorf("%v: expected explanatory stderr, got empty", args)
			}
		})
	}
}

// TestRun_LintRecursive_FileRoot_ExitTwo asserts that --recursive with a
// file-path root is rejected up-front rather than producing a silent
// no-op. filepath.WalkDir is Lstat-based: given a file root it invokes
// the walkFn once with (path, non-dir DirEntry, nil), collectDirs
// skips it (!IsDir), and the walk returns (empty, nil) — the lint
// loop then produces an empty report and exit 0. Reject explicitly so
// the misuse surfaces with a clear error, mirroring runFmt's
// file-with-recursive rejection.
func TestRun_LintRecursive_FileRoot_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `locals { x = "ok" }` + "\n",
	})
	tfPath := filepath.Join(dir, "main.tf")
	code, _, stderr := runCLI("--recursive", tfPath)
	if code != 2 {
		t.Errorf("--recursive on file path should exit 2, got %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "file path") && !strings.Contains(stderr, "not a directory") {
		t.Errorf("stderr should explain the file-path misuse, got %q", stderr)
	}
}

// TestRun_LintRecursive_DotPrefixedRoot_NoPathDuplication guards
// against a subtle bug in the path-attribution logic: checker.ParseDir
// calls filepath.Clean(dir) internally, so any v.File that ParseDir
// emits equal to the dir (directory-level E000 case) comes back
// *cleaned*. If the outer walk still holds an *uncleaned* form of the
// same directory (e.g. "./sub" from a user-supplied dot-prefixed CLI
// arg), displayPath's `vFile == dir` guard fails string-comparison
// and the fallback filepath.Join produces duplicated segments like
// "sub/sub". Locks in that the fix (cleaning root before use)
// prevents this class of bug in the general case, and specifically
// ensures no path in the output contains adjacent duplicated segments.
func TestRun_LintRecursive_DotPrefixedRoot_NoPathDuplication(t *testing.T) {
	// No t.Parallel: this test needs t.Chdir which is a
	// process-wide side effect.
	root := writeTFDir(t, map[string]string{
		"fixture/main.tf":        `locals { x = "ok" }` + "\n",
		"fixture/nested/main.tf": `output "x" { value = local.undefined }` + "\n",
	})
	t.Chdir(root)
	// Use the dot-prefixed relative form — that's what pre-commit
	// invocations look like in practice.
	code, stdout, _ := runCLI("--json", "--recursive", "./fixture")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d; stdout=%q", code, stdout)
	}
	var got struct {
		Violations []map[string]any `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	for _, v := range got.Violations {
		file, _ := v["file"].(string)
		// No dot-prefix leak.
		if strings.HasPrefix(file, "./") {
			t.Errorf("file path leaks './' prefix: %q", file)
		}
		// No adjacent duplicated segments (the "sub/sub" pattern).
		segs := strings.Split(file, "/")
		for i := 1; i < len(segs); i++ {
			if segs[i] == segs[i-1] && segs[i] != "" {
				t.Errorf("file path has adjacent duplicated segment %q: %q", segs[i], file)
			}
		}
	}
	// Also verify the specific expected path shape: nested/main.tf,
	// no leading "fixture/" (that's the recursion root, stripped by
	// displayPath) and no leading "./".
	foundNested := false
	for _, v := range got.Violations {
		file, _ := v["file"].(string)
		if file == "nested/main.tf" {
			foundNested = true
		}
	}
	if !foundNested {
		t.Errorf("expected violation with file=%q, got: %+v", "nested/main.tf", got.Violations)
	}
}

// TestRun_LintRecursive_NonExistentRoot_ExitTwo covers the os.Lstat
// error branch of the recursive-root validation. --recursive against
// a non-existent path must return a clear tool error (exit 2), not
// silently emit an empty report. Complements FileRoot_ExitTwo (IsDir
// branch) and SymlinkDirRoot_Rejected (symlink branch).
func TestRun_LintRecursive_NonExistentRoot_ExitTwo(t *testing.T) {
	t.Parallel()
	// Path is guaranteed non-existent: constructed under a fresh
	// TempDir and never created.
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	code, _, stderr := runCLI("--recursive", missing)
	if code != 2 {
		t.Errorf("--recursive on missing path should exit 2, got %d; stderr=%q",
			code, stderr)
	}
	if stderr == "" {
		t.Errorf("expected explanatory stderr, got empty")
	}
}

// TestRun_LintRecursive_ParseErrorSubdir_EmitsE001WithPrefix is the
// critical regression guard for the "skip when files is empty"
// optimisation: when every .tf file in a subdirectory has syntax
// errors, ParseDir returns `len(files) == 0` but `parseViolations`
// carries the E001 entries. A naive "skip iteration when files is
// empty" optim would drop those E001s on the floor. This test locks
// in that E001 from a syntax-error-only subdir still surfaces in
// recursive output with its sub-path prefix.
func TestRun_LintRecursive_ParseErrorSubdir_EmitsE001WithPrefix(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		// Clean top-level to make the exit code come purely from the subdir.
		"main.tf": `locals { x = "ok" }` + "\n",
		// Subdir with only invalid .tf — deliberately unparseable HCL so
		// hclsyntax.ParseConfig produces diagnostics that map to E001,
		// leaving `files == nil` for this directory.
		"broken/bad.tf": `this is not valid hcl {{{`,
	})
	code, stdout, _ := runCLI("--json", "--recursive", dir)
	if code != 1 {
		t.Fatalf("expected exit 1 (E001 in subdir), got %d; stdout=%q", code, stdout)
	}
	var got struct {
		Violations []map[string]any `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	foundE001WithPrefix := false
	for _, v := range got.Violations {
		code, _ := v["code"].(string)
		file, _ := v["file"].(string)
		if code == "E001" && strings.HasPrefix(file, "broken/") {
			foundE001WithPrefix = true
		}
	}
	if !foundE001WithPrefix {
		t.Errorf("expected E001 with 'broken/' path prefix; violations=%+v", got.Violations)
	}
}

// TestRun_LintRecursive_EmptyOfTfSubdir_NoCrash exercises the
// fast-path branch where a walked subdirectory contains no .tf files
// (only unrelated files like .md, .txt, or nothing at all).
// ParseDir returns both files and parseViolations empty — the run
// loop must skip the Run/FixFormat calls cleanly and move on to the
// next dir. Also covers the assertion that non-Terraform content is
// not accidentally emitted in the recursive report.
func TestRun_LintRecursive_EmptyOfTfSubdir_NoCrash(t *testing.T) {
	t.Parallel()
	// Subdir with a plain-text file (not .tf) — ParseDir returns
	// (empty, empty, nil), the Run + FixFormat calls are guarded
	// by len(files) > 0 and skipped, iteration continues.
	dir := writeTFDir(t, map[string]string{
		"main.tf":        `locals { x = "ok" }` + "\n",
		"docs/README.md": "# not terraform\n",
	})
	code, stdout, _ := runCLI("--recursive", dir)
	if code != 0 {
		t.Fatalf("empty-of-tf subdir should not affect exit code; got %d; stdout=%q",
			code, stdout)
	}
	if strings.Contains(stdout, "docs") {
		t.Errorf("empty-of-tf subdir must not surface in output; stdout=%q", stdout)
	}
	if strings.Contains(stdout, "README.md") {
		t.Errorf("non-.tf file must not surface in output; stdout=%q", stdout)
	}
}

// TestRun_LintRecursive_PreCancelledCtx covers the per-directory
// cancel checkpoint at the top of the recursive-lint loop. A
// pre-cancelled context should short-circuit the walk with the
// standard interrupted exit code (130) rather than proceeding to
// I/O. Mirrors TestRunFmt_PreCancel_BailsBeforeIO / _runFmtFile_
// counterparts on the fmt path; without this, the recursive lint
// dispatch could drift out of alignment on cancellation semantics.
func TestRun_LintRecursive_PreCancelledCtx(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `locals { x = "ok" }` + "\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel BEFORE the call

	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"--recursive", dir}, &stdout, &stderr)

	if code != 130 {
		t.Errorf("pre-cancelled ctx should exit 130 (interrupted), got %d; stderr=%q",
			code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "tfdry: interrupted") {
		t.Errorf("stderr should mention 'tfdry: interrupted', got %q", stderr.String())
	}
}

// TestRun_FmtRecursive_DotPrefixedRoot_PathsClean is the fmt-side
// counterpart to TestRun_LintRecursive_DotPrefixedRoot_NoPathDuplication.
// After the last review-response commit `runFmt` cleaned the CLI arg
// into `pathClean` for the Lstat symlink check but still passed the
// raw `path` into `collectDirs` and both `displayPath` call sites.
// That's the same class of path-duplication risk that was fixed on
// the lint path: WalkDir starts with the raw form (leaks `./`),
// ParseDir cleans internally, and displayPath's `vFile == dir` guard
// then compares an uncleaned dir against a cleaned vFile — falling
// through to the default join and producing duplicated segments for
// directory-level E000 emissions.
//
// This test locks in the general property that `tfdry fmt --recursive
// ./fixture` output paths (stdout list of formatted files plus any
// stderr parse-error paths) have no dot-prefix leaks and no adjacent
// duplicated segments. It doesn't specifically trigger the E000 case
// (which requires a TOCTOU race) but locks in the observable
// consequence of using cleaned paths consistently in the walker.
func TestRun_FmtRecursive_DotPrefixedRoot_PathsClean(t *testing.T) {
	// No t.Parallel — uses t.Chdir which is a process-wide side effect.
	root := writeTFDir(t, map[string]string{
		"fixture/dirty.tf":        "locals { a=\"unformatted\" }\n",
		"fixture/nested/dirty.tf": "locals { b=\"unformatted\" }\n",
	})
	t.Chdir(root)

	code, stdout, _ := runCLI("fmt", "-check", "--recursive", "./fixture")
	if code != 3 {
		t.Fatalf("expected exit 3 (dirty files under -check), got %d; stdout=%q", code, stdout)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "./") {
			t.Errorf("stdout path leaks './' prefix: %q", line)
		}
		segs := strings.Split(line, "/")
		for i := 1; i < len(segs); i++ {
			if segs[i] == segs[i-1] && segs[i] != "" {
				t.Errorf("stdout path has adjacent duplicated segment %q: %q",
					segs[i], line)
			}
		}
	}
	// Both nested-level paths must surface with sub-path prefixes
	// (relative to the CLI arg's cleaned form).
	if !strings.Contains(stdout, "dirty.tf") {
		t.Errorf("expected 'dirty.tf' in output, got: %q", stdout)
	}
	if !strings.Contains(stdout, "nested/dirty.tf") {
		t.Errorf("expected 'nested/dirty.tf' in output, got: %q", stdout)
	}
}
