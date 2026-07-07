// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

// Family describes a group of related check codes that share a numeric range.
// The Code field is the reserved family header — never assigned to an actual
// check. Concrete checks belong to a family via their Family field (see
// CheckInfo) which stores the corresponding header code.
//
// Range scheme (see docs/families.md for the long-form rationale):
//
//	E000-E099 / W001-W099  Language mechanics (existing)
//	E100-E199 / W100-W199  Network
//	E200-E399 / W200-W399  AWS
//	E400-E599 / W400-W599  GCP
//	E600-E799 / W600-W799  Azure
//	E800-E899 / W800-W899  Kubernetes / container
//	E900-E999 / W900-W999  Reserved
//	E1000+                 Overflow (out-of-band, if ever needed)
//
// The hundreds digit uniquely identifies the family, which lets a maintainer
// (or an automated review dashboard) bucket findings by family without a
// lookup table. See issue #23 for the design discussion.
type Family struct {
	// Code is the family header (E000, E100, E200, ...). Reserved — never
	// assigned to an actual check. Serves as the family identifier that
	// each CheckInfo references via its Family field.
	Code string
	// Severity mirrors the E/W prefix of the family header. Individual
	// checks within an E-family can still be warnings if their own Code
	// carries the W prefix, but the family-level severity captures the
	// dominant intent.
	Severity string
	// Name is the short human-readable family name ("Network", "AWS", ...).
	Name string
	// Description explains what the family covers and what belongs in it.
	// Kept short — full family docs live in docs/families.md.
	Description string
}

// AllFamilies returns the canonical ordered list of check families.
// Family headers are documented reservations; they are not check codes and
// are rejected by ValidateCheckCodes.
func AllFamilies() []Family { return allFamiliesList }

// allFamiliesList is the single source of truth for family metadata.
// Adding a new family: append here and set the Family field on the
// corresponding CheckInfo entries in allChecksList. Ranges must not overlap.
var allFamiliesList = []Family{
	{
		Code:        "E000",
		Severity:    "error",
		Name:        "Language mechanics",
		Description: "HCL syntax, locals, module inputs, formatting. Provider-agnostic checks that catch structural issues in Terraform sources.",
	},
	{
		Code:        "E100",
		Severity:    "error",
		Name:        "Network",
		Description: "Protocol-level literals — CIDR blocks (IPv4/IPv6), IP addresses, port ranges, protocol names. Applies across all cloud providers.",
	},
	// E200 (AWS), E400 (GCP), E600 (Azure), E800 (Kubernetes) are reserved
	// by the range scheme but not yet materialised as Family entries.
	// Add them when their first concrete check lands.
}

// familyHeaderCodes indexes family headers so ValidateCheckCodes can reject
// attempts to use a family header as a check code.
var familyHeaderCodes = func() map[string]struct{} {
	m := make(map[string]struct{}, len(allFamiliesList))
	for _, f := range allFamiliesList {
		m[f.Code] = struct{}{}
	}
	return m
}()
