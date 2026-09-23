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

// openRegularFileRead follows symlinks for read-only compatibility while
// refusing non-regular targets before reading. The Unix non-blocking flag also
// closes the Stat/Open race for FIFOs; post-open Stat handles any other swap.
func openRegularFileRead(path string) (*os.File, os.FileInfo, error) {
	pre, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !pre.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("not a regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|oReadNonblock, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("not a regular file")
	}
	return f, fi, nil
}

func rangeFilename(r hcl.Range, fallback string) string {
	if r.Filename != "" {
		return r.Filename
	}
	return fallback
}

// ParsedFile holds the parsed AST and original source for one native HCL
// configuration file (.tf or .tofu).
type ParsedFile struct {
	Name string
	Body *hclsyntax.Body
	Src  []byte // original file bytes, used for format checking
}

// DirParseViews contains two projections of one physical directory load.
// PhysicalFiles includes every successfully parsed native-HCL file; SemanticFiles
// applies OpenTofu same-basename precedence using filenames, regardless of
// parse success. Files present in both views share Body and Src storage.
type DirParseViews struct {
	PhysicalFiles      []ParsedFile
	PhysicalViolations []Violation
	SemanticFiles      []ParsedFile
	SemanticViolations []Violation
}

type dirParseOps struct {
	readDir  func(string) ([]os.DirEntry, error)
	parseOne func(string, os.DirEntry) parseResult
}

// nativeConfigEntries selects the native-HCL Terraform/OpenTofu files from a
// directory listing. OpenTofu's loading contract gives a .tofu file precedence
// over a same-basename .tf file (main.tofu shadows main.tf); otherwise distinct
// .tf and .tofu files are both part of the module. JSON variants are excluded
// because this parser intentionally supports native HCL only.
//
// Every .tofu filename remains a candidate regardless of entry metadata:
// module precedence is filename-driven, while openRegularFileRead follows
// symlinks only to regular files and rejects broken/non-regular targets without
// blocking. A .tofu candidate remains authoritative over its same-basename .tf
// peer; any read failure surfaces as E000 rather than silently changing the
// loaded configuration.
//
// os.ReadDir returns entries sorted by filename, and this function preserves
// their order so parsing and diagnostics remain deterministic.
func nativeConfigEntries(entries []os.DirEntry) []os.DirEntry {
	tofuBases := make(map[string]struct{})
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) == ".tofu" {
			tofuBases[name[:len(name)-len(".tofu")]] = struct{}{}
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
			selected = append(selected, e)
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

// ParseDirViews parses every physical native-HCL file exactly once and returns
// both the physical formatting view and the precedence-selected semantic view.
func ParseDirViews(ctx context.Context, dir string) (DirParseViews, error) {
	return parseDirViewsWith(ctx, dir, dirParseOps{readDir: os.ReadDir, parseOne: parseOne})
}

func parseDirViewsWith(ctx context.Context, dir string, ops dirParseOps) (DirParseViews, error) {
	if err := ctx.Err(); err != nil {
		return DirParseViews{}, err
	}
	dir = filepath.Clean(dir)
	entries, err := ops.readDir(dir)
	if err != nil {
		violation := Violation{Code: "E000", Severity: "error", File: dir, Message: fmt.Sprintf("cannot read directory: %v", err)}
		return DirParseViews{
			PhysicalViolations: []Violation{violation},
			SemanticViolations: []Violation{violation},
		}, nil
	}

	physicalEntries := allNativeConfigEntries(entries)
	semanticNames := make(map[string]struct{})
	for _, entry := range nativeConfigEntries(entries) {
		semanticNames[entry.Name()] = struct{}{}
	}
	results, parseErr := parseConfigEntries(ctx, dir, physicalEntries, ops.parseOne)
	views := projectDirParseViews(physicalEntries, results, semanticNames)
	return views, parseErr
}

func projectDirParseViews(entries []os.DirEntry, results []parseResult, semanticNames map[string]struct{}) DirParseViews {
	var views DirParseViews
	for i, entry := range entries {
		if i >= len(results) {
			break
		}
		result := results[i]
		views.PhysicalViolations = append(views.PhysicalViolations, result.violations...)
		if result.file != nil {
			views.PhysicalFiles = append(views.PhysicalFiles, *result.file)
		}
		if _, semantic := semanticNames[entry.Name()]; !semantic {
			continue
		}
		views.SemanticViolations = append(views.SemanticViolations, result.violations...)
		if result.file != nil {
			views.SemanticFiles = append(views.SemanticFiles, *result.file)
		}
	}
	return views
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

	results, parseErr := parseConfigEntries(ctx, dir, configEntries, parseOne)
	files, violations := collectResults(results)
	return files, violations, parseErr
}

func parseConfigEntries(ctx context.Context, dir string, entries []os.DirEntry, parse func(string, os.DirEntry) parseResult) ([]parseResult, error) {
	results := make([]parseResult, len(entries))

	// Sequential fallback for small directories: goroutine setup + scheduling
	// overhead exceeds the parallelism win below this threshold.
	const parallelThreshold = 4
	if len(entries) <= parallelThreshold {
		for i, entry := range entries {
			if err := ctx.Err(); err != nil {
				return results[:i], err
			}
			results[i] = parse(dir, entry)
		}
		return results, nil
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU() * 2)
	for i, entry := range entries {
		if err := gctx.Err(); err != nil {
			break
		}
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			results[i] = parse(dir, entry)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return results, err
	}
	if err := ctx.Err(); err != nil {
		return results, err
	}
	return results, nil
}

func parseOne(dir string, e os.DirEntry) parseResult {
	path := filepath.Join(dir, e.Name())

	// Read-only loading follows symlinks to regular files, matching
	// Terraform/OpenTofu, while the shared opener rejects non-regular targets
	// without blocking. Formatting writes retain separate no-follow guards.
	f, fi, err := openRegularFileRead(path)
	if err != nil {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: fmt.Sprintf("cannot open file: %v", err)}}}
	}
	// Read-only path: a failed Close after a successful Read has no
	// recoverable signal (the data we read is already in memory).
	defer func() { _ = f.Close() }()

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
