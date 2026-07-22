// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// ── E204: AWS S3 bucket name grammar ────────────────────────────────────────
//
// E204 catches structural violations of AWS S3 general-purpose bucket
// naming rules. Rules covered (verified against
// docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html):
//
//  1. Length: 3-63 characters
//  2. Character set: lowercase letters (a-z), digits (0-9), period,
//     hyphen only
//  3. Must begin AND end with letter or digit (not `.` or `-`)
//  4. No consecutive periods (`..`)
//  5. Must not be formatted as an IP address (e.g. 192.168.5.4)
//
// Not covered in v1 (deferred):
//   - Reserved prefixes (xn--, sthree-, amzn-s3-demo-)
//   - Reserved suffixes (-s3alias, --ol-s3, .mrap, --x-s3, --table-s3)
//   - Account-regional-namespace -an suffix pattern
//
// Triggers: `bucket` and `bucket_name` attributes inside any `aws_s3_*`
// resource or data source. Non-AWS resources with a matching attribute
// name (e.g. google_storage_bucket) are silently skipped.
//
// Interpolation-aware: interpolated / templated values are silently
// skipped (matches E101/E201's policy — no useful signal from partial
// composed forms when the rules are all boundary-sensitive).

// ── Rule 1: Length 3-63 ─────────────────────────────────────────────────────

func TestE204_TooShort_TwoChars_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "ab"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("2-char bucket name must fire E204 (min length 3), got: %v", codes(vs))
	}
}

func TestE204_TooLong_64Chars_Fires(t *testing.T) {
	// 64 chars = one over the 63-char maximum.
	name := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if len(name) != 64 {
		t.Fatalf("test setup bug: expected 64 chars, got %d", len(name))
	}
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "` + name + `"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("64-char bucket name must fire E204 (max length 63), got: %v", codes(vs))
	}
}

func TestE204_MinLength_ThreeChars_NoFire(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "abc"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("3-char bucket 'abc' is valid — must NOT fire E204, got: %v", codes(vs))
	}
}

func TestE204_MaxLength_SixtyThreeChars_NoFire(t *testing.T) {
	// 63 chars = exact max.
	name := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if len(name) != 63 {
		t.Fatalf("test setup bug: expected 63 chars, got %d", len(name))
	}
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "` + name + `"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("63-char bucket name is valid — must NOT fire E204, got: %v", codes(vs))
	}
}

// ── Rule 2: Character set [a-z0-9.-] ────────────────────────────────────────

func TestE204_Uppercase_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "MyBucket"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("uppercase in bucket name must fire E204, got: %v", codes(vs))
	}
}

func TestE204_Underscore_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "my_bucket"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("underscore in bucket name must fire E204, got: %v", codes(vs))
	}
}

func TestE204_Space_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "my bucket"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("space in bucket name must fire E204, got: %v", codes(vs))
	}
}

func TestE204_ValidLowercaseWithHyphenAndDot_NoFire(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "my-app.logs.bucket-2026"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("valid bucket with hyphen and dot — must NOT fire E204, got: %v", codes(vs))
	}
}

// ── Rule 3: First/last must be letter or digit ──────────────────────────────

func TestE204_StartsWithDot_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = ".mybucket"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("bucket name starting with '.' must fire E204, got: %v", codes(vs))
	}
}

func TestE204_StartsWithHyphen_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "-mybucket"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("bucket name starting with '-' must fire E204, got: %v", codes(vs))
	}
}

func TestE204_EndsWithDot_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "mybucket."
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("bucket name ending with '.' must fire E204, got: %v", codes(vs))
	}
}

func TestE204_EndsWithHyphen_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "mybucket-"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("bucket name ending with '-' must fire E204, got: %v", codes(vs))
	}
}

func TestE204_StartsAndEndsWithDigits_NoFire(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "1abc2"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("digit-bounded bucket name is valid — must NOT fire E204, got: %v", codes(vs))
	}
}

// ── Rule 4: No consecutive periods ──────────────────────────────────────────

func TestE204_ConsecutivePeriods_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "example..com"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("consecutive periods must fire E204, got: %v", codes(vs))
	}
}

func TestE204_SinglePeriods_NoFire(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "www.example.com"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("single periods (www.example.com) are valid — must NOT fire E204, got: %v", codes(vs))
	}
}

// ── Rule 5: Not IP-shaped ───────────────────────────────────────────────────

func TestE204_IPv4Shape_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "192.168.5.4"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("IP-address-shaped bucket name (192.168.5.4) must fire E204, got: %v", codes(vs))
	}
}

func TestE204_LooksLikeIPButNotAllOctets_NoFire(t *testing.T) {
	// "192.168.1" has only 3 octets — not IP-shaped, should be valid.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "192.168.1"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("three-dot-separated numbers (192.168.1) is NOT IP-shaped — must NOT fire E204, got: %v", codes(vs))
	}
}

func TestE204_NumbersInBucketName_NoFire(t *testing.T) {
	// Numbers mixed with letters/hyphens are fine — not IP-shaped.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "logs-2026-01"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("numbers-in-name is valid — must NOT fire E204, got: %v", codes(vs))
	}
}

// ── AWS-scope discipline: only aws_s3_* triggers ────────────────────────────

// TestE204_NonS3AWSResource_NoFire verifies E204 doesn't fire on a
// `bucket` attribute inside a non-S3 AWS resource. The rules only
// apply to actual S3 buckets — a different AWS service might use
// `bucket` for something entirely different.
func TestE204_NonS3AWSResource_NoFire(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_athena_workgroup" "example" {
  bucket = "MyUppercaseThing"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("bucket inside non-S3 AWS resource must NOT fire E204, got: %v", codes(vs))
	}
}

// TestE204_NonAWSResource_NoFire verifies non-AWS resources with a
// `bucket` attribute (e.g. google_storage_bucket) are silently skipped.
// GCP bucket names have DIFFERENT rules — E204 must not enforce S3
// rules on non-S3 buckets.
func TestE204_NonAWSResource_NoFire(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "google_storage_bucket" "example" {
  bucket = "My_GCP_Bucket_With_Underscores"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("bucket inside non-AWS resource must NOT fire E204, got: %v", codes(vs))
	}
}

// TestE204_S3BucketDataSource_Fires verifies E204 fires on `bucket`
// inside an `aws_s3_bucket` data source too — a bucket name that
// can't exist can't be referenced either.
func TestE204_S3BucketDataSource_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
data "aws_s3_bucket" "example" {
  bucket = "INVALID"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("data source aws_s3_bucket with invalid bucket name must fire E204, got: %v", codes(vs))
	}
}

// TestE204_S3BucketPolicyBucket_Fires verifies E204 fires on the
// referenced `bucket` attribute in an aws_s3_bucket_policy resource.
// Names must still be valid to be referenced.
func TestE204_S3BucketPolicyBucket_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket_policy" "example" {
  bucket = "INVALID_UPPER"
  policy = "{}"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("aws_s3_bucket_policy.bucket with invalid name must fire E204, got: %v", codes(vs))
	}
}

// ── bucket_name attribute alias ─────────────────────────────────────────────

// TestE204_BucketName_Attribute_Fires verifies E204 also fires on
// `bucket_name` (some resources use this alternative attribute name).
func TestE204_BucketName_Attribute_Fires(t *testing.T) {
	// aws_s3control_bucket doesn't exist as a real resource type,
	// but if there were one that used bucket_name inside aws_s3_*,
	// this shape captures the intent. Real example: some data-plane
	// resources use bucket_name.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket_object" "example" {
  bucket_name = "INVALID_UPPER"
  key         = "foo.txt"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("bucket_name attribute in aws_s3_* resource must fire E204, got: %v", codes(vs))
	}
}

// ── Interpolation and reference handling ────────────────────────────────────

// TestE204_ReferenceExpression_Skipped verifies E204 skips
// scope-traversal references (bucket = aws_s3_bucket.mine.id or
// bucket = local.name). The check operates only on literal strings.
func TestE204_ReferenceExpression_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket_versioning" "example" {
  bucket = aws_s3_bucket.mine.id
  versioning_configuration {
    status = "Enabled"
  }
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("reference expression must NOT fire E204 (only literals validated), got: %v", codes(vs))
	}
}

// TestE204_InterpolatedString_Skipped verifies E204 skips values
// containing template interpolation (e.g. "${var.env}-bucket").
// Placeholder-composed validation isn't meaningful for bucket names
// because the character-set rule applies pointwise and the
// substituted value is unknown.
func TestE204_InterpolatedString_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "${var.env}-my-bucket"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("interpolated bucket name must NOT fire E204 (skipped), got: %v", codes(vs))
	}
}

// TestE204_VariableDefault_Skipped verifies E204 does not fire inside
// a variable block's default value — matches E101/E201/E202 policy
// (variable defaults are Tier-3-excluded across the grammar family).
func TestE204_VariableDefault_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
variable "bucket_name" {
  default = "INVALID_DEFAULT"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("variable default should not fire E204, got: %v", codes(vs))
	}
}

// ── --checks disabled ───────────────────────────────────────────────────────

// TestE204_Disabled_NoViolation verifies that a real violation is not
// reported when E204 is disabled via --checks.
func TestE204_Disabled_NoViolation(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = "INVALID"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	enabled := checker.CheckSet{"E101": {}} // deliberately not E204
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	if hasCode(vs, "E204") {
		t.Fatalf("E204 must not fire when disabled via --checks, got: %v", codes(vs))
	}
}
