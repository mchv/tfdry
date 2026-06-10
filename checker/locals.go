package checker

import (
	"strconv"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// LocalInfo holds the resolved type (TypeUnknown if unresolvable) and source location.
type LocalInfo struct {
	Type VarType
	Expr hclsyntax.Expression // original expression, for structural kind resolution
	File string
	Line int
}

// BuildLocalsMap walks all parsed files and returns:
//   - a map of local name -> LocalInfo (all defined locals, type may be TypeUnknown)
//   - E002 violations for duplicate definitions
func BuildLocalsMap(files []ParsedFile) (map[string]LocalInfo, []Violation) {
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
	locals := make(map[string]LocalInfo, hint)
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
				locals[name] = LocalInfo{
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
func inferFuncReturnType(name string) VarType {
	switch name {
	case "tostring", "format", "join", "lower", "upper", "trimspace", "replace", "substr":
		return TypeString
	case "tonumber", "length", "index":
		return TypeNumber
	case "tobool":
		return TypeBool
	case "tolist", "toset", "flatten", "concat", "setunion",
		"tomap", "merge", "zipmap":
		return TypeObject
	default:
		return TypeUnknown
	}
}
