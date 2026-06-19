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

// C34: parseOne previously created E001 violations with hardcoded
// Severity:"error" for every diagnostic, regardless of d.Severity. If
// hclsyntax emits a mix of warnings and errors (e.g. deprecation
// warnings alongside parse errors), warnings would be reported as E001
// errors, inflating the error count and exit code. The fix filters to
// hcl.DiagError only — matching the behaviour of runFmtFile (G24).
func TestParseDiagsToViolations_FiltersWarnings(t *testing.T) {
	t.Parallel()
	subject := &hcl.Range{Filename: "main.tf", Start: hcl.Pos{Line: 7, Column: 1}, End: hcl.Pos{Line: 7, Column: 5}}
	diags := hcl.Diagnostics{
		{Severity: hcl.DiagError, Summary: "Bad expression", Detail: "expected ; here", Subject: subject},
		{Severity: hcl.DiagWarning, Summary: "Deprecated syntax", Detail: "use foo instead", Subject: subject},
		{Severity: hcl.DiagError, Summary: "Unterminated string", Subject: nil},
	}
	got := parseDiagsToViolations(diags, "main.tf")
	if len(got) != 2 {
		t.Fatalf("expected 2 error violations (warning filtered out), got %d: %+v", len(got), got)
	}
	for _, v := range got {
		if v.Code != "E001" {
			t.Errorf("expected E001, got %q", v.Code)
		}
		if v.Severity != "error" {
			t.Errorf("expected severity=error, got %q", v.Severity)
		}
		if v.File != "main.tf" {
			t.Errorf("expected File=main.tf, got %q", v.File)
		}
		// The warning's "use foo instead" Detail must NOT appear.
		if v.Message == "use foo instead" || v.Message == "Deprecated syntax" {
			t.Errorf("warning leaked into violations: %+v", v)
		}
	}
	// The first error has Subject (line=7); the second doesn't (line should be 0).
	if got[0].Line != 7 {
		t.Errorf("expected first violation line=7, got %d", got[0].Line)
	}
	if got[1].Line != 0 {
		t.Errorf("expected second violation line=0 (no Subject), got %d", got[1].Line)
	}
}
