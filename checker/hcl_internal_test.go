package checker

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
)

// G23: hclsyntax diagnostics may set only Summary (token-level errors,
// some lex paths), leaving Detail empty. Using d.Detail directly produces
// an E001 with no message, which makes the violation impossible to
// diagnose. The diagMessage helper falls back to Summary in that case
// and provides a sentinel when both are empty.
func TestDiagMessage_FallbacksAndShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		summary string
		detail  string
		want    string
	}{
		{name: "detail set, summary set: prefer detail", summary: "Bad expression", detail: "expected ; here", want: "expected ; here"},
		{name: "detail empty, summary set: fallback to summary", summary: "Invalid character", detail: "", want: "Invalid character"},
		{name: "detail set, summary empty: keep detail", summary: "", detail: "expected ; here", want: "expected ; here"},
		{name: "both empty: sentinel", summary: "", detail: "", want: "(no diagnostic message)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diagMessage(&hcl.Diagnostic{Summary: tc.summary, Detail: tc.detail})
			if got != tc.want {
				t.Errorf("diagMessage(summary=%q, detail=%q) = %q, want %q",
					tc.summary, tc.detail, got, tc.want)
			}
		})
	}
}
