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

// Tiny indirection to keep the top-of-file imports identical to the
// pre-existing helpers — hclv2ParseConfigForTest wraps the
// hcl/v2/hclsyntax.ParseConfig signature so the test file doesn't have
// to import hcl.Pos at the top (still imported via the wrapper).
func hclv2ParseConfigForTest(src []byte, filename string) (*hcl.File, hcl.Diagnostics) {
	return hclsyntax.ParseConfig(src, filename, hcl.Pos{Line: 1, Column: 1})
}

// Unrecognised bare identifier in `type = ...` (typo / custom-type
// reference tfdry doesn't model) must fall through to SchemaUnknown
// so downstream compareExprToSchema doesn't emit misleading E006
// against a broken module declaration.
func TestParseTypeSchema_UnrecognisedTraversal_ReturnsUnknown(t *testing.T) {
	t.Parallel()
	expr := parseExprForTest(t, "mystery_type")
	if got := parseTypeSchema(expr); got.Kind != SchemaUnknown {
		t.Errorf("parseTypeSchema(`mystery_type`) = %v, want SchemaUnknown", got.Kind)
	}
}

// schemaKindToVarType — every case + default branch.
func TestSchemaKindToVarType_ExhaustiveCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   SchemaKind
		want VarType
	}{
		{"string", SchemaString, TypeString},
		{"number", SchemaNumber, TypeNumber},
		{"bool", SchemaBool, TypeBool},
		{"list → unknown (no scalar mapping)", SchemaList, TypeUnknown},
		{"map → unknown", SchemaMap, TypeUnknown},
		{"set → unknown", SchemaSet, TypeUnknown},
		{"object → unknown", SchemaObject, TypeUnknown},
		{"unknown → unknown", SchemaUnknown, TypeUnknown},
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
}

// schemaKindLabel — every case + default. The existing
// TestTypeSchema_label in modules_test.go tests the TypeSchema.label()
// method; this one tests the underlying schemaKindLabel helper which
// production code calls directly in some places.
func TestSchemaKindLabel_ExhaustiveCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   SchemaKind
		want string
	}{
		{SchemaList, "list"},
		{SchemaMap, "map"},
		{SchemaSet, "set"},
		{SchemaObject, "object"},
		{SchemaString, "unknown"},  // default branch
		{SchemaUnknown, "unknown"}, // explicit unknown
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := schemaKindLabel(tc.in); got != tc.want {
				t.Errorf("schemaKindLabel(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
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
// locals must return SchemaUnknown rather than crash on nil. The
// CyclicLocals case is already covered by the existing
// TestVarTypeToSchemaKind_CyclicLocals_NoPanic in checks_test.go.
func TestVarTypeToSchemaKind_NonLocalRef(t *testing.T) {
	t.Parallel()
	expr := parseExprForTest(t, "var.foo")
	if got := varTypeToSchemaKind(expr, nil, nil); got != SchemaUnknown {
		t.Errorf("varTypeToSchemaKind(var.foo) = %v, want SchemaUnknown", got)
	}
}

func TestVarTypeToSchemaKind_UndefinedLocal(t *testing.T) {
	t.Parallel()
	expr := parseExprForTest(t, "local.missing")
	if got := varTypeToSchemaKind(expr, map[string]LocalInfo{}, nil); got != SchemaUnknown {
		t.Errorf("varTypeToSchemaKind(local.missing) = %v, want SchemaUnknown", got)
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
	if got := resolveExprTypeRecursive(expr, map[string]LocalInfo{}, nil); got != TypeUnknown {
		t.Errorf("resolveExprTypeRecursive(local.missing) = %v, want TypeUnknown", got)
	}
}

func TestResolveExprTypeRecursive_Cycle(t *testing.T) {
	t.Parallel()
	exprA := parseExprForTest(t, "local.b")
	exprB := parseExprForTest(t, "local.a")
	locals := map[string]LocalInfo{
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
func TestParseModuleVarSchemas_CacheHit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vars.tf"),
		[]byte(`variable "foo" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := make(map[string]map[string]TypeSchema)

	// First call: must read the dir AND populate the cache.
	first := parseModuleVarSchemas(dir, cache)
	if _, ok := first["foo"]; !ok {
		t.Fatalf("first call: missing 'foo' in result %v", first)
	}
	if _, hit := cache[dir]; !hit {
		t.Fatalf("cache entry missing after FIRST call; subsequent calls would redo I/O")
	}

	// Second call: must return the cached value (we don't have a
	// direct hook to assert "no I/O happened" without an interface
	// seam, but the result must equal the first call's result and
	// the cache entry must still be there).
	second := parseModuleVarSchemas(dir, cache)
	if len(second) != len(first) {
		t.Errorf("second call returned %d entries, want %d", len(second), len(first))
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
	cache := make(map[string]map[string]TypeSchema)
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
