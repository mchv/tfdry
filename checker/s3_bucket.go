// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ── E204: AWS S3 general-purpose bucket name grammar ────────────────────────
//
// E204 catches structural violations of AWS S3 general-purpose bucket
// naming rules. Rules validated (verified against the AWS S3 User
// Guide, docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html):
//
//  1. Length: 3-63 characters
//  2. Character set: lowercase letters (a-z), digits (0-9), period
//     (.), and hyphen (-) only
//  3. Must begin AND end with a letter or digit (not `.` or `-`)
//  4. No consecutive periods (`..`)
//  5. Must not be formatted as an IP address (four dot-separated
//     digit runs — e.g. 192.168.5.4)
//
// Deferred (v2 or by real-world signal):
//   - Reserved prefixes (`xn--`, `sthree-`, `amzn-s3-demo-`)
//   - Reserved suffixes (`-s3alias`, `--ol-s3`, `.mrap`, `--x-s3`,
//     `--table-s3`)
//   - `-an` suffix reserved for the account-regional-namespace format
//
// Design principles:
//
//   - Trigger surface: enumerated attribute names (`bucket`,
//     `bucket_name`) scoped to top-level `resource "aws_s3_*"` /
//     `data "aws_s3_*"` blocks. Non-S3 AWS resources (e.g.
//     aws_athena_workgroup.bucket) and non-AWS resources (e.g.
//     google_storage_bucket) are silently skipped — different
//     services have different naming rules.
//
//   - Direct attributes only in v1. `bucket` inside a nested block
//     of an aws_s3_* resource is NOT scanned. `resource` and `data`
//     appear only at top level in Terraform, so no recursion is
//     needed.
//
//   - Interpolation-aware: values containing `${...}` template
//     interpolation are skipped. Placeholder-composed validation
//     isn't meaningful here because S3 rules are pointwise (every
//     character must be valid), and the substituted content is
//     unknown at author time.
//
//   - Zero-alloc fast path: pure-literal values go through
//     TryLiteralString and validateS3BucketName without allocating
//     any []TemplatePart slice.

// s3BucketTriggers lists the attribute names that trigger E204 when
// they appear inside an aws_s3_* resource or data source. Kept small
// and enumerated (not a regex) to make additions a deliberate act
// and to avoid false positives on lookalike names.
//
// Attribute names verified against terraform-provider-aws docs:
//   - `bucket` — appears on aws_s3_bucket (creation), and all
//     aws_s3_bucket_* companion resources (aws_s3_bucket_policy,
//     aws_s3_bucket_versioning, ...) as a reference to the target
//   - `bucket_name` — appears on some data-plane resources
//     (aws_s3_bucket_object, aws_s3_object)
//
// Additions should cite the provider doc in the comment above.
var s3BucketTriggers = map[string]struct{}{
	"bucket":      {},
	"bucket_name": {},
}

// s3BucketPrefix is the resource-type prefix that triggers E204
// applicability. Any top-level `resource` or `data` block whose
// first label starts with this string is considered an S3 bucket
// scope. Kept as a constant so adding future S3 subtypes (all
// `aws_s3_*`) is automatic.
const s3BucketPrefix = "aws_s3_"

// S3 bucket name length bounds. Enforced up front by
// validateS3BucketName so grossly out-of-range inputs (a full URL,
// an accidentally-set long string) short-circuit before the byte
// loops run.
const (
	s3BucketNameMinLength = 3
	s3BucketNameMaxLength = 63
)

// checkS3BucketName runs E204 over a single parsed file, returning
// one Violation per finding. Called from Run() when E204 is enabled.
//
// Structure follows E210's flat top-level scan: `resource` and
// `data` blocks appear only at top level in Terraform, and v1
// examines only direct attributes of aws_s3_* blocks. No recursion.
func checkS3BucketName(f ParsedFile) []Violation {
	if f.Body == nil {
		return nil
	}
	var violations []Violation
	for _, block := range f.Body.Blocks {
		if block.Type != "resource" && block.Type != "data" {
			continue
		}
		if len(block.Labels) == 0 {
			continue
		}
		if !strings.HasPrefix(block.Labels[0], s3BucketPrefix) {
			continue
		}
		for _, attr := range block.Body.Attributes {
			if _, ok := s3BucketTriggers[attr.Name]; !ok {
				continue
			}
			checkS3BucketAttr(f.Name, attr, &violations)
		}
	}
	return violations
}

// checkS3BucketAttr validates a single bucket / bucket_name attribute.
// Non-template expressions (bare traversals, function calls) skip
// silently — statically-unresolvable references can't be validated
// as literals. Interpolated / templated values also skip; every S3
// rule is boundary-sensitive and a partial composed form gives no
// useful signal.
//
// Empty literal strings (bucket = "") are NOT skipped: an empty
// value violates the length rule (3-63) unambiguously and firing
// E204 with a clear "must be at least 3 characters" message is
// more useful than silence.
func checkS3BucketAttr(file string, attr *hclsyntax.Attribute, violations *[]Violation) {
	s, ok := TryLiteralString(attr.Expr)
	if !ok {
		return
	}
	valid, reason := validateS3BucketName(s)
	if valid {
		return
	}
	*violations = append(*violations, s3BucketViolation(file, attr.Expr.Range().Start.Line, attr.Name, s, reason))
}

// validateS3BucketName reports whether s is a well-formed AWS S3
// general-purpose bucket name and, if not, returns a short reason
// suitable for the diagnostic message. Zero-alloc.
//
// The rule order below is deliberate: cheap length filter first,
// then boundary-character checks, then a single pass over the
// interior for character-set + consecutive-dot validation, and
// finally the IP-shape check (which walks the string once more but
// only for names that pass all earlier checks).
func validateS3BucketName(s string) (valid bool, reason string) {
	n := len(s)
	if n < s3BucketNameMinLength {
		return false, "must be at least 3 characters"
	}
	if n > s3BucketNameMaxLength {
		return false, "must be at most 63 characters"
	}
	if !isS3BucketBoundaryByte(s[0]) {
		return false, "must begin with a lowercase letter or digit"
	}
	if !isS3BucketBoundaryByte(s[n-1]) {
		return false, "must end with a lowercase letter or digit"
	}
	// Single pass: character set + consecutive dot detection.
	for i := 0; i < n; i++ {
		c := s[i]
		if !isS3BucketByte(c) {
			return false, "must contain only lowercase letters, digits, periods, and hyphens"
		}
		if c == '.' && i+1 < n && s[i+1] == '.' {
			return false, "must not contain consecutive periods"
		}
	}
	if isIPv4Shape(s) {
		return false, "must not be formatted as an IP address"
	}
	return true, ""
}

// isS3BucketByte reports whether c is in the S3 bucket-name character
// set: lowercase letter, digit, period, or hyphen.
func isS3BucketByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '.' || c == '-':
		return true
	}
	return false
}

// isS3BucketBoundaryByte reports whether c is valid as the first or
// last character of an S3 bucket name (lowercase letter or digit —
// not `.` or `-`, unlike the interior).
func isS3BucketBoundaryByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	return false
}

// isIPv4Shape reports whether s is shaped like an IPv4 address: four
// dot-separated runs of digits, no other characters. Intentionally
// permissive on octet value (matches any digit run, not just 0-255)
// — AWS forbids anything "formatted as" an IP address, not just
// well-formed ones. A name like `256.256.256.256` still trips this
// rule at deployment time.
func isIPv4Shape(s string) bool {
	dots := 0
	digitsSinceDot := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.':
			if digitsSinceDot == 0 {
				return false // consecutive dots or leading dot — not IP-shape
			}
			dots++
			digitsSinceDot = 0
		case c >= '0' && c <= '9':
			digitsSinceDot++
		default:
			return false // any non-digit non-dot rules out IP shape
		}
	}
	return dots == 3 && digitsSinceDot > 0
}

// s3BucketViolation packages a Violation for E204. The message names
// the attribute, the offending literal, and the specific rule that
// was violated so the diagnostic doubles as the fix.
func s3BucketViolation(file string, line int, attrName, value, reason string) Violation {
	return Violation{
		Code:     "E204",
		Severity: "error",
		File:     file,
		Line:     line,
		Message:  attrName + `: invalid S3 bucket name "` + value + `" — ` + reason,
	}
}
