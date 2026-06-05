package checker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"golang.org/x/sync/errgroup"
)

const maxFileSize = 10 * 1024 * 1024 // 10 MB

// readAll reads up to size bytes from r into a fresh buffer, handling
// short reads correctly. If r reaches EOF before size bytes (file shrank
// between stat and read), the buffer is truncated to bytes actually read.
// Non-EOF errors are returned as-is.
func readAll(r io.Reader, size int64) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return buf[:n], nil
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
	// Check for ".." segments before Clean so we catch traversal attempts.
	if hasDotDotSegment(dir) {
		return nil, []Violation{{Code: "E000", Severity: "error", File: dir, Message: "directory path must not contain '..' segments"}}
	}
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

// hasDotDotSegment reports whether any path segment is exactly "..".
func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func parseOne(dir string, e os.DirEntry) parseResult {
	path := filepath.Join(dir, e.Name())

	// Open with O_NOFOLLOW to atomically reject symlinks and read the file,
	// eliminating the TOCTOU race between Lstat and ReadFile.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		// ELOOP means the path is a symlink — skip silently.
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return parseResult{}
		}
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: "cannot open file"}}}
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: "cannot stat file"}}}
	}
	if !fi.Mode().IsRegular() {
		return parseResult{} // skip non-regular files silently
	}
	if fi.Size() > maxFileSize {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: fmt.Sprintf("file exceeds size limit (%d MB)", maxFileSize/1024/1024)}}}
	}

	src, err := readAll(f, fi.Size())
	if err != nil {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: "cannot read file"}}}
	}
	if int64(len(src)) > maxFileSize {
		return parseResult{violations: []Violation{{Code: "E000", Severity: "error", File: e.Name(), Message: fmt.Sprintf("file exceeds size limit (%d MB)", maxFileSize/1024/1024)}}}
	}

	parsed, diags := hclsyntax.ParseConfig(src, e.Name(), hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		var vs []Violation
		for _, d := range diags {
			v := Violation{Code: "E001", Severity: "error", Message: d.Detail}
			if d.Subject != nil {
				v.File = d.Subject.Filename
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
