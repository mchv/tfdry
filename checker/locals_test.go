// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

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
		// More well-known string-returning functions.
		{"file", TypeString},
		{"templatefile", TypeString},
		{"jsonencode", TypeString},
		{"base64encode", TypeString},
		{"base64decode", TypeString},
		{"uuid", TypeString},
		{"timestamp", TypeString},
		// Number returns
		{"tonumber", TypeNumber},
		{"length", TypeNumber},
		{"index", TypeNumber},
		// Bool returns
		{"tobool", TypeBool},
		// Object returns (TypeObject is "any non-scalar" in our type system,
		// so list-returning functions like keys/values map here too).
		{"tolist", TypeObject},
		{"toset", TypeObject},
		{"flatten", TypeObject},
		{"concat", TypeObject},
		{"setunion", TypeObject},
		{"tomap", TypeObject},
		{"merge", TypeObject},
		{"zipmap", TypeObject},
		// keys/values return lists → TypeObject in our minimal type system.
		{"keys", TypeObject},
		{"values", TypeObject},
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
