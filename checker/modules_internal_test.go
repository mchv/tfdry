package checker

import (
	"testing"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// G26: cty.NullVal(cty.String).Type().FriendlyName() returns "string" but
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

// G28: parseModuleVarSchemas writes to its `cache` argument
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
