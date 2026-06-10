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

// Summary holds the count of errors and warnings in a Report.
type Summary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

// NewReport builds a Report from a directory path and a list of violations.
// A nil violations slice is normalised to an empty slice so JSON output is [] not null.
func NewReport(dir string, violations []checker.Violation) Report {
	if violations == nil {
		violations = make([]checker.Violation, 0)
	}
	s := Summary{}
	for _, v := range violations {
		if v.Severity == "error" {
			s.Errors++
		} else {
			s.Warnings++
		}
	}
	return Report{TfdryVersion: Version, Directory: dir, Violations: violations, Summary: s}
}

// WriteJSON writes r to w as indented JSON.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteHuman writes r to w in a human-readable format with severity icons.
func WriteHuman(w io.Writer, r Report) {
	if len(r.Violations) == 0 {
		io.WriteString(w, "✓ No violations found.\n")
		return
	}
	// Build into a local buffer that grows lazily, then write once. Avoids
	// the 4 KB upfront allocation that bufio.NewWriter would impose for
	// small outputs (the common 1-10 violations case), while still
	// minimising syscalls for the large-output case.
	var b bytes.Buffer
	// Pre-size for the typical ~110-byte line plus the trailing summary.
	// Saves the doubling-growth waste for the large-output case.
	b.Grow(len(r.Violations)*128 + 64)
	for _, v := range r.Violations {
		b.WriteString(severityIcon(v.Severity))
		b.WriteString("  [")
		b.WriteString(v.Code)
		b.WriteString("] ")
		b.WriteString(sanitize(v.File))
		if v.Line > 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(v.Line))
		}
		b.WriteString("  ")
		b.WriteString(sanitize(v.Message))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strconv.Itoa(r.Summary.Errors))
	b.WriteString(" error(s), ")
	b.WriteString(strconv.Itoa(r.Summary.Warnings))
	b.WriteString(" warning(s)\n")
	w.Write(b.Bytes())
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
// All control characters except '\t' and '\n' are also stripped.
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
			if unicode.IsControl(r) && r != '\t' && r != '\n' {
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
