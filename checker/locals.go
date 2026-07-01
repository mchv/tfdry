// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"strconv"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// localInfo holds the resolved type (TypeUnknown if unresolvable) and source location.
type localInfo struct {
	Type VarType
	Expr hclsyntax.Expression // original expression, for structural kind resolution
	File string
	Line int
}

// buildLocalsMap walks all parsed files and returns:
//   - a map of local name -> localInfo (all defined locals, type may be TypeUnknown)
//   - E002 violations for duplicate definitions
func buildLocalsMap(files []ParsedFile) (map[string]localInfo, []Violation) {
	// Pre-size the map: count attributes in all locals blocks in one O(blocks)
	// pass so the underlying hashmap allocates the right number of buckets up
	// front. Without the hint, Go's map grows by doubling its bucket array as
	// it fills, paying a rehash cost at each grow phase. The pre-pass is
	// cheap (no hashing, no allocation) and trades one extra walk for zero
	// growth phases on the main insert loop.
	hint := 0
	for _, f := range files {
		for _, block := range f.Body.Blocks {
			if block.Type == "locals" {
				hint += len(block.Body.Attributes)
			}
		}
	}
	locals := make(map[string]localInfo, hint)
	var violations []Violation

	for _, f := range files {
		for _, block := range f.Body.Blocks {
			if block.Type != "locals" {
				continue
			}
			for name, attr := range block.Body.Attributes {
				if existing, ok := locals[name]; ok {
					violations = append(violations, Violation{
						Code:     "E002",
						Severity: "error",
						File:     f.Name,
						Line:     attr.NameRange.Start.Line,
						Message:  "duplicate local \"" + name + "\", first defined at " + existing.File + ":" + strconv.Itoa(existing.Line),
					})
					continue
				}
				locals[name] = localInfo{
					Type: inferExprType(attr.Expr),
					Expr: attr.Expr,
					File: f.Name,
					Line: attr.NameRange.Start.Line,
				}
			}
		}
	}
	return locals, violations
}

// inferExprType returns the VarType of an expression, or TypeUnknown if not statically resolvable.
func inferExprType(expr hclsyntax.Expression) VarType {
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		ty := e.Val.Type()
		switch ty.FriendlyName() {
		case "string":
			return TypeString
		case "number":
			return TypeNumber
		case "bool":
			return TypeBool
		}
		return TypeUnknown
	case *hclsyntax.TemplateExpr:
		return TypeString
	case *hclsyntax.TemplateWrapExpr:
		return TypeString
	case *hclsyntax.ObjectConsExpr:
		return TypeObject
	case *hclsyntax.TupleConsExpr:
		return TypeObject
	case *hclsyntax.ConditionalExpr:
		t := inferExprType(e.TrueResult)
		if t != TypeUnknown && t == inferExprType(e.FalseResult) {
			return t
		}
		return TypeUnknown
	case *hclsyntax.FunctionCallExpr:
		return inferFuncReturnType(e.Name)
	default:
		return TypeUnknown
	}
}

// inferFuncReturnType returns the return type for well-known Terraform functions.
//
// The list intentionally covers only the functions tfdry sees most often in
// practice (locals, module inputs). Adding more pure-typed functions here
// directly improves E004 (non-scalar in interpolation) and E006 (module
// input type mismatch) precision by reducing TypeUnknown returns. Functions
// that would return TypeUnknown for "depends on the input" reasons (e.g.
// `lookup`, `coalesce`, `try`) are intentionally omitted.
func inferFuncReturnType(name string) VarType {
	switch name {
	// Pure scalar-returning string functions.
	case "tostring", "format", "join", "lower", "upper", "trimspace", "replace", "substr",
		// Well-known string-returning functions used widely in terraform
		// code (file/template I/O, encoding, identifiers).
		"file", "templatefile", "jsonencode",
		"base64encode", "base64decode",
		"uuid", "timestamp":
		return TypeString
	case "tonumber", "length", "index":
		return TypeNumber
	case "tobool":
		return TypeBool
	// Non-scalar (object/list/map/set) returns. TypeObject is the catch-all
	// non-scalar in our minimal type system, so list-returning functions
	// (keys/values) map here too.
	case "tolist", "toset", "flatten", "concat", "setunion",
		"tomap", "merge", "zipmap",
		// keys/values return lists — non-scalar.
		"keys", "values":
		return TypeObject
	default:
		return TypeUnknown
	}
}
