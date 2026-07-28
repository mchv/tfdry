// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import "testing"

// TestS3Contexts_TableIntegrity keeps every evidence-backed context, its value
// kind, and its regression coverage in deliberate three-way sync.
func TestS3Contexts_TableIntegrity(t *testing.T) {
	t.Parallel()

	expected := map[s3Context]s3ValueKind{
		{blockType: "resource", resourceType: "aws_s3_bucket", attribute: s3BucketAttribute}:           s3GeneralPurposeBucketName,
		{blockType: "resource", resourceType: "aws_s3_directory_bucket", attribute: s3BucketAttribute}: s3DirectoryBucketName,
		{blockType: "resource", resourceType: "aws_s3_object", attribute: s3BucketAttribute}:           s3BucketNameOrAccessPointARN,
		{blockType: "resource", resourceType: "aws_s3_bucket_object", attribute: s3BucketAttribute}:    s3BucketNameOrAccessPointARN,
		{blockType: "data", resourceType: "aws_s3_object", attribute: s3BucketAttribute}:               s3BucketNameOrAccessPointARN,
		{blockType: "data", resourceType: "aws_s3_bucket", attribute: s3BucketAttribute}:               s3BucketNameOrAccessPointARN,
		{blockType: "resource", resourceType: "aws_s3_bucket_policy", attribute: s3BucketAttribute}:    s3ExistingBucketReference,
	}

	if got, want := len(s3Contexts), len(expected); got != want {
		t.Errorf("s3Contexts has %d entries, want %d", got, want)
	}
	for context, wantKind := range expected {
		gotKind, ok := s3Contexts[context]
		if !ok {
			t.Errorf("expected S3 context missing: %+v", context)
			continue
		}
		if gotKind != wantKind {
			t.Errorf("S3 context %+v has kind %d, want %d", context, gotKind, wantKind)
		}
	}
	for context := range s3Contexts {
		if _, ok := expected[context]; !ok {
			t.Errorf("unexpected S3 context: %+v — add evidence and regression coverage before updating this sentinel", context)
		}
	}
}

func TestS3BucketNameConstants(t *testing.T) {
	t.Parallel()

	if s3BucketNameMinLength != 3 {
		t.Errorf("s3BucketNameMinLength = %d, want 3", s3BucketNameMinLength)
	}
	if s3BucketNameMaxLength != 63 {
		t.Errorf("s3BucketNameMaxLength = %d, want 63", s3BucketNameMaxLength)
	}
	if s3DirectorySuffix != "--x-s3" {
		t.Errorf("s3DirectorySuffix = %q, want %q", s3DirectorySuffix, "--x-s3")
	}
}

func TestValidateS3DirectoryBucketName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name string
		want bool
	}{
		"availability zone": {name: "example--usw2-az1--x-s3", want: true},
		"local zone":        {name: "example--usw2-xxx-lz1--x-s3", want: true},
		"missing suffix":    {name: "plain-directory-bucket", want: false},
		"empty zone":        {name: "example----x-s3", want: false},
		"period":            {name: "example.logs--usw2-az1--x-s3", want: false},
		"uppercase":         {name: "Example--usw2-az1--x-s3", want: false},
	}

	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			t.Parallel()
			got, _ := validateS3DirectoryBucketName(test.name)
			if got != test.want {
				t.Errorf("validateS3DirectoryBucketName(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}
