// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

const (
	nativeHCLSyntax        = "native_hcl"
	terraformDialect       = "terraform"
	opentofuDialect        = "opentofu"
	terraformFileExtension = ".tf"
	opentofuFileExtension  = ".tofu"
)

// SourceSupportInfo describes the configuration sources understood by the
// checker. Dialects identify language compatibility while FileExtensions
// limits that claim to the native-HCL formats the loader actually selects.
type SourceSupportInfo struct {
	Syntax         string
	Dialects       []string
	FileExtensions []string
}

// SourceSupport returns canonical source support metadata. Each call owns its
// slices so callers cannot mutate the metadata observed by later consumers.
func SourceSupport() SourceSupportInfo {
	return SourceSupportInfo{
		Syntax:         nativeHCLSyntax,
		Dialects:       []string{terraformDialect, opentofuDialect},
		FileExtensions: []string{terraformFileExtension, opentofuFileExtension},
	}
}
