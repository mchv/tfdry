// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ── E203: Malformed ARN structure ────────────────────────────────────────────
//
// Validates ARN grammar (prefix, segment count, partition set, service
// lexical form, region shape, account shape, non-empty resource). Does
// NOT validate service-specific semantics — the resource segment is
// too heterogeneous across services to check meaningfully without a
// per-service schema. The check catches malformed structure, not
// invalid-for-a-specific-service references.
//
// AWS ARN grammar:
//
//	arn:PARTITION:SERVICE:REGION:ACCOUNT:RESOURCE
//
// Fields:
//   - PARTITION   one of the AWS partitions (aws, aws-us-gov, aws-cn,
//                 aws-iso, aws-iso-b, aws-iso-e, aws-iso-f) or `*`
//   - SERVICE     lowercase service identifier (iam, s3, ec2, lambda, ...)
//   - REGION      an AWS region code, or empty for global services (IAM, S3).
//                 Strict region validation is skipped when the partition
//                 is an ISO partition — those region sets are not publicly
//                 enumerable in a stable form.
//   - ACCOUNT     a 12-digit account ID, empty, or the sentinel "aws" for
//                 AWS-managed IAM policies (arn:aws:iam::aws:policy/...)
//   - RESOURCE    everything after the 5th colon — service-specific,
//                 may contain colons and slashes (Lambda function versions,
//                 log group streams, ...)
//
// Attribute triggers use a suffix pattern (Tier 1):
//   - Scalar: attribute name ends in `_arn` (with underscore prefix)
//   - List:   attribute name ends in `_arns` (with underscore prefix)
//
// A bare `arn` attribute is NOT triggered — it's almost always an output
// attribute in Terraform resources rather than a user input, and linting
// output positions would produce noise.
//
// Interpolation-aware validation: uses the template subsystem from
// PR #47. The permissive composed-form rule: split the composed
// value by `:`, then for each of the 5 pre-resource fields:
//   - Fully-literal field → validate strictly against its grammar
//   - Field containing an interpolation placeholder → skip validation
//     of that field (can't statically resolve)
//   - Resource field → always accepted if non-empty
//
// This lets the common template pattern (partition or account fully
// interpolated, e.g. `"arn:${data.aws_partition.current.partition}:iam::aws:policy/foo"`)
// pass cleanly while still catching typos in adjacent literal fields.
//
// Zero-alloc hot path:
//   - strings.HasPrefix("arn:") — 4-byte compare, SIMD-friendly.
//   - 4× strings.IndexByte(':') — SIMD-accelerated single-byte scan on
//     amd64 (SSE2/AVX2) and arm64 (NEON) via Go's stdlib.
//   - Fixed-size [5]string array for field boundaries — stack-allocated.
//   - Fields extracted via substring slicing (zero-copy string headers).

// awsPartitions enumerates the AWS partitions. The three commercial-style
// partitions (aws, aws-us-gov, aws-cn) are documented in AWS's
// Fault Isolation Boundaries whitepaper. The four ISO partitions
// (aws-iso, aws-iso-b, aws-iso-e, aws-iso-f) are used in air-gapped
// classified environments (CIA, SC2S, NATO-partner MODs); they're
// documented in the AWS Go/Java SDKs and the terraform-provider-aws
// issue tracker (hashicorp/terraform-provider-aws#18593). Legitimate
// ISO ARNs must not false-positive on the partition field.
//
// Locked list — new partitions have not been added since aws-iso-f;
// adding one requires a documented AWS announcement.
var awsPartitions = map[string]struct{}{
	// Commercial / GovCloud / China
	"aws":        {}, // commercial
	"aws-us-gov": {}, // GovCloud
	"aws-cn":     {}, // China (Sinnet + NWCD)
	// ISO (classified environments — regions are not publicly enumerable
	// in a stable form, so validateARNFields skips strict region
	// validation when the partition is one of these)
	"aws-iso":   {}, // C2S — US TS
	"aws-iso-b": {}, // SC2S — US SCI
	"aws-iso-e": {}, // ISOE — EU classified
	"aws-iso-f": {}, // ISOF — UK / NATO partner classified
}

// isISOPartition reports whether partition is one of the four ISO
// partitions. Called from validateARNFields to relax the region check
// (the ISO region set is not publicly enumerable in a stable form).
func isISOPartition(partition string) bool {
	switch partition {
	case "aws-iso", "aws-iso-b", "aws-iso-e", "aws-iso-f":
		return true
	}
	return false
}

// isISORegion reports whether region has the shape of an AWS ISO region
// code. Prefix-based heuristic keyed on the well-known first segments
// of each ISO partition's region names:
//
//   - us-iso-*   → aws-iso partition   (e.g. us-iso-east-1)
//   - us-isob-*  → aws-iso-b partition (e.g. us-isob-east-1)
//   - eu-isoe-*  → aws-iso-e partition (e.g. eu-isoe-west-1)
//   - us-isof-*  → aws-iso-f partition (e.g. us-isof-south-1)
//
// Used in validateARNFields to relax strict region validation when the
// partition is not a concrete literal (interpolated or wildcarded) but
// the region shape indicates ISO scope. This is deliberately narrower
// than "skip whenever partition is unknown": commercial-region typos in
// interpolated-partition templates are still caught because their
// region doesn't match any ISO prefix.
//
// Zero-alloc: 4× HasPrefix on constants, no map lookup.
func isISORegion(region string) bool {
	return strings.HasPrefix(region, "us-iso-") ||
		strings.HasPrefix(region, "us-isob-") ||
		strings.HasPrefix(region, "eu-isoe-") ||
		strings.HasPrefix(region, "us-isof-")
}

// arnPlaceholder is the sentinel value used to mark interpolation positions
// during template composition. Chosen to be lexically distinct from any
// legitimate ARN field content (contains `<>` which are illegal in AWS
// identifiers) so a field containing it can be reliably detected.
const arnPlaceholder = "<INTERP>"

// arnAccountSentinel is the special account-field value used for AWS-managed
// IAM policies: `arn:aws:iam::aws:policy/AdministratorAccess`. Callers
// validating the account field accept this literal alongside empty and a
// 12-digit account ID.
const arnAccountSentinel = "aws"

// arnTriggerSuffix is the attribute-name suffix that identifies scalar ARN
// attributes. The underscore prefix is intentional: `arn` alone (no prefix)
// is typically an output attribute; only prefixed forms like `role_arn`,
// `target_arn`, `topic_arn` are user inputs.
const arnTriggerSuffix = "_arn"

// arnListTriggerSuffix is the attribute-name suffix for list-typed ARN
// attributes: `policy_arns`, `managed_policy_arns`, `subject_alternative_arns`.
const arnListTriggerSuffix = "_arns"

// arnMinLength is the shortest possible well-formed ARN. The minimum
// concrete-partition-and-service form is `arn:aws:s3:::b` (14 bytes) —
// shortest partition (`aws`, 3 chars) plus shortest service (`s3`, 2
// chars) plus empty region and account plus 1-char resource. Round 4
// removed partition/service wildcard support, so this bound is now
// tight; anything shorter cannot possibly parse.
const arnMinLength = len("arn:aws:s3:::b")

// arnWildcard is the `*` character permitted in the region and account
// fields of an ARN. Round 4 narrowed the accepted positions: AWS docs
// explicitly forbid wildcards in the service segment, and the partition
// wildcard only appears in `Resource`/`NotResource` policy patterns
// which are outside this check's trigger surface (Terraform `*_arn`
// attributes always name a specific resource).
const arnWildcard = "*"

// checkARN runs E203 over a single parsed file, returning one Violation
// per finding. Called from Run() when E203 is enabled.
func checkARN(f ParsedFile) []Violation {
	var violations []Violation
	walkARNBlocks(f.Body, f.Name, &violations)
	return violations
}

// walkARNBlocks descends into a body's attributes and child blocks. The
// trigger detection uses HasSuffix (rather than a map lookup) because
// the trigger surface is a naming convention, not an enumeration.
func walkARNBlocks(body *hclsyntax.Body, file string, violations *[]Violation) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		switch arnAttributeShape(attr.Name) {
		case cidrShapeScalar:
			checkARNScalar(file, attr, violations)
		case cidrShapeList:
			checkARNList(file, attr, violations)
		case cidrShapeNone:
			// Not a trigger — silently ignore.
		}
	}
	for _, block := range body.Blocks {
		if block.Type == "variable" {
			continue
		}
		walkARNBlocks(block.Body, file, violations)
	}
}

// arnAttributeShape reports whether attribute `name` is an ARN trigger.
// Uses suffix matching rather than an enumerated map because the surface
// (all `*_arn` and `*_arns` attributes across every AWS resource type)
// is too large to maintain by hand and follows a strict Terraform naming
// convention.
//
// The underscore prefix in the suffix (`_arn` not `arn`) is deliberate:
// a bare `arn` attribute is almost always an output; only prefixed
// input forms are checked.
func arnAttributeShape(name string) cidrShape {
	switch {
	case strings.HasSuffix(name, arnListTriggerSuffix):
		// Check `_arns` before `_arn` because `_arns` also ends in
		// `_arn` — order matters. Length must be > len(suffix) to
		// rule out a bare `_arns` attribute name.
		if len(name) > len(arnListTriggerSuffix) {
			return cidrShapeList
		}
	case strings.HasSuffix(name, arnTriggerSuffix):
		if len(name) > len(arnTriggerSuffix) {
			return cidrShapeScalar
		}
	}
	return cidrShapeNone
}

// checkARNScalar validates a scalar ARN attribute. Non-template
// expressions (bare traversals) are silently skipped — statically
// unresolvable. Pure-literal values go through validateARN directly;
// templated values go through the composed-form path.
func checkARNScalar(file string, attr *hclsyntax.Attribute, violations *[]Violation) {
	if s, ok := TryLiteralString(attr.Expr); ok {
		if s == "" {
			return
		}
		if diag := validateARN(s); diag != "" {
			*violations = append(*violations, arnViolation(file, attr.Expr.Range().Start.Line, attr.Name, s, diag))
		}
		return
	}
	parts := SplitTemplate(attr.Expr)
	if parts == nil {
		return
	}
	validateARNTemplate(file, attr.Expr.Range().Start.Line, attr.Name, parts, violations)
}

// checkARNList validates each element of a list-typed ARN attribute
// independently. Mirrors checkCIDRList's contract: one bad element
// produces one violation without silencing the siblings.
func checkARNList(file string, attr *hclsyntax.Attribute, violations *[]Violation) {
	tuple, ok := attr.Expr.(*hclsyntax.TupleConsExpr)
	if !ok {
		// Not a tuple literal (e.g. `_arns = var.foo` or `_arns = [for
		// x in var.list : x]`) — skip.
		return
	}
	for _, elem := range tuple.Exprs {
		if s, ok := TryLiteralString(elem); ok {
			if s == "" {
				continue
			}
			if diag := validateARN(s); diag != "" {
				*violations = append(*violations, arnViolation(file, elem.Range().Start.Line, attr.Name, s, diag))
			}
			continue
		}
		parts := SplitTemplate(elem)
		if parts == nil {
			continue
		}
		validateARNTemplate(file, elem.Range().Start.Line, attr.Name, parts, violations)
	}
}

// validateARN validates a pure-literal ARN string. Returns an empty
// string on success, or a short diagnostic describing the failure.
//
// Parsing uses strings.HasPrefix + 4× strings.IndexByte to locate the 5
// colon delimiters. IndexByte is SIMD-accelerated in Go's stdlib on
// amd64 (SSE2/AVX2) and arm64 (NEON), so the per-call cost scales at
// close to memory bandwidth even for long ARNs.
//
// Zero-alloc: fixed-size [5]string array on stack, all field extractions
// are substring slices (zero-copy string headers), no map allocations
// beyond the pre-existing package-level partitions/regions maps.
func validateARN(s string) string {
	// Prefix check first — a non-`arn:` value gets a more actionable
	// diagnostic than a length-based one, even for short inputs like
	// "xyz" or "not-an-arn".
	if !strings.HasPrefix(s, "arn:") {
		return "must start with \"arn:\""
	}
	if len(s) < arnMinLength {
		return "too short to be an ARN"
	}

	// Parse 5 fields after "arn:". Fields[0..3] are partition, service,
	// region, account. Fields[4] is the resource — everything after the
	// 5th colon (may itself contain colons and slashes).
	var fields [5]string
	start := 4
	for i := 0; i < 4; i++ {
		idx := strings.IndexByte(s[start:], ':')
		if idx < 0 {
			return "must have 6 colon-separated fields"
		}
		fields[i] = s[start : start+idx]
		start = start + idx + 1
	}
	fields[4] = s[start:]

	return validateARNFields(fields, false)
}

// validateARNTemplate validates a template-parts value by composing
// with a placeholder sentinel, then running the permissive fields
// validator that skips any field whose composed value contains the
// placeholder.
func validateARNTemplate(file string, line int, attrName string, parts []TemplatePart, violations *[]Violation) {
	composed := Compose(parts, arnPlaceholder)
	if !strings.HasPrefix(composed, "arn:") {
		// Templates where the "arn:" prefix itself comes from an
		// interpolation are silently skipped — no static signal.
		return
	}

	var fields [5]string
	start := 4
	for i := 0; i < 4; i++ {
		idx := strings.IndexByte(composed[start:], ':')
		if idx < 0 {
			// Composed form doesn't have all 6 fields — either the
			// template is truncated or the "resource" field is
			// entirely inside an interpolation. Skip either way.
			return
		}
		fields[i] = composed[start : start+idx]
		start = start + idx + 1
	}
	fields[4] = composed[start:]

	if diag := validateARNFields(fields, true); diag != "" {
		// For the display form, substitute the placeholder with a
		// human-readable marker in the error message.
		display := Compose(parts, "<P>")
		*violations = append(*violations, arnViolation(file, line, attrName, display, diag))
	}
}

// validateARNFields validates each of the 5 pre-resource fields plus the
// resource field. When permissive is true (template context), fields
// that contain the interpolation placeholder are accepted without strict
// validation — the caller can't statically resolve them anyway.
//
// Field validation order is chosen to catch the most likely user errors
// first: partition typos are the most common, then service, then region,
// then account.
func validateARNFields(fields [5]string, permissive bool) string {
	partition := fields[0]
	service := fields[1]
	region := fields[2]
	account := fields[3]
	resource := fields[4]

	// Partition — must be one of the known partitions (commercial or
	// ISO). The `*` wildcard is NOT accepted here: while wildcards
	// appear in AWS IAM `Resource`/`NotResource` policy patterns, this
	// check's trigger surface (Terraform `*_arn` attributes) always
	// references a specific resource — no legitimate `*_arn` value
	// carries a wildcard partition. Round 4 revision of round 1's
	// wildcard-permissive behaviour.
	if !fieldInterpolated(partition, permissive) {
		if _, ok := awsPartitions[partition]; !ok {
			return "invalid partition \"" + partition + "\""
		}
	}

	// Service — lowercase ASCII alphanumerics and hyphens. AWS's IAM
	// Resource docs explicitly forbid wildcards here:
	//   "You can't use a wildcard in the service segment that identifies
	//    the AWS product."
	// Round 4 removes the wildcard acceptance from round 1; the round-1
	// permissive design was based on a misreading of the IAM policy
	// grammar. Round 4 aligns with AWS documentation.
	if !fieldInterpolated(service, permissive) {
		if !isValidARNService(service) {
			return "invalid service \"" + service + "\""
		}
	}

	// Region — empty (global services), the wildcard `*`, a known AWS
	// region, OR skipped when partition context indicates ISO scope.
	// Skip cases:
	//   - partition is a concrete ISO literal (aws-iso, aws-iso-b, ...)
	//   - partition is interpolated, AND the region has the shape of
	//     an ISO region code (matches an isISORegion prefix)
	// The prefix-scoped skip means commercial-region typos in
	// interpolated-partition templates are still caught (the region
	// doesn't match any ISO prefix), while legitimate ISO ARNs like
	// `arn:${var.partition}:s3:us-iso-east-1:...` pass cleanly.
	// (Round 4: wildcard partition case removed — partition wildcards
	// no longer reach this branch, they've already returned above.)
	partitionInterpolated := fieldInterpolated(partition, permissive)
	skipRegion := isISOPartition(partition) || (partitionInterpolated && isISORegion(region))
	if !fieldInterpolated(region, permissive) && region != "" && region != arnWildcard && !skipRegion {
		if !validateRegion(region) {
			return "invalid region \"" + region + "\""
		}
	}

	// Account — empty, the wildcard `*`, "aws" (managed-policy sentinel),
	// or a 12-digit account ID.
	if !fieldInterpolated(account, permissive) && account != "" && account != arnWildcard {
		if account != arnAccountSentinel && !validateAccountID(account) {
			return "invalid account \"" + account + "\""
		}
	}

	// Resource — must be non-empty. Compose() always replaces
	// interpolations with a non-empty placeholder (arnPlaceholder), so
	// an empty resource field in the composed form can only come from
	// a literal empty resource in the template (e.g. `arn:aws:s3:::`
	// or `arn:${var.p}:s3:::`). That's always a malformed ARN — no
	// valid ARN has an empty resource — so permissive mode does NOT
	// skip this check.
	if resource == "" {
		return "resource must not be empty"
	}

	return ""
}

// fieldInterpolated reports whether the field's value contains the
// interpolation placeholder, indicating that the caller cannot
// statically validate the field. In non-permissive (pure-literal) mode,
// this always returns false because pure-literal input never contains
// the placeholder sentinel.
func fieldInterpolated(field string, permissive bool) bool {
	return permissive && strings.Contains(field, arnPlaceholder)
}

// isValidARNService reports whether s looks like a well-formed AWS
// service identifier. Enforces the lexical rule (lowercase, letters +
// digits + hyphens, must start with a letter) without pretending to
// enforce the exhaustive service list. AWS services are named lowercase
// by convention across every service-launch since 2006.
//
// Zero-alloc: single-pass byte scan.
func isValidARNService(s string) bool {
	if s == "" {
		return false
	}
	// Must start with a lowercase letter — services like `s3`, `iam`,
	// `lambda`, `ec2` all satisfy this. `123service` is illegal by
	// AWS convention.
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	// Loop starts at index 1: s[0] has already been validated as a
	// lowercase letter above, so re-scanning it would be redundant.
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

// arnViolation packages a Violation for E203.
func arnViolation(file string, line int, attrName, value, diag string) Violation {
	return Violation{
		Code:     "E203",
		Severity: "error",
		File:     file,
		Line:     line,
		Message:  attrName + ": malformed ARN \"" + value + "\" (" + diag + ")",
	}
}
