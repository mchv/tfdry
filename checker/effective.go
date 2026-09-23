// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

type effectiveConfig struct {
	files       []ParsedFile
	sourceOrder map[string]int
}

type effectiveBlockLocation struct {
	file  int
	block int
}

type effectiveAttributeLocation struct {
	file  int
	block int
}

func buildEffectiveConfig(files []ParsedFile) effectiveConfig {
	hasOverride := false
	for _, file := range files {
		if isOverrideFilename(file.Name) {
			hasOverride = true
			break
		}
	}
	if !hasOverride {
		return effectiveConfig{files: files}
	}

	primary := make([]ParsedFile, 0, len(files))
	overrides := make([]ParsedFile, 0)
	for _, file := range files {
		if isOverrideFilename(file.Name) {
			overrides = append(overrides, file)
		} else {
			primary = append(primary, file)
		}
	}

	sourceOrder := make(map[string]int, len(files))
	for i, file := range primary {
		sourceOrder[file.Name] = i
	}
	sort.SliceStable(overrides, func(i, j int) bool { return overrides[i].Name < overrides[j].Name })
	for i, file := range overrides {
		sourceOrder[file.Name] = len(primary) + i
	}

	effective := make([]ParsedFile, len(primary))
	for i, file := range primary {
		effective[i] = file
		effective[i].Body = cloneBody(file.Body)
	}

	blocks := make(map[string]effectiveBlockLocation)
	terraformBlocks := make([]effectiveBlockLocation, 0)
	locals := make(map[string]effectiveAttributeLocation)
	for fileIndex := range effective {
		body := effective[fileIndex].Body
		if body == nil {
			continue
		}
		for blockIndex, block := range body.Blocks {
			if block.Type == "terraform" && len(block.Labels) == 0 {
				terraformBlocks = append(terraformBlocks, effectiveBlockLocation{file: fileIndex, block: blockIndex})
			}
			if block.Type == "locals" {
				for name := range block.Body.Attributes {
					if _, exists := locals[name]; !exists {
						locals[name] = effectiveAttributeLocation{file: fileIndex, block: blockIndex}
					}
				}
				continue
			}
			identity := topLevelBlockIdentity(block)
			if identity != "" {
				if _, exists := blocks[identity]; !exists {
					blocks[identity] = effectiveBlockLocation{file: fileIndex, block: blockIndex}
				}
			}
		}
	}

	extraFiles := make(map[string]int)
	for _, override := range overrides {
		if override.Body == nil {
			continue
		}
		for _, block := range override.Body.Blocks {
			if forbiddenOverrideBlock(block.Type) {
				continue
			}
			if block.Type == "locals" {
				for name, attr := range block.Body.Attributes {
					location, exists := locals[name]
					if !exists {
						continue
					}
					replaceEffectiveAttribute(effective, location, name, attr)
				}
				continue
			}
			if block.Type == "terraform" && len(block.Labels) == 0 {
				if len(terraformBlocks) == 0 {
					fileIndex, ok := extraFiles[override.Name]
					if !ok {
						fileIndex = len(effective)
						extraFiles[override.Name] = fileIndex
						effective = append(effective, ParsedFile{Name: override.Name, Body: emptyBodyLike(override.Body)})
					}
					blockIndex := len(effective[fileIndex].Body.Blocks)
					effective[fileIndex].Body.Blocks = append(effective[fileIndex].Body.Blocks, block)
					terraformBlocks = append(terraformBlocks, effectiveBlockLocation{file: fileIndex, block: blockIndex})
				} else {
					applyTerraformOverride(effective, terraformBlocks, block)
				}
				continue
			}

			identity := topLevelBlockIdentity(block)
			location, exists := blocks[identity]
			if !exists {
				if !allowOverrideOnlyBlock(block) {
					continue
				}
				fileIndex, ok := extraFiles[override.Name]
				if !ok {
					fileIndex = len(effective)
					extraFiles[override.Name] = fileIndex
					effective = append(effective, ParsedFile{
						Name: override.Name,
						Body: emptyBodyLike(override.Body),
					})
				}
				blockIndex := len(effective[fileIndex].Body.Blocks)
				effective[fileIndex].Body.Blocks = append(effective[fileIndex].Body.Blocks, block)
				blocks[identity] = effectiveBlockLocation{file: fileIndex, block: blockIndex}
				continue
			}

			base := effective[location.file].Body.Blocks[location.block]
			effective[location.file].Body.Blocks[location.block] = mergeTopLevelBlock(base, block)
		}
	}

	return effectiveConfig{files: effective, sourceOrder: sourceOrder}
}

func applyTerraformOverride(files []ParsedFile, locations []effectiveBlockLocation, override *hclsyntax.Block) {
	primary := make([]*hclsyntax.Block, len(locations))
	for i, location := range locations {
		block := cloneBlock(files[location.file].Body.Blocks[location.block])
		files[location.file].Body.Blocks[location.block] = block
		primary[i] = block
	}
	first := primary[0]

	for name, attr := range override.Body.Attributes {
		for _, block := range primary {
			delete(block.Body.Attributes, name)
		}
		first.Body.Attributes[name] = attr
	}

	replaceTypes := make(map[string]struct{})
	replaceStorage := false
	for _, block := range override.Body.Blocks {
		switch block.Type {
		case "provider_meta", "required_providers":
			continue
		case "backend", "cloud", "state_store":
			replaceStorage = true
		default:
			replaceTypes[effectiveNestedBlockType(block)] = struct{}{}
		}
	}
	for _, block := range primary {
		kept := block.Body.Blocks[:0]
		for _, nested := range block.Body.Blocks {
			if replaceStorage && isTerraformStorageBlock(nested.Type) {
				continue
			}
			if _, replaced := replaceTypes[effectiveNestedBlockType(nested)]; replaced {
				continue
			}
			kept = append(kept, nested)
		}
		block.Body.Blocks = kept
	}

	for _, block := range override.Body.Blocks {
		switch block.Type {
		case "provider_meta":
			continue
		case "required_providers":
			mergeRequiredProvidersOverride(primary, block)
		default:
			first.Body.Blocks = append(first.Body.Blocks, block)
		}
	}
}

func mergeRequiredProvidersOverride(primary []*hclsyntax.Block, override *hclsyntax.Block) {
	if len(override.Body.Attributes) == 0 {
		return
	}
	var target *hclsyntax.Block
	for _, terraformBlock := range primary {
		for i, nested := range terraformBlock.Body.Blocks {
			if nested.Type != "required_providers" {
				continue
			}
			cloned := cloneBlock(nested)
			terraformBlock.Body.Blocks[i] = cloned
			if target == nil {
				target = cloned
			}
			for name := range override.Body.Attributes {
				delete(cloned.Body.Attributes, name)
			}
		}
	}
	if target == nil {
		target = cloneBlock(override)
		primary[0].Body.Blocks = append(primary[0].Body.Blocks, target)
	}
	for name, attr := range override.Body.Attributes {
		target.Body.Attributes[name] = attr
	}
}

func isOverrideFilename(name string) bool {
	ext := filepath.Ext(name)
	if ext != ".tf" && ext != ".tofu" {
		return false
	}
	base := strings.TrimSuffix(name, ext)
	return base == "override" || strings.HasSuffix(base, "_override")
}

func topLevelBlockIdentity(block *hclsyntax.Block) string {
	if block == nil || block.Type == "locals" {
		return ""
	}
	var b strings.Builder
	b.WriteString(block.Type)
	for _, label := range block.Labels {
		b.WriteByte(0)
		b.WriteString(label)
	}
	if block.Type == "provider" {
		b.WriteByte(0)
		if alias, ok := block.Body.Attributes["alias"]; ok {
			b.WriteString(stringLiteralValue(alias.Expr))
		}
	}
	return b.String()
}

func forbiddenOverrideBlock(blockType string) bool {
	return blockType == "check" || blockType == "moved" || blockType == "import" || blockType == "removed"
}

func allowOverrideOnlyBlock(block *hclsyntax.Block) bool {
	if block == nil {
		return false
	}
	if block.Type == "terraform" && len(block.Labels) == 0 {
		return true
	}
	if block.Type == "provider" && len(block.Labels) == 1 {
		_, aliased := block.Body.Attributes["alias"]
		return !aliased
	}
	return false
}

func replaceEffectiveAttribute(files []ParsedFile, location effectiveAttributeLocation, name string, attr *hclsyntax.Attribute) {
	block := cloneBlock(files[location.file].Body.Blocks[location.block])
	block.Body.Attributes[name] = attr
	files[location.file].Body.Blocks[location.block] = block
}

func mergeTopLevelBlock(base, override *hclsyntax.Block) *hclsyntax.Block {
	merged := cloneBlock(base)
	merged.Body = mergeBody(base.Body, override.Body, blockSpecialMergeTypes(base.Type))
	if forbidsOverrideDependsOn(base.Type) {
		if primary, exists := base.Body.Attributes["depends_on"]; exists {
			merged.Body.Attributes["depends_on"] = primary
		} else {
			delete(merged.Body.Attributes, "depends_on")
		}
	}
	restoreForbiddenNestedBlocks(merged.Body, base.Body, base.Type)
	if base.Type == "terraform" {
		replaceTerraformStorage(merged.Body, override.Body)
	}
	return merged
}

func forbidsOverrideDependsOn(blockType string) bool {
	return blockType == "resource" || blockType == "data" || blockType == "ephemeral" || blockType == "module" || blockType == "output"
}

func restoreForbiddenNestedBlocks(merged, primary *hclsyntax.Body, topLevelType string) {
	var forbiddenType string
	switch topLevelType {
	case "variable":
		forbiddenType = "validation"
	case "output":
		forbiddenType = "precondition"
	case "terraform":
		forbiddenType = "provider_meta"
	default:
		return
	}

	kept := merged.Blocks[:0]
	for _, block := range merged.Blocks {
		if block.Type != forbiddenType {
			kept = append(kept, block)
		}
	}
	for _, block := range primary.Blocks {
		if block.Type == forbiddenType {
			kept = append(kept, block)
		}
	}
	merged.Blocks = kept
}

func replaceTerraformStorage(merged, override *hclsyntax.Body) {
	var replacements []*hclsyntax.Block
	for _, block := range override.Blocks {
		if isTerraformStorageBlock(block.Type) {
			replacements = append(replacements, block)
		}
	}
	if len(replacements) == 0 {
		return
	}
	kept := merged.Blocks[:0]
	for _, block := range merged.Blocks {
		if !isTerraformStorageBlock(block.Type) {
			kept = append(kept, block)
		}
	}
	kept = append(kept, replacements...)
	merged.Blocks = kept
}

func isTerraformStorageBlock(blockType string) bool {
	return blockType == "backend" || blockType == "cloud" || blockType == "state_store"
}

func blockSpecialMergeTypes(topLevelType string) map[string]struct{} {
	switch topLevelType {
	case "resource", "data":
		return map[string]struct{}{"lifecycle": {}}
	case "terraform":
		return map[string]struct{}{"required_providers": {}}
	default:
		return nil
	}
}

func mergeBody(base, override *hclsyntax.Body, special map[string]struct{}) *hclsyntax.Body {
	merged := cloneBody(base)
	for name, attr := range override.Attributes {
		merged.Attributes[name] = attr
	}

	overrideByType := make(map[string][]*hclsyntax.Block)
	for _, block := range override.Blocks {
		typeName := effectiveNestedBlockType(block)
		overrideByType[typeName] = append(overrideByType[typeName], block)
	}
	if len(overrideByType) == 0 {
		return merged
	}

	baseByType := make(map[string][]*hclsyntax.Block)
	kept := make([]*hclsyntax.Block, 0, len(base.Blocks)+len(override.Blocks))
	for _, block := range base.Blocks {
		typeName := effectiveNestedBlockType(block)
		if _, replaced := overrideByType[typeName]; replaced {
			baseByType[typeName] = append(baseByType[typeName], block)
			continue
		}
		kept = append(kept, block)
	}

	mergedSpecial := make(map[string]*hclsyntax.Block)
	for typeName, overrideBlocks := range overrideByType {
		if _, mergeSpecial := special[typeName]; !mergeSpecial || len(baseByType[typeName]) == 0 {
			continue
		}
		block := baseByType[typeName][0]
		for _, overrideBlock := range overrideBlocks {
			block = mergeSpecialNestedBlock(typeName, block, overrideBlock)
		}
		mergedSpecial[typeName] = block
	}

	emittedSpecial := make(map[string]struct{})
	for _, overrideBlock := range override.Blocks {
		typeName := effectiveNestedBlockType(overrideBlock)
		if block, exists := mergedSpecial[typeName]; exists {
			if _, emitted := emittedSpecial[typeName]; emitted {
				continue
			}
			emittedSpecial[typeName] = struct{}{}
			kept = append(kept, block)
			continue
		}
		kept = append(kept, overrideBlock)
	}
	merged.Blocks = kept
	return merged
}

func mergeSpecialNestedBlock(typeName string, base, override *hclsyntax.Block) *hclsyntax.Block {
	if typeName != "lifecycle" {
		return mergeNestedBlock(base, override)
	}
	filtered := cloneBlock(override)
	filtered.Body.Blocks = filtered.Body.Blocks[:0]
	for _, block := range override.Body.Blocks {
		if block.Type == "action_trigger" {
			filtered.Body.Blocks = append(filtered.Body.Blocks, block)
		}
	}
	return mergeNestedBlock(base, filtered)
}

func mergeNestedBlock(base, override *hclsyntax.Block) *hclsyntax.Block {
	merged := cloneBlock(base)
	merged.Body = mergeBody(base.Body, override.Body, nil)
	return merged
}

func effectiveNestedBlockType(block *hclsyntax.Block) string {
	if block != nil && block.Type == "dynamic" && len(block.Labels) == 1 {
		return block.Labels[0]
	}
	if block == nil {
		return ""
	}
	return block.Type
}

func cloneBody(body *hclsyntax.Body) *hclsyntax.Body {
	if body == nil {
		return nil
	}
	clone := *body
	clone.Attributes = make(hclsyntax.Attributes, len(body.Attributes))
	for name, attr := range body.Attributes {
		clone.Attributes[name] = attr
	}
	clone.Blocks = append(hclsyntax.Blocks(nil), body.Blocks...)
	return &clone
}

func emptyBodyLike(body *hclsyntax.Body) *hclsyntax.Body {
	clone := *body
	clone.Attributes = make(hclsyntax.Attributes)
	clone.Blocks = nil
	return &clone
}

func cloneBlock(block *hclsyntax.Block) *hclsyntax.Block {
	clone := *block
	clone.Labels = append([]string(nil), block.Labels...)
	clone.LabelRanges = append(clone.LabelRanges[:0:0], block.LabelRanges...)
	clone.Body = cloneBody(block.Body)
	return &clone
}
