// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

// Package checker implements tfdry's static analysis checks for Terraform
// files. It parses .tf files via hashicorp/hcl, builds a per-directory map
// of locals and module schemas, and runs a configurable set of checks
// (E001-E009, E101, E201-E203, E210, W001, W009) without requiring `terraform init` or
// any provider downloads.
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
	{Code: "E009", Severity: "error", Summary: "Invalid Terraform scope root in expression", Family: "E000"},
	{Code: "W001", Severity: "warning", Summary: "Local defined but never used", Family: "E000"},
	{Code: "W009", Severity: "warning", Summary: "Unfamiliar Terraform scope root (may be typo or unrecognised construct)", Family: "E000"},
	{Code: "E101", Severity: "error", Summary: "Invalid CIDR block literal", Family: "E100"},
	{Code: "E201", Severity: "error", Summary: "Invalid AWS region", Family: "E200"},
	{Code: "E202", Severity: "error", Summary: "Invalid AWS account ID", Family: "E200"},
	{Code: "E203", Severity: "error", Summary: "Malformed ARN structure", Family: "E200"},
	{Code: "E210", Severity: "error", Summary: "AWS resource block-name typo (singular/plural)", Family: "E200"},
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
		walkExpressions(f.Body, nil, func(expr hclsyntax.Expression, iterators map[string]struct{}) {
			switch e := expr.(type) {
			case *hclsyntax.ScopeTraversalExpr:
				// E009 / W009: scope-root validation. Runs on every
				// ScopeTraversalExpr — bare traversals (`bucket =
				// vars.name`) as well as interpolated ones
				// (`"prefix-${vars.env}"`) — so scope-root issues
				// are caught in any attribute, not just CIDR-triggering
				// ones. Iterators map carries dynamic-block-content and
				// for-expression scope from the enclosing walker.
				//
				// Severity split (see scopeRootViolation): known typos
				// (`vars`, `locals`, ...) fire E009 error; genuinely
				// uncertain roots fire W009 warning. The validator
				// runs when EITHER code is enabled; the resulting
				// violation's code determines final emission.
				if checks.Enabled("E009") || checks.Enabled("W009") {
					if diag := ValidateScopeRoot(e, iterators); diag != nil {
						v := scopeRootViolation(f.Name, diag)
						if checks.Enabled(v.Code) {
							violations = append(violations, v)
						}
					}
				}
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
			violations = append(violations, checkCIDR(f, checks)...)
		}
		if checks.Enabled("E201") {
			violations = append(violations, checkRegion(f)...)
		}
		if checks.Enabled("E202") {
			violations = append(violations, checkAccountID(f)...)
		}
		if checks.Enabled("E203") {
			violations = append(violations, checkARN(f)...)
		}
		if checks.Enabled("E210") {
			violations = append(violations, checkBlockTypo(f)...)
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

// walkExpressions calls fn for every expression in a body, recursively,
// with iterators giving the set of iterator-variable names in scope at
// that point.
//
// The iterators parameter is passed to fn on every callback so
// scope-aware checks (E009 / W009) can consult it. Callers that don't
// do scope-aware work can pass nil at the top-level call and ignore
// the parameter in their callback.
//
// Two scope-introducing constructs are handled:
//
//   - Dynamic blocks: `dynamic "X" { content { ... } }` binds X (or
//     the value of `iterator = <name>`) inside content{}. The walker
//     visits the block's own attributes (for_each, iterator, labels,
//     ...) with the OUTER scope — those expressions are evaluated
//     before the iterator is bound — then descends into content{}
//     with the iterator name added to a fresh copy of the iterators
//     map. Non-content sub-blocks (unusual — Terraform's grammar
//     allows only `content`) are visited with the outer scope. See
//     walkDynamicBlock.
//
//   - For-expressions: `[for K, V in COLL : ...]` and `{for K, V in
//     COLL : ... => ...}` bind KeyVar and ValVar inside KeyExpr,
//     ValExpr, and CondExpr — but NOT inside CollExpr. HCL synthesises
//     ChildScope wrapper nodes around KeyExpr/ValExpr/CondExpr during
//     Walk; scopedExprWalker pushes/pops iterator names as those
//     wrappers enter/exit.
//
// The walker is re-entrant and side-effect free: each augmented
// iterator map is cloned rather than mutated, and the scope stack is
// saved on push and restored on pop.
func walkExpressions(body *hclsyntax.Body, iterators map[string]struct{}, fn func(hclsyntax.Expression, map[string]struct{})) {
	if body == nil {
		return
	}
	// One walker per body — reuse across all attributes in this body
	// to avoid a heap allocation per attribute. Safe because
	// hclsyntax.Walk guarantees symmetric Enter/Exit calls, so the
	// stack is empty when Walk returns; the `[:0]` reset is defensive.
	// Recursive walkExpressions and walkDynamicBlock calls still
	// allocate their own walker (they're bounded by nesting depth,
	// not attribute count).
	w := &scopedExprWalker{fn: fn}
	for _, attr := range body.Attributes {
		w.iterators = iterators
		w.stack = w.stack[:0]
		//nolint:errcheck // Callback returns no diagnostics; we don't use hclsyntax.Walk's aggregated diagnostics.
		hclsyntax.Walk(attr.Expr, w)
	}
	for _, block := range body.Blocks {
		if block.Type == "dynamic" && len(block.Labels) == 1 {
			walkDynamicBlock(block, iterators, fn)
			continue
		}
		walkExpressions(block.Body, iterators, fn)
	}
}

// scopedExprWalker is an hclsyntax.Walker that tracks iterator-variable
// scope introduced by for-expressions. HCL wraps a ForExpr's KeyExpr /
// ValExpr / CondExpr in ChildScope nodes during Walk (see
// hclsyntax.ForExpr.walkChildNodes); this walker responds to those
// wrappers by pushing the local names onto its stack. Non-wrapper
// expression nodes just forward to fn with the current scope.
type scopedExprWalker struct {
	iterators map[string]struct{}   // current in-scope iterator names
	stack     []map[string]struct{} // saved states, one per active ChildScope frame
	fn        func(hclsyntax.Expression, map[string]struct{})
}

// Enter implements hclsyntax.Walker. Pushes iterator names on
// ChildScope frames (introduced by ForExpr) and forwards Expression
// nodes to the caller's fn with the current scope.
func (w *scopedExprWalker) Enter(n hclsyntax.Node) hcl.Diagnostics {
	switch tn := n.(type) {
	case hclsyntax.ChildScope:
		// HCL synthesises ChildScope around ForExpr's KeyExpr /
		// ValExpr / CondExpr (never around CollExpr, which is
		// evaluated in the outer scope). Push a fresh iterators map
		// with the ForExpr's KeyVar and ValVar added.
		w.stack = append(w.stack, w.iterators)
		w.iterators = cloneIteratorsBulk(w.iterators, tn.LocalNames)
	case hclsyntax.Expression:
		w.fn(tn, w.iterators)
	}
	return nil
}

// Exit implements hclsyntax.Walker. Pops the scope stack when leaving
// a ChildScope frame, restoring the iterator set to what it was on
// entry.
func (w *scopedExprWalker) Exit(n hclsyntax.Node) hcl.Diagnostics {
	if _, ok := n.(hclsyntax.ChildScope); ok {
		top := len(w.stack) - 1
		w.iterators = w.stack[top]
		// Nil the popped slot before truncating so the map it referenced
		// can be garbage-collected immediately. Without this, the
		// underlying array (which survives the [:top] slice down) keeps
		// the map reachable until either the array is grown past this
		// index or the walker itself is discarded. Bounded but real
		// retention — matters more now that the walker is reused across
		// multiple attributes in walkExpressions and walkDynamicBlock.
		w.stack[top] = nil
		w.stack = w.stack[:top]
	}
	return nil
}

// cloneIteratorsBulk returns a new map containing every entry in
// iterators plus every key in add. Nil-safe on both arguments. Mirrors
// cloneIterators (single-name) but avoids repeated allocations when
// pushing multi-name scopes like a ForExpr's key+value pair.
func cloneIteratorsBulk(iterators, add map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(iterators)+len(add))
	for k := range iterators {
		out[k] = struct{}{}
	}
	for k := range add {
		out[k] = struct{}{}
	}
	return out
}

// walkDynamicBlock handles a `dynamic "X" { ... }` block, applying the
// scoping rule described in walkExpressions. Extracted to keep the
// main walker readable.
func walkDynamicBlock(block *hclsyntax.Block, iterators map[string]struct{}, fn func(hclsyntax.Expression, map[string]struct{})) {
	if block == nil || block.Body == nil {
		return
	}
	iterName := dynamicIteratorName(block)

	// Visit dynamic-level attributes (for_each, labels, iterator) with
	// the OUTER scope — they are evaluated before the iterator is bound.
	// Notably: the `iterator = <name>` attribute itself parses as a
	// ScopeTraversalExpr but is a declaration, not a reference; we skip
	// it here so E009 doesn't flag the iterator name being introduced.
	//
	// Uses the same scoped Walker as walkExpressions so ForExpr scope
	// (e.g. `for_each = [for x in var.list : x.id]`) is honoured inside
	// the dynamic-block's own attribute expressions. One walker per
	// invocation, reused across attributes (matches walkExpressions).
	w := &scopedExprWalker{fn: fn}
	for _, attr := range block.Body.Attributes {
		if attr.Name == "iterator" {
			continue
		}
		w.iterators = iterators
		w.stack = w.stack[:0]
		//nolint:errcheck // Callback returns no diagnostics.
		hclsyntax.Walk(attr.Expr, w)
	}

	// Descend into sub-blocks. content{} sees the iterator; others don't
	// (Terraform's grammar only allows content, but we tolerate other
	// shapes without crashing).
	augmented := cloneIterators(iterators, iterName)
	for _, sub := range block.Body.Blocks {
		if sub.Type == "content" {
			walkExpressions(sub.Body, augmented, fn)
		} else {
			walkExpressions(sub.Body, iterators, fn)
		}
	}
}

// dynamicIteratorName returns the iterator name for a `dynamic "X"` block:
// the value of `iterator = <name>` if present (a bare-identifier
// ScopeTraversalExpr), otherwise the block label X. Returns the empty
// string only if the block has no label — a malformed input that the
// caller should already have skipped via the `len(block.Labels) == 1`
// guard.
func dynamicIteratorName(block *hclsyntax.Block) string {
	if attr, ok := block.Body.Attributes["iterator"]; ok {
		if trav, ok := attr.Expr.(*hclsyntax.ScopeTraversalExpr); ok && len(trav.Traversal) == 1 {
			if root, ok := trav.Traversal[0].(hcl.TraverseRoot); ok {
				return root.Name
			}
		}
	}
	if len(block.Labels) == 1 {
		return block.Labels[0]
	}
	return ""
}

// cloneIterators returns a new map containing every entry in iterators
// plus name. Nil-safe: a nil input yields a fresh single-entry map.
// Cloning is intentional — the walker is otherwise side-effect free,
// and mutating a shared map across recursive calls would corrupt outer
// scope on return.
func cloneIterators(iterators map[string]struct{}, name string) map[string]struct{} {
	out := make(map[string]struct{}, len(iterators)+1)
	for k := range iterators {
		out[k] = struct{}{}
	}
	if name != "" {
		out[name] = struct{}{}
	}
	return out
}
