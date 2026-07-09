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
