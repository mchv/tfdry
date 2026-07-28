// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ── E204: AWS S3 bucket name grammar ────────────────────────────────────────
//
// E204 validates literal bucket declarations only where the containing
// Terraform block establishes the name's semantics. A provider-wide
// `aws_s3_*` prefix is not sufficient: some `bucket` arguments accept access
// point ARNs, existing buckets may use grandfathered pre-2018 names, and
// directory buckets have their own required format.
//
// General-purpose names are checked for:
//
//  1. Length: 3-63 characters
//  2. Character set: lowercase letters (a-z), digits (0-9), period
//     (.), and hyphen (-) only
//  3. Must begin and end with a letter or digit
//  4. No consecutive periods (`..`)
//  5. Must not be formatted as an IP address
//
// Directory-bucket names use the same length and boundary limits, allow only
// lowercase letters, digits, and hyphens, and must have the documented
// `<base-name>--<zone-id>--x-s3` format.
//
// Reserved general-purpose prefixes and suffixes remain deferred until the
// semantic context is complete enough to add them without rejecting another
// S3 bucket subtype.

type s3ValueKind uint8

const (
	s3GeneralPurposeBucketName s3ValueKind = iota + 1
	s3DirectoryBucketName
	s3BucketNameOrAccessPointARN
	s3ExistingBucketReference
)

// s3Context identifies one provider argument whose S3 value semantics have
// been checked against current terraform-provider-aws documentation or source.
// Contexts absent from s3Contexts fail closed: E204 stays silent rather than
// applying general-purpose creation rules to a newly added provider subtype.
type s3Context struct {
	blockType    string
	resourceType string
	attribute    string
}

const s3BucketAttribute = "bucket"

// s3Contexts is deliberately explicit. Each entry needs positive and negative
// coverage in checks_e204_test.go and must be mirrored by the integrity test.
//
// The object resource, deprecated bucket-object resource, and object data
// source document `bucket` as a bucket name or S3 access-point ARN. The bucket
// data source implementation also handles ARNs. Those contexts are skipped by
// E204 because a non-ARN literal may still be a grandfathered existing name.
// Bucket policies can target either general-purpose or directory buckets and
// are likewise reference-only.
var s3Contexts = map[s3Context]s3ValueKind{
	{blockType: "resource", resourceType: "aws_s3_bucket", attribute: s3BucketAttribute}:           s3GeneralPurposeBucketName,
	{blockType: "resource", resourceType: "aws_s3_directory_bucket", attribute: s3BucketAttribute}: s3DirectoryBucketName,
	{blockType: "resource", resourceType: "aws_s3_object", attribute: s3BucketAttribute}:           s3BucketNameOrAccessPointARN,
	{blockType: "resource", resourceType: "aws_s3_bucket_object", attribute: s3BucketAttribute}:    s3BucketNameOrAccessPointARN,
	{blockType: "data", resourceType: "aws_s3_object", attribute: s3BucketAttribute}:               s3BucketNameOrAccessPointARN,
	{blockType: "data", resourceType: "aws_s3_bucket", attribute: s3BucketAttribute}:               s3BucketNameOrAccessPointARN,
	{blockType: "resource", resourceType: "aws_s3_bucket_policy", attribute: s3BucketAttribute}:    s3ExistingBucketReference,
}

const (
	s3ResourcePrefix      = "aws_s3_"
	s3BucketNameMinLength = 3
	s3BucketNameMaxLength = 63
	s3DirectorySuffix     = "--x-s3"
)

// checkS3BucketName runs E204 over a single parsed file. The prefix test is
// only a fast rejection for unrelated resources; s3Contexts remains the sole
// source of applicability. Only direct `bucket` attributes in explicitly
// classified top-level resource or data blocks are considered.
func checkS3BucketName(f ParsedFile) []Violation {
	if f.Body == nil {
		return nil
	}

	var violations []Violation
	for _, block := range f.Body.Blocks {
		if (block.Type != "resource" && block.Type != "data") || len(block.Labels) == 0 {
			continue
		}
		if !strings.HasPrefix(block.Labels[0], s3ResourcePrefix) {
			continue
		}

		context := s3Context{
			blockType:    block.Type,
			resourceType: block.Labels[0],
			attribute:    s3BucketAttribute,
		}
		kind, ok := s3Contexts[context]
		if !ok {
			continue
		}

		attr, ok := block.Body.Attributes[context.attribute]
		if !ok {
			continue
		}
		checkS3BucketAttr(f.Name, attr, kind, &violations)
	}
	return violations
}

// checkS3BucketAttr validates one classified bucket attribute. References and
// interpolated values are statically unresolved and therefore skipped.
// Existing-bucket and name-or-ARN contexts are also skipped: strict modern
// naming rules would reject valid access-point ARNs and grandfathered names.
func checkS3BucketAttr(file string, attr *hclsyntax.Attribute, kind s3ValueKind, violations *[]Violation) {
	if kind == s3BucketNameOrAccessPointARN || kind == s3ExistingBucketReference {
		return
	}

	s, ok := TryLiteralString(attr.Expr)
	if !ok {
		return
	}

	var valid bool
	var reason string
	switch kind {
	case s3GeneralPurposeBucketName:
		valid, reason = validateS3BucketName(s)
	case s3DirectoryBucketName:
		valid, reason = validateS3DirectoryBucketName(s)
	case s3BucketNameOrAccessPointARN, s3ExistingBucketReference:
		return
	default:
		return
	}
	if valid {
		return
	}

	*violations = append(*violations, s3BucketViolation(file, attr.Expr.Range().Start.Line, attr.Name, s, reason))
}

// s3BucketByteTable is a precomputed lookup for the general-purpose
// bucket-name character set.
var s3BucketByteTable = func() [256]bool {
	var table [256]bool
	for c := byte('a'); c <= 'z'; c++ {
		table[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		table[c] = true
	}
	table['.'] = true
	table['-'] = true
	return table
}()

// s3DirectoryBucketByteTable excludes periods, which directory-bucket names
// do not permit.
var s3DirectoryBucketByteTable = func() [256]bool {
	var table [256]bool
	for c := byte('a'); c <= 'z'; c++ {
		table[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		table[c] = true
	}
	table['-'] = true
	return table
}()

// s3BucketBoundaryTable permits lowercase letters and digits at name
// boundaries.
var s3BucketBoundaryTable = func() [256]bool {
	var table [256]bool
	for c := byte('a'); c <= 'z'; c++ {
		table[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		table[c] = true
	}
	return table
}()

// validateS3BucketName reports whether s follows the selected
// general-purpose S3 bucket-name rules. Zero-allocation.
func validateS3BucketName(s string) (valid bool, reason string) {
	n := len(s)
	if n < s3BucketNameMinLength {
		return false, "must be at least 3 characters"
	}
	if n > s3BucketNameMaxLength {
		return false, "must be at most 63 characters"
	}
	if !s3BucketBoundaryTable[s[0]] {
		return false, "must begin with a lowercase letter or digit"
	}
	if !s3BucketBoundaryTable[s[n-1]] {
		return false, "must end with a lowercase letter or digit"
	}

	dotCount := 0
	seenNonDigitNonDot := false
	for i := 0; i < n; i++ {
		c := s[i]
		if !s3BucketByteTable[c] {
			return false, "must contain only lowercase letters, digits, periods, and hyphens"
		}
		if c == '.' {
			if i+1 < n && s[i+1] == '.' {
				return false, "must not contain consecutive periods"
			}
			dotCount++
		} else if c < '0' || c > '9' {
			seenNonDigitNonDot = true
		}
	}
	if !seenNonDigitNonDot && dotCount == 3 {
		return false, "must not be formatted as an IP address"
	}
	return true, ""
}

// validateS3DirectoryBucketName reports whether s follows the documented
// directory-bucket character and `<base-name>--<zone-id>--x-s3` rules.
// Zone IDs are not enumerated because their availability is account- and
// region-dependent; this check validates the stable structural contract.
func validateS3DirectoryBucketName(s string) (valid bool, reason string) {
	n := len(s)
	if n < s3BucketNameMinLength {
		return false, "must be at least 3 characters"
	}
	if n > s3BucketNameMaxLength {
		return false, "must be at most 63 characters"
	}
	if !s3BucketBoundaryTable[s[0]] {
		return false, "must begin with a lowercase letter or digit"
	}
	if !s3BucketBoundaryTable[s[n-1]] {
		return false, "must end with a lowercase letter or digit"
	}

	for i := 0; i < n; i++ {
		if !s3DirectoryBucketByteTable[s[i]] {
			return false, "must contain only lowercase letters, digits, and hyphens"
		}
	}

	if !strings.HasSuffix(s, s3DirectorySuffix) {
		return false, "must use the format <base-name>--<zone-id>--x-s3"
	}
	withoutSuffix := s[:n-len(s3DirectorySuffix)]
	separator := strings.LastIndex(withoutSuffix, "--")
	if separator <= 0 || separator+2 == len(withoutSuffix) {
		return false, "must use the format <base-name>--<zone-id>--x-s3"
	}

	return true, ""
}

// s3BucketViolation packages an E204 violation with the offending literal and
// the rule it violated.
func s3BucketViolation(file string, line int, attrName, value, reason string) Violation {
	return Violation{
		Code:     "E204",
		Severity: "error",
		File:     file,
		Line:     line,
		Message:  attrName + `: invalid S3 bucket name "` + value + `" — ` + reason,
	}
}
