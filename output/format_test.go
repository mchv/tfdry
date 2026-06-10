package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mchv/tfdry/checker"
	"github.com/mchv/tfdry/output"
)

func TestNewReport_NilViolations_EmptyJSONArray(t *testing.T) {
	r := output.NewReport("/dir", nil)
	var buf bytes.Buffer
	if err := output.WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	vs, ok := out["violations"]
	if !ok {
		t.Fatal("missing violations key")
	}
	// Must be [] not null.
	arr, ok := vs.([]any)
	if !ok || arr == nil {
		t.Fatalf("violations must be JSON array, got %T %v", vs, vs)
	}
}

func TestNewReport_SummaryCounting(t *testing.T) {
	vs := []checker.Violation{
		{Code: "E001", Severity: "error"},
		{Code: "E002", Severity: "error"},
		{Code: "W001", Severity: "warning"},
	}
	r := output.NewReport("/dir", vs)
	if r.Summary.Errors != 2 {
		t.Fatalf("expected 2 errors, got %d", r.Summary.Errors)
	}
	if r.Summary.Warnings != 1 {
		t.Fatalf("expected 1 warning, got %d", r.Summary.Warnings)
	}
}

func TestWriteHuman_NoViolations(t *testing.T) {
	var buf bytes.Buffer
	output.WriteHuman(&buf, output.NewReport("/dir", nil))
	if !strings.Contains(buf.String(), "No violations found") {
		t.Fatalf("expected clean message, got: %q", buf.String())
	}
}

func TestWriteHuman_WithViolations(t *testing.T) {
	vs := []checker.Violation{
		{Code: "E003", Severity: "error", File: "main.tf", Line: 5, Message: "reference to undefined local \"x\""},
		{Code: "W001", Severity: "warning", File: "main.tf", Line: 2, Message: "local \"y\" is defined but never used"},
	}
	var buf bytes.Buffer
	output.WriteHuman(&buf, output.NewReport("/dir", vs))
	out := buf.String()
	if !strings.Contains(out, "E003") || !strings.Contains(out, "W001") {
		t.Fatalf("expected both codes in output, got: %q", out)
	}
	if !strings.Contains(out, "1 error(s)") || !strings.Contains(out, "1 warning(s)") {
		t.Fatalf("expected summary line, got: %q", out)
	}
}

func TestSanitize_ANSIEscapeStripped(t *testing.T) {
	// Craft a violation whose message contains an ANSI escape sequence.
	vs := []checker.Violation{
		{Code: "E003", Severity: "error", File: "main.tf", Line: 1, Message: "\x1b[31mred\x1b[0m"},
	}
	var buf bytes.Buffer
	output.WriteHuman(&buf, output.NewReport("/dir", vs))
	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("ANSI escape not stripped from human output: %q", out)
	}
	if !strings.Contains(out, "red") {
		t.Fatalf("visible text should be preserved: %q", out)
	}
}

// TestSanitize_TerminalInjection exercises every escape-sequence shape that
// can affect a terminal: CSI, OSC (BEL- and ST-terminated), DCS, SOS, PM, APC.
// All of these can be produced by an attacker via crafted .tf file names or
// local names, and all of them must be fully consumed (not leaked into output).
func TestSanitize_TerminalInjection(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		mustNot  []string // substrings that must NOT appear in output
		mustHave []string // substrings that MUST appear in output
	}{
		{
			name:     "CSI letter terminator",
			input:    "before \x1b[31mRED\x1b[0m after",
			mustNot:  []string{"\x1b", "[31m", "[0m"},
			mustHave: []string{"before", "RED", "after"},
		},
		{
			name:     "CSI non-letter terminator",
			input:    "x \x1b[1;2H y",
			mustNot:  []string{"\x1b", "[1;2H"},
			mustHave: []string{"x ", " y"},
		},
		{
			name:     "OSC 0 set window title BEL terminated",
			input:    "before \x1b]0;EVIL TITLE\x07 after",
			mustNot:  []string{"\x1b", "\x07", "EVIL TITLE", "0;EVIL"},
			mustHave: []string{"before", "after"},
		},
		{
			name:     "OSC 8 hyperlink ST terminated",
			input:    "click \x1b]8;;https://evil.example/x\x1b\\here\x1b]8;;\x1b\\ now",
			mustNot:  []string{"\x1b", "https://evil.example", "8;;"},
			mustHave: []string{"click ", "here", " now"},
		},
		{
			name:     "OSC 52 clipboard injection BEL terminated",
			input:    "log \x1b]52;c;cm0gLXJmIH4K\x07 entry",
			mustNot:  []string{"\x1b", "\x07", "52;c;", "cm0gLXJmIH4K"},
			mustHave: []string{"log ", " entry"},
		},
		{
			name:     "DCS device control ST terminated",
			input:    "a \x1bPq evil \x1b\\ b",
			mustNot:  []string{"\x1b", "Pq", "evil"},
			mustHave: []string{"a ", " b"},
		},
		{
			name:     "APC application program command",
			input:    "u \x1b_payload\x1b\\ v",
			mustNot:  []string{"\x1b", "payload"},
			mustHave: []string{"u ", " v"},
		},
		{
			name:     "single-char ESC sequence (ESC c reset)",
			input:    "x\x1bcy",
			mustNot:  []string{"\x1b"},
			mustHave: []string{"x", "y"},
		},
		{
			name:     "control chars dropped except tab/newline",
			input:    "good\x00bad\x07more\ttab\nline",
			mustNot:  []string{"\x00", "\x07"},
			mustHave: []string{"goodbadmore", "\t", "\n"},
		},
		{
			name:     "no escapes pass through unchanged",
			input:    "modules/foo/bar.tf",
			mustNot:  nil,
			mustHave: []string{"modules/foo/bar.tf"},
		},
		// Regression: ESC inside an OSC string-mode body. The first ESC begins a
		// candidate ST (ESC \). If a second ESC follows immediately, the first
		// ESC was bogus and the second ESC must itself be treated as a fresh ST
		// candidate. Without this fix the OSC never terminates and the
		// remainder of the line gets silently eaten as OSC body.
		{
			name:     "OSC with ESC ESC \\ — second ESC starts ST",
			input:    "pre \x1b]0;evil\x1b\x1b\\post end",
			mustNot:  []string{"\x1b", "evil", "0;"},
			mustHave: []string{"pre ", "post end"},
		},
		{
			name:     "OSC with multiple stray ESCs before ST",
			input:    "pre \x1b]8;;url\x1b\x1b\x1b\\after end",
			mustNot:  []string{"\x1b", "url", "8;;"},
			mustHave: []string{"pre ", "after end"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vs := []checker.Violation{
				{Code: "E001", Severity: "error", File: tc.input, Line: 1, Message: tc.input},
			}
			var buf bytes.Buffer
			output.WriteHuman(&buf, output.NewReport("/dir", vs))
			out := buf.String()
			for _, s := range tc.mustNot {
				if strings.Contains(out, s) {
					t.Errorf("output must not contain %q\nfull output: %q", s, out)
				}
			}
			for _, s := range tc.mustHave {
				if !strings.Contains(out, s) {
					t.Errorf("output must contain %q\nfull output: %q", s, out)
				}
			}
		})
	}
}
