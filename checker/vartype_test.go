package checker

import "testing"

// TestVarType_Label covers every VarType.Label() branch, including the
// default fallthrough for unrecognised values.
func TestVarType_Label(t *testing.T) {
	cases := []struct {
		t    VarType
		want string
	}{
		{TypeString, "string"},
		{TypeNumber, "number"},
		{TypeBool, "bool"},
		{TypeObject, "object"},
		{TypeUnknown, "unknown"},
		{VarType(99), "unknown"}, // default branch
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.t.Label(); got != tc.want {
				t.Errorf("VarType(%v).Label() = %q, want %q", tc.t, got, tc.want)
			}
		})
	}
}

// TestVarType_IsScalar covers every VarType.IsScalar() branch.
func TestVarType_IsScalar(t *testing.T) {
	cases := []struct {
		t    VarType
		want bool
	}{
		{TypeString, true},
		{TypeNumber, true},
		{TypeBool, true},
		{TypeObject, false},
		{TypeUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.t.Label(), func(t *testing.T) {
			if got := tc.t.IsScalar(); got != tc.want {
				t.Errorf("VarType(%v).IsScalar() = %v, want %v", tc.t, got, tc.want)
			}
		})
	}
}
