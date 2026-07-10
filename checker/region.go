// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ── E201: AWS region validation ─────────────────────────────────────────────
//
// Attribute triggers are enumerated (not regex) — same discipline as E101.
// A misplaced entry produces user-visible false positives that erode trust
// in the whole check. Adding an attribute here is a deliberate act.
//
// Tier 1 (initial): the sole `region` attribute name, which appears on:
//   - `provider "aws" { region = "..." }` — the canonical binding
//   - `resource "aws_s3_bucket_replication_configuration"` destination blocks
//   - a handful of similar cross-region resources
//
// Interpolation-aware validation (deferred): regions have no natural
// segment-boundary characters (a region code is a single indivisible token
// like `us-east-1`), so placeholder-composed validation of templated
// regions would be guesswork. Interpolated / templated values are silently
// skipped — matches E101's "no useful signal → skip" policy.
//
// Zero-alloc hot path: length-filter fast rejection, then a single
// `map[string]struct{}` lookup. The map is precomputed at package init
// via a var initialiser (not a function call), so lookup is constant-time
// with no allocation.

// awsRegions is the enumerated set of AWS region codes across all three
// partitions:
//
//   - aws (commercial) — the standard partition
//   - aws-us-gov — GovCloud
//   - aws-cn — China (operated by Sinnet in Beijing and NWCD in Ningxia)
//
// Sourced from the AWS documentation:
// https://docs.aws.amazon.com/general/latest/gr/rande.html
//
// Locked list — additions require a documented AWS region announcement,
// not a hunch. New regions are announced infrequently (~1-2 per year) and
// take months to appear in customer accounts, so the maintenance burden
// of a hardcoded list is minimal compared with the false-positive risk of
// a permissive regex.
var awsRegions = map[string]struct{}{
	// Commercial (aws partition) — US
	"us-east-1": {}, // N. Virginia
	"us-east-2": {}, // Ohio
	"us-west-1": {}, // N. California
	"us-west-2": {}, // Oregon
	// Commercial (aws partition) — Africa
	"af-south-1": {}, // Cape Town
	// Commercial (aws partition) — Asia Pacific
	"ap-east-1":      {}, // Hong Kong
	"ap-south-1":     {}, // Mumbai
	"ap-south-2":     {}, // Hyderabad
	"ap-northeast-1": {}, // Tokyo
	"ap-northeast-2": {}, // Seoul
	"ap-northeast-3": {}, // Osaka
	"ap-southeast-1": {}, // Singapore
	"ap-southeast-2": {}, // Sydney
	"ap-southeast-3": {}, // Jakarta
	"ap-southeast-4": {}, // Melbourne
	"ap-southeast-5": {}, // Malaysia
	"ap-southeast-7": {}, // Thailand
	// Commercial (aws partition) — Canada
	"ca-central-1": {}, // Central
	"ca-west-1":    {}, // Calgary
	// Commercial (aws partition) — Europe
	"eu-central-1": {}, // Frankfurt
	"eu-central-2": {}, // Zurich
	"eu-west-1":    {}, // Ireland
	"eu-west-2":    {}, // London
	"eu-west-3":    {}, // Paris
	"eu-north-1":   {}, // Stockholm
	"eu-south-1":   {}, // Milan
	"eu-south-2":   {}, // Spain
	// Commercial (aws partition) — Israel
	"il-central-1": {}, // Tel Aviv
	// Commercial (aws partition) — Mexico
	"mx-central-1": {}, // Central
	// Commercial (aws partition) — Middle East
	"me-central-1": {}, // UAE
	"me-south-1":   {}, // Bahrain
	// Commercial (aws partition) — South America
	"sa-east-1": {}, // São Paulo
	// GovCloud (aws-us-gov partition)
	"us-gov-east-1": {},
	"us-gov-west-1": {},
	// China (aws-cn partition)
	"cn-north-1":     {},
	"cn-northwest-1": {},
}

// regionTriggers is the enumerated attribute-name → shape table for E201.
// Tier 1 only: the sole `region` attribute. Additions require a documented
// use case (real Terraform module referencing the attribute with a
// region-code literal) rather than a heuristic guess.
//
// Uses the same shape enum as cidrTriggers so a future list-typed region
// attribute (e.g. `replica_regions`) can be added without restructuring.
var regionTriggers = map[string]cidrShape{
	"region": cidrShapeScalar,
}

// checkRegion runs E201 over a single parsed file, returning one Violation
// per finding. Called from Run() when E201 is enabled.
func checkRegion(f ParsedFile) []Violation {
	var violations []Violation
	walkRegionBlocks(f.Body, f.Name, &violations)
	return violations
}

// walkRegionBlocks descends into a body's attributes and child blocks,
// mirroring walkCIDRBlocks. Skips `variable` blocks entirely because
// their defaults are Tier-3-excluded (mirrors the CIDR policy).
func walkRegionBlocks(body *hclsyntax.Body, file string, violations *[]Violation) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		if regionTriggers[attr.Name] != cidrShapeScalar {
			continue
		}
		checkRegionScalar(file, attr, violations)
	}
	for _, block := range body.Blocks {
		if block.Type == "variable" {
			continue
		}
		walkRegionBlocks(block.Body, file, violations)
	}
}

// checkRegionScalar validates a single-string region attribute. Non-template
// expressions (bare traversals, numeric literals, function calls) are
// silently skipped — statically-unresolvable references can't be validated
// as region codes. Interpolated / templated values are also skipped;
// composed-form validation of regions is not meaningful because a region
// code is an indivisible token with no segment boundaries.
//
// Fast path: pure-literal values (the overwhelming majority of hardcoded
// regions) go through TryLiteralString and validateRegion directly, without
// allocating any []TemplatePart slice.
func checkRegionScalar(file string, attr *hclsyntax.Attribute, violations *[]Violation) {
	s, ok := TryLiteralString(attr.Expr)
	if !ok || s == "" {
		return
	}
	if _, valid := awsRegions[s]; valid {
		return
	}
	*violations = append(*violations, regionViolation(file, attr.Expr.Range().Start.Line, attr.Name, s))
}

// Region length bounds. Enforced up front by validateRegion so grossly
// out-of-range inputs (a full URL, a paragraph of text, an empty string
// that slipped past the caller filter) short-circuit before hitting the
// map lookup's hash + compare. Computed from the current awsRegions set:
//
//   - Min: 9 bytes  ("us-east-1", "us-west-1", "eu-west-1", "sa-east-1")
//   - Max: 14 bytes ("ap-northeast-3", "ap-southeast-4", "cn-northwest-1")
//
// A new AWS region with a shorter or longer code would require updating
// these bounds; the awsRegions comment references the same documentation
// URL that would introduce such a region.
const (
	regionMinLength = 9
	regionMaxLength = 14
)

// validateRegion reports whether s is a known AWS region code. Fast path:
// a length filter shortcuts out-of-range inputs in a single comparison
// before the map lookup runs (hash + key compare on Go maps costs several
// ns even for miss lookups). Zero-alloc.
func validateRegion(s string) bool {
	if len(s) < regionMinLength || len(s) > regionMaxLength {
		return false
	}
	_, ok := awsRegions[s]
	return ok
}

// regionViolation packages a Violation for E201. Extracted for consistency
// with cidrViolation's pattern.
func regionViolation(file string, line int, attrName, value string) Violation {
	return Violation{
		Code:     "E201",
		Severity: "error",
		File:     file,
		Line:     line,
		Message:  attrName + ": invalid AWS region \"" + value + "\"",
	}
}
