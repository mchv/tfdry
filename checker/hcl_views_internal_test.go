// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestParseDirViewsWithParsesEachPhysicalEntryOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`locals { selected = "terraform" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tofu"), []byte(`locals { selected = `), 0o644); err != nil {
		t.Fatal(err)
	}

	counts := make(map[string]int)
	var mu sync.Mutex
	ops := dirParseOps{
		readDir: os.ReadDir,
		parseOne: func(parseDir string, entry os.DirEntry) parseResult {
			mu.Lock()
			counts[entry.Name()]++
			mu.Unlock()
			result := parseOne(parseDir, entry)
			if entry.Name() == "main.tofu" {
				if err := os.WriteFile(filepath.Join(parseDir, entry.Name()), []byte(`locals { selected = "repaired" }`), 0o644); err != nil {
					t.Errorf("rewrite fixture: %v", err)
				}
			}
			return result
		},
	}
	views, err := parseDirViewsWith(context.Background(), dir, ops)
	if err != nil {
		t.Fatal(err)
	}
	if counts["main.tf"] != 1 || counts["main.tofu"] != 1 {
		t.Fatalf("parse counts = %v, want exactly once per file", counts)
	}
	if len(views.PhysicalViolations) != 1 || len(views.SemanticViolations) != 1 {
		t.Fatalf("diagnostics diverged after rewrite: physical=%v semantic=%v", views.PhysicalViolations, views.SemanticViolations)
	}
	if views.PhysicalViolations[0].File != "main.tofu" || views.SemanticViolations[0].File != "main.tofu" {
		t.Fatalf("unexpected diagnostic files: physical=%v semantic=%v", views.PhysicalViolations, views.SemanticViolations)
	}
	if len(views.SemanticFiles) != 0 {
		t.Fatalf("repaired authoritative file was reparsed semantically: %+v", views.SemanticFiles)
	}
}

func TestParseDirViewsWithDoesNotReadOutsideParser(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte(`locals { value = "once" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := dirParseOps{
		readDir: os.ReadDir,
		parseOne: func(parseDir string, entry os.DirEntry) parseResult {
			result := parseOne(parseDir, entry)
			if err := os.Remove(filepath.Join(parseDir, entry.Name())); err != nil {
				t.Errorf("remove one-shot source: %v", err)
			}
			return result
		},
	}
	views, err := parseDirViewsWith(context.Background(), dir, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(views.PhysicalFiles) != 1 || len(views.SemanticFiles) != 1 {
		t.Fatalf("one-shot source was read again: physical=%v semantic=%v", views.PhysicalFiles, views.SemanticFiles)
	}
	if views.PhysicalFiles[0].Body != views.SemanticFiles[0].Body {
		t.Fatal("one-shot projections do not share parsed body")
	}
}

func TestParseDirViewsWithConcurrentBranchParsesEachEntryOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const files = 6
	for i := range files {
		name := fmt.Sprintf("file_%02d.tf", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`locals { value = "ok" }`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	counts := make(map[string]int)
	var mu sync.Mutex
	ops := dirParseOps{
		readDir: os.ReadDir,
		parseOne: func(parseDir string, entry os.DirEntry) parseResult {
			mu.Lock()
			counts[entry.Name()]++
			mu.Unlock()
			return parseOne(parseDir, entry)
		},
	}
	views, err := parseDirViewsWith(context.Background(), dir, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(views.PhysicalFiles) != files || len(views.SemanticFiles) != files {
		t.Fatalf("view sizes = %d/%d, want %d/%d", len(views.PhysicalFiles), len(views.SemanticFiles), files, files)
	}
	for name, count := range counts {
		if count != 1 {
			t.Fatalf("parse count for %s = %d, want 1", name, count)
		}
	}
	if len(counts) != files {
		t.Fatalf("parsed entries = %d, want %d", len(counts), files)
	}
}

func TestParseDirViewsWithCancellationReturnsPartialViews(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.tf", "b.tf"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`locals { value = "ok" }`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	ops := dirParseOps{
		readDir: os.ReadDir,
		parseOne: func(parseDir string, entry os.DirEntry) parseResult {
			calls++
			result := parseOne(parseDir, entry)
			if calls == 1 {
				cancel()
			}
			return result
		},
	}
	views, err := parseDirViewsWith(ctx, dir, ops)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("parse calls = %d, want 1", calls)
	}
	if len(views.PhysicalFiles) != 1 || len(views.SemanticFiles) != 1 {
		t.Fatalf("partial view sizes = %d/%d, want 1/1", len(views.PhysicalFiles), len(views.SemanticFiles))
	}
}

func TestParseDirViewsWithPreCancelledContextSkipsReadDir(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	readCalled := false
	_, err := parseDirViewsWith(ctx, t.TempDir(), dirParseOps{
		readDir: func(string) ([]os.DirEntry, error) {
			readCalled = true
			return nil, nil
		},
		parseOne: parseOne,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if readCalled {
		t.Fatal("readDir called for pre-cancelled context")
	}
}
