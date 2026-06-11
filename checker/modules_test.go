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
		want SchemaKind
	}{
		{"primitive string traversal", "x = string", SchemaString},
		{"primitive number traversal", "x = number", SchemaNumber},
		{"primitive bool traversal", "x = bool", SchemaBool},
		{"any traversal → unknown", "x = any", SchemaUnknown},

		{"primitive string function-call", "x = string()", SchemaString},
		{"primitive number function-call", "x = number()", SchemaNumber},
		{"primitive bool function-call", "x = bool()", SchemaBool},
		{"any function-call → unknown", "x = any()", SchemaUnknown},

		{"unknown function-call falls through", "x = nope()", SchemaUnknown},
		{"object()", "x = object({})", SchemaObject},
		{"list()", "x = list(string)", SchemaList},
		{"map()", "x = map(number)", SchemaMap},
		{"set()", "x = set(bool)", SchemaSet},

		{"optional() unwraps to inner", "x = optional(string)", SchemaString},
		{"optional() with no args → unknown", "x = optional()", SchemaUnknown},

		{"unknown traversal name → unknown (skip checks)", "x = mystery", SchemaUnknown},

		// C17: malformed container types must return Unknown, not concrete
		// SchemaList/Set/Map with Elem=nil. Emitting a concrete kind makes
		// downstream compareExprToSchema produce misleading E006 ("declared
		// list, got string") when the actual problem is the module-side type
		// constraint. SchemaUnknown short-circuits the check, which matches
		// the fail-safe stance for unrecognised types.
		{"list() no args → unknown", "x = list()", SchemaUnknown},
		{"list(a, b) too many args → unknown", "x = list(string, number)", SchemaUnknown},
		{"set() no args → unknown", "x = set()", SchemaUnknown},
		{"set(a, b) too many args → unknown", "x = set(string, number)", SchemaUnknown},
		{"map() no args → unknown", "x = map()", SchemaUnknown},
		{"map(a, b) too many args → unknown", "x = map(string, number)", SchemaUnknown},
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
	if got.Kind != SchemaNumber {
		t.Fatalf("expected SchemaNumber, got %v", got.Kind)
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
	if got.Kind != SchemaString {
		t.Errorf("TemplateWrapExpr-wrapped string should resolve to SchemaString, got %v", got.Kind)
	}
}

// TestTypeSchema_label covers all branches of TypeSchema.label().
func TestTypeSchema_label(t *testing.T) {
	cases := []struct {
		kind SchemaKind
		want string
	}{
		{SchemaString, "string"},
		{SchemaNumber, "number"},
		{SchemaBool, "bool"},
		{SchemaObject, "object"},
		{SchemaList, "list"},
		{SchemaMap, "map"},
		{SchemaSet, "set"},
		{SchemaUnknown, "unknown"},
		{SchemaKind(99), "unknown"}, // default branch
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			s := TypeSchema{Kind: tc.kind}
			if got := s.label(); got != tc.want {
				t.Errorf("TypeSchema{Kind:%v}.label() = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// TestTypeSchema_isScalar covers all branches of TypeSchema.isScalar().
func TestTypeSchema_isScalar(t *testing.T) {
	cases := []struct {
		kind SchemaKind
		want bool
	}{
		{SchemaString, true},
		{SchemaNumber, true},
		{SchemaBool, true},
		{SchemaObject, false},
		{SchemaList, false},
		{SchemaMap, false},
		{SchemaSet, false},
		{SchemaUnknown, false},
	}
	for _, tc := range cases {
		t.Run(TypeSchema{Kind: tc.kind}.label(), func(t *testing.T) {
			s := TypeSchema{Kind: tc.kind}
			if got := s.isScalar(); got != tc.want {
				t.Errorf("TypeSchema{Kind:%v}.isScalar() = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}
