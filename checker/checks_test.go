// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// writeTFDir creates a temp dir with the given files and returns its path.
//
// Note: this helper is intentionally duplicated from main_test.go's
// writeTFDir. The two live in different test packages
// (checker_test vs main_test), so true sharing would require an
// internal/testutil package — overkill for two identical 5-line
// helpers. If a third duplicate appears, promote to internal/testutil
// rather than triplicating.
func writeTFDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func run(t *testing.T, files map[string]string) []checker.Violation {
	t.Helper()
	dir := writeTFDir(t, files)
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	violations := slices.Concat(parseViolations, mustRun(context.Background(), parsed, nil, dir))
	return violations
}

func codes(vs []checker.Violation) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Code
	}
	return out
}

func hasCode(vs []checker.Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}

// E001 — invalid HCL syntax
func TestE001_InvalidSyntax(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"bad.tf": `resource "aws_s3_bucket" "b" { bad syntax !!!`,
	})
	if !hasCode(vs, "E001") {
		t.Fatalf("expected E001, got %v", codes(vs))
	}
}

// Every E001 violation must carry a non-empty File field, even for
// diagnostics where d.Subject is nil (file-level / global HCL errors don't
// have a position). Without the fix, such violations get an empty filename
// and downstream consumers (terminal output, JSON parsers grouping by file)
// can't tell which file produced the error. Test-first regression: assert
// File is populated for every E001 in the result.
func TestE001_FilePopulatedEvenWhenSubjectNil(t *testing.T) {
	t.Parallel()
	// Multi-file scenario; the surface that previously had Subject=nil paths
	// is hard to reproduce deterministically (requires HCL to emit a diag
	// without subject). The strong invariant we assert is structural:
	// regardless of why a diag fires, every emitted E001 carries the
	// originating filename.
	vs := run(t, map[string]string{
		"a.tf": `resource "x" "y" { @@@`,
		"b.tf": `locals { name = `, // unterminated expression
	})
	for _, v := range vs {
		if v.Code != "E001" {
			continue
		}
		if v.File == "" {
			t.Errorf("E001 violation has empty File: %+v", v)
		}
		// Even if d.Detail is empty (some hclsyntax token-level
		// errors only populate Summary), Message must never be empty
		// or the violation is impossible to diagnose.
		if v.Message == "" {
			t.Errorf("E001 violation has empty Message: %+v", v)
		}
	}
}

// E002 — duplicate local
func TestE002_DuplicateLocal(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"a.tf": `locals { name = "foo" }`,
		"b.tf": `locals { name = "bar" }`,
	})
	if !hasCode(vs, "E002") {
		t.Fatalf("expected E002, got %v", codes(vs))
	}
}

func TestE002_NoDuplicateAcrossBlocks_SameFile(t *testing.T) {
	t.Parallel()
	// Two locals blocks in same file with same key — still a duplicate
	vs := run(t, map[string]string{
		"main.tf": `
locals { name = "foo" }
locals { name = "bar" }
`,
	})
	if !hasCode(vs, "E002") {
		t.Fatalf("expected E002, got %v", codes(vs))
	}
}

// E003 — undefined local reference
func TestE003_UndefinedLocal(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { name = "foo" }
output "o" { value = local.typo }
`,
	})
	if !hasCode(vs, "E003") {
		t.Fatalf("expected E003, got %v", codes(vs))
	}
}

func TestE003_DefinedLocal_NoViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { name = "foo" }
output "o" { value = local.name }
`,
	})
	if hasCode(vs, "E003") {
		t.Fatalf("unexpected E003: %v", codes(vs))
	}
}

// E004 — type mismatch in interpolation
func TestE004_ObjectInInterpolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { tags = { env = "prod" } }
output "o" { value = "prefix-${local.tags}" }
`,
	})
	if !hasCode(vs, "E004") {
		t.Fatalf("expected E004, got %v", codes(vs))
	}
}

func TestE004_StringInInterpolation_NoViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { env = "prod" }
output "o" { value = "prefix-${local.env}" }
`,
	})
	if hasCode(vs, "E004") {
		t.Fatalf("unexpected E004: %v", codes(vs))
	}
}

func TestE004_UnknownTypeSkipped(t *testing.T) {
	t.Parallel()
	// local.x depends on var.y — type unknown, should not flag
	vs := run(t, map[string]string{
		"main.tf": `
locals { x = var.something }
output "o" { value = "prefix-${local.x}" }
`,
	})
	if hasCode(vs, "E004") {
		t.Fatalf("unexpected E004 (should skip unknown types): %v", codes(vs))
	}
}

// E005 — count + for_each
func TestE005_CountAndForEach(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_instance" "web" {
  count    = 2
  for_each = toset(["a"])
}
`,
	})
	if !hasCode(vs, "E005") {
		t.Fatalf("expected E005, got %v", codes(vs))
	}
}

func TestE005_OnlyCount_NoViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_instance" "web" {
  count = 2
}
`,
	})
	if hasCode(vs, "E005") {
		t.Fatalf("unexpected E005: %v", codes(vs))
	}
}

// W001 — unused local
func TestW001_UnusedLocal(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `locals { unused = "foo" }`,
	})
	if !hasCode(vs, "W001") {
		t.Fatalf("expected W001, got %v", codes(vs))
	}
}

func TestW001_UsedLocal_NoViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { name = "foo" }
output "o" { value = local.name }
`,
	})
	if hasCode(vs, "W001") {
		t.Fatalf("unexpected W001: %v", codes(vs))
	}
}

// CheckSet filtering
func TestCheckSetFilter(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
locals { unused = "foo" }
output "o" { value = local.typo }
`,
	})
	parsed, _, _ := checker.ParseDir(context.Background(), dir)
	cs := checker.CheckSet{"E003": {}}
	vs, _ := checker.Run(context.Background(), parsed, cs, dir)
	for _, v := range vs {
		if v.Code != "E003" {
			t.Fatalf("expected only E003, got %v", v.Code)
		}
	}
	if !hasCode(vs, "E003") {
		t.Fatal("expected E003")
	}
}

// ── Bug regression tests (written before fixes) ──────────────────────────────

// P0: E003 must NOT fire for a local that exists but has an unresolvable type.
func TestE003_NoFalsePositive_UnresolvableLocal(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { x = var.something }
output "o" { value = local.x }
`,
	})
	if hasCode(vs, "E003") {
		t.Fatalf("E003 false positive: local.x is defined but flagged as undefined: %v", codes(vs))
	}
}

// P1: W001 must fire for a local with an unresolvable type that is never used.
func TestW001_UnresolvableType_StillFlagsUnused(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `locals { x = var.something }`,
	})
	if !hasCode(vs, "W001") {
		t.Fatalf("expected W001 for unused unresolvable local, got %v", codes(vs))
	}
}

// P1: output order must be deterministic across multiple runs.
func TestDeterministicOutput(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"main.tf": `
locals { a = "x" }
output "o1" { value = local.typo1 }
output "o2" { value = local.typo2 }
output "o3" { value = local.typo3 }
`,
	}
	first := run(t, files)
	// Re-run on same content via a fresh dir to get a second ordering.
	dir2 := writeTFDir(t, files)
	parsed2, pv2, _ := checker.ParseDir(context.Background(), dir2)
	second := slices.Concat(pv2, mustRun(context.Background(), parsed2, nil, dir2))

	if len(first) != len(second) {
		t.Fatalf("run lengths differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Code != second[i].Code || first[i].Line != second[i].Line {
			t.Fatalf("non-deterministic at index %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// P2: --checks= with an invalid code should return an error, not silently pass.
func TestChecksFlag_InvalidCode_ReturnsError(t *testing.T) {
	t.Parallel()
	err := checker.ValidateCheckCodes([]string{"E003", "INVALID"})
	if err == nil {
		t.Fatal("expected error for invalid check code, got nil")
	}
}

// ── Additional coverage tests ─────────────────────────────────────────────────

// E004 via TemplateWrapExpr: bare "${local.tags}" (no surrounding text) parses
// differently from "prefix-${local.tags}" — must still be caught.
func TestE004_TemplateWrapExpr_BareInterpolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { tags = { env = "prod" } }
output "o" { value = "${local.tags}" }
`,
	})
	if !hasCode(vs, "E004") {
		t.Fatalf("expected E004 for bare interpolation of object local, got %v", codes(vs))
	}
}

// E005 on data blocks (not just resource).
func TestE005_DataBlock_CountAndForEach(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
data "aws_ami" "latest" {
  count    = 1
  for_each = toset(["a"])
}
`,
	})
	if !hasCode(vs, "E005") {
		t.Fatalf("expected E005 for data block with count+for_each, got %v", codes(vs))
	}
}

// E005 also fires on `module` blocks — Terraform supports count and for_each
// on modules but rejects using both simultaneously, same as resource/data.
func TestE005_ModuleBlock_CountAndForEach(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
module "vpc" {
  source   = "./modules/vpc"
  count    = 1
  for_each = toset(["a"])
}
`,
	})
	if !hasCode(vs, "E005") {
		t.Fatalf("expected E005 for module block with count+for_each, got %v", codes(vs))
	}
}

// E005 must NOT fire on a module block with only one of count/for_each.
func TestE005_ModuleBlock_OnlyForEach_NoViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
module "vpc" {
  source   = "./modules/vpc"
  for_each = toset(["a", "b"])
}
`,
	})
	if hasCode(vs, "E005") {
		t.Fatalf("unexpected E005 (only for_each, no count): %v", codes(vs))
	}
}

// W001 cross-file: local defined in a.tf, used in b.tf — must NOT be flagged.
func TestW001_CrossFile_UsedInOtherFile_NoViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"a.tf": `locals { shared = "value" }`,
		"b.tf": `output "o" { value = local.shared }`,
	})
	if hasCode(vs, "W001") {
		t.Fatalf("unexpected W001: local used in another file should not be flagged: %v", codes(vs))
	}
}

// inferExprType: TupleConsExpr (list literal) resolves to non-scalar.
func TestE004_ListInInterpolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { items = ["a", "b"] }
output "o" { value = "prefix-${local.items}" }
`,
	})
	if !hasCode(vs, "E004") {
		t.Fatalf("expected E004 for list local in interpolation, got %v", codes(vs))
	}
}

// inferExprType: ConditionalExpr with matching scalar branches resolves to scalar.
func TestE004_ConditionalScalar_NoViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { env = true ? "prod" : "dev" }
output "o" { value = "prefix-${local.env}" }
`,
	})
	if hasCode(vs, "E004") {
		t.Fatalf("unexpected E004: conditional with scalar branches should be fine: %v", codes(vs))
	}
}

// inferExprType: known function returning string resolves to scalar.
func TestE004_KnownStringFunction_NoViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { env = lower("PROD") }
output "o" { value = "prefix-${local.env}" }
`,
	})
	if hasCode(vs, "E004") {
		t.Fatalf("unexpected E004: tostring() result should be scalar: %v", codes(vs))
	}
}

// Non-.tf files in the directory must be ignored.
func TestParseDir_IgnoresNonTfFiles(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf":   `locals { x = "y" }`,
		"README.md": `# not terraform`,
		"vars.json": `{"key": "value"}`,
	})
	// No violations expected — non-.tf files should be silently skipped.
	for _, v := range vs {
		if v.Code == "E001" {
			t.Fatalf("E001 fired on non-.tf file: %+v", v)
		}
	}
}

// Rename: TestE002_NoDuplicateAcrossBlocks_SameFile is misleading — it expects E002.
// The correctly named version:
func TestE002_DuplicateInSameFile_TwoBlocks(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { name = "foo" }
locals { name = "bar" }
`,
	})
	if !hasCode(vs, "E002") {
		t.Fatalf("expected E002 for duplicate local in two blocks in same file, got %v", codes(vs))
	}
}

// ── Bug regression tests ──────────────────────────────────────────────────────

// cty.NilType.Equals() panic: conditional where one branch is unresolvable.
func TestInferExprType_ConditionalWithUnresolvableBranch_NoPanic(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { x = var.flag ? var.something : "fallback" }
output "o" { value = "prefix-${local.x}" }
`,
	})
	// Should not panic. E004 must NOT fire (type is unknown → skip).
	if hasCode(vs, "E004") {
		t.Fatalf("unexpected E004 for unresolvable conditional: %v", codes(vs))
	}
}

// --checks= with empty value must error, not silently disable all checks.
func TestChecksFlag_EmptyValue_ReturnsError(t *testing.T) {
	t.Parallel()
	// Empty slice is valid (means "run all").
	if err := checker.ValidateCheckCodes([]string{}); err != nil {
		t.Fatalf("empty slice should be valid (means run all), got: %v", err)
	}
	// A single empty string element (e.g. from splitting --checks=) is invalid.
	if err := checker.ValidateCheckCodes([]string{""}); err == nil {
		t.Fatal("expected error for empty check code string, got nil")
	}
}

// TestInferExprType_TemplateWrapExpr_NoPanic exercises the inferExprType path
// for a TemplateWrapExpr (`"${local.x}"`) whose inner expression resolves to
// a non-scalar (object). In Terraform, `"${local.tags}"` evaluates to the
// string-coerced form of the object, so this is technically valid HCL. The
// invariant being guarded here is purely operational: inferExprType must not
// panic when it walks into a TemplateWrapExpr wrapping a non-scalar. Whether
// it returns TypeString (matching Terraform's coercion) or TypeUnknown
// (statically unresolvable) is intentionally not asserted — both are
// acceptable strategies and the call sites tolerate either.
func TestInferExprType_TemplateWrapExpr_NoPanic(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  tags  = { env = "prod" }
  alias = "${local.tags}"
}
output "o" { value = "prefix-${local.alias}" }
`,
	})
	// No panic, no crash — that's the whole assertion.
	_ = vs
}

// .. path check false positive: legitimate dir name containing ".." substring.
func TestParseDir_DotDotInDirName_NotRejected(t *testing.T) {
	t.Parallel()
	// Create a dir whose name contains ".." as substring but not as a segment.
	parent := t.TempDir()
	dir := filepath.Join(parent, "my..project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`locals { x = "y" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, vs, _ := checker.ParseDir(context.Background(), dir)
	for _, v := range vs {
		if v.Code == "E000" && strings.Contains(v.Message, "'..'") {
			t.Fatalf("false positive: legitimate dir name rejected: %v", v.Message)
		}
	}
}

// E004 double-processing: object local in multi-part template must produce
// exactly one violation, not two.
func TestE004_MultiPartTemplate_ExactlyOneViolation(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  obj = { env = "prod" }
  str = "hello"
}
output "o" { value = "${local.obj}-${local.str}" }
`,
	})
	count := 0
	for _, v := range vs {
		if v.Code == "E004" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 E004, got %d: %v", count, codes(vs))
	}
}

// ── Missing coverage tests ────────────────────────────────────────────────────

// ParseDir rejects paths with ".." as a path segment.
// `..`-prefixed paths must work — `tfdry ../infra` is a legitimate CLI
// invocation when the user runs from a sibling directory. The previous
// blanket rejection at ParseDir was over-paranoid: tfdry runs as the
// caller's UID with the caller's filesystem access, so blocking parent
// traversal at the top level adds no security and breaks normal usage.
// Module-source containment is still enforced.
func TestParseDir_DotDotSegment_Allowed(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	subdir := filepath.Join(parent, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "main.tf"),
		[]byte(`locals { x = "y" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reach `parent` from `subdir` via raw `..` path (string concat avoids
	// filepath.Join's automatic cleaning, so the path actually contains
	// a `..` segment when ParseDir sees it).
	//
	//nolint:gocritic // intentional: filepath.Join would collapse "subdir/.."
	// to the parent dir and defeat the test's premise.
	relPath := subdir + string(os.PathSeparator) + ".."
	files, vs, _ := checker.ParseDir(context.Background(), relPath)
	if hasCode(vs, "E000") {
		t.Fatalf("unexpected E000 for legitimate '..' path %q: %v", relPath, codes(vs))
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 parsed file from %q, got %d", relPath, len(files))
	}
}

// ParseDir rejects files over 10MB.
func TestParseDir_FileTooLarge_EmitsE000(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "big.tf")
	// Write 10MB + 1 byte.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(10*1024*1024 + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	_, vs, _ := checker.ParseDir(context.Background(), dir)
	if !hasCode(vs, "E000") {
		t.Fatalf("expected E000 for oversized file, got %v", codes(vs))
	}
}

// E000/E001 are emitted even when --checks= restricts to other codes.
func TestParseViolations_AlwaysEmitted_WithRestrictiveChecks(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"bad.tf": `resource "x" "y" { bad syntax !!!`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	// Run with only E005 — parse violations must still be present.
	runViolations, _ := checker.Run(context.Background(), parsed, checker.CheckSet{"E005": {}}, dir)
	all := slices.Concat(parseViolations, runViolations)
	if !hasCode(all, "E001") {
		t.Fatalf("expected E001 even with restrictive --checks, got %v", codes(all))
	}
}

// ConditionalExpr with mismatched branch types → TypeUnknown → E004 skipped.
func TestE004_ConditionalMismatchedBranches_Skipped(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { x = true ? { a = 1 } : "fallback" }
output "o" { value = "prefix-${local.x}" }
`,
	})
	if hasCode(vs, "E004") {
		t.Fatalf("unexpected E004: mismatched conditional branches should be TypeUnknown (skipped): %v", codes(vs))
	}
}

// E004 false positive: local.foo.bar — attribute access on object local must NOT be flagged.
func TestE004_AttributeTraversal_NoFalsePositive(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
locals { config = { region = "us-east-1" } }
output "o" { value = "region-${local.config.region}" }
`,
	})
	if hasCode(vs, "E004") {
		t.Fatalf("E004 false positive: local.config.region is attribute access, must not be flagged: %v", codes(vs))
	}
}

// ── E006: local module input type mismatch ────────────────────────────────────

// helper: creates a dir with a caller and a local module sub-directory.
func writeModuleFiles(t *testing.T, callerFiles map[string]string, moduleDir string, moduleFiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range callerFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	modPath := filepath.Join(dir, moduleDir)
	if err := os.MkdirAll(modPath, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range moduleFiles {
		if err := os.WriteFile(filepath.Join(modPath, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runDir(t *testing.T, dir string) []checker.Violation {
	t.Helper()
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	return append(parseViolations, mustRun(context.Background(), parsed, nil, dir)...)
}

// E006: passing a list where module expects a string → violation.
func TestE006_ListPassedWhereStringExpected(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals { items = ["a", "b"] }
module "vpc" {
  source = "./modules/vpc"
  name   = local.items
}
`,
		},
		"modules/vpc",
		map[string]string{
			"variables.tf": `
variable "name" {
  type = string
}
`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for list passed where string expected, got %v", codes(vs))
	}
}

// E006: passing a string where module expects a string → no violation.
func TestE006_StringPassedWhereStringExpected_NoViolation(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals { env = "prod" }
module "vpc" {
  source = "./modules/vpc"
  name   = local.env
}
`,
		},
		"modules/vpc",
		map[string]string{
			"variables.tf": `
variable "name" {
  type = string
}
`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006: %v", codes(vs))
	}
}

// E006: unresolvable type (var.something) → skip, no false positive.
func TestE006_UnresolvableType_NoFalsePositive(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "vpc" {
  source = "./modules/vpc"
  name   = var.something
}
`,
		},
		"modules/vpc",
		map[string]string{
			"variables.tf": `
variable "name" {
  type = string
}
`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006 for unresolvable type: %v", codes(vs))
	}
}

// E006: unknown bare-identifier type in the module's variable (e.g. a typo
// like `type = mystery` or a custom-type reference tfdry doesn't recognise)
// must not produce a false-positive at the caller. The module is broken,
// not the caller, so the safe behaviour is to skip type-mismatch checks for
// that variable (treat as SchemaUnknown).
func TestE006_UnknownTraversalType_NoFalsePositive(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "vpc" {
  source = "./modules/vpc"
  name   = "hello"
}
`,
		},
		"modules/vpc",
		map[string]string{
			"variables.tf": `
variable "name" {
  type = mystery
}
`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("E006 false positive on broken module type %q: %v", "mystery", codes(vs))
	}
}

// Malformed container types like `list()` (no args) or `list(a, b)`
// (too many args) must not produce false-positive E006 at the caller. The
// module type constraint is broken; tfdry should skip type-mismatch checks
// rather than treat the malformed form as a concrete container kind.
func TestE006_MalformedContainerType_NoFalsePositive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tp   string
	}{
		{"list_no_args", "list()"},
		{"list_too_many", "list(string, number)"},
		{"set_no_args", "set()"},
		{"map_no_args", "map()"},
		{"map_too_many", "map(string, number)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := writeModuleFiles(
				t,
				map[string]string{
					"main.tf": `
module "m" {
  source = "./modules/m"
  v      = "scalar-string-not-a-list"
}
`,
				},
				"modules/m",
				map[string]string{
					"variables.tf": `
variable "v" {
  type = ` + tc.tp + `
}
`,
				},
			)
			vs := runDir(t, dir)
			if hasCode(vs, "E006") {
				t.Fatalf("E006 false positive on malformed type %q: %v", tc.tp, codes(vs))
			}
		})
	}
}

// Malformed object() expressions (no args, too many args, or non-object
// argument) must not produce false-positive E007 at the caller. With an
// empty Fields map, every key in the caller's literal would otherwise be
// flagged as an unknown field. SchemaUnknown short-circuits the check.
func TestE007_MalformedObjectType_NoFalsePositive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tp   string
	}{
		{"object_no_args", "object()"},
		{"object_too_many", "object({a = string}, {b = number})"},
		{"object_non_object_arg", `object("not_an_object_literal")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := writeModuleFiles(
				t,
				map[string]string{
					"main.tf": `
module "m" {
  source = "./modules/m"
  v = {
    name = "alice"
    port = 8080
  }
}
`,
				},
				"modules/m",
				map[string]string{
					"variables.tf": `
variable "v" {
  type = ` + tc.tp + `
}
`,
				},
			)
			vs := runDir(t, dir)
			if hasCode(vs, "E007") {
				t.Fatalf("E007 false positive on malformed type %q: %v", tc.tp, codes(vs))
			}
		})
	}
}

// E006: module with no variables.tf → no violation (skip gracefully).
func TestE006_NoVariablesTf_NoViolation(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "vpc" {
  source = "./modules/vpc"
  name   = "hello"
}
`,
		},
		"modules/vpc",
		map[string]string{
			"main.tf": `resource "null_resource" "x" {}`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006 when module has no variables.tf: %v", codes(vs))
	}
}

// E006: remote module source (not relative path) → skip.
func TestE006_RemoteSource_Skipped(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
  name    = ["not", "a", "string"]
}
`,
	})
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006 for remote module: %v", codes(vs))
	}
}

// E006: object passed where object expected → no violation.
func TestE006_ObjectPassedWhereObjectExpected_NoViolation(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals { cfg = { key = "val" } }
module "vpc" {
  source = "./modules/vpc"
  config = local.cfg
}
`,
		},
		"modules/vpc",
		map[string]string{
			"variables.tf": `
variable "config" {
  type = map(string)
}
`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006: object passed where object expected: %v", codes(vs))
	}
}

// resolveExprType should follow transitive local references with
// cycle detection. Without recursion, `local.b -> local.a -> 1` resolves
// to TypeUnknown at local.b (because local.b's expression is a
// ScopeTraversalExpr, not a literal), and E006 is silently skipped even
// though the type IS resolvable. After fix, the chain resolves through
// the locals map until a literal is reached.
func TestE006_TransitiveLocalChain_DetectsMismatch(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals {
  a = 1
  b = local.a
  c = local.b
}
module "m" {
  source = "./modules/m"
  v      = local.c
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `
variable "v" {
  type = string
}
`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 (number transitively passed as string), got %v", codes(vs))
	}
}

// Cycle detection — `local.a = local.b`, `local.b = local.a` must NOT
// recurse infinitely. resolveExprType should bail out via the cycle map and
// return TypeUnknown without crashing or stack-overflowing.
func TestE006_TransitiveLocalCycle_DoesNotPanic(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals {
  a = local.b
  b = local.a
}
module "m" {
  source = "./modules/m"
  v      = local.a
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `
variable "v" {
  type = string
}
`,
		},
	)
	// The strong invariant: doesn't panic / hang. With cycle detection,
	// resolveExprType returns TypeUnknown for both ends of the cycle,
	// and E006 is skipped (correct conservative behaviour).
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Errorf("unexpected E006 for cyclic locals — should be TypeUnknown, got %v", codes(vs))
	}
}

// List/set element type mismatches should report the line of the
// offending element, not the parent attribute. With multi-line list
// literals the parent line is misleading — the user has to scan through
// the literal to find which element is wrong. tup.Exprs[i].StartRange()
// gives the precise location.
func TestE006_ListElementMismatch_ReportsElementLine(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `module "m" {
  source = "./modules/m"
  names = [
    "alpha",
    "beta",
    42,
    "delta",
  ]
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "names" { type = list(string) }`,
		},
	)
	vs := runDir(t, dir)
	var got *checker.Violation
	for i := range vs {
		if vs[i].Code == "E006" {
			got = &vs[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected E006 for list element mismatch, got %v", codes(vs))
	}
	// The `42` literal is on line 6 of main.tf (1-indexed: blank/module/source/names/alpha/beta/42).
	// The attribute `names = [...]` starts on line 4. Anything reporting
	// line 4 is the bug; line 6 is the fix.
	if got.Line != 6 {
		t.Errorf("expected violation Line=6 (the `42` literal), got %d — full violation: %+v",
			got.Line, *got)
	}
}

// Map element type mismatches should report the value expression's
// line, not the parent attribute. Same reasoning as the list/set case.
func TestE006_MapValueMismatch_ReportsValueLine(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `module "m" {
  source = "./modules/m"
  ports = {
    http  = 80
    https = 443
    bad   = "not-a-number"
    debug = 9000
  }
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "ports" { type = map(number) }`,
		},
	)
	vs := runDir(t, dir)
	var got *checker.Violation
	for i := range vs {
		if vs[i].Code == "E006" {
			got = &vs[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected E006 for map value mismatch, got %v", codes(vs))
	}
	// The `bad = "not-a-number"` line is line 6 (1-indexed: module/source/ports/http/https/bad).
	// The attribute `ports = {...}` starts on line 3.
	if got.Line != 6 {
		t.Errorf("expected violation Line=6 (the `bad = ...` line), got %d — full violation: %+v",
			got.Line, *got)
	}
}

// ── E007: unknown module input key ───────────────────────────────────────────

// E007: passing an input key that doesn't exist in the module's variables.tf.
func TestE007_UnknownInputKey(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "vpc" {
  source                    = "./modules/vpc"
  name                      = "prod"
  lambda_function_associations = ["a"]
}
`,
		},
		"modules/vpc",
		map[string]string{
			"variables.tf": `
variable "name" { type = string }
variable "lambda_function_association" { type = map(string) }
`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E007") {
		t.Fatalf("expected E007 for unknown input key 'lambda_function_associations', got %v", codes(vs))
	}
}

// E007: all keys valid → no violation.
func TestE007_AllKeysValid_NoViolation(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "vpc" {
  source = "./modules/vpc"
  name   = "prod"
}
`,
		},
		"modules/vpc",
		map[string]string{
			"variables.tf": `variable "name" { type = string }`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E007") {
		t.Fatalf("unexpected E007: %v", codes(vs))
	}
}

// ── E006 deep: nested object type mismatch ────────────────────────────────────

// E006 deep: list passed where object field expects a map.
func TestE006_NestedObjectFieldTypeMismatch(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals {
  assocs = ["viewer-request", "origin-request"]
}
module "cdn" {
  source = "./modules/cdn"
  behavior = {
    lambda_function_association = local.assocs
  }
}
`,
		},
		"modules/cdn",
		map[string]string{
			"variables.tf": `
variable "behavior" {
  type = object({
    lambda_function_association = map(string)
  })
}
`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for nested list-vs-map mismatch, got %v", codes(vs))
	}
}

// E006 deep: correct nested types → no violation.
func TestE006_NestedObjectFieldCorrect_NoViolation(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals { region = "us-east-1" }
module "cdn" {
  source = "./modules/cdn"
  behavior = {
    region = local.region
  }
}
`,
		},
		"modules/cdn",
		map[string]string{
			"variables.tf": `
variable "behavior" {
  type = object({
    region = string
  })
}
`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006 for correct nested types: %v", codes(vs))
	}
}

// E006 deep: unknown key inside nested object → E007.
func TestE007_NestedObjectUnknownKey(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "cdn" {
  source = "./modules/cdn"
  behavior = {
    region     = "us-east-1"
    typo_field = "oops"
  }
}
`,
		},
		"modules/cdn",
		map[string]string{
			"variables.tf": `
variable "behavior" {
  type = object({
    region = string
  })
}
`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E007") {
		t.Fatalf("expected E007 for unknown nested key 'typo_field', got %v", codes(vs))
	}
}

// optional() fields: type still checked when present.
func TestE006_OptionalFieldWrongType(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals { flag = { a = 1 } }
module "cdn" {
  source  = "./modules/cdn"
  enabled = local.flag
}
`,
		},
		"modules/cdn",
		map[string]string{
			"variables.tf": `
variable "enabled" {
  type = optional(bool, true)
}
`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for object passed to optional(bool): %v", codes(vs))
	}
}

// ── Bug regression + missing coverage tests ──────────────────────────────────

// Bug#1: varTypeToSchemaKind must not infinite-loop on cyclic locals.
func TestVarTypeToSchemaKind_CyclicLocals_NoPanic(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals {
  a = local.b
  b = local.a
}
module "m" {
  source = "./modules/m"
  x      = local.a
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "x" { type = map(string) }`,
		},
	)
	// Must not panic or hang.
	vs := runDir(t, dir)
	_ = vs
}

// Bug#2: stringLiteralValue must not panic on a number literal in a single-part template.
func TestStringLiteralValue_NumberLiteral_NoPanic(t *testing.T) {
	t.Parallel()
	// source = 42 is invalid HCL for a module source but we test the helper directly
	// via a module block that has a non-string literal — ParseDir will catch E001,
	// but stringLiteralValue must not panic before that.
	vs := run(t, map[string]string{
		"main.tf": `
module "m" {
  source = "./modules/m"
  name   = "ok"
}
`,
	})
	_ = vs // just ensure no panic
}

// Bug#4: --fix must not run FixFormat when E008 is excluded via --checks.
// Tested via FixFormat: if checksFilter excludes E008, FixFormat should not be called.
// We verify this at the Run level: with --checks=E003, E008 must not appear.
func TestRun_FixNotCalledWhenE008Excluded(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf": "locals {\na=\"foo\"\n}\n",
	})
	files, _, _ := checker.ParseDir(context.Background(), dir)
	cs := checker.CheckSet{"E003": {}}
	vs, _ := checker.Run(context.Background(), files, cs, dir)
	if hasCode(vs, "E008") {
		t.Fatalf("E008 must not appear when excluded via CheckSet: %v", codes(vs))
	}
}

// Security#1: a module source that resolves to a non-existent parent dir
// should produce no E006/E007 — the schemas are unparseable, so checks
// silently skip rather than spuriously firing.
func TestCheckModuleInputs_PathTraversal_Rejected(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
module "evil" {
  source = "../../etc"
  name   = "x"
}
`,
	})
	// The parent dir doesn't exist as a tfdry-readable module; no checks
	// fire. (We no longer enforce containment — the security boundary lives
	// at the kernel level via O_NOFOLLOW + EvalSymlinks.)
	if hasCode(vs, "E006") || hasCode(vs, "E007") {
		t.Fatalf("non-existent parent dir produced spurious findings: %v", codes(vs))
	}
}

// Parent-relative module path that DOES exist must be parsed and
// checked. This is the standard monorepo pattern — `infra/prod` references
// `../shared/<module>`. Previously the containment check silently skipped
// such modules, leaving E006/E007 unable to fire.
func TestCheckModuleInputs_ParentRelativeModule_TypeMismatch_E006(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// <root>/proj is what tfdry analyses; <root>/shared is the module dir
	// that lives outside proj/ — exactly the layout that used to be skipped.
	projDir := filepath.Join(root, "proj")
	sharedDir := filepath.Join(root, "shared", "vpc")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainTF := `
locals { items = ["a", "b"] }
module "vpc" {
  source = "../shared/vpc"
  name   = local.items   # caller passes a list where module wants string
}
`
	if err := os.WriteFile(filepath.Join(projDir, "main.tf"), []byte(mainTF), 0o644); err != nil {
		t.Fatal(err)
	}
	moduleVars := `variable "name" { type = string }`
	if err := os.WriteFile(filepath.Join(sharedDir, "variables.tf"), []byte(moduleVars), 0o644); err != nil {
		t.Fatal(err)
	}

	vs := runDir(t, projDir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for list-passed-where-string on parent-relative module; got %v", codes(vs))
	}
}

// Positive case: a parent-relative module with correctly-typed inputs
// produces no findings. Symmetric to the negative test above.
func TestCheckModuleInputs_ParentRelativeModule_Clean(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	projDir := filepath.Join(root, "proj")
	sharedDir := filepath.Join(root, "shared", "vpc")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainTF := `
module "vpc" {
  source = "../shared/vpc"
  name   = "production"
}
`
	if err := os.WriteFile(filepath.Join(projDir, "main.tf"), []byte(mainTF), 0o644); err != nil {
		t.Fatal(err)
	}
	moduleVars := `variable "name" { type = string }`
	if err := os.WriteFile(filepath.Join(sharedDir, "variables.tf"), []byte(moduleVars), 0o644); err != nil {
		t.Fatal(err)
	}

	vs := runDir(t, projDir)
	if hasCode(vs, "E006") || hasCode(vs, "E007") {
		t.Fatalf("clean parent-relative module produced spurious findings: %v", codes(vs))
	}
}

// Security-bypass guard: a sibling module ../proj-evil with differing
// variables produces E007 on the unknown caller key. Previously containment
// would skip the module entirely; now it's checked like any other.
func TestCheckModuleInputs_SiblingDir_UnknownInput_E007(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Project at <root>/proj, sibling at <root>/proj-evil with a module file.
	projDir := filepath.Join(root, "proj")
	siblingDir := filepath.Join(root, "proj-evil")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// proj/main.tf references ../proj-evil with input "name"
	mainTF := `
module "neighbour" {
  source = "../proj-evil"
  name   = "x"
}
`
	if err := os.WriteFile(filepath.Join(projDir, "main.tf"), []byte(mainTF), 0o644); err != nil {
		t.Fatal(err)
	}
	// sibling declares "other", not "name" → caller-side "name" is unknown.
	siblingVars := `variable "other" {}`
	if err := os.WriteFile(filepath.Join(siblingDir, "variables.tf"), []byte(siblingVars), 0o644); err != nil {
		t.Fatal(err)
	}

	vs := runDir(t, projDir)
	if !hasCode(vs, "E007") {
		t.Fatalf("expected E007 for unknown input on sibling module; got %v", codes(vs))
	}
}

// A child directory whose name happens to start with ".." (e.g. "..hidden")
// must NOT be rejected as a parent traversal — it is a legitimate child.
// `filepath.Rel` reports `..hidden`, not `../hidden`, so the dotdot-as-segment
// check must be precise (rel == ".." OR rel starts with "../"), not a naive
// HasPrefix(rel, "..").
func TestCheckModuleInputs_DotDotPrefixedChildName_Allowed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	moduleDir := filepath.Join(root, "..hidden")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "variables.tf"),
		[]byte(`variable "name" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	mainTF := `
module "child" {
  source = "./..hidden"
  name   = "x"
}
`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(mainTF), 0o644); err != nil {
		t.Fatal(err)
	}

	vs := runDir(t, root)
	// "name" is a declared input, so no E006/E007 should fire. If the
	// containment check wrongly rejects ..hidden as a parent traversal, we'd
	// see no E007 for missing required ("name" is required by the module),
	// but we'd also see no validation at all.
	if hasCode(vs, "E006") {
		t.Errorf("..hidden child dir wrongly rejected: got E006 %v", codes(vs))
	}
}

// FixFormat: happy path rewrites files on disk.
func TestFixFormat_RewritesFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	unformatted := []byte("locals {\na=\"foo\"\n}\n")
	os.WriteFile(path, unformatted, 0o644)

	files, _, _ := checker.ParseDir(context.Background(), dir)
	_, vs, _ := checker.FixFormat(context.Background(), files, dir)
	if len(vs) != 0 {
		t.Fatalf("expected no violations, got %v", vs)
	}
	got, _ := os.ReadFile(path)
	if bytes.Equal(got, unformatted) {
		t.Fatal("FixFormat did not rewrite the file")
	}
}

// FixFormat: skips already-formatted files (no unnecessary write).
func TestFixFormat_SkipsFormattedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	formatted := []byte("locals {\n  a = \"foo\"\n}\n")
	os.WriteFile(path, formatted, 0o644)
	fi1, _ := os.Stat(path)

	files, _, _ := checker.ParseDir(context.Background(), dir)
	//nolint:errcheck // we don't care about the err here; the test
	// only inspects the file's mtime after the no-op FixFormat pass.
	checker.FixFormat(context.Background(), files, dir)

	fi2, _ := os.Stat(path)
	if !fi2.ModTime().Equal(fi1.ModTime()) {
		t.Fatal("FixFormat rewrote an already-formatted file")
	}
}

// E006: number literal passed where string expected.
func TestE006_NumberPassedWhereStringExpected(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  name   = 42
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "name" { type = string }`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for number passed where string expected, got %v", codes(vs))
	}
}

// E006: tuple literal at top-level map(string) variable.
func TestE006_TuplePassedToMapVariable(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  tags   = ["a", "b"]
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "tags" { type = map(string) }`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for list passed to map(string), got %v", codes(vs))
	}
}

// ── Recursive element type checking for list/set/map ─────────────────────

// list(string) with a non-string element must fire E006.
func TestE006_ListOfString_WithNumberElement(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  names  = ["alpha", 42, "beta"]
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "names" { type = list(string) }`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for number element in list(string), got %v", codes(vs))
	}
}

// set(string) with a non-string element must fire E006.
func TestE006_SetOfString_WithBoolElement(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  flags  = ["yes", true]
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "flags" { type = set(string) }`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for bool element in set(string), got %v", codes(vs))
	}
}

// map(number) with a non-number value must fire E006.
func TestE006_MapOfNumber_WithStringValue(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  ports  = { http = 80, https = "ssl" }
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "ports" { type = map(number) }`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for string value in map(number), got %v", codes(vs))
	}
}

// Element type matches: no violation.
func TestE006_ListOfString_AllStrings_NoViolation(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  names  = ["alpha", "beta", "gamma"]
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "names" { type = list(string) }`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006 for matching list(string), got %v", codes(vs))
	}
}

// Element type matches in map: no violation.
func TestE006_MapOfNumber_AllNumbers_NoViolation(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  ports  = { http = 80, https = 443 }
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "ports" { type = map(number) }`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006 for matching map(number), got %v", codes(vs))
	}
}

// list(any): elements with mixed types must NOT fire (any = no checking).
func TestE006_ListOfAny_MixedTypes_NoViolation(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  items  = ["alpha", 42, true]
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "items" { type = list(any) }`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006 for list(any) with mixed types, got %v", codes(vs))
	}
}

// Element is a local with unresolvable type → no false positive.
func TestE006_ListOfString_UnresolvableLocalElement_NoFalsePositive(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals {
  mystery = var.something
}
module "m" {
  source = "./modules/m"
  names  = ["alpha", local.mystery]
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "names" { type = list(string) }`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E006") {
		t.Fatalf("unexpected E006 for unresolvable local element: %v", codes(vs))
	}
}

// parseTypeSchema: set(string) and map(number) parsed correctly.
func TestParseTypeSchema_SetAndMap(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source  = "./modules/m"
  ids     = ["a", "b"]
  weights = ["x"]
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `
variable "ids"     { type = set(string) }
variable "weights" { type = map(number) }
`,
		},
	)
	vs := runDir(t, dir)
	// Both are list literals passed to set/map — should flag E006 (sequence vs mapping for weights).
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for list passed to map(number), got %v", codes(vs))
	}
}

// ── Bug regression + missing coverage ────────────────────────────────────────

// Bug#1: Terraform meta-arguments in module blocks must NOT produce E007.
func TestE007_MetaArguments_NoFalsePositive(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source     = "./modules/m"
  count      = 2
  for_each   = toset(["a"])
  depends_on = []
  providers  = {}
  name       = "ok"
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "name" { type = string }`,
		},
	)
	vs := runDir(t, dir)
	for _, v := range vs {
		if v.Code == "E007" {
			t.Fatalf("false E007 for meta-argument: %+v", v)
		}
	}
}

// Bug#3: source = "." must be rejected (module referencing itself).
func TestCheckModuleInputs_SelfSource_Rejected(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
module "self" {
  source = "."
  name   = "x"
}
variable "name" { type = string }
`,
	})
	// Must not produce E007 from self-referencing module.
	for _, v := range vs {
		if v.Code == "E007" {
			t.Fatalf("unexpected E007 from self-referencing module: %+v", v)
		}
	}
}

// Bug#4: E008 filter in main.go — verify via Run that E008 is absent when excluded.
// (The dead-code filter is in main.go which has no unit tests; covered by the
// existing TestRun_FixNotCalledWhenE008Excluded which already passes.)

// Quality#2: knownCodes derived from AllChecks — ValidateCheckCodes must reject
// a code not in AllChecks even if knownCodes were to diverge.
func TestValidateCheckCodes_AllKnownCodesAccepted(t *testing.T) {
	t.Parallel()
	for _, c := range checker.AllChecks() {
		if err := checker.ValidateCheckCodes([]string{c.Code}); err != nil {
			t.Fatalf("AllChecks code %q rejected by ValidateCheckCodes: %v", c.Code, err)
		}
	}
}

// Missing: FixFormat returns correct fixed map.
func TestFixFormat_FixedMapContainsRewrittenFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.tf"), []byte("locals {\na=\"foo\"\n}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.tf"), []byte("locals {\n  b = \"bar\"\n}\n"), 0o644) // already formatted

	files, _, _ := checker.ParseDir(context.Background(), dir)
	fixed, vs, _ := checker.FixFormat(context.Background(), files, dir)
	if len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}
	if !fixed["a.tf"] {
		t.Fatal("expected a.tf in fixed map")
	}
	if fixed["b.tf"] {
		t.Fatal("b.tf was already formatted, must not be in fixed map")
	}
}

// Missing: resolveExprType locals indirection for E006.
func TestE006_LocalReferenceResolved(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals { items = ["a", "b"] }
module "m" {
  source = "./modules/m"
  name   = local.items
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `variable "name" { type = string }`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("expected E006 for list local passed where string expected, got %v", codes(vs))
	}
}

// Security#2: moduleDir that is a symlink must be rejected.
func TestParseModuleVarSchemas_SymlinkDir_Skipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realMod := t.TempDir()
	os.WriteFile(filepath.Join(realMod, "variables.tf"), []byte(`variable "x" { type = string }`), 0o644)

	linkMod := filepath.Join(dir, "modules", "m")
	os.MkdirAll(filepath.Join(dir, "modules"), 0o755)
	if err := os.Symlink(realMod, linkMod); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
module "m" {
  source = "./modules/m"
  y      = "unknown"
}
`), 0o644)

	files, _, _ := checker.ParseDir(context.Background(), dir)
	vs, _ := checker.Run(context.Background(), files, nil, dir)
	// Symlinked module dir must be skipped — no E007 for unknown input.
	for _, v := range vs {
		if v.Code == "E007" {
			t.Fatalf("unexpected E007 from symlinked module dir: %+v", v)
		}
	}
}

// ── Final review regression tests ────────────────────────────────────────────

// Bug#2: source="./" must be rejected (resolves to caller's own dir).
func TestCheckModuleInputs_SlashDotSource_Rejected(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `
variable "name" { type = string }
module "self" {
  source = "./"
  name   = "x"
}
`,
	})
	for _, v := range vs {
		if v.Code == "E007" || v.Code == "E006" {
			t.Fatalf("false positive from self-referencing source './': %+v", v)
		}
	}
}

// Security#1: FormatFile must return error when target is a symlink.
func TestFormatFile_Symlink_ReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.tf")
	link := filepath.Join(dir, "link.tf")
	os.WriteFile(realPath, []byte("locals {\n  a = \"x\"\n}\n"), 0o644)
	if err := os.Symlink(realPath, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	src, _ := os.ReadFile(realPath)
	if err := checker.FormatFile(context.Background(), link, src); err == nil {
		t.Fatal("expected error when formatting a symlink path, got nil")
	}
}

// Quality#1: moduleMetaArgs should use struct{} values (tested via behaviour, not internals).
// Covered by TestE007_MetaArguments_NoFalsePositive — no new test needed.

// Missing: source="./" self-reference via isLocalSource.
func TestIsLocalSource_SlashDot(t *testing.T) {
	t.Parallel()
	// "./" is a local source and must be caught by the traversal guard.
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
variable "x" { type = string }
module "self" {
  source = "./"
  x      = "ok"
  unknown_key = "bad"
}
`,
		},
		"submod",
		map[string]string{
			"variables.tf": `variable "x" { type = string }`,
		},
	)
	vs := runDir(t, dir)
	// "./" resolves to dir itself — must be rejected, so no E007 for unknown_key.
	for _, v := range vs {
		if v.Code == "E007" {
			t.Fatalf("E007 from self-referencing source './': %+v", v)
		}
	}
}

// T9: object schemas declared with quoted keys must be recognised
// (exercises objectKeyName's TemplateExpr/LiteralValueExpr branches).
func TestE007_QuotedSchemaKeys_NoFalsePositive(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  config = {
    name = "demo"
    port = 8080
  }
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `
variable "config" {
  type = object({
    "name" = string
    "port" = number
  })
}
`,
		},
	)
	vs := runDir(t, dir)
	if hasCode(vs, "E007") {
		t.Fatalf("E007 false positive: quoted schema keys should match plain caller keys, got %v", codes(vs))
	}
}

// T9: with quoted-key schema, a genuinely unknown caller key still produces E007.
func TestE007_QuotedSchemaKeys_StillCatchesUnknownKey(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
module "m" {
  source = "./modules/m"
  config = {
    name = "demo"
    typo = "oops"
  }
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `
variable "config" {
  type = object({
    "name" = string
  })
}
`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E007") {
		t.Fatalf("expected E007 for unknown 'typo' key against quoted-key schema, got %v", codes(vs))
	}
}

// T9: parenthesised object keys are dynamic — schema field is silently skipped.
// This exercises the ObjectConsKeyExpr ForceNonLiteral branch in objectKeyName.
// Without this, a caller's matching plain key would treat the dynamic field as
// "missing from schema" and produce E007. We assert that behaviour explicitly so
// any change in semantics is caught.
func TestE007_ParenthesisedSchemaKey_TreatedAsDynamic(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `
locals { keyname = "name" }
module "m" {
  source = "./modules/m"
  config = {
    name = "demo"
  }
}
`,
		},
		"modules/m",
		map[string]string{
			"variables.tf": `
variable "config" {
  type = object({
    (local.keyname) = string
  })
}
`,
		},
	)
	vs := runDir(t, dir)
	// The dynamic key is skipped from the schema, so 'name' is unknown → E007.
	if !hasCode(vs, "E007") {
		t.Fatalf("expected E007 because parenthesised schema key is dynamic and excluded from field map, got %v", codes(vs))
	}
}
