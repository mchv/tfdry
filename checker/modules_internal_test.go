// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// cty.NullVal(cty.String).Type().FriendlyName() returns "string" but
// .AsString() panics on null values. The two string-extraction helpers
// in this package — objectKeyName and stringLiteralValue (TemplateExpr
// and LiteralValueExpr branches) — gate their AsString calls on
// FriendlyName == "string". Without an additional !IsNull() check, a
// null literal would panic instead of yielding an empty value. In
// practice HCL string literals don't normally produce typed nulls during
// parse, but the defensive check costs one extra method call and
// removes a panic surface that downstream evaluation could otherwise
// reach. (Note: inferExprType in locals.go only inspects FriendlyName
// for type detection — it never calls AsString — so it isn't part of
// this panic surface.)
func TestObjectKeyName_NullStringDoesNotPanic(t *testing.T) {
	t.Parallel()
	// LiteralValueExpr carrying a null string. Wrapped in a deferred
	// recover so a regression is caught as a test failure (not a panic
	// that aborts the whole package run).
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("objectKeyName panicked on null string literal: %v", r)
		}
	}()
	expr := &hclsyntax.LiteralValueExpr{Val: cty.NullVal(cty.String)}
	got := objectKeyName(expr)
	if got != "" {
		t.Errorf("objectKeyName(null string) = %q, want empty string", got)
	}
}

func TestStringLiteralValue_NullStringDoesNotPanic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stringLiteralValue panicked on null string literal: %v", r)
		}
	}()
	expr := &hclsyntax.LiteralValueExpr{Val: cty.NullVal(cty.String)}
	got := stringLiteralValue(expr)
	if got != "" {
		t.Errorf("stringLiteralValue(null string) = %q, want empty string", got)
	}
}

// And the same for stringLiteralValue's TemplateExpr branch — a template
// wrapping a single null string literal must also not panic.
func TestStringLiteralValue_NullStringInTemplateDoesNotPanic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stringLiteralValue panicked on null string in template: %v", r)
		}
	}()
	inner := &hclsyntax.LiteralValueExpr{Val: cty.NullVal(cty.String)}
	tmpl := &hclsyntax.TemplateExpr{Parts: []hclsyntax.Expression{inner}}
	got := stringLiteralValue(tmpl)
	if got != "" {
		t.Errorf("stringLiteralValue(template{null string}) = %q, want empty string", got)
	}
}

// parseModuleVarSchemas writes to its `cache` argument
// (`cache[moduleDir] = nil` on early-out paths, `cache[moduleDir] = schemas`
// on success). If the caller passes a nil map — which is the natural way
// to bypass caching from a test or a one-shot caller — those writes panic.
// Lazy-init a local cache when the caller passes nil.
func TestParseModuleVarSchemas_NilCacheDoesNotPanic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("parseModuleVarSchemas panicked with nil cache: %v", r)
		}
	}()
	// Use t.TempDir so the dir exists but contains no .tf files.
	// Both branches (cache hit, then later miss) need to handle nil.
	dir := t.TempDir()
	if got := parseModuleVarSchemas(dir, nil); got == nil {
		// Empty map is fine for an empty dir; nil would mean "couldn't
		// read it", which is wrong for an empty-but-readable dir. The
		// fix should return an empty map.
		t.Errorf("parseModuleVarSchemas(empty dir, nil cache) returned nil, want empty map")
	}
	// Second call also passes nil — must not panic on the cache lookup.
	if got := parseModuleVarSchemas(dir, nil); got == nil {
		t.Errorf("parseModuleVarSchemas (second call, nil cache) returned nil")
	}
}

// ── parseExprForTest helper + bucket-C E006 typing edge-case tests ───────────
//
// The tests above (Null-string + nil-cache) are panic-surface guards.
// The tests below cover the structural fallback paths in modules.go
// that production callers reach when a module declaration is
// malformed, a type-system case is unrecognised, or a local-reference
// chain doesn't resolve. Each test exercises a branch that the
// integration tests in checks_test.go don't reach.
//
// parseExprForTest parses a single HCL expression by wrapping it in a
// `x = <expr>` attribute and pulling the parsed expression back out.
// Returns a parsed hclsyntax.Expression suitable for direct use with
// the module-typing helpers under test.
func parseExprForTest(t *testing.T, src string) hclsyntax.Expression {
	t.Helper()
	f, diags := hclv2ParseConfigForTest([]byte("x = "+src), "test.tf")
	if diags.HasErrors() {
		t.Fatalf("parseExprForTest %q: %v", src, diags)
	}
	body := f.Body.(*hclsyntax.Body)
	return body.Attributes["x"].Expr
}

// hclv2ParseConfigForTest hides the hcl.Pos{Line: 1, Column: 1}
// boilerplate from callers — hcl/v2 is already imported at the top
// of this file (for hcl.File / hcl.Diagnostics types), so the
// wrapper doesn't save an import. The win is purely call-site
// brevity: tests can write `hclv2ParseConfigForTest(src, name)`
// instead of repeating the always-(1,1) start position.
func hclv2ParseConfigForTest(src []byte, filename string) (*hcl.File, hcl.Diagnostics) {
	return hclsyntax.ParseConfig(src, filename, hcl.Pos{Line: 1, Column: 1})
}

// Unrecognised bare identifier in `type = ...` (typo / custom-type
// reference tfdry doesn't model) must fall through to schemaUnknown
// so downstream compareExprToSchema doesn't emit misleading E006
// against a broken module declaration.
func TestParseTypeSchema_UnrecognisedTraversal_ReturnsUnknown(t *testing.T) {
	t.Parallel()
	expr := parseExprForTest(t, "mystery_type")
	if got := parseTypeSchema(expr); got.Kind != schemaUnknown {
		t.Errorf("parseTypeSchema(`mystery_type`) = %v, want schemaUnknown", got.Kind)
	}
}

// schemaKindToVarType — every case + defensive panic on out-of-range.
// All 8 declared schemaKind values are enumerated below (scalars map
// to their VarType equivalents; compound and explicit-unknown map to
// TypeUnknown). Out-of-range values panic to catch forgotten enum
// extensions loudly at test time — belt-and-braces alongside the
// exhaustive linter's compile-time coverage check.
func TestSchemaKindToVarType_ExhaustiveCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   schemaKind
		want VarType
	}{
		{"string", schemaString, TypeString},
		{"number", schemaNumber, TypeNumber},
		{"bool", schemaBool, TypeBool},
		{"list → unknown (no scalar mapping)", schemaList, TypeUnknown},
		{"map → unknown", schemaMap, TypeUnknown},
		{"set → unknown", schemaSet, TypeUnknown},
		{"object → unknown", schemaObject, TypeUnknown},
		{"unknown → unknown", schemaUnknown, TypeUnknown},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := schemaKindToVarType(tc.in); got != tc.want {
				t.Errorf("schemaKindToVarType(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	t.Run("out_of_range_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected schemaKindToVarType(schemaKind(99)) to panic, got nil recover")
			}
		}()
		_ = schemaKindToVarType(schemaKind(99))
	})
}

// schemaKindLabel — every case + defensive panic on out-of-range. The
// existing TestTypeSchema_label in modules_test.go tests the
// typeSchema.label() method; this one tests the underlying
// schemaKindLabel helper which production code calls directly in some
// places.
//
// schemaKindLabel intentionally groups scalar and unknown kinds into
// "unknown" — from its callers' perspective the interesting kinds are
// the compound ones (list/map/set/object). Both the explicit-scalar
// cases and the explicit-unknown case exercise the same "unknown"
// branch; out-of-range values panic to catch forgotten enum
// extensions loudly at test time.
//
// Subtest names are derived from the *input* schemaKind so cases
// that share an output ("unknown") don't collide. With colliding
// names Go appends "#01" to disambiguate, but failure attribution
// becomes ambiguous and `go test -run` can't target individual cases.
func TestSchemaKindLabel_ExhaustiveCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   schemaKind
		want string
	}{
		{"list", schemaList, "list"},
		{"map", schemaMap, "map"},
		{"set", schemaSet, "set"},
		{"object", schemaObject, "object"},
		{"string_maps_to_unknown", schemaString, "unknown"},
		{"number_maps_to_unknown", schemaNumber, "unknown"},
		{"bool_maps_to_unknown", schemaBool, "unknown"},
		{"explicit_unknown", schemaUnknown, "unknown"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := schemaKindLabel(tc.in); got != tc.want {
				t.Errorf("schemaKindLabel(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Out-of-range values are a programmer error (someone added a new
	// schemaKind constant without extending the switch, or constructed
	// an invalid enum value directly). Assert that they panic so the
	// mistake surfaces loudly at test time rather than silently
	// swallowing the value as "unknown".
	t.Run("out_of_range_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected schemaKindLabel(schemaKind(99)) to panic, got nil recover")
			}
		}()
		_ = schemaKindLabel(schemaKind(99))
	})
}

// "${local.x}" parses (in the only-interpolation case) directly to a
// top-level TemplateWrapExpr wrapping a ScopeTraversalExpr. unwrapExpr
// must strip the wrapper so the type-inference helpers see the inner
// expression.
func TestUnwrapExpr_TemplateWrapStrips(t *testing.T) {
	t.Parallel()
	scope := parseExprForTest(t, "local.x")
	if got := unwrapExpr(scope); got != scope {
		t.Errorf("unwrapExpr(ScopeTraversalExpr) = %T, want input passthrough", got)
	}
	wrap := parseExprForTest(t, `"${local.x}"`)
	if _, ok := wrap.(*hclsyntax.TemplateWrapExpr); !ok {
		t.Fatalf("expected TemplateWrapExpr, got %T", wrap)
	}
	if _, ok := unwrapExpr(wrap).(*hclsyntax.ScopeTraversalExpr); !ok {
		t.Errorf("unwrapExpr(TemplateWrapExpr) = %T, want *ScopeTraversalExpr",
			unwrapExpr(wrap))
	}
}

// varTypeToSchemaKind edge branches — non-local refs and undefined
// locals must return schemaUnknown rather than crash on nil. The
// CyclicLocals case is already covered by the existing
// TestVarTypeToSchemaKind_CyclicLocals_NoPanic in checks_test.go.
func TestVarTypeToSchemaKind_NonLocalRef(t *testing.T) {
	t.Parallel()
	expr := parseExprForTest(t, "var.foo")
	if got := varTypeToSchemaKind(expr, nil, nil); got != schemaUnknown {
		t.Errorf("varTypeToSchemaKind(var.foo) = %v, want schemaUnknown", got)
	}
}

func TestVarTypeToSchemaKind_UndefinedLocal(t *testing.T) {
	t.Parallel()
	expr := parseExprForTest(t, "local.missing")
	if got := varTypeToSchemaKind(expr, map[string]localInfo{}, nil); got != schemaUnknown {
		t.Errorf("varTypeToSchemaKind(local.missing) = %v, want schemaUnknown", got)
	}
}

// resolveExprTypeRecursive edge branches — mirrors the
// varTypeToSchemaKind cases above but for the value-type-inference
// helper that locals.go drives. Together with the existing
// inferExprType coverage in checks_test.go these pin the fall-through
// to TypeUnknown for every "we don't know" code path.
func TestResolveExprTypeRecursive_NonTraversal(t *testing.T) {
	t.Parallel()
	// A function call inferExprType doesn't recognise as a known type
	// → resolveExprTypeRecursive must short-circuit to TypeUnknown
	// rather than dereferencing a non-existent traversal.
	expr := parseExprForTest(t, "foo(bar)")
	if got := resolveExprTypeRecursive(expr, nil, nil); got != TypeUnknown {
		t.Errorf("resolveExprTypeRecursive(foo(bar)) = %v, want TypeUnknown", got)
	}
}

func TestResolveExprTypeRecursive_UndefinedLocal(t *testing.T) {
	t.Parallel()
	expr := parseExprForTest(t, "local.missing")
	if got := resolveExprTypeRecursive(expr, map[string]localInfo{}, nil); got != TypeUnknown {
		t.Errorf("resolveExprTypeRecursive(local.missing) = %v, want TypeUnknown", got)
	}
}

func TestResolveExprTypeRecursive_Cycle(t *testing.T) {
	t.Parallel()
	exprA := parseExprForTest(t, "local.b")
	exprB := parseExprForTest(t, "local.a")
	locals := map[string]localInfo{
		"a": {Expr: exprA},
		"b": {Expr: exprB},
	}
	if got := resolveExprTypeRecursive(exprA, locals, nil); got != TypeUnknown {
		t.Errorf("resolveExprTypeRecursive(cycle) = %v, want TypeUnknown", got)
	}
}

// parseModuleVarSchemas cache hit: the first call must populate the
// cache (proving it would return the cached value on subsequent calls
// without redoing I/O). The cache-presence check is positioned
// immediately after the first call so the assertion matches its
// stated intent — checking after the second call would prove only
// that *some* call populated the cache, not that the first did.
//
// The second call asserts cache identity via a mutation marker: we
// insert a sentinel key into `first` BEFORE calling again. If `second`
// is the SAME map instance returned from cache, the marker is visible;
// if `second` is a freshly-built map (i.e. parseModuleVarSchemas
// re-parsed the directory despite the cache entry being present),
// the marker is absent. Length-comparison alone would miss a bug that
// re-parses but still rebuilds an equivalently-sized map.
func TestParseModuleVarSchemas_CacheHit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vars.tf"),
		[]byte(`variable "foo" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := make(map[string]map[string]typeSchema)

	// First call: must read the dir AND populate the cache.
	first := parseModuleVarSchemas(dir, cache)
	if _, ok := first["foo"]; !ok {
		t.Fatalf("first call: missing 'foo' in result %v", first)
	}
	if _, hit := cache[dir]; !hit {
		t.Fatalf("cache entry missing after FIRST call; subsequent calls would redo I/O")
	}

	// Second call: must return the same map instance (proving cache
	// hit, not a re-parse that coincidentally produces an
	// equivalently-shaped map). The mutation marker is a sentinel key
	// the production parser would never emit — its presence in the
	// second result is observable iff first === second.
	const markerKey = "__cache_hit_assertion_marker__"
	first[markerKey] = typeSchema{Kind: schemaBool}
	second := parseModuleVarSchemas(dir, cache)
	if _, ok := second[markerKey]; !ok {
		t.Errorf("second call returned a fresh map (cache miss); want cached map instance")
	}
}

// parseModuleVarSchemas not-a-directory: passing a file path (not a
// directory) must short-circuit via the Lstat-IsDir guard, returning
// nil and caching the negative result so subsequent calls skip the I/O.
func TestParseModuleVarSchemas_NotADir_CachesNil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notADir := filepath.Join(dir, "regular_file")
	if err := os.WriteFile(notADir, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := make(map[string]map[string]typeSchema)
	if got := parseModuleVarSchemas(notADir, cache); got != nil {
		t.Errorf("parseModuleVarSchemas(not-a-dir) = %v, want nil", got)
	}
	cached, ok := cache[notADir]
	if !ok {
		t.Errorf("cache entry missing after Lstat-not-dir; second call would redo I/O")
	}
	if cached != nil {
		t.Errorf("cache entry = %v, want nil (negative caching)", cached)
	}
}

// ── P1.4: defensive continue paths in parseModuleVarSchemas ──────────────────
//
// The integration-level E006/E007 tests exercise parseModuleVarSchemas
// indirectly via Run(). These tests target the individual `continue`
// branches inside the per-entry loop (modules.go:104-148) so a
// regression in one of them surfaces as a sharp, targeted failure
// instead of a confusing E006 false-positive in an integration test.
//
// Untested branches that stay uncovered by design:
//   - Stat error after a successful Open (essentially unreachable on
//     normal filesystems; would need fault injection).
//   - readAll error after a successful Stat (same as above).
//   - Oversize file (> 10 MiB) — already covered via parseDir's
//     TestRun_E000_FileExceedsSize_ExitTwo and writing a 10 MiB
//     fixture per test bloats CI.
//   - body type-assertion failure (hclsyntax.ParseConfig guarantees
//     *hclsyntax.Body for a non-erroring parse; unreachable in practice).

// Tests requiring POSIX-only behaviour (e.g. unreadable file via
// chmod 0o000) live in modules_internal_unix_test.go to keep this
// file cross-platform-clean.

// Malformed HCL must be skipped silently. The parseModuleVarSchemas
// path is used to type-check `module` blocks; a broken neighbour .tf
// shouldn't cascade into spurious E006s on the well-formed files.
func TestParseModuleVarSchemas_ParseError_SkippedSilently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.tf"),
		[]byte(`variable "good" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Intentionally unterminated block.
	if err := os.WriteFile(filepath.Join(dir, "broken.tf"),
		[]byte(`variable "broken" { type = `), 0o644); err != nil {
		t.Fatal(err)
	}
	got := parseModuleVarSchemas(dir, nil)
	if _, ok := got["good"]; !ok {
		t.Errorf("good.tf must still be parsed: got %v", got)
	}
	if _, ok := got["broken"]; ok {
		t.Errorf("broken.tf (parse error) must NOT appear in schemas: got %v", got)
	}
}

// Variable block without a `type` attribute should map to schemaUnknown
// (so compareExprToSchema can't generate E006 against a module variable
// whose declared shape is unknown).
func TestParseModuleVarSchemas_VariableWithoutType_ReturnsUnknown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vars.tf"),
		[]byte(`variable "untyped" { default = "x" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := parseModuleVarSchemas(dir, nil)
	schema, ok := got["untyped"]
	if !ok {
		t.Fatalf("variable 'untyped' missing from schemas: got %v", got)
	}
	if schema.Kind != schemaUnknown {
		t.Errorf("variable without `type` should map to schemaUnknown, got %v", schema.Kind)
	}
}

// Non-`variable` blocks (resource, output, module, ...) must be
// skipped silently — only `variable` blocks contribute to the schema
// map. Bug-shape: a typo in the block-type check could silently
// accept resource blocks and produce noise.
func TestParseModuleVarSchemas_NonVariableBlocks_Skipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := `
variable "real" { type = string }

output "x" { value = "y" }

resource "aws_s3_bucket" "b" {
  bucket = "name"
}

module "m" {
  source = "./m"
}
`
	if err := os.WriteFile(filepath.Join(dir, "mixed.tf"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := parseModuleVarSchemas(dir, nil)
	if len(got) != 1 {
		t.Errorf("expected exactly 1 schema entry (the variable), got %d: %v", len(got), got)
	}
	if _, ok := got["real"]; !ok {
		t.Errorf("variable 'real' missing: got %v", got)
	}
}

// variable block with the wrong number of labels (e.g. zero or two)
// must be skipped silently. Real HCL requires exactly one label for
// a variable block, but a malformed module file shouldn't crash the
// parser.
func TestParseModuleVarSchemas_MalformedVariableLabels_Skipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Zero labels — `variable {}` parses (HCL doesn't reject it at
	// syntactic level; it's a Terraform semantic error). Must skip.
	if err := os.WriteFile(filepath.Join(dir, "noname.tf"),
		[]byte(`variable { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Valid neighbour to confirm the loop continued.
	if err := os.WriteFile(filepath.Join(dir, "valid.tf"),
		[]byte(`variable "v" { type = number }`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := parseModuleVarSchemas(dir, nil)
	if _, ok := got["v"]; !ok {
		t.Errorf("valid.tf's 'v' variable missing: got %v", got)
	}
	if len(got) != 1 {
		t.Errorf("expected exactly 1 schema entry (only the well-formed variable), got %d: %v", len(got), got)
	}
}

// Non-.tf files in the module dir must be skipped silently — the
// loop's filepath.Ext filter is the gate. A file named "README" or
// "vars.tf.json" must not be opened.
func TestParseModuleVarSchemas_NonTFFiles_Skipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vars.tf"),
		[]byte(`variable "v" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	// These must NOT be parsed:
	for _, name := range []string{"README.md", "vars.tf.json", "config.yaml", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, name),
			[]byte(`variable "should_not_appear" { type = bool }`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := parseModuleVarSchemas(dir, nil)
	if _, ok := got["should_not_appear"]; ok {
		t.Errorf("non-.tf files must not contribute to schemas: got %v", got)
	}
	if len(got) != 1 {
		t.Errorf("expected exactly 1 entry (from vars.tf), got %d: %v", len(got), got)
	}
}
