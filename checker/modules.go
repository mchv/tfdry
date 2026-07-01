// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// schemaKind is the kind of a typeSchema node.
type schemaKind int

// schemaKind enumerates the recognised kinds of a Terraform type
// expression. The zero value schemaUnknown signals an unresolvable
// schema (e.g., a remote module reference we didn't load, an unparsable
// type literal) and gates downstream checks so they skip rather than
// emit spurious E007 violations.
const (
	schemaUnknown schemaKind = iota // unresolvable — skip checks
	schemaString
	schemaNumber
	schemaBool
	schemaObject // Fields holds named child schemas
	schemaList   // Elem holds element schema
	schemaMap    // Elem holds value schema
	schemaSet    // Elem holds element schema
)

// typeSchema is a recursive representation of a Terraform type expression.
//
// typeSchema describes a *module-side* declared variable type (the right-hand
// side of `type = ...` in a variable block), parsed from module variables.tf
// for E006/E007. Unlike [VarType] — which is a flat enum for caller-side
// expressions — typeSchema is a tree because Terraform types can nest
// (object({ x = list(string) })). The lowercase isScalar/label methods
// mirror VarType.IsScalar/VarType.Label intentionally so the two can be
// compared side-by-side at module-input check sites.
type typeSchema struct {
	Kind     schemaKind
	Fields   map[string]typeSchema // schemaObject only
	Elem     *typeSchema           // schemaList/schemaMap/schemaSet only
	Optional bool
}

func (s typeSchema) isScalar() bool {
	return s.Kind == schemaString || s.Kind == schemaNumber || s.Kind == schemaBool
}

func (s typeSchema) label() string {
	switch s.Kind {
	case schemaString:
		return "string"
	case schemaNumber:
		return "number"
	case schemaBool:
		return "bool"
	case schemaObject:
		return "object"
	case schemaList:
		return "list"
	case schemaMap:
		return "map"
	case schemaSet:
		return "set"
	case schemaUnknown:
		return "unknown"
	default:
		// Out-of-range: a new schemaKind was added without extending
		// this switch, or a caller constructed an invalid enum value.
		// Panic makes the mistake loud at test time rather than
		// silently swallowing it as "unknown" in user-facing violation
		// messages. Safe because schemaKind is unexported: every
		// producer of these values lives in this package and only
		// yields values from the enumerated set.
		panic(fmt.Sprintf("unrecognised schemaKind: %d", s.Kind))
	}
}

// parseModuleVarSchemas reads all *.tf files in moduleDir and returns a map of
// variable name → typeSchema. Returns nil if the directory can't be read.
// Results are cached in the provided cache map (keyed by moduleDir).
func parseModuleVarSchemas(moduleDir string, cache map[string]map[string]typeSchema) map[string]typeSchema {
	// Tolerate a nil cache. Later code writes to cache[moduleDir]
	// (both early-out paths and the success path), which would panic on
	// a nil map. Lazy-init a local cache so callers that don't care
	// about result memoisation (tests, one-shot callers) can pass nil.
	if cache == nil {
		cache = make(map[string]map[string]typeSchema)
	}
	if cached, ok := cache[moduleDir]; ok {
		return cached
	}

	// Reject symlinked module directories to prevent traversal via symlink.
	dfi, err := os.Lstat(moduleDir)
	if err != nil || !dfi.IsDir() {
		cache[moduleDir] = nil
		return nil
	}

	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		cache[moduleDir] = nil
		return nil
	}

	schemas := make(map[string]typeSchema)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".tf" {
			continue
		}
		path := filepath.Join(moduleDir, e.Name())
		// Open with O_NOFOLLOW to atomically reject symlinks (matches parseOne).
		// On Windows oNoFollow = 0; the IsRegular check below provides a
		// best-effort fallback (see checker/nofollow_windows.go).
		fh, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
		if err != nil {
			continue
		}
		fi, err := fh.Stat()
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxFileSize {
			_ = fh.Close()
			continue
		}
		src, rerr := readAll(fh, fi.Size())
		_ = fh.Close()
		if rerr != nil {
			continue
		}
		// readAll is bounded to maxFileSize+1 but is robust against Stat
		// reporting a stale size (FUSE / file grew). Skip oversized files.
		if int64(len(src)) > maxFileSize {
			continue
		}
		f, diags := hclsyntax.ParseConfig(src, e.Name(), hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			continue
		}
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, block := range body.Blocks {
			if block.Type != "variable" || len(block.Labels) != 1 {
				continue
			}
			name := block.Labels[0]
			typeAttr, ok := block.Body.Attributes["type"]
			if !ok {
				schemas[name] = typeSchema{Kind: schemaUnknown}
				continue
			}
			schemas[name] = parseTypeSchema(typeAttr.Expr)
		}
	}

	cache[moduleDir] = schemas
	return schemas
}

// parseTypeSchema converts a Terraform type expression into a typeSchema.
func parseTypeSchema(expr hclsyntax.Expression) typeSchema {
	switch e := expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		switch e.Traversal.RootName() {
		case "string":
			return typeSchema{Kind: schemaString}
		case "number":
			return typeSchema{Kind: schemaNumber}
		case "bool":
			return typeSchema{Kind: schemaBool}
		case "any":
			return typeSchema{Kind: schemaUnknown}
		}
		// Unrecognised bare identifier (e.g. a typo like `type = mystery`,
		// or a custom-type reference tfdry doesn't model). Return Unknown
		// so type-mismatch checks at callers are skipped — the module is
		// broken, not the caller, so we should not produce false positives
		// downstream.
		return typeSchema{Kind: schemaUnknown}

	case *hclsyntax.FunctionCallExpr:
		// HCL type keywords like `string` / `number` / `bool` are
		// ScopeTraversalExpr in well-formed type constraints
		// (`type = string`). If they parse as FunctionCallExpr, the
		// constraint is malformed (e.g. `type = string(bad)`). Same
		// fail-safe stance as list/set/map below: return Unknown so
		// downstream compareExprToSchema doesn't emit misleading E006
		// against a broken declaration.
		switch e.Name {
		case "string":
			if len(e.Args) != 0 {
				return typeSchema{Kind: schemaUnknown}
			}
			return typeSchema{Kind: schemaString}
		case "number":
			if len(e.Args) != 0 {
				return typeSchema{Kind: schemaUnknown}
			}
			return typeSchema{Kind: schemaNumber}
		case "bool":
			if len(e.Args) != 0 {
				return typeSchema{Kind: schemaUnknown}
			}
			return typeSchema{Kind: schemaBool}
		case "list", "set":
			// Malformed list()/set() (zero args) or list(a, b) (too many args)
			// should not become a concrete container with Elem=nil — that
			// produces misleading E006 ("declared list, got string") at the
			// caller when the actual problem is the module's broken type
			// constraint. Fail safe: return Unknown so checks are skipped.
			if len(e.Args) != 1 {
				return typeSchema{Kind: schemaUnknown}
			}
			elem := parseTypeSchema(e.Args[0])
			s := typeSchema{Kind: schemaList, Elem: &elem}
			if e.Name == "set" {
				s.Kind = schemaSet
			}
			return s
		case "map":
			// Same fail-safe stance for map() / map(a, b) — see "list", "set".
			if len(e.Args) != 1 {
				return typeSchema{Kind: schemaUnknown}
			}
			elem := parseTypeSchema(e.Args[0])
			return typeSchema{Kind: schemaMap, Elem: &elem}
		case "object":
			return parseObjectSchema(e)
		case "optional":
			if len(e.Args) >= 1 {
				s := parseTypeSchema(e.Args[0])
				s.Optional = true
				return s
			}
			return typeSchema{Kind: schemaUnknown, Optional: true}
		case "any":
			return typeSchema{Kind: schemaUnknown}
		}
		return typeSchema{Kind: schemaUnknown}

	case *hclsyntax.TemplateWrapExpr:
		return parseTypeSchema(e.Wrapped)
	}
	return typeSchema{Kind: schemaUnknown}
}

// parseObjectSchema parses object({key=type, ...}) into a typeSchema.
// Malformed object() forms (wrong arity or non-object literal) return Unknown
// so compareObjectToSchema doesn't flag every key in the caller's literal as
// E007 "unknown field" — the real bug is the module type constraint.
func parseObjectSchema(e *hclsyntax.FunctionCallExpr) typeSchema {
	if len(e.Args) != 1 {
		return typeSchema{Kind: schemaUnknown}
	}
	obj, ok := e.Args[0].(*hclsyntax.ObjectConsExpr)
	if !ok {
		return typeSchema{Kind: schemaUnknown}
	}
	s := typeSchema{Kind: schemaObject, Fields: make(map[string]typeSchema)}
	for _, item := range obj.Items {
		key := objectKeyName(item.KeyExpr)
		if key == "" {
			continue
		}
		s.Fields[key] = parseTypeSchema(item.ValueExpr)
	}
	return s
}

// objectKeyName extracts the string name from an object key expression.
func objectKeyName(expr hclsyntax.Expression) string {
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		// cty.NullVal(cty.String) reports FriendlyName "string" but
		// AsString panics on null. HCL string literals don't normally
		// parse as typed-null, but the defensive check costs nothing.
		if e.Val.Type().FriendlyName() == "string" && !e.Val.IsNull() {
			return e.Val.AsString()
		}
	case *hclsyntax.TemplateExpr:
		if len(e.Parts) == 1 {
			return objectKeyName(e.Parts[0])
		}
	case *hclsyntax.TemplateWrapExpr:
		return objectKeyName(e.Wrapped)
	case *hclsyntax.ScopeTraversalExpr:
		return e.Traversal.RootName()
	case *hclsyntax.ObjectConsKeyExpr:
		if e.ForceNonLiteral {
			return ""
		}
		return objectKeyName(e.Wrapped)
	}
	return ""
}

// moduleMetaArgs are module block arguments that are not variable inputs.
var moduleMetaArgs = map[string]struct{}{
	"source": {}, "version": {},
	"count": {}, "for_each": {}, "depends_on": {}, "providers": {},
}

// checkModuleInputs checks all module blocks in f for type mismatches and unknown
// keys against the module's declared variable schemas. dir is the caller's directory.
func checkModuleInputs(f ParsedFile, dir string, locals map[string]localInfo, checks CheckSet, cache map[string]map[string]typeSchema) []Violation {
	var violations []Violation
	for _, block := range f.Body.Blocks {
		if block.Type != "module" || len(block.Labels) != 1 {
			continue
		}
		sourceAttr, ok := block.Body.Attributes["source"]
		if !ok {
			continue
		}
		source := stringLiteralValue(sourceAttr.Expr)
		if source == "" || !isLocalSource(source) {
			continue
		}

		// Resolve symlinks to detect self-references (source = "." or any
		// path that resolves back to the project root) and to follow any
		// indirection before checks. We intentionally do NOT enforce
		// containment within the project root: parent-relative paths like
		// `../shared/<module>` are the standard monorepo pattern and must
		// be checked. tfdry runs with the user's permissions on the user's
		// own files, so a project-root boundary doesn't add a real security
		// property — symlink rejection on file open (O_NOFOLLOW) is the
		// actual defence (see the EvalSymlinks-based root containment
		// check below).
		moduleDir := filepath.Join(dir, filepath.FromSlash(source))
		realModule, err1 := filepath.EvalSymlinks(moduleDir)
		realDir, err2 := filepath.EvalSymlinks(dir)
		if err1 != nil || err2 != nil {
			continue
		}
		if realModule == realDir {
			continue // self-reference — skip to avoid recursion
		}

		schemas := parseModuleVarSchemas(moduleDir, cache)
		if schemas == nil {
			continue
		}

		modName := block.Labels[0]
		for inputName, inputAttr := range block.Body.Attributes {
			if _, skip := moduleMetaArgs[inputName]; skip {
				continue
			}
			schema, declared := schemas[inputName]
			if !declared {
				if checks.Enabled("E007") {
					violations = append(violations, Violation{
						Code:     "E007",
						Severity: "error",
						File:     f.Name,
						Line:     inputAttr.NameRange.Start.Line,
						Message:  "module \"" + modName + "\" has no variable \"" + inputName + "\"",
					})
				}
				continue
			}
			if checks.Enabled("E006") {
				compareExprToSchema(f.Name, inputAttr.NameRange.Start.Line,
					"module \""+modName+"\" input \""+inputName+"\"",
					inputAttr.Expr, schema, locals, checks, &violations)
			}
		}
	}
	return violations
}

// compareExprToSchema recursively compares an expression against a typeSchema,
// appending E006/E007 violations to out. context is a human-readable path for messages.
func compareExprToSchema(file string, line int, context string, expr hclsyntax.Expression, schema typeSchema, locals map[string]localInfo, checks CheckSet, out *[]Violation) {
	if schema.Kind == schemaUnknown {
		return
	}
	if schema.Kind == schemaObject {
		if obj, ok := unwrapExpr(expr).(*hclsyntax.ObjectConsExpr); ok {
			compareObjectToSchema(file, context, obj, schema, locals, checks, out)
			return
		}
	}
	// Recursive element-type checking for list/set/map. The kind-mismatch
	// fall-through below still catches "passed an object where list expected"
	// type errors; this branch only fires when the kinds match and we have
	// an Elem schema to validate the contents against.
	if schema.Elem != nil && schema.Elem.Kind != schemaUnknown {
		switch schema.Kind {
		case schemaList, schemaSet:
			if tup, ok := unwrapExpr(expr).(*hclsyntax.TupleConsExpr); ok {
				for i, elemExpr := range tup.Exprs {
					// Use the element's own line for multi-line literals
					// so the violation points at the offending item, not
					// the parent attribute.
					elemLine := line
					if r := elemExpr.StartRange(); r.Start.Line > 0 {
						elemLine = r.Start.Line
					}
					compareExprToSchema(file, elemLine,
						fmt.Sprintf("%s[%d]", context, i),
						elemExpr, *schema.Elem, locals, checks, out)
				}
				return
			}
		case schemaMap:
			if obj, ok := unwrapExpr(expr).(*hclsyntax.ObjectConsExpr); ok {
				for _, item := range obj.Items {
					key := objectKeyName(item.KeyExpr)
					if key == "" {
						key = "?"
					}
					// Same idea as the tuple element line tracking above —
					// point at the value expression's line for multi-line
					// object literals.
					valLine := line
					if r := item.ValueExpr.StartRange(); r.Start.Line > 0 {
						valLine = r.Start.Line
					}
					compareExprToSchema(file, valLine,
						fmt.Sprintf("%s[%q]", context, key),
						item.ValueExpr, *schema.Elem, locals, checks, out)
				}
				return
			}
		}
	}
	if !checks.Enabled("E006") {
		return
	}
	exprType := resolveExprType(expr, locals)
	if exprType == TypeUnknown {
		return
	}
	// Scalar vs non-scalar mismatch.
	if schema.isScalar() != exprType.IsScalar() {
		*out = append(*out, Violation{
			Code:     "E006",
			Severity: "error",
			File:     file,
			Line:     line,
			Message:  context + ": declared " + schema.label() + ", got " + exprType.Label(),
		})
		return
	}
	// Both scalar: check specific scalar type (string vs number vs bool).
	if schema.isScalar() && exprType.IsScalar() {
		schemaVarType := schemaKindToVarType(schema.Kind)
		if schemaVarType != TypeUnknown && schemaVarType != exprType {
			*out = append(*out, Violation{
				Code:     "E006",
				Severity: "error",
				File:     file,
				Line:     line,
				Message:  context + ": declared " + schema.label() + ", got " + exprType.Label(),
			})
		}
		return
	}
	// Both non-scalar: flag list/set passed where map/object expected (or vice versa).
	exprKind := varTypeToSchemaKind(expr, locals, nil)
	if exprKind != schemaUnknown && kindIsSequence(exprKind) != kindIsSequence(schema.Kind) {
		*out = append(*out, Violation{
			Code:     "E006",
			Severity: "error",
			File:     file,
			Line:     line,
			Message:  context + ": declared " + schema.label() + ", got " + schemaKindLabel(exprKind),
		})
	}
}

// schemaKindToVarType maps a scalar schemaKind to its VarType equivalent.
func schemaKindToVarType(k schemaKind) VarType {
	switch k {
	case schemaString:
		return TypeString
	case schemaNumber:
		return TypeNumber
	case schemaBool:
		return TypeBool
	}
	return TypeUnknown
}

// varTypeToSchemaKind infers the structural schemaKind from an expression's AST shape,
// following local references through the locals map. seen prevents infinite loops.
func varTypeToSchemaKind(expr hclsyntax.Expression, locals map[string]localInfo, seen map[string]struct{}) schemaKind {
	e := unwrapExpr(expr)
	switch e.(type) {
	case *hclsyntax.TupleConsExpr:
		return schemaList
	case *hclsyntax.ObjectConsExpr:
		return schemaObject
	}
	ref, ok := e.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(ref.Traversal) != 2 || ref.Traversal.RootName() != "local" {
		return schemaUnknown
	}
	attr, ok := ref.Traversal[1].(hcl.TraverseAttr)
	if !ok {
		return schemaUnknown
	}
	if seen == nil {
		seen = make(map[string]struct{})
	}
	if _, cycle := seen[attr.Name]; cycle {
		return schemaUnknown // cycle detected
	}
	seen[attr.Name] = struct{}{}
	if li, defined := locals[attr.Name]; defined && li.Expr != nil {
		return varTypeToSchemaKind(li.Expr, locals, seen)
	}
	return schemaUnknown
}

func schemaKindLabel(k schemaKind) string {
	switch k {
	case schemaList:
		return "list"
	case schemaMap:
		return "map"
	case schemaSet:
		return "set"
	case schemaObject:
		return "object"
	case schemaUnknown, schemaString, schemaNumber, schemaBool:
		// schemaKindLabel intentionally groups scalar and unknown
		// kinds under "unknown" — from callers' perspective the
		// interesting kinds are the compound ones (list/map/set/object).
		return "unknown"
	default:
		// Out-of-range: a new schemaKind was added without extending
		// this switch, or a caller constructed an invalid enum value.
		// Panic to catch forgotten enum extensions loudly at test
		// time. Safe because schemaKind is unexported.
		panic(fmt.Sprintf("unrecognised schemaKind: %d", k))
	}
}

// kindIsSequence reports whether a schemaKind is a sequence (list/set) vs a mapping (map/object).
func kindIsSequence(k schemaKind) bool {
	return k == schemaList || k == schemaSet
}

// compareObjectToSchema checks each key of an object literal against a schemaObject.
//
// The function uses each item's own KeyExpr.StartRange() for the
// per-key violation line — the caller's parse line is irrelevant
// once we're inside the object literal — so no `line` parameter
// is needed (caught by unparam in PR A3).
func compareObjectToSchema(file, context string, obj *hclsyntax.ObjectConsExpr, schema typeSchema, locals map[string]localInfo, checks CheckSet, out *[]Violation) {
	for _, item := range obj.Items {
		key := objectKeyName(item.KeyExpr)
		if key == "" {
			continue
		}
		fieldSchema, exists := schema.Fields[key]
		if !exists {
			if checks.Enabled("E007") {
				*out = append(*out, Violation{
					Code:     "E007",
					Severity: "error",
					File:     file,
					Line:     item.KeyExpr.StartRange().Start.Line,
					Message:  context + " has no field \"" + key + "\"",
				})
			}
			continue
		}
		compareExprToSchema(file, item.ValueExpr.StartRange().Start.Line,
			context+"."+key, item.ValueExpr, fieldSchema, locals, checks, out)
	}
}

// unwrapExpr strips TemplateWrapExpr wrappers to get the inner expression.
func unwrapExpr(expr hclsyntax.Expression) hclsyntax.Expression {
	if tw, ok := expr.(*hclsyntax.TemplateWrapExpr); ok {
		return unwrapExpr(tw.Wrapped)
	}
	return expr
}

// resolveExprType infers the type of an expression, resolving local.X
// references through the locals map when direct inference returns
// TypeUnknown. Transitive chains (local.b → local.a → 1) are resolved by
// recursing through the referenced local's expression with cycle detection,
// matching the same pattern used by varTypeToSchemaKind.
func resolveExprType(expr hclsyntax.Expression, locals map[string]localInfo) VarType {
	return resolveExprTypeRecursive(expr, locals, nil)
}

func resolveExprTypeRecursive(expr hclsyntax.Expression, locals map[string]localInfo, seen map[string]struct{}) VarType {
	if t := inferExprType(expr); t != TypeUnknown {
		return t
	}
	ref, ok := expr.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(ref.Traversal) != 2 || ref.Traversal.RootName() != "local" {
		return TypeUnknown
	}
	attr, ok := ref.Traversal[1].(hcl.TraverseAttr)
	if !ok {
		return TypeUnknown
	}
	// Lazy init + cycle detection — a chain like local.a → local.b →
	// local.a must terminate with TypeUnknown rather than recurse forever.
	if seen == nil {
		seen = make(map[string]struct{})
	}
	if _, cycle := seen[attr.Name]; cycle {
		return TypeUnknown
	}
	seen[attr.Name] = struct{}{}
	li, defined := locals[attr.Name]
	if !defined {
		return TypeUnknown
	}
	// Prefer the cached Type when known (literal/template/etc).
	if li.Type != TypeUnknown {
		return li.Type
	}
	// Otherwise recurse through the referenced local's expression — this
	// is the case for transitive refs whose Type came back Unknown at
	// build time because they pointed at another local.
	if li.Expr != nil {
		return resolveExprTypeRecursive(li.Expr, locals, seen)
	}
	return TypeUnknown
}

// isLocalSource reports whether a module source is a local relative path.
func isLocalSource(source string) bool {
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
}

// stringLiteralValue returns the string value of a literal string expression, or "".
func stringLiteralValue(expr hclsyntax.Expression) string {
	switch e := expr.(type) {
	case *hclsyntax.TemplateExpr:
		if len(e.Parts) == 1 {
			// Defensive null check — see objectKeyName.
			if lit, ok := e.Parts[0].(*hclsyntax.LiteralValueExpr); ok &&
				lit.Val.Type().FriendlyName() == "string" && !lit.Val.IsNull() {
				return lit.Val.AsString()
			}
		}
	case *hclsyntax.LiteralValueExpr:
		// Defensive null check — see objectKeyName.
		if e.Val.Type().FriendlyName() == "string" && !e.Val.IsNull() {
			return e.Val.AsString()
		}
	case *hclsyntax.TemplateWrapExpr:
		return stringLiteralValue(e.Wrapped)
	}
	return ""
}
