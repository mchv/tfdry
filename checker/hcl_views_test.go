// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/mchv/tfdry/checker"
)

func TestParseDirViewsSharesParsedFilesAndAppliesFilenamePrecedence(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf":     `locals { selected = "terraform" }`,
		"main.tofu":   `locals { selected = "opentofu" }`,
		"output.tf":   `output "x" { value = local.selected }`,
		"ignored.txt": `ignored`,
	})
	views, err := checker.ParseDirViews(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(views.PhysicalViolations) != 0 || len(views.SemanticViolations) != 0 {
		t.Fatalf("unexpected violations: physical=%v semantic=%v", views.PhysicalViolations, views.SemanticViolations)
	}
	physical := parsedNames(views.PhysicalFiles)
	semantic := parsedNames(views.SemanticFiles)
	if !slices.Equal(physical, []string{"main.tf", "main.tofu", "output.tf"}) {
		t.Fatalf("physical files = %v", physical)
	}
	if !slices.Equal(semantic, []string{"main.tofu", "output.tf"}) {
		t.Fatalf("semantic files = %v", semantic)
	}
	var physicalTofu, semanticTofu *checker.ParsedFile
	for i := range views.PhysicalFiles {
		if views.PhysicalFiles[i].Name == "main.tofu" {
			physicalTofu = &views.PhysicalFiles[i]
		}
	}
	for i := range views.SemanticFiles {
		if views.SemanticFiles[i].Name == "main.tofu" {
			semanticTofu = &views.SemanticFiles[i]
		}
	}
	if physicalTofu == nil || semanticTofu == nil {
		t.Fatal("main.tofu missing from one view")
	}
	if physicalTofu.Body != semanticTofu.Body {
		t.Fatal("semantic and physical views do not share the parsed AST")
	}
	if len(physicalTofu.Src) == 0 || &physicalTofu.Src[0] != &semanticTofu.Src[0] {
		t.Fatal("semantic and physical views do not share source bytes")
	}
}

func TestParseDirViewsFailedAuthoritativeOpenTofuStillShadowsTerraform(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf":   `locals { selected = "terraform" }`,
		"main.tofu": `locals { selected = `,
	})
	views, err := checker.ParseDirViews(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(views.PhysicalViolations, "E001") || !hasCode(views.SemanticViolations, "E001") {
		t.Fatalf("authoritative parse failure missing: physical=%v semantic=%v", codes(views.PhysicalViolations), codes(views.SemanticViolations))
	}
	if got := parsedNames(views.PhysicalFiles); !slices.Equal(got, []string{"main.tf"}) {
		t.Fatalf("physical successful files = %v, want main.tf", got)
	}
	if len(views.SemanticFiles) != 0 {
		t.Fatalf("failed main.tofu fell back to main.tf: %v", parsedNames(views.SemanticFiles))
	}
}

func TestParseDirViewsShadowedTerraformFailureIsPhysicalOnly(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf":   `locals { selected = `,
		"main.tofu": `locals { selected = "opentofu" }`,
	})
	views, err := checker.ParseDirViews(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(views.PhysicalViolations, "E001") {
		t.Fatalf("physical shadowed failure missing: %v", codes(views.PhysicalViolations))
	}
	if len(views.SemanticViolations) != 0 {
		t.Fatalf("shadowed Terraform failure leaked into semantic view: %v", views.SemanticViolations)
	}
	if got := parsedNames(views.SemanticFiles); !slices.Equal(got, []string{"main.tofu"}) {
		t.Fatalf("semantic files = %v, want main.tofu", got)
	}
}

func parsedNames(files []checker.ParsedFile) []string {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Name
	}
	sort.Strings(names)
	return names
}
