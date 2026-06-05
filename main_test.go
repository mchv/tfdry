package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI invokes the package-level run() function with captured stdout/stderr.
// Returns exit code, stdout, stderr.
func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// writeTFDir creates a temp dir with the given files and returns its path.
func writeTFDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ── Exit codes ───────────────────────────────────────────────────────────────

func TestRun_CleanDir_ExitZero(t *testing.T) {
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
	dir := writeTFDir(t, map[string]string{
		"main.tf": "locals{a=\"x\"}\n",
	})
	code, _, _ := runCLI("--fix", dir)
	if code != 0 {
		t.Fatalf("expected exit 0 after --fix, got %d", code)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "main.tf"))
	if !strings.Contains(string(got), "a = \"x\"") {
		t.Errorf("file was not reformatted; got: %q", got)
	}
}

func TestRun_FixWithChecksFilterExcludingE008_DoesNotFix(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": "locals{a=\"x\"}\n",
	})
	original, _ := os.ReadFile(filepath.Join(dir, "main.tf"))
	code, _, _ := runCLI("--fix", "--checks=E001,E002", dir)
	got, _ := os.ReadFile(filepath.Join(dir, "main.tf"))
	if !bytes.Equal(original, got) {
		t.Errorf("--fix should not run when E008 is excluded by --checks=, but file was modified")
	}
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

// ── --checks= edge cases ─────────────────────────────────────────────────────

func TestRun_ChecksEmpty_ExitTwo(t *testing.T) {
	code, _, stderr := runCLI("--checks=", ".")
	if code != 2 {
		t.Fatalf("expected exit 2 for empty --checks=, got %d", code)
	}
	if !strings.Contains(stderr, "--checks=") {
		t.Errorf("stderr should mention --checks=, got: %q", stderr)
	}
}

func TestRun_ChecksUnknownCode_ExitTwo(t *testing.T) {
	code, _, stderr := runCLI("--checks=E999", ".")
	if code != 2 {
		t.Fatalf("expected exit 2 for unknown check, got %d", code)
	}
	if !strings.Contains(stderr, "E999") && !strings.Contains(stderr, "unknown") {
		t.Errorf("stderr should mention the unknown code, got: %q", stderr)
	}
}

func TestRun_ChecksFilters_OnlyEmitsRequestedCodes(t *testing.T) {
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

// ── describe subcommand ──────────────────────────────────────────────────────

func TestRun_Describe_ListsChecks(t *testing.T) {
	code, stdout, _ := runCLI("describe")
	if code != 0 {
		t.Fatalf("describe should exit 0, got %d", code)
	}
	for _, code := range []string{"E001", "E008", "W001"} {
		if !strings.Contains(stdout, code) {
			t.Errorf("describe output missing %s; got: %s", code, stdout)
		}
	}
}

func TestRun_DescribeJSON_ParsesAndContainsAllCodes(t *testing.T) {
	code, stdout, _ := runCLI("describe", "--json")
	if code != 0 {
		t.Fatalf("describe --json should exit 0, got %d", code)
	}
	var got struct {
		Checks []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	codes := map[string]bool{}
	for _, c := range got.Checks {
		codes[c.Code] = true
	}
	for _, want := range []string{"E001", "E002", "E003", "E004", "E005", "E006", "E007", "E008", "W001"} {
		if !codes[want] {
			t.Errorf("describe --json missing %s", want)
		}
	}
}

// ── version subcommand ───────────────────────────────────────────────────────

func TestRun_Version_PrintsVersion(t *testing.T) {
	code, stdout, _ := runCLI("version")
	if code != 0 {
		t.Fatalf("version should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "tfdry") {
		t.Errorf("version output should contain 'tfdry'; got: %q", stdout)
	}
}

// ── default dir argument ─────────────────────────────────────────────────────

func TestRun_DefaultDirIsCurrent(t *testing.T) {
	// No args → defaults to ".". cwd is the project root which has no .tf files.
	// Exit code depends on whether checker reports any errors on an empty/non-tf dir.
	// We just verify the call returns and doesn't panic.
	code, _, _ := runCLI()
	if code < 0 {
		t.Fatalf("run() should not return negative exit code, got %d", code)
	}
}

// ── fmt subcommand (mirrors terraform fmt) ───────────────────────────────────

const fmtDirtyTF = "locals {\n  a=\"b\"\n   c =   \"d\"\n}\n"
const fmtCleanTF = "locals {\n  a = \"b\"\n  c = \"d\"\n}\n"

func TestRun_Fmt_RewritesUnformattedFile_PrintsName_ExitZero(t *testing.T) {
	dir := writeTFDir(t, map[string]string{"dirty.tf": fmtDirtyTF})
	code, stdout, _ := runCLI("fmt", dir)
	if code != 0 {
		t.Fatalf("fmt should exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "dirty.tf") {
		t.Fatalf("expected dirty.tf in output, got %q", stdout)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "dirty.tf"))
	if string(got) != fmtCleanTF {
		t.Fatalf("file not formatted:\nexpected: %q\ngot:      %q", fmtCleanTF, string(got))
	}
}

func TestRun_Fmt_AlreadyFormatted_NoOutput_ExitZero(t *testing.T) {
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
	dir := writeTFDir(t, map[string]string{"dirty.tf": fmtDirtyTF})
	code, stdout, _ := runCLI("fmt", "-check", dir)
	if code != 3 {
		t.Fatalf("fmt -check on dirty dir should exit 3, got %d", code)
	}
	if !strings.Contains(stdout, "dirty.tf") {
		t.Fatalf("expected dirty.tf in output, got %q", stdout)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "dirty.tf"))
	if string(got) != fmtDirtyTF {
		t.Fatalf("fmt -check must not rewrite the file, got %q", string(got))
	}
}

func TestRun_FmtCheck_Clean_ExitZero(t *testing.T) {
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
	dir := t.TempDir()
	// Top-level dirty.
	if err := os.WriteFile(filepath.Join(dir, "dirty.tf"), []byte(fmtDirtyTF), 0644); err != nil {
		t.Fatal(err)
	}
	// Nested dirty in subdir/.
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.tf"), []byte(fmtDirtyTF), 0644); err != nil {
		t.Fatal(err)
	}
	// Deeper.
	if err := os.MkdirAll(filepath.Join(dir, "subdir", "deep"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "deep", "deep.tf"), []byte(fmtDirtyTF), 0644); err != nil {
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
		got, _ := os.ReadFile(filepath.Join(dir, p))
		if string(got) != fmtCleanTF {
			t.Errorf("%s not formatted: %q", p, string(got))
		}
	}
}

func TestRun_FmtRecursive_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{".terraform", ".git", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "x.tf"), []byte(fmtDirtyTF), 0644); err != nil {
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
		got, _ := os.ReadFile(filepath.Join(dir, sub, "x.tf"))
		if string(got) != fmtDirtyTF {
			t.Errorf("file in hidden dir %s was modified: %q", sub, string(got))
		}
	}
}

func TestRun_FmtRecursiveCheck_DirtyInSubdir_ExitThree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clean.tf"), []byte(fmtCleanTF), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.tf"), []byte(fmtDirtyTF), 0644); err != nil {
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
	got, _ := os.ReadFile(filepath.Join(dir, "subdir", "nested.tf"))
	if string(got) != fmtDirtyTF {
		t.Errorf("file rewritten despite -check: %q", string(got))
	}
}
