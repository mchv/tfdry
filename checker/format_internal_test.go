package checker

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// C41: writeFormatted must Lstat the target path immediately before
// os.Rename, not only at the start of the function. Without this
// defense-in-depth check, a TOCTOU race where an attacker swaps the
// path to a symlink between the initial check and the final rename
// would have Rename silently destroy the symlink and create a regular
// file in its place. Adding a final Lstat catches that race and fails
// closed.
//
// The test uses the writeFormattedBeforeRename hook (production code
// leaves it nil; tests set it to perform the swap deterministically).
func TestWriteFormatted_RaceToSymlink_RefusesRename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on POSIX symlink behaviour")
	}
	dir := t.TempDir()
	// Real file at the target path: writeFormatted's initial Lstat/Open
	// checks will succeed because this is a regular file.
	target := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(target, []byte("locals { x = 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Outside file the symlink will eventually point at.
	otherTarget := filepath.Join(dir, "elsewhere.tf")
	if err := os.WriteFile(otherTarget, []byte("locals { y = 2 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(otherTarget)

	// Install the hook: between the initial check and the final rename,
	// the hook removes `target` and replaces it with a symlink to
	// `otherTarget`. This is exactly the race a malicious attacker (or
	// concurrent process) could create.
	prev := writeFormattedBeforeRename
	writeFormattedBeforeRename = func(p string) {
		if p != target {
			return
		}
		_ = os.Remove(p)
		_ = os.Symlink(otherTarget, p)
	}
	defer func() { writeFormattedBeforeRename = prev }()

	formatted := []byte("locals {\n  x = 1\n}\n")
	ok, err := writeFormatted(target, formatted)
	if err == nil {
		t.Fatalf("expected error from writeFormatted when path becomes a symlink mid-flight, got ok=%v, err=nil", ok)
	}
	if ok {
		t.Fatalf("ok=true with err=%v — should be (false, err)", err)
	}

	// Strong invariants of the defense:
	//   1. The eventual target of the (now-)symlink must NOT have been
	//      overwritten with the formatted content. Rename replacing the
	//      symlink would have left `target` as a regular file (different
	//      file from elsewhere.tf), but it could also have followed the
	//      symlink in some implementations. Either way, otherTarget's
	//      content must be unchanged.
	final, _ := os.ReadFile(otherTarget)
	if string(final) != string(original) {
		t.Errorf("symlink target file was modified despite C41 check; got %q want %q",
			string(final), string(original))
	}
	//   2. The temp file we wrote should not be left behind in dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == "" && len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
