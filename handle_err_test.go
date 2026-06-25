package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestHandleCtxErr_Categorization pins the cancel-only contract used by
// every run() callsite that consumes a checker package error. The helper
// distinguishes cancellation from generic errors so callers can decide
// case-by-case whether to exit immediately (top-level run()/runFmt
// orchestration) or accumulate (per-file write loops):
//
//	nil                              -> (0, false), no stderr — caller proceeds.
//	context.Canceled / Deadline      -> (130, true), "tfdry: interrupted" stderr.
//	any other non-nil error          -> (0, false), no stderr — caller handles.
//
// The wrap cases verify errors.Is() unwrapping is preserved so wrapped
// cancellations still produce exit 130 rather than falling through.
//
// This test exists because two earlier silent-failure bugs (G46/G47,
// G49-G52) were caused by code that ignored the bool — either using
// `code, _ := handleCtxErr(...)` and returning code unconditionally, or
// using `if code, ok := handleCtxErr(...); ok { return code }` without
// a fall-through for the generic-error case. Pinning the (0, false)
// return for generic errors makes the cancel-only semantics explicit
// and gives callers a deterministic contract to write against.
func TestHandleCtxErr_Categorization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantCode   int
		wantOK     bool
		wantStderr string
	}{
		{
			name:     "nil error returns (0, false) with no stderr",
			err:      nil,
			wantCode: 0,
			wantOK:   false,
		},
		{
			name:       "context.Canceled returns (130, true) with interrupted message",
			err:        context.Canceled,
			wantCode:   130,
			wantOK:     true,
			wantStderr: "tfdry: interrupted\n",
		},
		{
			name:       "context.DeadlineExceeded returns (130, true) with interrupted message",
			err:        context.DeadlineExceeded,
			wantCode:   130,
			wantOK:     true,
			wantStderr: "tfdry: interrupted\n",
		},
		{
			name:       "wrapped context.Canceled is unwrapped via errors.Is",
			err:        fmt.Errorf("checker.Run: %w", context.Canceled),
			wantCode:   130,
			wantOK:     true,
			wantStderr: "tfdry: interrupted\n",
		},
		{
			name:       "wrapped context.DeadlineExceeded is unwrapped via errors.Is",
			err:        fmt.Errorf("parse loop: %w", context.DeadlineExceeded),
			wantCode:   130,
			wantOK:     true,
			wantStderr: "tfdry: interrupted\n",
		},
		{
			name:     "generic non-cancellation error returns (0, false) — caller handles",
			err:      errors.New("disk full"),
			wantCode: 0,
			wantOK:   false,
		},
		{
			name:     "wrapped generic error stays (0, false)",
			err:      fmt.Errorf("parse: %w", errors.New("malformed input")),
			wantCode: 0,
			wantOK:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			gotCode, gotOK := handleCtxErr(tc.err, &stderr)
			if gotCode != tc.wantCode {
				t.Errorf("code = %d, want %d", gotCode, tc.wantCode)
			}
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			gotStderr := stderr.String()
			if gotStderr != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", gotStderr, tc.wantStderr)
			}
		})
	}
}
