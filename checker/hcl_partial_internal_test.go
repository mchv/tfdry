// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// TestCollectResults_PartialAndNilFiles pins the partial-collection
// contract used by ParseDir's cancellation paths. The helper must:
//   - Surface every violation, including those from results whose
//     file slot is nil (the file parsed unsuccessfully and only
//     violations were collected).
//   - Skip nil-file entries from the files output.
//   - Tolerate empty slots (zero-value parseResult, meaning the
//     worker never ran because cancellation fired first) — they
//     contribute neither files nor violations.
//
// Together these properties give ParseDir its "partial results on
// cancel" guarantee: workers that completed before the cancellation
// fired are preserved, slots that never ran simply don't appear.
func TestCollectResults_PartialAndNilFiles(t *testing.T) {
	t.Parallel()
	results := []parseResult{
		// Fully populated entry.
		{
			file:       &ParsedFile{Name: "a.tf"},
			violations: []Violation{{Code: "W001", File: "a.tf"}},
		},
		// Parse-failed entry: file=nil, violations present (E001 etc.).
		{
			file:       nil,
			violations: []Violation{{Code: "E001", File: "b.tf"}},
		},
		// Successful parse with no violations.
		{
			file: &ParsedFile{Name: "c.tf"},
		},
		// Empty slot (worker never ran due to cancellation).
		{},
	}
	files, violations := collectResults(results)
	if len(files) != 2 {
		t.Errorf("len(files) = %d, want 2 (a.tf, c.tf)", len(files))
	}
	if len(violations) != 2 {
		t.Errorf("len(violations) = %d, want 2 (W001 + E001)", len(violations))
	}
	wantNames := map[string]bool{"a.tf": false, "c.tf": false}
	for _, f := range files {
		if _, ok := wantNames[f.Name]; !ok {
			t.Errorf("unexpected file name %q", f.Name)
		}
		wantNames[f.Name] = true
	}
	for n, seen := range wantNames {
		if !seen {
			t.Errorf("expected file %q in output, was not present", n)
		}
	}
}

// TestCollectResults_EmptyInput is a basic-shape sanity check that an
// all-zero results slice (length > 0, every entry empty) and a
// zero-length input both return empty slices without panicking. The
// concurrent cancel path can produce a results slice that is entirely
// unpopulated if cancellation fires before any worker grabs a slot.
func TestCollectResults_EmptyInput(t *testing.T) {
	t.Parallel()
	if files, vs := collectResults(nil); len(files) != 0 || len(vs) != 0 {
		t.Errorf("collectResults(nil) = (%v, %v), want empty", files, vs)
	}
	empty := make([]parseResult, 4)
	if files, vs := collectResults(empty); len(files) != 0 || len(vs) != 0 {
		t.Errorf("collectResults(empty×4) = (%v, %v), want empty", files, vs)
	}
}

type nativeConfigTestDirEntry struct {
	name    string
	mode    os.FileMode
	infoErr error
}

func (e nativeConfigTestDirEntry) Name() string      { return e.name }
func (e nativeConfigTestDirEntry) IsDir() bool       { return false }
func (e nativeConfigTestDirEntry) Type() os.FileMode { return 0 }
func (e nativeConfigTestDirEntry) Info() (os.FileInfo, error) {
	if e.infoErr != nil {
		return nil, e.infoErr
	}
	return nativeConfigTestFileInfo(e), nil
}

type nativeConfigTestFileInfo nativeConfigTestDirEntry

func (i nativeConfigTestFileInfo) Name() string       { return i.name }
func (i nativeConfigTestFileInfo) Size() int64        { return 0 }
func (i nativeConfigTestFileInfo) Mode() os.FileMode  { return i.mode }
func (i nativeConfigTestFileInfo) ModTime() time.Time { return time.Time{} }
func (i nativeConfigTestFileInfo) IsDir() bool        { return false }
func (i nativeConfigTestFileInfo) Sys() any           { return nil }

func TestNativeConfigEntries_UnknownTypeOpenTofuClassification(t *testing.T) {
	t.Parallel()
	regularTerraform := nativeConfigTestDirEntry{name: "main.tf", mode: 0o644}
	tests := []struct {
		name      string
		entries   []os.DirEntry
		wantNames []string
	}{
		{
			name: "regular tofu shadows terraform",
			entries: []os.DirEntry{
				regularTerraform,
				nativeConfigTestDirEntry{name: "main.tofu", mode: 0o644},
			},
			wantNames: []string{"main.tofu"},
		},
		{
			name: "symlink tofu is excluded and does not shadow terraform",
			entries: []os.DirEntry{
				regularTerraform,
				nativeConfigTestDirEntry{name: "main.tofu", mode: os.ModeSymlink | 0o777},
			},
			wantNames: []string{"main.tf"},
		},
		{
			name: "unreadable tofu metadata is excluded when terraform peer exists",
			entries: []os.DirEntry{
				regularTerraform,
				nativeConfigTestDirEntry{name: "main.tofu", mode: 0o644, infoErr: errors.New("metadata unavailable")},
			},
			wantNames: []string{"main.tf"},
		},
		{
			name: "standalone tofu with unreadable metadata remains a parse candidate",
			entries: []os.DirEntry{
				nativeConfigTestDirEntry{name: "main.tofu", mode: 0o644, infoErr: errors.New("metadata unavailable")},
			},
			wantNames: []string{"main.tofu"},
		},
		{
			name: "standalone tofu symlink is excluded",
			entries: []os.DirEntry{
				nativeConfigTestDirEntry{name: "main.tofu", mode: os.ModeSymlink | 0o777},
			},
			wantNames: nil,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			selected := nativeConfigEntries(tc.entries)
			got := make([]string, len(selected))
			for i, entry := range selected {
				got[i] = entry.Name()
			}
			if !slices.Equal(got, tc.wantNames) {
				t.Fatalf("nativeConfigEntries() = %v, want %v", got, tc.wantNames)
			}
		})
	}
}
