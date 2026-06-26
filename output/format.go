// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

// Package output formats tfdry check results for human and machine consumption.
package output

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/mchv/tfdry/checker"
)

// Version is set at build time via -ldflags="-X github.com/mchv/tfdry/output.Version=..."
var Version = "dev"

// Report is the top-level JSON output structure returned by tfdry.
type Report struct {
	TfdryVersion string              `json:"tfdry_version"`
	Directory    string              `json:"directory"`
	Violations   []checker.Violation `json:"violations"`
	Summary      Summary             `json:"summary"`
}

// Summary holds violation counts for a Report.
//
// ToolErrors is a sub-count of Errors that only includes E000 violations
// (tool/infrastructure failures: unreadable directories, files exceeding
// the size limit, write failures during --fix). The split lets main.go
// route those to exit code 2 (tool error) rather than exit code 1 (lint
// found issues). Errors still counts ALL error-severity violations
// including E000, so existing human-output "X error(s) found" and JSON
// `summary.errors` consumers stay backwards-compatible.
type Summary struct {
	Errors     int `json:"errors"`
	Warnings   int `json:"warnings"`
	ToolErrors int `json:"tool_errors"`
}

// NewReport builds a Report from a directory path and a list of violations.
// A nil violations slice is normalised to an empty slice so JSON output is [] not null.
//
// The File and Message fields of every violation are sanitized here:
// stripping ANSI/control sequences and Bidi-override format characters
// before either the human or JSON writer sees the data. JSON consumers
// commonly pipe values to a terminal (e.g. `jq`, CI dashboards), so the
// JSON path needs the same protection as the human path. Sanitizing once
// at the constructor keeps both writers consistent and lets the human
// writer skip per-field re-sanitization.
//
// Report.Directory is also sanitized — the caller-supplied path can
// contain control / ANSI / Bidi-override characters on Unix and would
// otherwise leak into the JSON "directory" field, enabling the same
// terminal- and line-injection attacks that the human and fmt-subcommand
// paths defend against.
func NewReport(dir string, violations []checker.Violation) Report {
	if violations == nil {
		violations = make([]checker.Violation, 0)
	}
	clean := make([]checker.Violation, len(violations))
	for i, v := range violations {
		v.File = sanitize(v.File)
		v.Message = sanitize(v.Message)
		clean[i] = v
	}
	s := Summary{}
	for _, v := range clean {
		switch v.Severity {
		case "error":
			s.Errors++
			if v.Code == "E000" {
				s.ToolErrors++
			}
		case "warning":
			s.Warnings++
		default:
			// Unknown severities (empty, future variants like "info" or
			// "fatal", or accidentally-empty due to a checker bug) are
			// counted as errors so a downstream CI pipeline fails loudly
			// rather than passing silently on a violation we don't
			// recognise. An earlier shape of this switch had a `default`
			// arm that tallied unknowns as warnings — which would have
			// hidden a checker bug behind exit code 0.
			s.Errors++
		}
	}
	return Report{TfdryVersion: Version, Directory: sanitize(dir), Violations: clean, Summary: s}
}

// WriteJSON writes r to w as indented JSON.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteHuman writes r to w in a human-readable format with severity icons.
// Returns any error encountered while writing to w. Callers should
// propagate this so a stdout failure (closed pipe, full disk) maps to a
// non-zero exit code, consistent with the JSON output path.
func WriteHuman(w io.Writer, r Report) error {
	if len(r.Violations) == 0 {
		_, err := io.WriteString(w, "✓ No violations found.\n")
		return err
	}
	// Build into a local buffer that grows lazily, then write once. Avoids
	// the 4 KB upfront allocation that bufio.NewWriter would impose for
	// small outputs (the common 1-10 violations case), while still
	// minimising syscalls for the large-output case.
	var b bytes.Buffer
	// Pre-size for the typical ~110-byte line plus the trailing summary.
	// Saves the doubling-growth waste for the large-output case. The
	// helper guards against integer overflow on pathologically large
	// violation slices (which would panic bytes.Buffer.Grow on a negative
	// argument).
	b.Grow(humanPreGrow(len(r.Violations)))
	for _, v := range r.Violations {
		b.WriteString(severityIcon(v.Severity))
		b.WriteString("  [")
		b.WriteString(v.Code)
		b.WriteString("] ")
		// Fields are pre-sanitized by NewReport — see godoc there.
		b.WriteString(v.File)
		if v.Line > 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(v.Line))
		}
		b.WriteString("  ")
		b.WriteString(v.Message)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strconv.Itoa(r.Summary.Errors))
	b.WriteString(" error(s), ")
	b.WriteString(strconv.Itoa(r.Summary.Warnings))
	b.WriteString(" warning(s)\n")
	// Use bytes.Buffer.WriteTo rather than w.Write(b.Bytes()). The
	// io.Writer contract requires non-nil error on short write, but real
	// implementations sometimes break that. WriteTo detects n != len(p)
	// and surfaces io.ErrShortWrite, so a partial write doesn't slip
	// through as silent success.
	_, err := b.WriteTo(w)
	return err
}

// WriteChecksJSON writes the list of available checks as 2-space-indented JSON
// with a top-level "checks" key. Used by `tfdry describe --json`.
func WriteChecksJSON(w io.Writer, checks []checker.CheckInfo) error {
	type entry struct {
		Code     string `json:"code"`
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
	}
	type wrap struct {
		Checks []entry `json:"checks"`
	}
	entries := make([]entry, len(checks))
	for i, c := range checks {
		entries[i] = entry{c.Code, c.Severity, c.Summary}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(wrap{entries})
}

func severityIcon(s string) string {
	if s == "error" {
		return "✗"
	}
	return "⚠"
}

// humanPreGrow returns the byte capacity to pre-allocate for the WriteHuman
// buffer given a violation count. Each violation produces ~110 bytes; we use
// 128 for headroom plus 64 for the trailing summary line. Capped at 16 MB so
// pathologically large counts (e.g. 16M+ violations on a 32-bit int) don't
// either overflow the multiplication into a negative value (which would
// panic bytes.Buffer.Grow) or pre-allocate gigabytes for unlikely cases.
func humanPreGrow(n int) int {
	const (
		perViolation = 128
		summaryBytes = 64
		maxPreGrow   = 16 << 20 // 16 MB
	)
	if n <= 0 {
		return summaryBytes
	}
	// Detect multiplication overflow: if n*perViolation/n != perViolation,
	// the multiplication wrapped. Treat as "too big" → cap.
	mul := n * perViolation
	if mul/n != perViolation {
		return maxPreGrow
	}
	want := mul + summaryBytes
	if want < 0 || want > maxPreGrow {
		return maxPreGrow
	}
	return want
}

// Sanitize is the exported alias for the package-internal sanitize helper.
// Use it from outside the output package — e.g. the fmt subcommand in main.go
// — when emitting attacker-influenced strings (filenames, HCL diagnostic
// text) directly to stdout/stderr without going through NewReport.
func Sanitize(s string) string {
	return sanitize(s)
}

// sanitize removes ANSI escape sequences and control characters to prevent
// terminal injection via malicious content in .tf file names or local names.
//
// Recognised sequences (per ECMA-48):
//   - CSI: ESC [ ... <final byte 0x40-0x7E>
//   - OSC: ESC ] ... <BEL or ESC \>
//   - DCS: ESC P ... <ESC \>
//   - SOS: ESC X ... <ESC \>
//   - PM:  ESC ^ ... <ESC \>
//   - APC: ESC _ ... <ESC \>
//   - Single-char ESC sequences (e.g. ESC c reset, ESC = keypad mode)
//
// All control characters are stripped — INCLUDING `\n` and `\t`.
// Filenames on Unix can legitimately contain newlines/tabs, but in
// attacker-controlled values they enable line-injection attacks
// (forging fake violation lines in human output, breaking TSV
// consumers, injecting newlines into JSON-decoded fields). Report
// scaffolding (separators, summary lines) is added by WriteHuman
// AFTER sanitize and is not affected.
func sanitize(s string) string {
	const (
		stNormal    = iota
		stEsc       // just consumed ESC, deciding sequence type
		stCSI       // inside ESC [ ...
		stString    // inside ESC ] / P / X / ^ / _ ...
		stStringEsc // inside string-type sequence, just saw ESC (looking for \)
	)

	var b strings.Builder
	b.Grow(len(s))
	state := stNormal
	for _, r := range s {
		switch state {
		case stNormal:
			if r == '\x1b' {
				state = stEsc
				continue
			}
			// Strip Cc (control) characters. We previously preserved \t/\n
			// as "legitimate whitespace in our reports", but that
			// assumption only holds for report SCAFFOLDING (separators,
			// summary line) — not for the attacker-controlled VALUES
			// (File, Message) that go through this function. Strip them
			// to prevent line-injection attacks from crafted filenames.
			// Also strip Cf (format) characters in the Bidi-
			// override and isolate-control ranges — these are not
			// caught by unicode.IsControl (which only covers Cc) but
			// enable Trojan Source attacks (CVE-2021-42574) where U+202E
			// etc. visually re-orders trailing text.
			if isBidiOverride(r) {
				continue
			}
			if unicode.IsControl(r) {
				continue
			}
			b.WriteRune(r)
		case stEsc:
			switch r {
			case '[':
				state = stCSI
			case ']', 'P', 'X', '^', '_':
				state = stString
			default:
				// Single-char ESC sequence (e.g. ESC c) or unrecognised — consume it.
				state = stNormal
			}
		case stCSI:
			// CSI ends on a "final byte" in 0x40-0x7E.
			if r >= 0x40 && r <= 0x7E {
				state = stNormal
			}
		case stString:
			switch r {
			case '\x07': // BEL terminator (legacy OSC)
				state = stNormal
			case '\x1b': // possible start of ST = ESC \
				state = stStringEsc
			}
		case stStringEsc:
			switch r {
			case '\\':
				state = stNormal // ST consumed (ESC \)
			case '\x1b':
				// Another ESC: the previous ESC was bogus, this one is the
				// new ST candidate. Stay in stStringEsc so a following '\'
				// terminates the sequence. Without this, a sequence like
				// `\x1b]…\x1b\x1b\` would leak the trailing '\' into output.
				state = stStringEsc
			default:
				// Malformed: no '\' followed the ESC. Keep dropping.
				state = stString
			}
		}
	}
	return b.String()
}

// isBidiOverride reports whether r is a Unicode bidirectional override or
// isolate-control character. These belong to category Cf (format), so
// unicode.IsControl misses them, but they enable Trojan Source attacks
// (CVE-2021-42574) by visually re-ordering surrounding glyphs:
//
//   - U+202A LRE  (Left-to-Right Embedding)
//   - U+202B RLE  (Right-to-Left Embedding)
//   - U+202C PDF  (Pop Directional Formatting)
//   - U+202D LRO  (Left-to-Right Override)
//   - U+202E RLO  (Right-to-Left Override)
//   - U+2066 LRI  (Left-to-Right Isolate)
//   - U+2067 RLI  (Right-to-Left Isolate)
//   - U+2068 FSI  (First Strong Isolate)
//   - U+2069 PDI  (Pop Directional Isolate)
//
// Stripped from human and JSON report fields that originate in attacker-
// controlled values (filenames, local names, raw .tf contents).
func isBidiOverride(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}
