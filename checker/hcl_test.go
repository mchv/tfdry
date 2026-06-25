package checker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortReader returns at most chunkSize bytes per Read call, simulating
// network filesystems (NFS/FUSE) that can short-read without an error.
type shortReader struct {
	data      []byte
	pos       int
	chunkSize int
}

func (s *shortReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := s.chunkSize
	if n > len(p) {
		n = len(p)
	}
	if s.pos+n > len(s.data) {
		n = len(s.data) - s.pos
	}
	copy(p, s.data[s.pos:s.pos+n])
	s.pos += n
	return n, nil
}

// errReader always fails. Used to verify non-EOF errors propagate.
type errReader struct{ err error }

func (e errReader) Read(p []byte) (int, error) { return 0, e.err }

func TestReadAll_HandlesShortReads(t *testing.T) {
	data := []byte("locals {\n  env = \"prod\"\n}\n")
	r := &shortReader{data: data, chunkSize: 4} // forces multiple Read calls

	got, err := readAll(r, int64(len(data)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("got %q (len %d), want %q (len %d)", got, len(got), data, len(data))
	}
}

func TestReadAll_FileShrankBetweenStatAndRead(t *testing.T) {
	// Simulate the case where Stat reports 100 bytes but the file actually
	// has only 5 bytes by the time we read. Should return what we got, not error.
	data := []byte("hello")
	r := &shortReader{data: data, chunkSize: 100}

	got, err := readAll(r, 100)
	if err != nil {
		t.Fatalf("expected no error for shrunk file, got: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestReadAll_PropagatesNonEOFErrors(t *testing.T) {
	want := errors.New("disk failure")
	_, err := readAll(errReader{err: want}, 100)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, want) {
		t.Errorf("got %v, want wrapping %v", err, want)
	}
}

func TestReadAll_ZeroSizeFile(t *testing.T) {
	got, err := readAll(&shortReader{data: nil, chunkSize: 1}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d bytes, want 0", len(got))
	}
}

// FUSE / virtual / network filesystems can report Stat.Size = 0 for
// non-empty files (the size is computed lazily on read). readAll must
// drain the reader instead of trusting the size hint and returning early
// — otherwise the file is silently skipped and downstream parsing sees
// nil bytes.
func TestReadAll_StatZeroForNonEmptyFile(t *testing.T) {
	data := []byte(`locals { x = "y" }` + "\n")
	r := &shortReader{data: data, chunkSize: 8}

	// Caller passes 0 because Stat misreported (FUSE-style). readAll must
	// still return the actual content.
	got, err := readAll(r, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

// File grew between Stat and Read: caller passes the (now-stale) size,
// but the reader has more bytes available. readAll must read everything
// up to the maximum allowed size, not stop at the stale size.
func TestReadAll_FileGrewBetweenStatAndRead(t *testing.T) {
	data := []byte("locals { x = \"y\" }\n# extra content added after Stat\n")
	r := &shortReader{data: data, chunkSize: 8}

	got, err := readAll(r, 5) // stale Stat — file is much bigger now
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("got %q, want %q (full content)", got, data)
	}
}

// TestParseDir_ParallelBranch_NoRace stresses the parallel branch of
// ParseDir (>4 files) under the race detector. The original concern was
// goroutine closure capture of the for-range loop variables: in Go 1.21
// and earlier, `for i, e := range ...` reused i and e across iterations,
// so concurrent closures could observe the wrong values.
//
// Go 1.22+ creates fresh `i` and `e` per iteration, so the closures in
// hcl.go:ParseDir are safe. go.mod declares go 1.26.3, well past that
// boundary. This test exercises the parallel path repeatedly and
// asserts every file lands in its own results slot. Run with
// `go test -race` to catch any data race that might creep in via a
// future refactor.
func TestParseDir_ParallelBranch_NoRace(t *testing.T) {
	t.Parallel()

	const numFiles = 32 // well over the parallelThreshold = 4
	dir := t.TempDir()
	for i := 0; i < numFiles; i++ {
		// Each file's content uniquely encodes its index so we can verify
		// it ends up in the correct results slot.
		path := filepath.Join(dir, fmt.Sprintf("file_%02d.tf", i))
		content := fmt.Sprintf(`locals { idx_%02d = "marker_%02d" }`+"\n", i, i)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Run ParseDir many times to expose any goroutine ordering bugs.
	const iterations = 50
	for it := 0; it < iterations; it++ {
		files, violations, _ := ParseDir(context.Background(), dir)
		if len(violations) != 0 {
			t.Fatalf("iteration %d: unexpected violations: %v", it, violations)
		}
		if len(files) != numFiles {
			t.Fatalf("iteration %d: got %d files, want %d", it, len(files), numFiles)
		}

		// Build a name -> body-marker map and assert each file's content
		// matches its filename. If the loop-variable capture were broken,
		// some files would have content from the wrong source.
		seen := make(map[string]bool, numFiles)
		for _, f := range files {
			seen[f.Name] = true
			// Body source should contain the "marker_NN" matching the file
			// index in the name.
			expected := strings.TrimSuffix(strings.TrimPrefix(f.Name, "file_"), ".tf")
			marker := "marker_" + expected
			if !bytes.Contains(f.Src, []byte(marker)) {
				t.Errorf("iteration %d: file %s body does not contain %q\nbody: %s",
					it, f.Name, marker, f.Src)
			}
		}
		if len(seen) != numFiles {
			t.Errorf("iteration %d: only %d unique files seen, want %d", it, len(seen), numFiles)
		}
	}
}
