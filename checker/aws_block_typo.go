// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ── E210: AWS block-name typo validation ────────────────────────────────────
//
// E210 catches known singular/plural block-name typos on AWS provider
// resources — a class of bug that produces valid HCL syntax and passes
// `terraform validate` cheaply only after `terraform init` loads the
// provider schema. At author time (no init), tfdry today reports zero
// violations for `permission { }` on `aws_quicksight_data_set` (a real
// typo), or `permissions { }` on `aws_quicksight_data_source` (also a
// real typo — the schema expects singular for this outlier).
//
// Design principles:
//
//   - Curated table only. Every entry references the actual
//     terraform-provider-aws documentation. No fuzzy matching, no
//     edit-distance heuristics, no schema-fetch. Adding an entry is a
//     deliberate act — cite the docs in the comment above it.
//
//   - AWS-scoped by shape (resource/data prefix). Fires only when the
//     enclosing block is `resource "aws_*"` or `data "aws_*"`. Module
//     blocks are opaque and skipped.
//
//   - Direct children only in v1. `permissions { }` at resource top
//     level on `aws_quicksight_data_source` fires; `permissions { }`
//     inside a deeper nested block (e.g. `physical_table_map`) does
//     NOT fire — the check targets resource-level typos, not
//     "unknown block anywhere" (that's terraform validate's remit).
//
//   - False-positive discipline. New provider releases occasionally
//     add or rename blocks. The curated table only asserts what we
//     know to be wrong, never what we don't know to be right — so
//     unknown block names go unflagged.

// blockTypos maps `resource-type → wrong-block-name → correct-block-name`.
// The two-level structure gives O(1) lookup by resource type on the
// hot path (most files contain resources not in the table); when a
// match is found, the inner map answers whether a specific child
// block is a known typo and, if so, what to point the user at.
//
// Each entry is verified against the terraform-provider-aws source
// docs (website/docs/r/*.html.markdown or d/*.html.markdown on
// hashicorp/terraform-provider-aws main branch). The comment above
// each cluster cites the relevant doc file.
//
// QuickSight family: 6 of the 7 QuickSight resources use `permissions`
// (plural); `aws_quicksight_data_source` is the outlier that uses
// `permission` (singular). Both directions of the typo are real —
// a user familiar with data_source writes `permission` on the other
// six; a user familiar with any of the other six writes `permissions`
// on data_source.
//
// Non-QuickSight: `aws_lb_listener_rule.condition`,
// `aws_wafv2_web_acl.rule`, and `aws_iam_policy_document.statement`
// are all singular blocks that appear multiple times per resource
// — the plural form is the natural typo.
var blockTypos = map[string]map[string]string{
	// QuickSight resources using `permissions` (plural).
	// Docs: website/docs/r/quicksight_{analysis,dashboard,data_set,
	// folder,template,theme}.html.markdown — argument reference
	// lists `permissions` in each.
	"aws_quicksight_analysis":  {"permission": "permissions"},
	"aws_quicksight_dashboard": {"permission": "permissions"},
	"aws_quicksight_data_set":  {"permission": "permissions"},
	"aws_quicksight_folder":    {"permission": "permissions"},
	"aws_quicksight_template":  {"permission": "permissions"},
	"aws_quicksight_theme":     {"permission": "permissions"},
	// QuickSight outlier using `permission` (singular).
	// Docs: website/docs/r/quicksight_data_source.html.markdown —
	// argument reference lists `permission - (Optional) A set of
	// resource permissions on the data source.`
	"aws_quicksight_data_source": {"permissions": "permission"},
	// ELB listener rule. `condition` is required and can appear
	// multiple times.
	// Docs: website/docs/r/lb_listener_rule.html.markdown —
	// `condition - (Required) A Condition block. Multiple condition
	// blocks of different types can be set...`
	"aws_lb_listener_rule": {"conditions": "condition"},
	// WAF v2 Web ACL. `rule` is optional and repeatable.
	// Docs: website/docs/r/wafv2_web_acl.html.markdown —
	// `rule - (Optional) ... Rule blocks used to identify the web
	// requests that you want to allow, block, or count.`
	"aws_wafv2_web_acl": {"rules": "rule"},
	// IAM policy document. `statement` is optional and repeatable
	// — the most common data source in AWS Terraform code.
	// Docs: website/docs/d/iam_policy_document.html.markdown —
	// `statement (Optional) - Configuration block for a policy
	// statement.`
	"aws_iam_policy_document": {"statements": "statement"},
}

// checkBlockTypo runs E210 over a single parsed file, returning one
// Violation per finding. Called from Run() when E210 is enabled.
//
// Structure is intentionally flatter than E201/E202/E203's recursive
// walkers: `resource` and `data` blocks appear only at top level in
// Terraform, and v1 only inspects direct children of each such
// block — so there's nothing to recurse into.
func checkBlockTypo(f ParsedFile) []Violation {
	if f.Body == nil {
		return nil
	}
	var violations []Violation
	for _, block := range f.Body.Blocks {
		// Both `resource "aws_*" "..."` and `data "aws_*" "..."`
		// are in scope. Any other top-level block type (locals,
		// output, terraform, module, variable, provider, ...) is
		// skipped — the block-name-typo class this check targets
		// is specific to provider-schema-shaped nested blocks.
		if block.Type != "resource" && block.Type != "data" {
			continue
		}
		if len(block.Labels) == 0 {
			continue
		}
		resourceType := block.Labels[0]
		typos, known := blockTypos[resourceType]
		if !known {
			// Not in the curated table — say nothing. This is the
			// hot path for most files; keeping it a single map
			// lookup keeps E210 nearly free when no matches exist.
			continue
		}
		for _, child := range block.Body.Blocks {
			correct, isTypo := typos[child.Type]
			if !isTypo {
				continue
			}
			violations = append(violations, blockTypoViolation(f.Name, child, resourceType, correct))
		}
	}
	return violations
}

// blockTypoViolation packages a Violation for E210. The message
// doubles as the fix: it names the resource type, the wrong block
// name found, and the correct block name to substitute — so the
// user has everything they need without opening the provider docs.
func blockTypoViolation(file string, block *hclsyntax.Block, resourceType, correct string) Violation {
	return Violation{
		Code:     "E210",
		Severity: "error",
		File:     file,
		Line:     block.TypeRange.Start.Line,
		Message:  resourceType + `: unknown block "` + block.Type + `", did you mean "` + correct + `"?`,
	}
}
