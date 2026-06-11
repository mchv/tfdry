package checker

import (
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

// ParsedFile holds the parsed AST and original source for one .tf file.
type ParsedFile struct {
	Name string
	Body *hclsyntax.Body
	Src  []byte // original file bytes, used for format checking
}

// parseResult is the result of parsing a single file.
type parseResult struct {
	file       *ParsedFile
	violations []Violation
}

// ParseDir parses all .tf files in dir concurrently.
// Returns parsed files and any syntax/infrastructure violations.
func ParseDir(dir string) ([]ParsedFile, []Violation) {
	dir = filepath.Clean(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []Violation{{Code: "E000", Severity: "error", File: dir, Message: fmt.Sprintf("cannot read directory: %v", err)}}
	}

	// Collect eligible .tf entries (sequential, cheap).
	var tfEntries []os.DirEntry
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".tf" {
			continue
		}
		tfEntries = append(tfEntries, e)
	}

	results := make([]parseResult, len(tfEntries))

	// Sequential fallback for small directories: goroutine setup + scheduling
	// overhead exceeds the parallelism win below this threshold. Typical
	// Terraform modules have 1-5 .tf files, so this is the common case.
	const parallelThreshold = 4
	if len(tfEntries) <= parallelThreshold {
		for i, e := range tfEntries {
			results[i] = parseOne(dir, e)
		}
	} else {
		g := new(errgroup.Group)
		g.SetLimit(runtime.NumCPU() * 2)
		for i, e := range tfEntries {
			g.Go(func() error {
				results[i] = parseOne(dir, e)
				return nil
			})
		}
		g.Wait() //nolint:errcheck — parseOne never errors; violations are in results
	}

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
	defer f.Close()

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
		var vs []Violation
		for _, d := range diags {
			// Always populate File from the directory entry name — file-level
			// or global HCL diagnostics may have d.Subject == nil (e.g. lex-
			// time failures with no position), which would otherwise leave
			// File empty and prevent downstream consumers from grouping or
			// addressing the error to its source file (G20).
			v := Violation{Code: "E001", Severity: "error", File: e.Name(), Message: d.Detail}
			if d.Subject != nil {
				v.Line = d.Subject.Start.Line
			}
			vs = append(vs, v)
		}
		return parseResult{violations: vs}
	}

	body, ok := parsed.Body.(*hclsyntax.Body)
	if !ok {
		return parseResult{}
	}
	return parseResult{file: &ParsedFile{Name: e.Name(), Body: body, Src: src}}
}
