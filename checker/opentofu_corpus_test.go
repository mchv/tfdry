// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/mchv/tfdry/checker"
)

type opentofuCorpusExpectation struct {
	lintFiles   []string
	formatFiles []string
}

var opentofuCorpusExpectations = map[string]opentofuCorpusExpectation{
	"language-edition": {
		lintFiles:   []string{"main.tofu"},
		formatFiles: []string{"main.tofu"},
	},
	"mixed-precedence": {
		lintFiles:   []string{"locals.tofu", "output.tf", "shadow.tofu"},
		formatFiles: []string{"locals.tofu", "output.tf", "shadow.tf", "shadow.tofu"},
	},
	"module-conversions": {
		lintFiles:   []string{"main.tofu"},
		formatFiles: []string{"main.tofu", "modules/child/variables.tf", "modules/child/variables.tofu"},
	},
	"native-multifile": {
		lintFiles:   []string{"locals.tofu", "outputs.tofu", "variables.tofu"},
		formatFiles: []string{"locals.tofu", "outputs.tofu", "variables.tofu"},
	},
	"state-encryption": {
		lintFiles:   []string{"main.tofu"},
		formatFiles: []string{"main.tofu"},
	},
}

// TestOpenTofuCompatibilityCorpus runs every immediate subdirectory of
// testdata/opentofu as an independent workspace. Corpus workspaces must be
// parseable and clean under tfdry, and every native file must be canonically
// formatted. New workspace directories must declare their expected file sets.
func TestOpenTofuCompatibilityCorpus(t *testing.T) {
	t.Parallel()

	root := filepath.Join("testdata", "opentofu")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read OpenTofu compatibility corpus: %v", err)
	}

	var workspaces []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			workspaces = append(workspaces, entry.Name())
		}
	}
	if len(workspaces) != len(opentofuCorpusExpectations) {
		t.Fatalf("OpenTofu compatibility corpus has %d workspaces, want %d", len(workspaces), len(opentofuCorpusExpectations))
	}

	for _, name := range workspaces {
		name := name
		expectation, ok := opentofuCorpusExpectations[name]
		if !ok {
			t.Fatalf("OpenTofu compatibility workspace %q has no file-set expectation", name)
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			workspace := filepath.Join(root, name)

			files, parseViolations, err := checker.ParseDir(context.Background(), workspace)
			if err != nil {
				t.Fatalf("ParseDir: %v", err)
			}
			gotLintFiles := make([]string, len(files))
			for i, file := range files {
				gotLintFiles[i] = file.Name
			}
			if !slices.Equal(gotLintFiles, expectation.lintFiles) {
				t.Fatalf("selected lint files = %v, want %v", gotLintFiles, expectation.lintFiles)
			}

			violations := slices.Concat(parseViolations, mustRun(context.Background(), files, nil, workspace))
			if len(violations) != 0 {
				t.Fatalf("compatibility workspace must be clean, got %+v", violations)
			}

			var gotFormatFiles []string
			for _, configDir := range opentofuCorpusConfigDirs(t, workspace) {
				formatFiles, formatParseViolations, err := checker.ParseDirForFormat(context.Background(), configDir)
				if err != nil {
					t.Fatalf("ParseDirForFormat(%s): %v", configDir, err)
				}
				relDir, err := filepath.Rel(workspace, configDir)
				if err != nil {
					t.Fatalf("relative configuration directory: %v", err)
				}
				for _, file := range formatFiles {
					relName := file.Name
					if relDir != "." {
						relName = filepath.Join(relDir, file.Name)
					}
					gotFormatFiles = append(gotFormatFiles, filepath.ToSlash(relName))
				}

				formatViolations, err := checker.CheckFormat(context.Background(), formatFiles)
				if err != nil {
					t.Fatalf("CheckFormat(%s): %v", configDir, err)
				}
				formatViolations = slices.Concat(formatParseViolations, formatViolations)
				if len(formatViolations) != 0 {
					t.Fatalf("compatibility workspace must be canonically formatted, got %+v", formatViolations)
				}
			}
			sort.Strings(gotFormatFiles)
			if !slices.Equal(gotFormatFiles, expectation.formatFiles) {
				t.Fatalf("selected format files = %v, want %v", gotFormatFiles, expectation.formatFiles)
			}
		})
	}
}

func opentofuCorpusConfigDirs(t *testing.T, workspace string) []string {
	t.Helper()

	dirSet := make(map[string]struct{})
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != workspace && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(entry.Name()) {
		case ".tf", ".tofu":
			dirSet[filepath.Dir(path)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk compatibility workspace: %v", err)
	}

	dirs := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}
