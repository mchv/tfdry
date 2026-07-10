// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ── E203: AWS ARN validation ────────────────────────────────────────────────
//
// AWS ARN grammar:
//
//	arn:PARTITION:SERVICE:REGION:ACCOUNT:RESOURCE
//
// Fields:
//   - PARTITION   one of the three AWS partitions (aws, aws-us-gov, aws-cn)
//   - SERVICE     lowercase service identifier (iam, s3, ec2, lambda, ...)
//   - REGION      an AWS region code, or empty for global services (IAM, S3)
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

// awsPartitions enumerates the three AWS partitions. Locked list — new
// partitions have not been added since aws-cn in 2013; adding one requires
// a documented AWS announcement.
var awsPartitions = map[string]struct{}{
	"aws":        {}, // commercial
	"aws-us-gov": {}, // GovCloud
	"aws-cn":     {}, // China
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

// arnMinLength is the shortest possible well-formed ARN. Computed from
// the minimum-length wildcard form `arn:*:*:::*` (11 bytes) — partition
// and service each 1-char wildcard, empty region and account, 1-char
// resource. Anything shorter cannot possibly parse; used as a fast-reject
// filter. The concrete-partition minimum is 14 bytes (`arn:aws:s3:::b`);
// bounding on the shorter wildcard form is required to avoid falsely
// rejecting valid `arn:*:*:*:*:*` policy patterns before wildcard handling
// gets a chance to accept them.
const arnMinLength = len("arn:*:*:::*")

// arnWildcard is the AWS-standard wildcard character permitted in the
// partition, service, region, and account fields of an ARN. Policy
// documents in particular routinely use wildcards
// (`arn:aws:s3:::*`, `arn:aws:*:*:*`, `arn:*:*:*:*:*`), so accepting `*`
// in these positions is required to match AWS's actual grammar rather
// than a hypothetically strict one.
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

	// Partition — must be one of the three known partitions, or the
	// wildcard `*` (permitted by AWS's policy grammar in resource ARN
	// patterns like `arn:*:*:*:*:*` and `arn:*:s3:::*`).
	if !fieldInterpolated(partition, permissive) && partition != arnWildcard {
		if _, ok := awsPartitions[partition]; !ok {
			return "invalid partition \"" + partition + "\""
		}
	}

	// Service — lowercase ASCII alphanumerics and hyphens, or the wildcard
	// `*`. Doesn't enumerate all 200+ AWS services — a permissive grammar
	// check catches obvious typos (uppercase, whitespace, unexpected
	// punctuation) without pretending to enforce the exhaustive service
	// list. The wildcard case handles policy-document ARN patterns like
	// `arn:aws:*:*:*:*`.
	if !fieldInterpolated(service, permissive) && service != arnWildcard {
		if !isValidARNService(service) {
			return "invalid service \"" + service + "\""
		}
	}

	// Region — empty (global services), the wildcard `*`, or a known AWS
	// region. The wildcard is used in resource ARN patterns.
	if !fieldInterpolated(region, permissive) && region != "" && region != arnWildcard {
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

	// Resource — must be non-empty. In permissive mode, an empty
	// resource might mean the resource is entirely interpolated but
	// the composed form has it empty; treat as skip.
	if resource == "" {
		if permissive {
			return ""
		}
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
	for i := 0; i < len(s); i++ {
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
		Message:  attrName + ": invalid AWS ARN \"" + value + "\" (" + diag + ")",
	}
}
