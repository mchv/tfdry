// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

// Package checker implements tfdry's static analysis checks for Terraform
// files. It parses .tf files via hashicorp/hcl, builds a per-directory map
// of locals and module schemas, and runs a configurable set of checks
// (E001-E008, W001) without requiring `terraform init` or any provider
// downloads.
package checker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Violation is a single check finding.
type Violation struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	File     string `json:"file"`
	// Line is emitted uniformly for every violation — 0 is the sentinel
	// for file-level violations (E000, E008) where no specific source
	// line applies. Previously used json:"line,omitempty" which stripped
	// the field for file-level codes and broke consumer schema
	// expectations; see issue #19.
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// CheckSet is the set of check codes to run. Empty means all.
type CheckSet map[string]struct{}

// Enabled reports whether a check code should run.
func (cs CheckSet) Enabled(code string) bool {
	if len(cs) == 0 {
		return true
	}
	_, ok := cs[code]
	return ok
}

// CheckInfo describes a single check.
type CheckInfo struct {
	Code     string
	Severity string
	Summary  string
	// Family is the family header code this check belongs to (see families.go).
	// The hundreds digit of Code implies the family (E101 → E100, E205 → E200),
	// but Family is stored explicitly so consumers don't need to reconstruct
	// the mapping. Backfilled for E001-E008 and W001 to E000.
	Family string
}

// allChecksList is the canonical ordered list of all checks.
// Single source of truth — used by AllChecks, knownCodes, and ValidateCheckCodes.
var allChecksList = []CheckInfo{
	{Code: "E001", Severity: "error", Summary: "Invalid HCL syntax", Family: "E000"},
	{Code: "E002", Severity: "error", Summary: "Duplicate local definition", Family: "E000"},
	{Code: "E003", Severity: "error", Summary: "Reference to undefined local", Family: "E000"},
	{Code: "E004", Severity: "error", Summary: "Non-scalar local used in string interpolation", Family: "E000"},
	{Code: "E005", Severity: "error", Summary: "count and for_each used together on same resource/data/module block", Family: "E000"},
	{Code: "E006", Severity: "error", Summary: "Local module input type mismatch", Family: "E000"},
	{Code: "E007", Severity: "error", Summary: "Unknown local module input key", Family: "E000"},
	{Code: "E008", Severity: "error", Summary: "File not formatted (run tfdry --fix or terraform fmt)", Family: "E000"},
	{Code: "W001", Severity: "warning", Summary: "Local defined but never used", Family: "E000"},
	{Code: "E101", Severity: "error", Summary: "Invalid CIDR block literal", Family: "E100"},
}

// AllChecks returns the canonical ordered list of all checks.
func AllChecks() []CheckInfo { return allChecksList }

var knownCodes = func() map[string]struct{} {
	m := make(map[string]struct{}, len(allChecksList))
	for _, c := range allChecksList {
		m[c.Code] = struct{}{}
	}
	return m
}()

// ValidateCheckCodes returns an error if any code is not a known check code.
func ValidateCheckCodes(codes []string) error {
	for _, c := range codes {
		if c == "" {
			return fmt.Errorf("check code must not be empty")
		}
		if _, ok := knownCodes[c]; !ok {
			// Reserved family headers (E000, E100, ...) are documented
			// range identifiers, not check codes. Surface that
			// distinction so a user typing --checks=E100 gets a pointed
			// message instead of the generic "unknown code".
			if _, isFamily := familyHeaderCodes[c]; isFamily {
				return familyHeaderError(c)
			}
			return fmt.Errorf("unknown check code %q — run 'tfdry describe' for valid codes", c)
		}
	}
	return nil
}

// familyHeaderError formats the error returned when a user supplies a family
// header (e.g. --checks=E100) where a check code was expected.
//
// If the header has materialised checks, the message names the family and
// cites the first concrete check as an example (both derived at call time
// from allFamiliesList and allChecksList so the example stays accurate as
// families grow). If the header is reserved-but-empty — the range scheme
// documents it but no check has landed yet — a different message reflects
// that state without inventing a fake example.
func familyHeaderError(header string) error {
	var familyName string
	for _, f := range allFamiliesList {
		if f.Code == header {
			familyName = f.Name
			break
		}
	}
	for _, c := range allChecksList {
		if c.Family == header {
			if familyName != "" {
				return fmt.Errorf("%q is the %s family header — pick a specific check in that range (e.g. %s)", header, familyName, c.Code)
			}
			return fmt.Errorf("%q is a family header — pick a specific check in that range (e.g. %s)", header, c.Code)
		}
	}
	// Header is recognised (in familyHeaderCodes) but has no concrete
	// checks — i.e. a reserved family from reservedFamilyHeaders.
	return fmt.Errorf("%q is a reserved family header — no checks are registered in this family yet", header)
}

// Run executes all checks on the parsed files and returns all violations
// plus a non-nil error if ctx was cancelled mid-pass (violations may be
// partial in that case).
//
// dir is the directory that was parsed (needed for E006 local module
// resolution). ctx is checked once before any work and once per file
// at the top of the iteration — checks themselves run to completion
// per file because per-expression cancellation would noticeably slow
// the common case and the per-file granularity is enough to bound
// worst-case latency.
func Run(ctx context.Context, files []ParsedFile, checks CheckSet, dir string) ([]Violation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	locals, dupViolations := buildLocalsMap(files)

	var violations []Violation

	if checks.Enabled("E002") {
		violations = append(violations, dupViolations...)
	}

	// Single-pass: collect used locals + expression violations in one walk per file.
	usedLocals := make(map[string]struct{}, len(locals))
	// Cache for module variable schemas — avoids re-reading the same module dir.
	moduleCache := make(map[string]map[string]typeSchema)

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return violations, err
		}
		walkExpressions(f.Body, func(expr hclsyntax.Expression) {
			switch e := expr.(type) {
			case *hclsyntax.ScopeTraversalExpr:
				if len(e.Traversal) < 2 || e.Traversal.RootName() != "local" {
					return
				}
				attr, ok := e.Traversal[1].(hcl.TraverseAttr)
				if !ok {
					return
				}
				usedLocals[attr.Name] = struct{}{}
				if checks.Enabled("E003") {
					if _, defined := locals[attr.Name]; !defined {
						violations = append(violations, Violation{
							Code:     "E003",
							Severity: "error",
							File:     f.Name,
							Line:     e.SrcRange.Start.Line,
							Message:  "reference to undefined local \"" + attr.Name + "\"",
						})
					}
				}

			case *hclsyntax.TemplateExpr:
				if !checks.Enabled("E004") {
					return
				}
				for _, part := range e.Parts {
					if v := typeMismatchViolation(f.Name, part, locals); v != nil {
						violations = append(violations, *v)
					}
				}

			case *hclsyntax.TemplateWrapExpr:
				if !checks.Enabled("E004") {
					return
				}
				if v := typeMismatchViolation(f.Name, e.Wrapped, locals); v != nil {
					violations = append(violations, *v)
				}
			}
		})

		if checks.Enabled("E005") {
			violations = append(violations, checkCountForEach(f)...)
		}
		if checks.Enabled("E006") || checks.Enabled("E007") {
			violations = append(violations, checkModuleInputs(f, dir, locals, checks, moduleCache)...)
		}
		if checks.Enabled("E101") {
			violations = append(violations, checkCIDR(f)...)
		}
	}

	if checks.Enabled("E008") {
		fmtViolations, err := CheckFormat(ctx, files)
		// Append BEFORE checking err — CheckFormat may have collected
		// partial fmt violations before cancellation fired, and dropping
		// them would silently undermine the partial-results contract
		// documented on Run.
		violations = append(violations, fmtViolations...)
		if err != nil {
			return violations, err
		}
	}

	if checks.Enabled("W001") {
		for name, li := range locals {
			if _, used := usedLocals[name]; !used {
				violations = append(violations, Violation{
					Code:     "W001",
					Severity: "warning",
					File:     li.File,
					Line:     li.Line,
					Message:  "local \"" + name + "\" is defined but never used",
				})
			}
		}
	}

	sort.SliceStable(violations, func(i, j int) bool {
		a, b := violations[i], violations[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})

	return violations, nil
}

func typeMismatchViolation(file string, expr hclsyntax.Expression, locals map[string]localInfo) *Violation {
	ref, ok := expr.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(ref.Traversal) < 2 || ref.Traversal.RootName() != "local" {
		return nil
	}
	// local.foo.bar — attribute access on the object; leaf type is unknown, skip.
	if len(ref.Traversal) > 2 {
		return nil
	}
	attr, ok := ref.Traversal[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	li, defined := locals[attr.Name]
	if !defined || li.Type == TypeUnknown || li.Type.IsScalar() {
		return nil
	}
	return &Violation{
		Code:     "E004",
		Severity: "error",
		File:     file,
		Line:     ref.SrcRange.Start.Line,
		Message:  "local." + attr.Name + " is " + li.Type.Label() + ", used where string expected in interpolation",
	}
}

// checkCountForEach finds resource/data/module blocks with both count and
// for_each. Terraform supports both meta-arguments individually on all three
// block types but rejects using both simultaneously.
func checkCountForEach(f ParsedFile) []Violation {
	var violations []Violation
	for _, block := range f.Body.Blocks {
		if block.Type != "resource" && block.Type != "data" && block.Type != "module" {
			continue
		}
		_, hasCount := block.Body.Attributes["count"]
		_, hasForEach := block.Body.Attributes["for_each"]
		if hasCount && hasForEach {
			violations = append(violations, Violation{
				Code:     "E005",
				Severity: "error",
				File:     f.Name,
				Line:     block.OpenBraceRange.Start.Line,
				Message:  block.Type + " \"" + strings.Join(block.Labels, ".") + "\" uses both count and for_each",
			})
		}
	}
	return violations
}

// walkExpressions calls fn for every expression in a body, recursively.
func walkExpressions(body *hclsyntax.Body, fn func(hclsyntax.Expression)) {
	for _, attr := range body.Attributes {
		hclsyntax.VisitAll(attr.Expr, func(node hclsyntax.Node) hcl.Diagnostics { //nolint
			if expr, ok := node.(hclsyntax.Expression); ok {
				fn(expr)
			}
			return nil
		})
	}
	for _, block := range body.Blocks {
		walkExpressions(block.Body, fn)
	}
}
