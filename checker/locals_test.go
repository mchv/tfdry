package checker

import "testing"

// TestInferFuncReturnType covers every branch of the function-name → return-type
// table to catch silent regressions if the mapping is altered.
func TestInferFuncReturnType(t *testing.T) {
	cases := []struct {
		name string
		want VarType
	}{
		// String returns
		{"tostring", TypeString},
		{"format", TypeString},
		{"join", TypeString},
		{"lower", TypeString},
		{"upper", TypeString},
		{"trimspace", TypeString},
		{"replace", TypeString},
		{"substr", TypeString},
		// Number returns
		{"tonumber", TypeNumber},
		{"length", TypeNumber},
		{"index", TypeNumber},
		// Bool returns
		{"tobool", TypeBool},
		// Object returns
		{"tolist", TypeObject},
		{"toset", TypeObject},
		{"flatten", TypeObject},
		{"concat", TypeObject},
		{"setunion", TypeObject},
		{"tomap", TypeObject},
		{"merge", TypeObject},
		{"zipmap", TypeObject},
		// Default → Unknown
		{"unknown_func", TypeUnknown},
		{"", TypeUnknown},
	}
	for _, c := range cases {
		if got := inferFuncReturnType(c.name); got != c.want {
			t.Errorf("inferFuncReturnType(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
