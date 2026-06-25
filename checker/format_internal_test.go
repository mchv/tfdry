package checker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// leftoverFmtTemps returns the names of any files in dir whose names match
// the temp-file pattern used by writeFormatted (".tfdry-fmt-*"). Centralises
// the prefix check so both the C41 race test and the C45 success-path test
// use the same detection logic. C45: the previous inline check did
// `filepath.Ext(name) == ""` but filepath.Ext(".tfdry-fmt-123") returns
// the whole string, not "", so the assertion was dead.
func leftoverFmtTemps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var leftovers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tfdry-fmt-") {
			leftovers = append(leftovers, e.Name())
		}
	}
	return leftovers
}

// C45 regression guard for the detection helper itself. Drops a file whose
// name matches the temp pattern, then asserts leftoverFmtTemps surfaces it.
// Pre-fix, the inline check in TestWriteFormatted_RaceToSymlink_RefusesRename
// used filepath.Ext()=="", which never matches for dot-prefix names — so
// even an actual leak wouldn't be caught. This test pins the new logic.
func TestLeftoverFmtTemps_DetectsRealTempName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".tfdry-fmt-abc123"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := leftoverFmtTemps(t, dir)
	if len(got) != 1 || got[0] != ".tfdry-fmt-abc123" {
		t.Errorf("leftoverFmtTemps() = %v, want [.tfdry-fmt-abc123]", got)
	}
}

// C45: success path of writeFormatted must not leave any temp behind.
// On success, renamed=true and the deferred cleanup skips Remove because
// os.Rename already moved the temp file to its final location. Direct
// regression test (previously the C41 test exercised this property as a
// side-check, but its assertion was dead code — see leftoverFmtTemps).
func TestWriteFormatted_SuccessPath_NoLeftoverTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink rejection uses Unix permissions; not the focus here")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(target, []byte("locals { x = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := writeFormatted(target, []byte("locals {\n  x = 1\n}\n"))
	if err != nil || !ok {
		t.Fatalf("writeFormatted: ok=%v err=%v (want true, nil)", ok, err)
	}
	if leftovers := leftoverFmtTemps(t, dir); len(leftovers) > 0 {
		t.Errorf("leftover temp files after successful writeFormatted: %v", leftovers)
	}
}

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
	if err := os.WriteFile(target, []byte("locals { x = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Outside file the symlink will eventually point at.
	otherTarget := filepath.Join(dir, "elsewhere.tf")
	if err := os.WriteFile(otherTarget, []byte("locals { y = 2 }\n"), 0o644); err != nil {
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
	//      C45: prior to this commit the inline check used
	//      filepath.Ext(name) == "", which never matches dot-prefixed
	//      temp names (Ext returns the whole string in that case). The
	//      helper now does the right prefix check, so this assertion
	//      actually exercises the cleanup path.
	if leftovers := leftoverFmtTemps(t, dir); len(leftovers) > 0 {
		t.Errorf("leftover temp files after raced rename: %v", leftovers)
	}
}
