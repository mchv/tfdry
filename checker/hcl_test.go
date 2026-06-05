package checker

import (
	"bytes"
	"errors"
	"io"
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
