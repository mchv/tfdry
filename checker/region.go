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
	"ap-east-2":      {}, // Taipei (announced August 2025)
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
	"ap-southeast-6": {}, // New Zealand (Auckland — announced August 2025)
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
	// Entry point starts with awsContext=false; the walker sets it to true
	// as it descends into provider "aws", resource "aws_*", or data "aws_*"
	// blocks. Top-level attributes (which shouldn't appear anyway in
	// well-formed Terraform) are silently ignored.
	walkRegionBlocks(f.Body, f.Name, false, &violations)
	return violations
}

// walkRegionBlocks descends into a body's attributes and child blocks,
// carrying an AWS-context flag. E201 fires only on attributes inside a
// known AWS scope — `provider "aws"`, `resource "aws_*"`, or `data
// "aws_*"` — because the `region` attribute name is shared across many
// providers (GCP, DigitalOcean, Cloudflare Workers, ...). A default-error
// finding on a non-AWS region literal would be a false positive.
//
// Skipped block types:
//   - variable — bodies opaque (Tier-3 exclusion, mirrors E101/E202)
//   - module   — schema unknown; can't assume AWS applicability
//
// AWS context propagates to nested blocks (e.g. `destination { ... }`
// inside an `aws_s3_bucket_replication_configuration` still carries
// AWS scope).
func walkRegionBlocks(body *hclsyntax.Body, file string, awsContext bool, violations *[]Violation) {
	if body == nil {
		return
	}
	if awsContext {
		for _, attr := range body.Attributes {
			if regionTriggers[attr.Name] != cidrShapeScalar {
				continue
			}
			checkRegionScalar(file, attr, violations)
		}
	}
	for _, block := range body.Blocks {
		switch block.Type {
		case "variable", "module":
			continue
		case "provider", "resource", "data":
			walkRegionBlocks(block.Body, file, isAWSBlock(block.Type, block.Labels), violations)
		default:
			// Nested block inside a resource/data body (e.g.
			// `destination { ... }`) OR a top-level Terraform block like
			// `locals`, `output`, `terraform`, `check`, `moved`. The
			// nested case inherits AWS context; the top-level case has
			// awsContext=false already, which correctly excludes locals
			// and outputs from AWS-scoped validation.
			walkRegionBlocks(block.Body, file, awsContext, violations)
		}
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
// allocating any []TemplatePart slice. Using validateRegion (rather than
// a direct awsRegions map lookup) keeps the length-filter fast-reject
// on the E201 hot path — a single consolidated validation entry-point
// per the DRY principle.
func checkRegionScalar(file string, attr *hclsyntax.Attribute, violations *[]Violation) {
	s, ok := TryLiteralString(attr.Expr)
	if !ok || s == "" {
		return
	}
	if validateRegion(s) {
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
