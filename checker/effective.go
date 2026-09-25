// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
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
	openTofu := false
	for _, file := range files {
		if filepath.Ext(file.Name) == ".tofu" {
			openTofu = true
		}
		if isOverrideFilename(file.Name) {
			hasOverride = true
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
	copy(effective, primary)

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
	ownedFileBodies := make(map[int]struct{})
	ownedLocalsBlocks := make(map[effectiveAttributeLocation]struct{})
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
					replaceEffectiveAttribute(effective, location, name, attr, ownedFileBodies, ownedLocalsBlocks)
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
						ownedFileBodies[fileIndex] = struct{}{}
					}
					seed := cloneBlock(block)
					seed.Body = emptyBodyLike(block.Body)
					blockIndex := len(effective[fileIndex].Body.Blocks)
					effective[fileIndex].Body.Blocks = append(effective[fileIndex].Body.Blocks, seed)
					terraformBlocks = append(terraformBlocks, effectiveBlockLocation{file: fileIndex, block: blockIndex})
				}
				applyTerraformOverride(effective, terraformBlocks, block, ownedFileBodies)
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
					ownedFileBodies[fileIndex] = struct{}{}
				}
				blockIndex := len(effective[fileIndex].Body.Blocks)
				effective[fileIndex].Body.Blocks = append(effective[fileIndex].Body.Blocks, block)
				blocks[identity] = effectiveBlockLocation{file: fileIndex, block: blockIndex}
				continue
			}

			ensureEffectiveFileBodyOwned(effective, location.file, ownedFileBodies)
			base := effective[location.file].Body.Blocks[location.block]
			effective[location.file].Body.Blocks[location.block] = mergeTopLevelBlock(base, block, openTofu)
		}
	}

	return effectiveConfig{files: effective, sourceOrder: sourceOrder}
}

func applyTerraformOverride(files []ParsedFile, locations []effectiveBlockLocation, override *hclsyntax.Block, ownedFiles map[int]struct{}) {
	primary := make([]*hclsyntax.Block, len(locations))
	for i, location := range locations {
		primary[i] = files[location.file].Body.Blocks[location.block]
	}
	ownedBlocks := make(map[int]struct{})
	own := func(index int) *hclsyntax.Block {
		if _, owned := ownedBlocks[index]; owned {
			return primary[index]
		}
		location := locations[index]
		ensureEffectiveFileBodyOwned(files, location.file, ownedFiles)
		block := cloneBlock(files[location.file].Body.Blocks[location.block])
		files[location.file].Body.Blocks[location.block] = block
		primary[index] = block
		ownedBlocks[index] = struct{}{}
		return block
	}

	for name, attr := range override.Body.Attributes {
		for i, block := range primary {
			if _, exists := block.Body.Attributes[name]; exists {
				delete(own(i).Body.Attributes, name)
			}
		}
		own(0).Body.Attributes[name] = attr
	}

	replaceTypes := make(map[string]struct{})
	replaceStorage := false
	for _, block := range override.Body.Blocks {
		switch block.Type {
		case "provider_meta", "required_providers", "encryption":
			continue
		case "backend", "cloud", "state_store":
			replaceStorage = true
		default:
			replaceTypes[effectiveNestedBlockType(block)] = struct{}{}
		}
	}
	for i, block := range primary {
		needsFilter := false
		for _, nested := range block.Body.Blocks {
			if replaceStorage && isTerraformStorageBlock(nested.Type) {
				needsFilter = true
				break
			}
			if _, replaced := replaceTypes[effectiveNestedBlockType(nested)]; replaced {
				needsFilter = true
				break
			}
		}
		if !needsFilter {
			continue
		}
		owned := own(i)
		kept := owned.Body.Blocks[:0]
		for _, nested := range owned.Body.Blocks {
			if replaceStorage && isTerraformStorageBlock(nested.Type) {
				continue
			}
			if _, replaced := replaceTypes[effectiveNestedBlockType(nested)]; replaced {
				continue
			}
			kept = append(kept, nested)
		}
		owned.Body.Blocks = kept
	}

	for _, block := range override.Body.Blocks {
		switch block.Type {
		case "provider_meta":
			continue
		case "required_providers":
			mergeRequiredProvidersOverride(primary, own, block)
		case "encryption":
			mergeEncryptionOverride(primary, own, block)
		default:
			first := own(0)
			first.Body.Blocks = append(first.Body.Blocks, block)
		}
	}
}

func mergeEncryptionOverride(primary []*hclsyntax.Block, own func(int) *hclsyntax.Block, override *hclsyntax.Block) {
	// OpenTofu permits one primary encryption configuration per module.
	// Additional primary encryption blocks are invalid and intentionally remain
	// separate so an override cannot hide their still-visible expressions.
	for blockIndex, terraformBlock := range primary {
		for nestedIndex, nested := range terraformBlock.Body.Blocks {
			if nested.Type != "encryption" {
				continue
			}
			owned := own(blockIndex)
			owned.Body.Blocks[nestedIndex] = mergeEncryptionBlock(owned.Body.Blocks[nestedIndex], override)
			return
		}
	}
	first := own(0)
	first.Body.Blocks = append(first.Body.Blocks, override)
}

// mergeEncryptionBlock mirrors OpenTofu v1.12 EncryptionConfig.Merge for the
// syntax nodes tfdry observes. Named key providers and methods merge by
// type/name; state, plan, and remote targets retain omitted settings.
func mergeEncryptionBlock(base, override *hclsyntax.Block) *hclsyntax.Block {
	merged := cloneBlock(base)
	for name, attr := range override.Body.Attributes {
		merged.Body.Attributes[name] = attr
	}
	for _, nested := range override.Body.Blocks {
		switch nested.Type {
		case "key_provider", "method":
			mergeNamedEncryptionBlock(merged.Body, nested)
		case "state", "plan":
			mergeSingletonEncryptionTarget(merged.Body, nested)
		case "remote_state_data_sources":
			mergeRemoteEncryptionBlock(merged.Body, nested)
		}
	}
	return merged
}

func mergeNamedEncryptionBlock(body *hclsyntax.Body, override *hclsyntax.Block) {
	for i, existing := range body.Blocks {
		if existing.Type == override.Type && sameBlockLabels(existing.Labels, override.Labels) {
			merged := mergeNestedBlock(existing, override)
			if existing.Type == "key_provider" {
				if alias, exists := existing.Body.Attributes["encrypted_metadata_alias"]; exists {
					merged.Body.Attributes["encrypted_metadata_alias"] = alias
				} else {
					delete(merged.Body.Attributes, "encrypted_metadata_alias")
				}
			}
			body.Blocks[i] = merged
			return
		}
	}
	body.Blocks = append(body.Blocks, override)
}

func mergeSingletonEncryptionTarget(body *hclsyntax.Body, override *hclsyntax.Block) {
	for i, existing := range body.Blocks {
		if existing.Type == override.Type {
			body.Blocks[i] = mergeEncryptionTarget(existing, override)
			return
		}
	}
	body.Blocks = append(body.Blocks, override)
}

func mergeEncryptionTarget(base, override *hclsyntax.Block) *hclsyntax.Block {
	merged := mergeNestedBlock(base, override)
	baseEnforced, baseIsBool := literalBoolAttribute(base.Body.Attributes["enforced"])
	overrideEnforced, overrideIsBool := literalBoolAttribute(override.Body.Attributes["enforced"])
	if baseIsBool && baseEnforced && (!overrideIsBool || !overrideEnforced) {
		merged.Body.Attributes["enforced"] = base.Body.Attributes["enforced"]
	}
	return merged
}

func mergeRemoteEncryptionBlock(body *hclsyntax.Body, override *hclsyntax.Block) {
	for i, existing := range body.Blocks {
		if existing.Type != "remote_state_data_sources" {
			continue
		}
		merged := cloneBlock(existing)
		for _, target := range override.Body.Blocks {
			switch target.Type {
			case "default":
				mergeSingletonEncryptionTarget(merged.Body, target)
			case "remote_state_data_source":
				mergeNamedRemoteTarget(merged.Body, target)
			}
		}
		body.Blocks[i] = merged
		return
	}
	body.Blocks = append(body.Blocks, override)
}

func mergeNamedRemoteTarget(body *hclsyntax.Body, override *hclsyntax.Block) {
	for i, existing := range body.Blocks {
		if existing.Type == override.Type && sameBlockLabels(existing.Labels, override.Labels) {
			body.Blocks[i] = mergeEncryptionTarget(existing, override)
			return
		}
	}
	body.Blocks = append(body.Blocks, override)
}

func literalBoolAttribute(attr *hclsyntax.Attribute) (result, valid bool) {
	if attr == nil {
		return false, false
	}
	value, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || !value.IsKnown() || value.IsNull() {
		return false, false
	}
	value, err := convert.Convert(value, cty.Bool)
	if err != nil {
		return false, false
	}
	return value.True(), true
}

func sameBlockLabels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mergeRequiredProvidersOverride(primary []*hclsyntax.Block, own func(int) *hclsyntax.Block, override *hclsyntax.Block) {
	if len(override.Body.Attributes) == 0 {
		return
	}
	var target *hclsyntax.Block
	for blockIndex, terraformBlock := range primary {
		for nestedIndex, nested := range terraformBlock.Body.Blocks {
			if nested.Type != "required_providers" {
				continue
			}
			owned := own(blockIndex)
			cloned := cloneBlock(owned.Body.Blocks[nestedIndex])
			owned.Body.Blocks[nestedIndex] = cloned
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
		first := own(0)
		first.Body.Blocks = append(first.Body.Blocks, target)
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

func ensureEffectiveFileBodyOwned(files []ParsedFile, fileIndex int, owned map[int]struct{}) {
	if _, alreadyOwned := owned[fileIndex]; alreadyOwned {
		return
	}
	files[fileIndex].Body = cloneBody(files[fileIndex].Body)
	owned[fileIndex] = struct{}{}
}

func replaceEffectiveAttribute(files []ParsedFile, location effectiveAttributeLocation, name string, attr *hclsyntax.Attribute, ownedFiles map[int]struct{}, ownedBlocks map[effectiveAttributeLocation]struct{}) {
	ensureEffectiveFileBodyOwned(files, location.file, ownedFiles)
	if _, alreadyOwned := ownedBlocks[location]; !alreadyOwned {
		block := cloneBlock(files[location.file].Body.Blocks[location.block])
		files[location.file].Body.Blocks[location.block] = block
		ownedBlocks[location] = struct{}{}
	}
	files[location.file].Body.Blocks[location.block].Body.Attributes[name] = attr
}

func mergeTopLevelBlock(base, override *hclsyntax.Block, openTofu bool) *hclsyntax.Block {
	merged := cloneBlock(base)
	merged.Body = mergeBody(base.Body, override.Body, blockSpecialMergeTypes(base.Type), openTofu)
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
		// Terraform/OpenTofu output blocks support preconditions only;
		// postconditions are invalid even in primary configuration.
		forbiddenType = "precondition"
	case "data", "ephemeral":
		// Their lifecycle blocks contain conditions only; override check rules
		// are not applied by the engine's Resource.merge path.
		forbiddenType = "lifecycle"
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
	case "resource":
		return map[string]struct{}{"lifecycle": {}}
	case "action":
		return map[string]struct{}{"config": {}}
	case "terraform":
		return map[string]struct{}{"required_providers": {}}
	default:
		return nil
	}
}

func mergeBody(base, override *hclsyntax.Body, special map[string]struct{}, openTofu bool) *hclsyntax.Body {
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
		if _, mergeSpecial := special[typeName]; !mergeSpecial {
			continue
		}
		var block *hclsyntax.Block
		if len(baseByType[typeName]) != 0 {
			block = baseByType[typeName][0]
		} else if typeName == "lifecycle" {
			block = cloneBlock(overrideBlocks[0])
			block.Body = emptyBodyLike(overrideBlocks[0].Body)
		} else {
			continue
		}
		for _, overrideBlock := range overrideBlocks {
			block = mergeSpecialNestedBlock(typeName, block, overrideBlock, openTofu)
		}
		if len(block.Body.Attributes) == 0 && len(block.Body.Blocks) == 0 {
			block = nil
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
			if block != nil {
				kept = append(kept, block)
			}
			continue
		}
		kept = append(kept, overrideBlock)
	}
	merged.Blocks = kept
	return merged
}

func mergeSpecialNestedBlock(typeName string, base, override *hclsyntax.Block, openTofu bool) *hclsyntax.Block {
	if typeName == "lifecycle" {
		return mergeManagedLifecycleBlock(base, override, openTofu)
	}
	return mergeNestedBlock(base, override)
}

func mergeManagedLifecycleBlock(base, override *hclsyntax.Block, openTofu bool) *hclsyntax.Block {
	merged := cloneBlock(base)
	for _, name := range []string{"create_before_destroy", "prevent_destroy"} {
		if attr, exists := override.Body.Attributes[name]; exists {
			merged.Body.Attributes[name] = attr
		}
	}
	if openTofu {
		if attr, exists := override.Body.Attributes["destroy"]; exists {
			merged.Body.Attributes["destroy"] = attr
		}
	}
	baseIgnoreAll := isIgnoreAllChanges(base.Body.Attributes["ignore_changes"])
	if attr, exists := override.Body.Attributes["ignore_changes"]; exists && !baseIgnoreAll && !isEmptyTuple(attr.Expr) {
		merged.Body.Attributes["ignore_changes"] = attr
	}
	// replace_triggered_by and pre/postconditions are intentionally retained
	// from the primary configuration; Resource.merge does not copy them from an
	// override. Terraform action triggers replace only when the override supplies
	// at least one.
	var actionTriggers []*hclsyntax.Block
	for _, block := range override.Body.Blocks {
		if block.Type == "action_trigger" {
			actionTriggers = append(actionTriggers, block)
		}
	}
	if len(actionTriggers) != 0 {
		kept := merged.Body.Blocks[:0]
		for _, block := range merged.Body.Blocks {
			if block.Type != "action_trigger" {
				kept = append(kept, block)
			}
		}
		kept = append(kept, actionTriggers...)
		merged.Body.Blocks = kept
	}
	return merged
}

func isIgnoreAllChanges(attr *hclsyntax.Attribute) bool {
	if attr == nil {
		return false
	}
	traversal, ok := attr.Expr.(*hclsyntax.ScopeTraversalExpr)
	return ok && len(traversal.Traversal) == 1 && traversal.Traversal.RootName() == "all"
}

func isEmptyTuple(expr hclsyntax.Expression) bool {
	tuple, ok := expr.(*hclsyntax.TupleConsExpr)
	return ok && len(tuple.Exprs) == 0
}

func mergeNestedBlock(base, override *hclsyntax.Block) *hclsyntax.Block {
	merged := cloneBlock(base)
	merged.Body = mergeBody(base.Body, override.Body, nil, false)
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
