// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"golang.org/x/sync/errgroup"
)

const maxFileSize = 10 * 1024 * 1024 // 10 MB

// readAll drains r and returns up to maxFileSize+1 bytes. The size hint
// (typically from os.FileInfo.Size) is intentionally ignored — some
// filesystems (FUSE, network FS) report a stale or zero size for non-empty
// files, and the file may grow between stat and read. The +1 lets the
// caller distinguish "exactly at the limit" from "exceeded the limit" via
// `len(buf) > maxFileSize`.
func readAll(r io.Reader, _ int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxFileSize+1))
}

// ParsedFile holds the parsed AST and original source for one native HCL
// configuration file (.tf or .tofu).
type ParsedFile struct {
	Name string
	Body *hclsyntax.Body
	Src  []byte // original file bytes, used for format checking
}

// nativeConfigEntries selects the native-HCL Terraform/OpenTofu files from a
// directory listing. OpenTofu's loading contract gives a .tofu file precedence
// over a same-basename .tf file (main.tofu shadows main.tf); otherwise distinct
// .tf and .tofu files are both part of the module. JSON variants are excluded
// because this parser intentionally supports native HCL only.
//
// A known symlink or non-regular .tofu entry is excluded before parsing so
// Windows cannot follow it and merge it with a same-basename .tf file. If .tofu
// metadata cannot be read, the .tf peer wins conservatively; a standalone .tofu
// remains a candidate so the normal parser can surface an E000 if opening it
// also fails.
//
// os.ReadDir returns entries sorted by filename, and this function preserves
// their order so parsing and diagnostics remain deterministic.
func nativeConfigEntries(entries []os.DirEntry) []os.DirEntry {
	tfBases := make(map[string]struct{})
	tofuBases := make(map[string]struct{})
	tofuKinds := make(map[string]tofuEntryKind)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch filepath.Ext(name) {
		case ".tf":
			tfBases[name[:len(name)-len(".tf")]] = struct{}{}
		case ".tofu":
			kind := classifyTofuEntry(e)
			tofuKinds[name] = kind
			if kind == tofuEntryRegular {
				tofuBases[name[:len(name)-len(".tofu")]] = struct{}{}
			}
		}
	}

	selected := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch filepath.Ext(name) {
		case ".tofu":
			base := name[:len(name)-len(".tofu")]
			switch tofuKinds[name] {
			case tofuEntryRegular:
				selected = append(selected, e)
			case tofuEntryMetadataUnknown:
				if _, hasTerraformPeer := tfBases[base]; !hasTerraformPeer {
					selected = append(selected, e)
				}
			case tofuEntryExcluded:
				continue
			}
		case ".tf":
			base := name[:len(name)-len(".tf")]
			if _, shadowed := tofuBases[base]; !shadowed {
				selected = append(selected, e)
			}
		}
	}
	return selected
}

// allNativeConfigEntries selects every native-HCL Terraform/OpenTofu file
// without applying module-loading precedence. Formatters use this because
// `tofu fmt` formats same-basename .tf and .tofu files independently.
func allNativeConfigEntries(entries []os.DirEntry) []os.DirEntry {
	selected := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".tf", ".tofu":
			selected = append(selected, e)
		}
	}
	return selected
}

type tofuEntryKind uint8

const (
	tofuEntryMetadataUnknown tofuEntryKind = iota
	tofuEntryRegular
	tofuEntryExcluded
)

// classifyTofuEntry determines whether a .tofu entry can authoritatively
// shadow a same-basename .tf file. Calling Info is intentional even when
// DirEntry.Type reports no symlink bit: filesystems such as FUSE and network
// mounts may return Type()==0 for every entry, including symlinks.
func classifyTofuEntry(e os.DirEntry) tofuEntryKind {
	info, err := e.Info()
	if err != nil {
		return tofuEntryMetadataUnknown
	}
	if info.Mode().IsRegular() {
		return tofuEntryRegular
	}
	return tofuEntryExcluded
}

// parseResult is the result of parsing a single file.
type parseResult struct {
	file       *ParsedFile
	violations []Violation
}

// collectResults flattens the per-file parseResult slice into the
// public (files, violations) shape ParseDir returns. Nil-file entries
// (parse failed; violations populated instead) are skipped from files
// but their violations propagate. Empty slots (parseOne never ran due
// to cancellation) contribute nothing.
//
// Used in both ParseDir's success path and its two cancellation
// paths — when ctx fires mid-walk we still return whatever results
// the loop already populated, so callers see partial output rather
// than (nil, nil) alongside the cancellation error.
func collectResults(results []parseResult) ([]ParsedFile, []Violation) {
	var files []ParsedFile
	var violations []Violation
	for _, r := range results {
		violations = append(violations, r.violations...)
		if r.file != nil {
			files = append(files, *r.file)
		}
	}
	return files, violations
}

// ParseDir parses native-HCL .tf and .tofu files in dir concurrently, applying
// OpenTofu's same-basename precedence rule. Returns parsed files,
// any syntax/infrastructure violations, and a non-nil error if ctx was
// cancelled mid-walk. On cancellation, files and violations may be
// partial — every result populated before the cancellation fired is
// returned. Callers can use errors.Is(err, context.Canceled) or
// errors.Is(err, context.DeadlineExceeded) to detect cancellation;
// both checks must use errors.Is so wrapped sentinels (e.g.
// errgroup's wrapped error from the concurrent branch) still match.
//
// The cancellation contract: ctx is checked once before iterating the
// directory listing, once before each per-file parse in the sequential
// branch, and via errgroup.WithContext in the concurrent branch. A
// cancelled ctx propagates as context.Canceled / context.DeadlineExceeded.
func ParseDir(ctx context.Context, dir string) ([]ParsedFile, []Violation, error) {
	return parseDir(ctx, dir, true)
}

// ParseDirForFormat parses every native-HCL .tf and .tofu file in dir without
// applying same-basename precedence. OpenTofu applies precedence when loading a
// module, but `tofu fmt` formats both files independently.
func ParseDirForFormat(ctx context.Context, dir string) ([]ParsedFile, []Violation, error) {
	return parseDir(ctx, dir, false)
}

func parseDir(ctx context.Context, dir string, applyTofuPrecedence bool) ([]ParsedFile, []Violation, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	dir = filepath.Clean(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []Violation{{Code: "E000", Severity: "error", File: dir, Message: fmt.Sprintf("cannot read directory: %v", err)}}, nil
	}

	var configEntries []os.DirEntry
	if applyTofuPrecedence {
		// Linting models module loading, where .tofu shadows a same-basename
		// .tf file.
		configEntries = nativeConfigEntries(entries)
	} else {
		// Formatting models `tofu fmt`, which treats both files independently.
		configEntries = allNativeConfigEntries(entries)
	}

	results := make([]parseResult, len(configEntries))

	// Sequential fallback for small directories: goroutine setup + scheduling
	// overhead exceeds the parallelism win below this threshold. Typical
	// Terraform/OpenTofu modules have 1-5 configuration files, so this is the
	// common case.
	const parallelThreshold = 4
	if len(configEntries) <= parallelThreshold {
		for i, e := range configEntries {
			if err := ctx.Err(); err != nil {
				// Return what we've parsed so far rather than discarding it.
				files, violations := collectResults(results[:i])
				return files, violations, err
			}
			results[i] = parseOne(dir, e)
		}
	} else {
		// errgroup.WithContext gives each goroutine a derived ctx; when the
		// parent ctx is cancelled the workers see it and we surface the
		// cancellation through g.Wait().
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(runtime.NumCPU() * 2)
		for i, e := range configEntries {
			// Pre-dispatch cancel check. Without this, g.Go below
			// blocks the dispatcher on the SetLimit semaphore for every
			// remaining file even after cancellation has fired —
			// e.g. with 10 000 files and cancel at file 100, we'd still
			// spawn ~9 900 doomed goroutines that immediately return.
			// Checking here lets us break the dispatcher loop early.
			// The worker's own gctx.Err() check below stays for the
			// race between dispatcher's read and the worker's start.
			if err := gctx.Err(); err != nil {
				break
			}
			g.Go(func() error {
				if err := gctx.Err(); err != nil {
					return err
				}
				results[i] = parseOne(dir, e)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			// Workers that ran to completion before the cancellation
			// populated their slot in results; surface those partial
			// results rather than dropping them on the floor.
			files, violations := collectResults(results)
			return files, violations, err
		}
		// Dispatcher break path: if the per-iteration gctx.Err() check
		// fires BEFORE any g.Go call (immediate cancellation as the
		// concurrent branch is entered), no workers run, so g.Wait
		// has nothing to surface and returns nil. Without this guard
		// ParseDir would return (partial files, no E000, nil err)
		// even though the contract documented above says cancellation
		// must propagate as ctx.Err(). Explicitly surface it here so
		// the early-cancel case behaves identically to the
		// mid-execution case.
		if err := ctx.Err(); err != nil {
			files, violations := collectResults(results)
			return files, violations, err
		}
	}

	files, violations := collectResults(results)
	return files, violations, nil
}

func parseOne(dir string, e os.DirEntry) parseResult {
	path := filepath.Join(dir, e.Name())

	// Open with O_NOFOLLOW to atomically reject symlinks and read the file,
	// eliminating the TOCTOU race between Lstat and ReadFile. On Windows
	// oNoFollow = 0 (see checker/nofollow_windows.go).
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		// Symlink path — skip silently.
		if isSymlinkRejection(err) {
			return parseResult{}
		}
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: fmt.Sprintf("cannot open file: %v", err)}}}
	}
	// Read-only path: a failed Close after a successful Read has no
	// recoverable signal (the data we read is already in memory). Use
	// the explicit `_ =` form rather than excluding Close globally so
	// the intent is locally visible.
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: fmt.Sprintf("cannot stat file: %v", err)}}}
	}
	if !fi.Mode().IsRegular() {
		return parseResult{} // skip non-regular files silently
	}
	if fi.Size() > maxFileSize {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: fmt.Sprintf("file exceeds size limit (%d MB)", maxFileSize/1024/1024)}}}
	}

	src, err := readAll(f, fi.Size())
	if err != nil {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: fmt.Sprintf("cannot read file: %v", err)}}}
	}
	if int64(len(src)) > maxFileSize {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: fmt.Sprintf("file exceeds size limit (%d MB)", maxFileSize/1024/1024)}}}
	}

	parsed, diags := hclsyntax.ParseConfig(src, e.Name(), hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return parseResult{violations: parseDiagsToViolations(diags, e.Name())}
	}

	body, ok := parsed.Body.(*hclsyntax.Body)
	if !ok {
		return parseResult{}
	}
	return parseResult{file: &ParsedFile{Name: e.Name(), Body: body, Src: src}}
}

// parseDiagsToViolations converts an hcl.Diagnostics slice into E001
// violations. Only hcl.DiagError-severity diagnostics are emitted; warnings
// (e.g. deprecation notices from hclsyntax) are skipped so they don't
// inflate the error count or exit code, matching runFmtFile's behaviour
// which already filters to error-severity only.
//
// The File field is always populated from `filename` (which the caller
// passes as e.Name()) so file-level diagnostics with d.Subject == nil
// still carry a usable origin.
func parseDiagsToViolations(diags hcl.Diagnostics, filename string) []Violation {
	var vs []Violation
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		v := Violation{Code: "E001", Severity: "error", File: filename, Message: diagMessage(d)}
		if d.Subject != nil {
			v.Line = d.Subject.Start.Line
		}
		vs = append(vs, v)
	}
	return vs
}

// diagMessage returns a non-empty user-facing message for an HCL diagnostic.
// hclsyntax token-/lex-level errors sometimes populate only d.Summary and
// leave d.Detail empty; the original code used d.Detail directly, producing
// E001 violations with empty Message which made syntax errors hard to
// diagnose. Order of preference: Detail, then Summary, then a sentinel
// so consumers never see an empty Message.
func diagMessage(d *hcl.Diagnostic) string {
	if d == nil {
		return "(no diagnostic message)"
	}
	if d.Detail != "" {
		return d.Detail
	}
	if d.Summary != "" {
		return d.Summary
	}
	return "(no diagnostic message)"
}
