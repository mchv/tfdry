package checker

import (
	"testing"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// G26: cty.NullVal(cty.String).Type().FriendlyName() returns "string" but
// .AsString() panics on null values. The string-extraction helpers
// (objectKeyName, stringLiteralValue) and the LiteralValueExpr branch in
// inferExprType all gate their AsString calls on FriendlyName == "string"
// — without an additional !IsNull() check, a null literal would panic
// instead of yielding an empty/unknown value. In practice HCL string
// literals don't normally produce typed nulls during parse, but the
// defensive check costs one extra method call and removes a panic surface
// that downstream evaluation could otherwise reach.
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
