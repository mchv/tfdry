package checker

// VarType is a minimal type enum replacing go-cty for tfdry's needs.
//
// VarType represents the inferred type of a *caller-side* expression (local
// values, attribute values), used by E003 and E005. For *module-side*
// declared variable types — which are recursive (objects, lists, maps) —
// see [TypeSchema] in modules.go. The two types are used together in
// E006 module-input checking, where TypeSchema describes what a module
// declared and VarType describes what the caller passed.
type VarType int

const (
	TypeUnknown VarType = iota // unresolvable — skip checks
	TypeString
	TypeNumber
	TypeBool
	TypeObject // any non-scalar (object, list, map, set)
)

// IsScalar reports whether t is a primitive scalar type (string, number, bool).
// Object types and unknown types return false.
func (t VarType) IsScalar() bool {
	return t == TypeString || t == TypeNumber || t == TypeBool
}

// Label returns a human-readable name for t, used in violation messages.
// Returns "unknown" for unrecognised values.
func (t VarType) Label() string {
	switch t {
	case TypeString:
		return "string"
	case TypeNumber:
		return "number"
	case TypeBool:
		return "bool"
	case TypeObject:
		return "object"
	default:
		return "unknown"
	}
}
