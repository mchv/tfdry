// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

func TestIsResourceTypeIdentifier(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want bool
	}{
		// Positive — real provider resource-type names.
		{"aws_iam_role", true},
		{"aws_vpc", true},
		{"google_project", true},
		{"azurerm_virtual_network", true},
		{"kubernetes_namespace", true},
		{"null_resource", true},
		{"random_pet", true},
		{"aws_iam_role_policy_attachment_for_each", true}, // long, still valid

		// Negative — no underscore, so not a resource type by convention.
		{"var", false},
		{"local", false},
		{"data", false},
		{"vars", false},
		{"foo", false},

		// Negative — bad shape.
		{"", false},
		{"_aws_iam", false},      // leading underscore
		{"aws_iam_", false},      // trailing underscore
		{"aws__iam", false},      // consecutive underscores
		{"AWS_iam_role", false},  // uppercase
		{"1aws_iam_role", false}, // leading digit
		{"aws-iam-role", false},  // hyphens instead of underscores
		{"aws.iam.role", false},  // dots
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isResourceTypeIdentifier(tc.name); got != tc.want {
				t.Errorf("isResourceTypeIdentifier(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// parseExprFromString parses `expr` as a standalone HCL expression. Test
// helper — returns the parsed AST so tests can feed real expressions into
// ValidateScopeRoot rather than hand-constructing traversal nodes.
func parseExprFromString(t *testing.T, expr string) hclsyntax.Expression {
	t.Helper()
	e, diags := hclsyntax.ParseExpression([]byte(expr), "test.tf", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		t.Fatalf("ParseExpression(%q) failed: %s", expr, diags.Error())
	}
	return e
}

func TestValidateScopeRoot_ValidRoots(t *testing.T) {
	t.Parallel()

	valid := []string{
		// Fixed scope roots
		"var.foo",
		"local.bar",
		"module.child.output",
		"data.aws_caller_identity.current.account_id",
		"path.module",
		"path.root",
		"terraform.workspace",
		"each.key",
		"each.value",
		"count.index",
		"self.arn",

		// Resource type identifiers
		"aws_iam_role.example.arn",
		"aws_vpc.main.cidr_block",
		"null_resource.trigger.id",
		"google_project.p.number",
	}

	for _, expr := range valid {
		expr := expr
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			e := parseExprFromString(t, expr)
			if diag := ValidateScopeRoot(e, nil); diag != nil {
				t.Errorf("ValidateScopeRoot(%q) = %+v, want nil", expr, diag)
			}
		})
	}
}

func TestValidateScopeRoot_InvalidRoots(t *testing.T) {
	t.Parallel()

	cases := []struct {
		expr     string
		wantRoot string
		wantHint string
	}{
		{"vars.foo", "vars", "var"},
		{"locals.bar", "locals", "local"},
		{"modules.child.out", "modules", "module"},
		{"datas.aws.foo", "datas", "data"},
		{"paths.module", "paths", "path"},
		{"terraforms.workspace", "terraforms", "terraform"},

		// Unrecognised, no hint available
		{"foo.bar", "foo", ""},
		{"env.thing", "env", ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.expr, func(t *testing.T) {
			t.Parallel()
			e := parseExprFromString(t, tc.expr)
			diag := ValidateScopeRoot(e, nil)
			if diag == nil {
				t.Fatalf("ValidateScopeRoot(%q) = nil, want diagnostic", tc.expr)
			}
			if diag.Root != tc.wantRoot {
				t.Errorf("Root = %q, want %q", diag.Root, tc.wantRoot)
			}
			if diag.Hint != tc.wantHint {
				t.Errorf("Hint = %q, want %q", diag.Hint, tc.wantHint)
			}
		})
	}
}

func TestValidateScopeRoot_NonTraversalExpr_ReturnsNil(t *testing.T) {
	t.Parallel()

	// Not a ScopeTraversalExpr — should silently return nil (out of scope
	// for the first-pass root check; not a bug).
	cases := []string{
		`"literal string"`,
		"42",
		"true",
		"var.foo + var.bar",    // BinaryOpExpr
		`concat(["a"], ["b"])`, // FunctionCallExpr
	}

	for _, expr := range cases {
		expr := expr
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			e := parseExprFromString(t, expr)
			if diag := ValidateScopeRoot(e, nil); diag != nil {
				t.Errorf("ValidateScopeRoot(%q) = %+v, want nil (not a scope traversal)", expr, diag)
			}
		})
	}
}

// TestValidateScopeRoot_IteratorsRecognized verifies that a root name
// listed in the iterators map (dynamic-block scope) is accepted by
// ValidateScopeRoot even though it is not in tfScopeRoots and not a
// resource-type identifier.
//
// The iterators parameter carries lexical scope from the caller's walk
// (see walkExpressions in checks.go, which pushes dynamic-block iterator
// names when descending into content{} sub-blocks).
func TestValidateScopeRoot_IteratorsRecognized(t *testing.T) {
	t.Parallel()

	iterators := map[string]struct{}{"ingress": {}, "rule": {}}

	// In-scope iterator: accepted.
	e := parseExprFromString(t, "ingress.value")
	if diag := ValidateScopeRoot(e, iterators); diag != nil {
		t.Errorf("ValidateScopeRoot(ingress.value, {ingress, rule}) = %+v, want nil", diag)
	}

	// Custom-iterator name: accepted.
	e = parseExprFromString(t, "rule.key")
	if diag := ValidateScopeRoot(e, iterators); diag != nil {
		t.Errorf("ValidateScopeRoot(rule.key, {ingress, rule}) = %+v, want nil", diag)
	}

	// Not in the iterator set — still rejected.
	e = parseExprFromString(t, "egress.value")
	if diag := ValidateScopeRoot(e, iterators); diag == nil {
		t.Errorf("ValidateScopeRoot(egress.value, {ingress, rule}) = nil, want diagnostic")
	}

	// Nil iterators map — behaves like the pre-round-5 signature.
	e = parseExprFromString(t, "vars.foo")
	if diag := ValidateScopeRoot(e, nil); diag == nil {
		t.Errorf("ValidateScopeRoot(vars.foo, nil) = nil, want diagnostic")
	}
}
