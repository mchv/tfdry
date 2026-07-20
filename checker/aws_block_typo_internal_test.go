// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import "testing"

// TestBlockTypos_TableIntegrity is a drift alarm for the curated
// blockTypos table in aws_block_typo.go. Runs in the internal
// checker package so it can access the unexported blockTypos map
// directly — the previous version in checker_test could only check
// a hardcoded local slice, which meant table growth or shrinkage in
// aws_block_typo.go was invisible to the sentinel.
//
// When adding a new entry to blockTypos:
//
//  1. Add the (resource_type, wrong_block, correct_block) triple in
//     aws_block_typo.go, citing the provider docs above it.
//  2. Add positive + negative tests in checker/checks_e210_test.go
//     confirming E210 fires for the wrong form and stays silent for
//     the correct form.
//  3. Add the resource type to expectedResources below.
//
// The three-way sync (table, per-entry tests, this sentinel) is
// deliberate friction against uncurated growth: an entry can't ship
// without visible test coverage and an updated expected-key set.
func TestBlockTypos_TableIntegrity(t *testing.T) {
	// The resources every entry in blockTypos should cover. Kept
	// alphabetical within groups to make additions easy to review.
	expectedResources := map[string]struct{}{
		// QuickSight — six resources expecting `permissions` (plural).
		"aws_quicksight_analysis":  {},
		"aws_quicksight_dashboard": {},
		"aws_quicksight_data_set":  {},
		"aws_quicksight_folder":    {},
		"aws_quicksight_template":  {},
		"aws_quicksight_theme":     {},
		// QuickSight outlier expecting `permission` (singular).
		"aws_quicksight_data_source": {},
		// Non-QuickSight singular blocks (repeatable, high-frequency plurals).
		"aws_iam_policy_document": {},
		"aws_lb_listener_rule":    {},
		"aws_wafv2_web_acl":       {},
	}

	if got, want := len(blockTypos), len(expectedResources); got != want {
		t.Errorf("blockTypos has %d entries, want %d — update expectedResources to match (or update the table if the count is wrong)", got, want)
	}

	for r := range expectedResources {
		if _, ok := blockTypos[r]; !ok {
			t.Errorf("expected resource %q missing from blockTypos — add it back to the table or remove it from expectedResources", r)
		}
	}
	for r := range blockTypos {
		if _, ok := expectedResources[r]; !ok {
			t.Errorf("unexpected resource %q in blockTypos — add positive+negative tests in checks_e210_test.go AND add it to expectedResources here", r)
		}
	}
}
