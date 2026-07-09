// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"testing"
)

func TestSplitTemplate_PureLiteral(t *testing.T) {
	t.Parallel()
	e := parseExprFromString(t, `"hello world"`)
	parts := SplitTemplate(e)
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1; parts=%+v", len(parts), parts)
	}
	if parts[0].IsInterp() {
		t.Errorf("parts[0] is interpolation, want literal")
	}
	if parts[0].Literal != "hello world" {
		t.Errorf("parts[0].Literal = %q, want %q", parts[0].Literal, "hello world")
	}
	if !IsAllLiteral(parts) {
		t.Errorf("IsAllLiteral = false, want true")
	}
	if got := LiteralString(parts); got != "hello world" {
		t.Errorf("LiteralString = %q, want %q", got, "hello world")
	}
}

func TestSplitTemplate_PureInterp_TemplateWrap(t *testing.T) {
	t.Parallel()
	// `"${var.foo}"` — HCL emits this as TemplateWrapExpr (compact form).
	e := parseExprFromString(t, `"${var.foo}"`)
	parts := SplitTemplate(e)
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1; parts=%+v", len(parts), parts)
	}
	if !parts[0].IsInterp() {
		t.Errorf("parts[0] is literal, want interpolation")
	}
	if IsAllLiteral(parts) {
		t.Errorf("IsAllLiteral = true, want false")
	}
}

func TestSplitTemplate_MixedParts(t *testing.T) {
	t.Parallel()
	// `"a${var.b}c"` — TemplateExpr with 3 parts: literal, interp, literal.
	e := parseExprFromString(t, `"a${var.b}c"`)
	parts := SplitTemplate(e)
	if len(parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3; parts=%+v", len(parts), parts)
	}
	if parts[0].IsInterp() || parts[0].Literal != "a" {
		t.Errorf("parts[0] = %+v, want literal 'a'", parts[0])
	}
	if !parts[1].IsInterp() {
		t.Errorf("parts[1] = %+v, want interpolation", parts[1])
	}
	if parts[2].IsInterp() || parts[2].Literal != "c" {
		t.Errorf("parts[2] = %+v, want literal 'c'", parts[2])
	}
	if IsAllLiteral(parts) {
		t.Errorf("IsAllLiteral = true, want false")
	}
}

func TestSplitTemplate_EmptyString(t *testing.T) {
	t.Parallel()
	// Empty-string literal — TemplateExpr with either zero parts or one
	// empty literal part depending on HCL's parser choices. Either shape
	// counts as "all literal" and yields an empty LiteralString.
	e := parseExprFromString(t, `""`)
	parts := SplitTemplate(e)
	if !IsAllLiteral(parts) {
		t.Errorf("IsAllLiteral = false, want true (empty string is a valid literal)")
	}
	if got := LiteralString(parts); got != "" {
		t.Errorf("LiteralString = %q, want %q", got, "")
	}
}

func TestSplitTemplate_NonTemplate_ReturnsNil(t *testing.T) {
	t.Parallel()

	// Bare traversal, number, boolean, function call — none of these are
	// template expressions. SplitTemplate returns nil so callers skip.
	cases := []string{
		"var.foo",
		"42",
		"true",
		"false",
		"null",
		`concat(["a"], ["b"])`,
	}
	for _, expr := range cases {
		expr := expr
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			e := parseExprFromString(t, expr)
			if parts := SplitTemplate(e); parts != nil {
				t.Errorf("SplitTemplate(%q) = %+v, want nil", expr, parts)
			}
		})
	}
}

func TestCompose_ReplacesInterpWithPlaceholder(t *testing.T) {
	t.Parallel()
	e := parseExprFromString(t, `"10.0.${var.subnet}.0/24"`)
	parts := SplitTemplate(e)
	if len(parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(parts))
	}

	// Digit placeholder — turns into a valid literal CIDR shape.
	if got := Compose(parts, "0"); got != "10.0.0.0/24" {
		t.Errorf("Compose(parts, \"0\") = %q, want %q", got, "10.0.0.0/24")
	}
	// Sentinel placeholder — human-readable form for error messages.
	if got := Compose(parts, "<P>"); got != "10.0.<P>.0/24" {
		t.Errorf("Compose(parts, \"<P>\") = %q, want %q", got, "10.0.<P>.0/24")
	}
	// Empty placeholder — collapses the template to just its literal parts.
	if got := Compose(parts, ""); got != "10.0..0/24" {
		t.Errorf("Compose(parts, \"\") = %q, want %q", got, "10.0..0/24")
	}
}

func TestCompose_AllLiteral_Identity(t *testing.T) {
	t.Parallel()
	e := parseExprFromString(t, `"static value"`)
	parts := SplitTemplate(e)
	// With no interpolations, the placeholder is unused and Compose
	// returns the same string regardless of placeholder value.
	for _, ph := range []string{"", "X", "any"} {
		if got := Compose(parts, ph); got != "static value" {
			t.Errorf("Compose(parts, %q) = %q, want %q", ph, got, "static value")
		}
	}
}

func TestCompose_AllInterp_JustPlaceholder(t *testing.T) {
	t.Parallel()
	// `"${var.foo}"` — one interp part, no literal.
	e := parseExprFromString(t, `"${var.foo}"`)
	parts := SplitTemplate(e)
	if got := Compose(parts, "<P>"); got != "<P>" {
		t.Errorf("Compose(parts, \"<P>\") = %q, want %q", got, "<P>")
	}
}

func TestTryLiteralString_PureLiteral(t *testing.T) {
	t.Parallel()
	e := parseExprFromString(t, `"hello world"`)
	s, ok := TryLiteralString(e)
	if !ok {
		t.Fatalf("TryLiteralString ok=false, want true")
	}
	if s != "hello world" {
		t.Errorf("TryLiteralString = %q, want %q", s, "hello world")
	}
}

func TestTryLiteralString_EmptyLiteral(t *testing.T) {
	t.Parallel()
	e := parseExprFromString(t, `""`)
	s, ok := TryLiteralString(e)
	// Empty literal is a valid pure-literal — ok=true, string is "".
	// The bool disambiguates from ("", false) meaning "not a literal".
	if !ok {
		t.Fatalf("TryLiteralString ok=false, want true (empty literal)")
	}
	if s != "" {
		t.Errorf("TryLiteralString = %q, want empty", s)
	}
}

func TestTryLiteralString_Interpolated_ReturnsFalse(t *testing.T) {
	t.Parallel()
	// Any interpolation content means "not a pure literal" — the fast
	// path returns false so callers fall through to SplitTemplate.
	cases := []string{
		`"${var.foo}"`,
		`"prefix-${var.foo}"`,
		`"${var.foo}-suffix"`,
	}
	for _, expr := range cases {
		expr := expr
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			e := parseExprFromString(t, expr)
			if _, ok := TryLiteralString(e); ok {
				t.Errorf("TryLiteralString(%q) ok=true, want false (has interp)", expr)
			}
		})
	}
}

func TestTryLiteralString_NonTemplate_ReturnsFalse(t *testing.T) {
	t.Parallel()
	cases := []string{
		"var.foo",
		"42",
		"true",
		"null",
	}
	for _, expr := range cases {
		expr := expr
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			e := parseExprFromString(t, expr)
			if _, ok := TryLiteralString(e); ok {
				t.Errorf("TryLiteralString(%q) ok=true, want false", expr)
			}
		})
	}
}

// TestSplitTemplate_ForDirective_ReturnsNil verifies that a template
// containing a %{for ...}...%{endfor} directive is treated as
// "not analysable" — the resulting AST includes a TemplateJoinExpr
// whose semantics (variable-iteration join) don't fit the flat
// literal/interpolation part model.
//
// Why existing tests missed this: prior fixtures covered pure literals,
// pure interpolations, and mixed literal/interp parts, but never
// exercised a template with a directive part. The AST shape wasn't in
// the coverage matrix.
func TestSplitTemplate_ForDirective_ReturnsNil(t *testing.T) {
	t.Parallel()
	e := parseExprFromString(t, `"prefix-%{for x in [1, 2]}${x}%{endfor}-suffix"`)
	if parts := SplitTemplate(e); parts != nil {
		t.Errorf("SplitTemplate on %%{for} template = %+v, want nil", parts)
	}
}

// TestSplitTemplate_IfDirective_StillSplit verifies the flip side:
// %{if}...%{else}...%{endif} produces a ConditionalExpr in the AST
// (structurally identical to a normal `${x ? y : z}` conditional), so
// SplitTemplate handles it as a normal interpolation part.
//
// Rationale: we cannot tell a template `%{if}` directive from a normal
// `${x ? y : z}` interpolation at the AST level — both are
// ConditionalExpr. Rejecting all ConditionalExpr parts would
// over-restrict legitimate conditional interpolations, so we accept the
// %{if} case as an opaque interp. Placeholder substitution over it is
// no worse than any other conditional-value interp.
func TestSplitTemplate_IfDirective_StillSplit(t *testing.T) {
	t.Parallel()
	e := parseExprFromString(t, `"prefix-%{if x}A%{else}B%{endif}-suffix"`)
	parts := SplitTemplate(e)
	if parts == nil {
		t.Fatalf("SplitTemplate on %%{if} template = nil, want split parts")
	}
	if len(parts) != 3 {
		t.Errorf("len(parts) = %d, want 3", len(parts))
	}
	if !parts[1].IsInterp() {
		t.Errorf("middle part should be interp (ConditionalExpr)")
	}
}
