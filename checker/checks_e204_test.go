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
// E204 validates S3 bucket literals only when an explicit Terraform context
// establishes the value kind. General-purpose declarations and directory
// bucket declarations have separate validators. Existing-bucket references
// and arguments that also accept access-point ARNs are skipped because strict
// modern naming rules would reject legal values.
//
// General-purpose rules covered (verified against
// docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html):
//
//  1. Length: 3-63 characters
//  2. Character set: lowercase letters (a-z), digits (0-9), period,
//     hyphen only
//  3. Must begin and end with a letter or digit
//  4. No consecutive periods (`..`)
//  5. Must not be formatted as an IP address
//
// Directory-bucket declarations additionally require the documented
// `<base-name>--<zone-id>--x-s3` shape and disallow periods.
//
// Interpolated values and references are silently skipped because their final
// strings cannot be resolved without provider evaluation.

// ── Rule 1: Length 3-63 ─────────────────────────────────────────────────────

// TestE204_EmptyString_Fires verifies that an empty literal `bucket = ""`
// fires E204 — the length rule (3-63) applies just as it does to any
// other under-length name. The check must not skip empty literals as a
// "no signal" case; empty is an unambiguous rule violation.
func TestE204_EmptyString_Fires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "example" {
  bucket = ""
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("empty bucket name must fire E204 (length rule), got: %v", codes(vs))
	}
}

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

// ── AWS scope discipline ───────────────────────────────────────────────────

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

// ── Semantic contexts: creation, references, and ARN alternatives ──────────

func TestE204_AccessPointARNContexts_NoFire(t *testing.T) {
	t.Parallel()

	const accessPointARN = "arn:aws:s3:eu-west-1:123456789012:accesspoint/build-artefacts"
	tests := map[string]string{
		"object resource": `
resource "aws_s3_object" "artifact" {
  bucket  = "` + accessPointARN + `"
  key     = "release.zip"
  content = "payload"
}
`,
		"deprecated bucket object resource": `
resource "aws_s3_bucket_object" "artifact" {
  bucket  = "` + accessPointARN + `"
  key     = "release.zip"
  content = "payload"
}
`,
		"object data source": `
data "aws_s3_object" "artifact" {
  bucket = "` + accessPointARN + `"
  key    = "release.zip"
}
`,
		"bucket data source": `
data "aws_s3_bucket" "artifact" {
  bucket = "` + accessPointARN + `"
}
`,
	}

	for name, tf := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{"main.tf": tf})
			if hasCode(vs, "E204") {
				t.Fatalf("documented access-point ARN must not be classified as a bucket name, got: %v", codes(vs))
			}
		})
	}
}

func TestE204_LegacyExistingBucketReferences_NoFire(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"bucket data source": `
data "aws_s3_bucket" "legacy" {
  bucket = "Legacy_Bucket"
}
`,
		"bucket policy": `
resource "aws_s3_bucket_policy" "legacy" {
  bucket = "Legacy_Bucket"
  policy = "{}"
}
`,
		"bucket versioning": `
resource "aws_s3_bucket_versioning" "legacy" {
  bucket = "Legacy_Bucket"
  versioning_configuration {
    status = "Enabled"
  }
}
`,
	}

	for name, tf := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{"main.tf": tf})
			if hasCode(vs, "E204") {
				t.Fatalf("grandfathered existing bucket reference must not fire E204, got: %v", codes(vs))
			}
		})
	}
}

func TestE204_DirectoryBucketName_Valid_NoFire(t *testing.T) {
	t.Parallel()

	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_directory_bucket" "example" {
  bucket = "example--usw2-az1--x-s3"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("valid directory-bucket name must not fire E204, got: %v", codes(vs))
	}
}

func TestE204_DirectoryBucketName_MissingSuffix_Fires(t *testing.T) {
	t.Parallel()

	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_directory_bucket" "example" {
  bucket = "plain-directory-bucket"
}
`,
	})
	if !hasCode(vs, "E204") {
		t.Fatalf("directory-bucket name without --<zone-id>--x-s3 must fire E204, got: %v", codes(vs))
	}
}

func TestE204_DirectoryBucketPolicyReference_NoFire(t *testing.T) {
	t.Parallel()

	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket_policy" "example" {
  bucket = "example--usw2-az1--x-s3"
  policy = "{}"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("directory-bucket policy reference must not fire E204, got: %v", codes(vs))
	}
}

func TestE204_UnsupportedBucketNameAttribute_NoFire(t *testing.T) {
	t.Parallel()

	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket_object" "example" {
  bucket_name = "INVALID_UPPER"
  key         = "foo.txt"
}
`,
	})
	if hasCode(vs, "E204") {
		t.Fatalf("unsupported bucket_name attribute is outside E204's evidence-backed surface, got: %v", codes(vs))
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
