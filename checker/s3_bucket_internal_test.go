// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import "testing"

// TestS3BucketTriggers_TableIntegrity is a drift alarm for the
// s3BucketTriggers set in s3_bucket.go. Runs in the internal checker
// package so it can access the unexported set directly. Follows
// the same pattern as TestBlockTypos_TableIntegrity for E210:
//
// When adding a new trigger attribute name:
//
//  1. Add it to s3BucketTriggers in s3_bucket.go, citing the
//     terraform-provider-aws doc that uses the name.
//  2. Add positive + negative tests in checker/checks_e204_test.go
//     confirming E204 fires for the wrong form and stays silent for
//     the correct form.
//  3. Add it to expectedTriggers below.
//
// The three-way sync (set, per-entry tests, this sentinel) is
// deliberate friction against uncurated growth.
func TestS3BucketTriggers_TableIntegrity(t *testing.T) {
	expectedTriggers := map[string]struct{}{
		"bucket":      {},
		"bucket_name": {},
	}

	if got, want := len(s3BucketTriggers), len(expectedTriggers); got != want {
		t.Errorf("s3BucketTriggers has %d entries, want %d — update expectedTriggers to match (or update the trigger set if the count is wrong)", got, want)
	}

	for name := range expectedTriggers {
		if _, ok := s3BucketTriggers[name]; !ok {
			t.Errorf("expected trigger %q missing from s3BucketTriggers — add it back to the set or remove it from expectedTriggers", name)
		}
	}
	for name := range s3BucketTriggers {
		if _, ok := expectedTriggers[name]; !ok {
			t.Errorf("unexpected trigger %q in s3BucketTriggers — add positive+negative tests in checks_e204_test.go AND add it to expectedTriggers here", name)
		}
	}
}

// TestS3BucketNameLength_Bounds locks the min/max length constants
// against silent drift. AWS S3 general-purpose bucket names are
// documented as 3-63 characters — changing this without a matching
// doc update would silently accept or reject names that AWS accepts
// or rejects.
func TestS3BucketNameLength_Bounds(t *testing.T) {
	if s3BucketNameMinLength != 3 {
		t.Errorf("s3BucketNameMinLength = %d, want 3 (AWS S3 rule)", s3BucketNameMinLength)
	}
	if s3BucketNameMaxLength != 63 {
		t.Errorf("s3BucketNameMaxLength = %d, want 63 (AWS S3 rule)", s3BucketNameMaxLength)
	}
}
