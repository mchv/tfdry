// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// ── E009 broadening — fires from walkExpressions, not just CIDR context ─────

// TestE009_NonCIDRAttribute_FiresE009 verifies E009 catches scope-root
// typos in attributes that are not CIDR-triggering. Prior to round 5,
// E009 emitted only from checkCIDR, so a typo in e.g. a resource `name`
// attribute went undetected. The registry summary implied broader
// coverage than the implementation delivered.
func TestE009_NonCIDRAttribute_FiresE009(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "b" {
  bucket = "prefix-${vars.env}"
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 on scope-root typo 'vars' in non-CIDR attribute, got: %v", codes(vs))
	}
}

// TestE009_VariableDefault_FiresE009 verifies E009 fires on typos inside
// variable default values — which walkCIDRBlocks previously skipped by
// design (variable defaults are Tier-3-excluded for CIDR). Broadening
// E009 to the general expression walker reaches these positions.
func TestE009_VariableDefault_FiresE009(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
variable "env_tag" {
  default = "prefix-${vars.env}"
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 on scope-root typo 'vars' inside variable default, got: %v", codes(vs))
	}
}

// TestE009_BareTraversal_FiresE009 verifies E009 fires on a bare
// (non-template) traversal with an unknown root. Prior to round 5, E009
// went through SplitTemplate which returns nil for bare traversals, so
// this case was silently missed even in CIDR context.
func TestE009_BareTraversal_FiresE009(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "b" {
  bucket = vars.name
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 on bare traversal 'vars.name', got: %v", codes(vs))
	}
}

// ── Dynamic-block iterator scoping ──────────────────────────────────────────

// TestE009_DynamicBlockDefaultIterator_NoFalsePositive verifies that a
// reference to the default iterator name (the dynamic block's label)
// inside its content{} block does not fire E009. Reported by Gemini
// as a HIGH-priority false positive: `dynamic "ingress"` introduces
// `ingress` as a valid scope root inside the content block, but the
// naked ValidateScopeRoot allowlist would reject it.
func TestE009_DynamicBlockDefaultIterator_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  dynamic "ingress" {
    for_each = var.cidrs
    content {
      cidr_blocks = [ingress.value]
      description = "from ${ingress.key}"
    }
  }
}
`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("dynamic block iterator 'ingress' must not fire E009, got: %v", codes(vs))
	}
}

// TestE009_DynamicBlockCustomIterator_NoFalsePositive verifies that a
// dynamic block with an explicit `iterator = X` override uses X (not
// the label) as the in-scope iterator name.
func TestE009_DynamicBlockCustomIterator_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  dynamic "ingress" {
    for_each = var.cidrs
    iterator = rule
    content {
      cidr_blocks = [rule.value]
      description = "from ${rule.key}"
    }
  }
}
`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("custom iterator 'rule' must not fire E009, got: %v", codes(vs))
	}
}

// TestE009_DynamicBlockOutsideScope_StillFires verifies that the
// iterator name is only in scope inside the dynamic block's content{}.
// A reference to `ingress.value` in a sibling attribute or outside the
// block must still fire E009 — otherwise the iterator scope is leaking.
func TestE009_DynamicBlockOutsideScope_StillFires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  # ingress.value is NOT in scope here — the dynamic block below
  # introduces the iterator only inside its content{}.
  description = "sg with ${ingress.value}"
  dynamic "ingress" {
    for_each = var.cidrs
    content {
      cidr_blocks = [ingress.value]
    }
  }
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 on 'ingress' used outside dynamic block content, got: %v", codes(vs))
	}
}

// TestE009_DynamicBlockNested_BothIteratorsInScope verifies that nested
// dynamic blocks stack their iterators — the inner content{} sees both
// the inner and outer iterator names.
func TestE009_DynamicBlockNested_BothIteratorsInScope(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  dynamic "ingress" {
    for_each = var.groups
    content {
      dynamic "cidr_blocks" {
        for_each = ingress.value.cidrs
        content {
          # Both ingress.key (outer) and cidr_blocks.value (inner)
          # must be recognised as iterators here.
          description = "${ingress.key}: ${cidr_blocks.value}"
        }
      }
    }
  }
}
`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("nested dynamic-block iterators must not fire E009, got: %v", codes(vs))
	}
}

// TestE009_DynamicBlockIteratorAttribute_NotFlagged verifies that the
// `iterator = X` attribute itself does not fire E009. X is a bare
// identifier that Terraform interprets as the iterator name, not a
// reference to an existing scope root. It's syntactically a
// ScopeTraversalExpr in HCL but semantically a declaration.
//
// This is a corner case: without a special case, walkExpressions would
// visit the `rule` traversal and E009 would flag it as unknown (it's
// not in tfScopeRoots and not a resource-type identifier).
func TestE009_DynamicBlockIteratorAttribute_NotFlagged(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  dynamic "ingress" {
    for_each = var.cidrs
    iterator = rule
    content {
      cidr_blocks = [rule.value]
    }
  }
}
`,
	})
	// The iterator attribute value ('rule') must not itself be flagged.
	// If E009 fires with root "rule" here, the iterator declaration
	// is being mis-interpreted as a reference.
	for _, v := range vs {
		if v.Code != "E009" {
			continue
		}
		t.Fatalf("unexpected E009 %q — the iterator declaration must not be treated as a reference", v.Message)
	}
}

// ── Regression guards for the CIDR-context tests already in cidr_test.go ────
// Under the new architecture (E009 emitted from walkExpressions, not from
// checkCIDR), the E009-only mode must still fire on CIDR-attribute
// interpolations. Kept lightweight — the primary coverage is in cidr_test.go.

// TestE009_CIDRContext_StillFiresAfterMove verifies that moving E009
// emission out of checkCIDR into walkExpressions did not regress the
// original CIDR-context detection. Same fixture as
// TestE009_InterpolatedInvalidScopeRoot_ViolationE009 in cidr_test.go.
func TestE009_CIDRContext_StillFiresAfterMove(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${vars.subnet}.0/24"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	enabled := checker.CheckSet{"E009": {}}
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	if !hasCode(vs, "E009") {
		t.Fatalf("E009 must still fire in CIDR context after move, got: %v", codes(vs))
	}
}

// ── ForExpr scope tracking (fix for merged-code bug) ────────────────────────

// TestE009_ForExprValueVar_NoFalsePositive verifies that the value
// variable of a tuple for-expression is in scope inside the value
// expression, and does NOT fire E009 as an unknown scope root.
//
// The bug: walkExpressions previously used hclsyntax.VisitAll on
// attribute expressions, which visits every descendant with the
// enclosing scope. Inside `[for s in var.names : upper(s)]`, the
// traversal `s` was seen without `s` being in the iterators map, so
// E009 flagged it as an unknown root.
//
// The fix uses hclsyntax.Walk with a scoped Walker that responds to
// the ChildScope nodes HCL synthesises around ForExpr's KeyExpr /
// ValExpr / CondExpr — pushing KeyVar and ValVar into scope on entry
// and popping on exit.
func TestE009_ForExprValueVar_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  upper_names = [for s in var.names : upper(s)]
}
`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("for-expression value var 's' must not fire E009, got: %v", codes(vs))
	}
}

// TestE009_ForExprKeyValueVars_NoFalsePositive verifies that BOTH the
// key and value variables of an object for-expression are in scope
// inside the produced key and value expressions. Covers the `{for K,
// V in COLL : K => V.name}` shape.
func TestE009_ForExprKeyValueVars_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  name_by_key = {for k, v in var.items : k => v.name}
}
`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("for-expression key/value vars 'k'/'v' must not fire E009, got: %v", codes(vs))
	}
}

// TestE009_ForExprIfCondition_NoFalsePositive verifies that the
// iterator var is in scope inside the `if` clause of a for-expression.
// HCL synthesises a ChildScope for CondExpr too, so if the walker
// mishandles that node the guard fires wrongly.
func TestE009_ForExprIfCondition_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  non_empty = [for s in var.names : s if s != ""]
}
`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("for-expression iterator 's' in if-condition must not fire E009, got: %v", codes(vs))
	}
}

// TestE009_ForExprCollectionTypo_StillFires guards the scope
// boundary: the collection expression is evaluated BEFORE the
// iterator vars are bound, so a scope-root typo inside CollExpr
// must still fire E009. Otherwise the fix would over-relax and
// stop catching legitimate typos.
func TestE009_ForExprCollectionTypo_StillFires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  bad = [for s in vars.names : upper(s)]
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("scope-root typo 'vars' in for-expression collection must still fire E009, got: %v", codes(vs))
	}
}

// TestE009_ForExprAfterEnd_StillFires verifies the iterator scope is
// popped after the for-expression: a reference to the iterator name
// in an adjacent attribute must fire E009 (the name is not
// package-scoped, only expression-scoped).
func TestE009_ForExprAfterEnd_StillFires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  inside  = [for s in var.names : upper(s)]
  outside = s
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("iterator 's' used outside its for-expression must fire E009, got: %v", codes(vs))
	}
}

// TestE009_NestedForExpr_BothIteratorsInScope covers scope-stack
// discipline. Inside an inner for-expression, both the outer and
// inner iterator names must be visible. If the walker replaces
// (rather than augments) the scope on entry, the outer name would
// disappear and cause a false E009.
func TestE009_NestedForExpr_BothIteratorsInScope(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  matrix = [for row in var.rows : [for cell in row : cell + row[0]]]
}
`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("nested for-expression must see both iterators; got E009: %v", codes(vs))
	}
}

// ── ephemeral root (Terraform 1.10+) ────────────────────────────────────────

// TestE009_EphemeralRoot_NoFalsePositive verifies that the
// `ephemeral.*` scope root (introduced in Terraform 1.10 for ephemeral
// resources like `ephemeral "random_password" "..."`) is recognised
// and does not fire E009 as an unknown root.
//
// Bug: tfScopeRoots was frozen at Terraform 1.x baseline (var, local,
// module, data, path, terraform, each, count, self) and had not been
// updated when Terraform 1.10 added `ephemeral`. Any Terraform config
// referencing `ephemeral.<TYPE>.<NAME>.<ATTR>` would fire E009.
func TestE009_EphemeralRoot_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
ephemeral "random_password" "db_password" {
  length = 32
}

resource "aws_db_instance" "x" {
  password = ephemeral.random_password.db_password.result
}
`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("ephemeral.* root must not fire E009, got: %v", codes(vs))
	}
}
