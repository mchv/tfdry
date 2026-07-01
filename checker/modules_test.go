// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// TestStringLiteralValue exercises every branch of stringLiteralValue.
// HCL parsing of `attr = "value"` always produces a TemplateExpr wrapping a
// LiteralValueExpr (the common branch). The bare LiteralValueExpr and
// TemplateWrapExpr branches are defensive — we construct those AST shapes
// directly to ensure the function still extracts the string correctly.
func TestStringLiteralValue(t *testing.T) {
	t.Run("TemplateExpr with single LiteralValueExpr part", func(t *testing.T) {
		// Parse `x = "hello"` and grab the value expression — HCL produces
		// TemplateExpr{Parts: [LiteralValueExpr]}.
		expr := parseAttrExpr(t, `x = "hello"`)
		if got := stringLiteralValue(expr); got != "hello" {
			t.Errorf(`stringLiteralValue(TemplateExpr "hello") = %q, want "hello"`, got)
		}
	})

	t.Run("bare LiteralValueExpr with string", func(t *testing.T) {
		// AST shape that wouldn't come from normal .tf parsing but is defensive.
		expr := &hclsyntax.LiteralValueExpr{
			Val: cty.StringVal("direct"),
		}
		if got := stringLiteralValue(expr); got != "direct" {
			t.Errorf(`stringLiteralValue(LiteralValueExpr) = %q, want "direct"`, got)
		}
	})

	t.Run("bare LiteralValueExpr with non-string returns empty", func(t *testing.T) {
		expr := &hclsyntax.LiteralValueExpr{
			Val: cty.NumberIntVal(42),
		}
		if got := stringLiteralValue(expr); got != "" {
			t.Errorf("stringLiteralValue(non-string LiteralValueExpr) = %q, want empty", got)
		}
	})

	t.Run("TemplateWrapExpr unwraps to inner literal", func(t *testing.T) {
		inner := &hclsyntax.LiteralValueExpr{
			Val: cty.StringVal("wrapped"),
		}
		expr := &hclsyntax.TemplateWrapExpr{Wrapped: inner}
		if got := stringLiteralValue(expr); got != "wrapped" {
			t.Errorf(`stringLiteralValue(TemplateWrapExpr) = %q, want "wrapped"`, got)
		}
	})

	t.Run("multi-part TemplateExpr returns empty (not a literal)", func(t *testing.T) {
		// Interpolation: parse `x = "${foo}"` — multi-part template, not a static literal.
		expr := parseAttrExpr(t, `x = "prefix-${foo}"`)
		if got := stringLiteralValue(expr); got != "" {
			t.Errorf("stringLiteralValue(interpolated template) = %q, want empty", got)
		}
	})

	t.Run("unsupported expression type returns empty", func(t *testing.T) {
		// e.g. a function call expression has no string literal value.
		expr := &hclsyntax.FunctionCallExpr{Name: "tostring"}
		if got := stringLiteralValue(expr); got != "" {
			t.Errorf("stringLiteralValue(FunctionCallExpr) = %q, want empty", got)
		}
	})
}

// parseAttrExpr parses src as an HCL block body and returns the expression
// of the first attribute. Used to obtain real AST nodes from HCL source.
func parseAttrExpr(t *testing.T, src string) hclsyntax.Expression {
	t.Helper()
	f, diags := hclsyntax.ParseConfig([]byte(src), "test.tf", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		t.Fatalf("parse error: %v", diags)
	}
	body := f.Body.(*hclsyntax.Body)
	for _, attr := range body.Attributes {
		return attr.Expr
	}
	t.Fatal("no attribute found in source")
	return nil
}

// TestParseTypeSchema covers the parser branches not exercised by the
// integration tests in checks_test.go: 'any', primitive function-call forms
// (string()/number()/bool()), and TemplateWrapExpr fallthrough.
func TestParseTypeSchema(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want schemaKind
	}{
		{"primitive string traversal", "x = string", schemaString},
		{"primitive number traversal", "x = number", schemaNumber},
		{"primitive bool traversal", "x = bool", schemaBool},
		{"any traversal → unknown", "x = any", schemaUnknown},

		{"primitive string function-call", "x = string()", schemaString},
		{"primitive number function-call", "x = number()", schemaNumber},
		{"primitive bool function-call", "x = bool()", schemaBool},
		{"any function-call → unknown", "x = any()", schemaUnknown},

		{"unknown function-call falls through", "x = nope()", schemaUnknown},
		{"object()", "x = object({})", schemaObject},
		{"list()", "x = list(string)", schemaList},
		{"map()", "x = map(number)", schemaMap},
		{"set()", "x = set(bool)", schemaSet},

		{"optional() unwraps to inner", "x = optional(string)", schemaString},
		{"optional() with no args → unknown", "x = optional()", schemaUnknown},

		{"unknown traversal name → unknown (skip checks)", "x = mystery", schemaUnknown},

		// Malformed container types must return Unknown, not concrete
		// schemaList/schemaSet/schemaMap with Elem=nil. Emitting a concrete kind makes
		// downstream compareExprToSchema produce misleading E006 ("declared
		// list, got string") when the actual problem is the module-side type
		// constraint. schemaUnknown short-circuits the check, which matches
		// the fail-safe stance for unrecognised types.
		{"list() no args → unknown", "x = list()", schemaUnknown},
		{"list(a, b) too many args → unknown", "x = list(string, number)", schemaUnknown},
		{"set() no args → unknown", "x = set()", schemaUnknown},
		{"set(a, b) too many args → unknown", "x = set(string, number)", schemaUnknown},
		{"map() no args → unknown", "x = map()", schemaUnknown},
		{"map(a, b) too many args → unknown", "x = map(string, number)", schemaUnknown},

		// Same fail-safe stance for primitives. `string`/`number`/`bool`
		// are TYPE KEYWORDS in HCL, not function calls — they should appear
		// as ScopeTraversalExpr. If they parse as FunctionCallExpr, the
		// type constraint is malformed (e.g. `type = string(bad)`). Returning
		// the matching scalar Schema would let downstream compareExprToSchema
		// emit misleading E006 against a broken declaration.
		{"string(bad) malformed → unknown", "x = string(bad)", schemaUnknown},
		{"string() with arg → unknown", `x = string("foo")`, schemaUnknown},
		{"number(1) malformed → unknown", "x = number(1)", schemaUnknown},
		{"bool(true) malformed → unknown", "x = bool(true)", schemaUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr := parseAttrExpr(t, tc.src)
			got := parseTypeSchema(expr)
			if got.Kind != tc.want {
				t.Errorf("parseTypeSchema(%q).Kind = %v, want %v", tc.src, got.Kind, tc.want)
			}
		})
	}
}

// TestParseTypeSchema_Optional asserts the Optional flag is set correctly.
func TestParseTypeSchema_Optional(t *testing.T) {
	expr := parseAttrExpr(t, "x = optional(number)")
	got := parseTypeSchema(expr)
	if got.Kind != schemaNumber {
		t.Fatalf("expected schemaNumber, got %v", got.Kind)
	}
	if !got.Optional {
		t.Errorf("Optional should be true for optional(number)")
	}
}

// TestParseTypeSchema_TemplateWrapExpr covers the TemplateWrapExpr branch:
// parser unwraps `${type}` to the inner expression. Constructed manually
// because real .tf files don't usually template-wrap a type expression.
func TestParseTypeSchema_TemplateWrapExpr(t *testing.T) {
	inner := parseAttrExpr(t, "x = string")
	wrapped := &hclsyntax.TemplateWrapExpr{Wrapped: inner}
	got := parseTypeSchema(wrapped)
	if got.Kind != schemaString {
		t.Errorf("TemplateWrapExpr-wrapped string should resolve to schemaString, got %v", got.Kind)
	}
}

// TestTypeSchema_label covers all branches of typeSchema.label().
func TestTypeSchema_label(t *testing.T) {
	cases := []struct {
		name string
		kind schemaKind
		want string
	}{
		{"string", schemaString, "string"},
		{"number", schemaNumber, "number"},
		{"bool", schemaBool, "bool"},
		{"object", schemaObject, "object"},
		{"list", schemaList, "list"},
		{"map", schemaMap, "map"},
		{"set", schemaSet, "set"},
		{"explicit_unknown", schemaUnknown, "unknown"},
		{"out_of_range_defaults_to_unknown", schemaKind(99), "unknown"}, // default branch
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := typeSchema{Kind: tc.kind}
			if got := s.label(); got != tc.want {
				t.Errorf("typeSchema{Kind:%v}.label() = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// TestTypeSchema_isScalar covers all branches of typeSchema.isScalar().
func TestTypeSchema_isScalar(t *testing.T) {
	cases := []struct {
		kind schemaKind
		want bool
	}{
		{schemaString, true},
		{schemaNumber, true},
		{schemaBool, true},
		{schemaObject, false},
		{schemaList, false},
		{schemaMap, false},
		{schemaSet, false},
		{schemaUnknown, false},
	}
	for _, tc := range cases {
		t.Run(typeSchema{Kind: tc.kind}.label(), func(t *testing.T) {
			s := typeSchema{Kind: tc.kind}
			if got := s.isScalar(); got != tc.want {
				t.Errorf("typeSchema{Kind:%v}.isScalar() = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}
