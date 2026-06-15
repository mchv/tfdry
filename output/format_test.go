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

// C21: violations carrying terminal-injection or Trojan Source payloads
// in their File/Message fields must not leak into JSON output. JSON
// consumers commonly print decoded values to a terminal (e.g. `jq`,
// pretty-printers in CI dashboards), so unsanitized Bidi/control chars
// in JSON would attack downstream just as they would in human output.
// The Report constructor sanitizes once, so both writers see clean data.
func TestWriteJSON_StripsTerminalInjection(t *testing.T) {
	const rlo = "\u202E"
	vs := []checker.Violation{
		{Code: "E001", Severity: "error",
			File:    "evil" + rlo + "fn.tf\x1b[31m",
			Message: "msg" + rlo + "rest\x1b]0;TITLE\x07",
		},
	}
	var buf bytes.Buffer
	if err := output.WriteJSON(&buf, output.NewReport("/dir", vs)); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	// JSON-encoded forms of these characters must also be absent (encoding/json
	// escapes control chars to \uXXXX sequences, which still render in
	// terminals when downstream tools print decoded values).
	mustNot := []string{
		"\u202E",  // bidi RLO (raw)
		"\\u202E", // JSON-escaped bidi RLO
		"\\u202e",
		"\x1b",    // raw ESC
		"\\u001B", // JSON-escaped ESC
		"\\u001b",
		"\x07",    // raw BEL
		"\\u0007", // JSON-escaped BEL
	}
	for _, s := range mustNot {
		if strings.Contains(got, s) {
			t.Errorf("JSON output must not contain %q (full output: %q)", s, got)
		}
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

// C30: filenames on Unix can contain `\n` and `\t`. An attacker who
// controls a .tf file name (or a local name embedded in an error
// message) could inject newlines that forge fake violation lines in
// human output, or inject \n/\t into JSON-decoded fields that
// downstream consumers print verbatim. sanitize() previously preserved
// \n and \t as "legitimate whitespace in our reports", but that
// assumption only holds for the report SCAFFOLDING (separators,
// summary line) — not for attacker-controlled VALUES.
//
// Strips \n/\t from violation File and Message fields. Both human and
// JSON paths must be free of these characters in field values.
func TestSanitize_StripsNewlineAndTabInjection(t *testing.T) {
	t.Parallel()
	const fakeLine = "FAKE [E001] forged.tf:1  injected error"
	vs := []checker.Violation{
		// Newline-injected filename — forges a second violation line in human output.
		{Code: "E003", Severity: "error",
			File:    "main.tf\n✗  [E001] " + fakeLine,
			Line:    1,
			Message: "ok"},
		// Tab-injected filename — could break TSV consumers / forge column boundaries.
		{Code: "E003", Severity: "error",
			File:    "main.tf\tfoo",
			Line:    2,
			Message: "ok"},
		// Newline-injected message — forges a fake follow-up line.
		{Code: "E003", Severity: "error",
			File:    "main.tf",
			Line:    3,
			Message: "real error\n✗  [E001] " + fakeLine},
	}
	r := output.NewReport("/dir", vs)

	// Human output: must NOT contain raw \n / \t inside the rendered values.
	// The KEY invariant is that each violation produces exactly one line
	// (no line injection). The forged TEXT content may still appear (it's
	// part of the user's data, even if attacker-controlled), but it can
	// no longer fake a separate violation line — strings like "✗  [E001]"
	// will visibly run on after the previous line's content, making the
	// injection obvious rather than convincing.
	var buf bytes.Buffer
	if err := output.WriteHuman(&buf, r); err != nil {
		t.Fatalf("WriteHuman: %v", err)
	}
	human := buf.String()
	// 3 violation lines + blank separator + summary line = 5 newlines.
	// If \n leaked through sanitize, this would be 7+.
	humanLines := strings.Count(human, "\n")
	if humanLines != 5 {
		t.Errorf("expected 5 newlines in human output (3 violations + blank + summary), got %d:\n%s",
			humanLines, human)
	}
	// No raw tab character anywhere in the rendered output.
	if strings.Contains(human, "\t") {
		t.Errorf("tab character leaked into human output: %q", human)
	}

	// JSON output: must NOT contain literal \n or \t INSIDE field values.
	// JSON itself uses newlines as structural whitespace, so we look at
	// each violation's File and Message fields after decoding.
	var got struct {
		Violations []struct {
			File    string `json:"file"`
			Message string `json:"message"`
		} `json:"violations"`
	}
	var jbuf bytes.Buffer
	if err := output.WriteJSON(&jbuf, r); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if err := json.Unmarshal(jbuf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jbuf.String())
	}
	for i, v := range got.Violations {
		if strings.ContainsAny(v.File, "\n\t") {
			t.Errorf("violations[%d].file contains \\n or \\t after sanitize: %q", i, v.File)
		}
		if strings.ContainsAny(v.Message, "\n\t") {
			t.Errorf("violations[%d].message contains \\n or \\t after sanitize: %q", i, v.Message)
		}
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
		// ── G13 (Trojan Source / CVE-2021-42574 family) ──────────────────────
		// Bidi override and isolate-control characters belong to Unicode's Cf
		// (format) category, which unicode.IsControl does NOT cover. Without
		// explicit stripping, an attacker could craft a filename or local name
		// like `passwd\u202E.tf` so that the rendered text reads `passwdfg.tx`
		// in the human report — visually misrepresenting the actual content.
		// All Cf characters in the Bidi/Isolate ranges must be stripped.
		{
			name:     "Bidi RLO (right-to-left override) U+202E",
			input:    "before\u202Eevil_target.txafter",
			mustNot:  []string{"\u202E"},
			mustHave: []string{"before", "evil_target.tx", "after"},
		},
		{
			name:     "Bidi LRO (left-to-right override) U+202D",
			input:    "x\u202Dy",
			mustNot:  []string{"\u202D"},
			mustHave: []string{"xy"},
		},
		{
			name:     "Bidi LRE/RLE/PDF embedding controls U+202A/B/C",
			input:    "a\u202Ab\u202Bc\u202Cd",
			mustNot:  []string{"\u202A", "\u202B", "\u202C"},
			mustHave: []string{"abcd"},
		},
		{
			name:     "Bidi LRI/RLI/FSI/PDI isolate controls U+2066-U+2069",
			input:    "p\u2066q\u2067r\u2068s\u2069t",
			mustNot:  []string{"\u2066", "\u2067", "\u2068", "\u2069"},
			mustHave: []string{"pqrst"},
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
