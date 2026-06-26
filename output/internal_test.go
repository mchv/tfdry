// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"math"
	"testing"
)

// TestHumanPreGrow_CapsAndOverflowGuard exercises the size calculation used
// by WriteHuman to pre-grow its buffer. Crafted to catch the buffer-overflow regression:
// a previous form `len(r.Violations)*128 + 64` could overflow on
// pathologically large counts and pass a negative argument to
// bytes.Buffer.Grow, which panics. The helper now caps the result and
// detects multiplication wrap-around explicitly.
func TestHumanPreGrow_CapsAndOverflowGuard(t *testing.T) {
	const (
		summary = 64
		cap16MB = 16 << 20
	)
	cases := []struct {
		name string
		n    int
		want int
	}{
		{"zero violations → just summary", 0, summary},
		{"one violation → 128 + 64", 1, 128 + summary},
		{"ten violations", 10, 10*128 + summary},
		// 16MB / 128 = 131072 — the boundary above which we cap.
		{"just below cap", 131000, 131000*128 + summary},
		{"at cap boundary", 131072, cap16MB}, // 131072*128 = 16MB, +64 > 16MB → cap
		{"well above cap", 1_000_000, cap16MB},
		// MaxInt: would wrap on multiply. Must return cap, not panic.
		{"MaxInt → cap (no panic, no negative)", math.MaxInt, cap16MB},
		// Negative input (defensive — shouldn't happen but mustn't crash).
		{"negative count → just summary", -5, summary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanPreGrow(tc.n)
			if got != tc.want {
				t.Errorf("humanPreGrow(%d) = %d, want %d", tc.n, got, tc.want)
			}
			if got < 0 {
				t.Errorf("humanPreGrow(%d) returned negative %d (would panic Grow)", tc.n, got)
			}
		})
	}
}
