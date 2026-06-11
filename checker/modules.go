package checker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// SchemaKind is the kind of a TypeSchema node.
type SchemaKind int

const (
	SchemaUnknown SchemaKind = iota // unresolvable — skip checks
	SchemaString
	SchemaNumber
	SchemaBool
	SchemaObject // Fields holds named child schemas
	SchemaList   // Elem holds element schema
	SchemaMap    // Elem holds value schema
	SchemaSet    // Elem holds element schema
)

// TypeSchema is a recursive representation of a Terraform type expression.
//
// TypeSchema describes a *module-side* declared variable type (the right-hand
// side of `type = ...` in a variable block), parsed from module variables.tf
// for E006/E007. Unlike [VarType] — which is a flat enum for caller-side
// expressions — TypeSchema is a tree because Terraform types can nest
// (object({ x = list(string) })). The lowercase isScalar/label methods
// mirror VarType.IsScalar/VarType.Label intentionally so the two can be
// compared side-by-side at module-input check sites.
type TypeSchema struct {
	Kind     SchemaKind
	Fields   map[string]TypeSchema // SchemaObject only
	Elem     *TypeSchema           // SchemaList/Map/Set only
	Optional bool
}

func (s TypeSchema) isScalar() bool {
	return s.Kind == SchemaString || s.Kind == SchemaNumber || s.Kind == SchemaBool
}

func (s TypeSchema) label() string {
	switch s.Kind {
	case SchemaString:
		return "string"
	case SchemaNumber:
		return "number"
	case SchemaBool:
		return "bool"
	case SchemaObject:
		return "object"
	case SchemaList:
		return "list"
	case SchemaMap:
		return "map"
	case SchemaSet:
		return "set"
	default:
		return "unknown"
	}
}

// parseModuleVarSchemas reads all *.tf files in moduleDir and returns a map of
// variable name → TypeSchema. Returns nil if the directory can't be read.
// Results are cached in the provided cache map (keyed by moduleDir).
func parseModuleVarSchemas(moduleDir string, cache map[string]map[string]TypeSchema) map[string]TypeSchema {
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

	schemas := make(map[string]TypeSchema)
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
			fh.Close()
			continue
		}
		src, rerr := readAll(fh, fi.Size())
		fh.Close()
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
				schemas[name] = TypeSchema{Kind: SchemaUnknown}
				continue
			}
			schemas[name] = parseTypeSchema(typeAttr.Expr)
		}
	}

	cache[moduleDir] = schemas
	return schemas
}

// parseTypeSchema converts a Terraform type expression into a TypeSchema.
func parseTypeSchema(expr hclsyntax.Expression) TypeSchema {
	switch e := expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		switch e.Traversal.RootName() {
		case "string":
			return TypeSchema{Kind: SchemaString}
		case "number":
			return TypeSchema{Kind: SchemaNumber}
		case "bool":
			return TypeSchema{Kind: SchemaBool}
		case "any":
			return TypeSchema{Kind: SchemaUnknown}
		}
		// Unrecognised bare identifier (e.g. a typo like `type = mystery`,
		// or a custom-type reference tfdry doesn't model). Return Unknown
		// so type-mismatch checks at callers are skipped — the module is
		// broken, not the caller, so we should not produce false positives
		// downstream.
		return TypeSchema{Kind: SchemaUnknown}

	case *hclsyntax.FunctionCallExpr:
		switch e.Name {
		case "string":
			return TypeSchema{Kind: SchemaString}
		case "number":
			return TypeSchema{Kind: SchemaNumber}
		case "bool":
			return TypeSchema{Kind: SchemaBool}
		case "list", "set":
			// Malformed list()/set() (zero args) or list(a, b) (too many args)
			// should not become a concrete container with Elem=nil — that
			// produces misleading E006 ("declared list, got string") at the
			// caller when the actual problem is the module's broken type
			// constraint. Fail safe: return Unknown so checks are skipped.
			if len(e.Args) != 1 {
				return TypeSchema{Kind: SchemaUnknown}
			}
			elem := parseTypeSchema(e.Args[0])
			s := TypeSchema{Kind: SchemaList, Elem: &elem}
			if e.Name == "set" {
				s.Kind = SchemaSet
			}
			return s
		case "map":
			// Same fail-safe stance for map() / map(a, b) — see "list", "set".
			if len(e.Args) != 1 {
				return TypeSchema{Kind: SchemaUnknown}
			}
			elem := parseTypeSchema(e.Args[0])
			return TypeSchema{Kind: SchemaMap, Elem: &elem}
		case "object":
			return parseObjectSchema(e)
		case "optional":
			if len(e.Args) >= 1 {
				s := parseTypeSchema(e.Args[0])
				s.Optional = true
				return s
			}
			return TypeSchema{Kind: SchemaUnknown, Optional: true}
		case "any":
			return TypeSchema{Kind: SchemaUnknown}
		}
		return TypeSchema{Kind: SchemaUnknown}

	case *hclsyntax.TemplateWrapExpr:
		return parseTypeSchema(e.Wrapped)
	}
	return TypeSchema{Kind: SchemaUnknown}
}

// parseObjectSchema parses object({key=type, ...}) into a TypeSchema.
// Malformed object() forms (wrong arity or non-object literal) return Unknown
// so compareObjectToSchema doesn't flag every key in the caller's literal as
// E007 "unknown field" — the real bug is the module type constraint.
func parseObjectSchema(e *hclsyntax.FunctionCallExpr) TypeSchema {
	if len(e.Args) != 1 {
		return TypeSchema{Kind: SchemaUnknown}
	}
	obj, ok := e.Args[0].(*hclsyntax.ObjectConsExpr)
	if !ok {
		return TypeSchema{Kind: SchemaUnknown}
	}
	s := TypeSchema{Kind: SchemaObject, Fields: make(map[string]TypeSchema)}
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
		if e.Val.Type().FriendlyName() == "string" {
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
func checkModuleInputs(f ParsedFile, dir string, locals map[string]LocalInfo, checks CheckSet, cache map[string]map[string]TypeSchema) []Violation {
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
		// actual defence (see G10 / round 4).
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

// compareExprToSchema recursively compares an expression against a TypeSchema,
// appending E006/E007 violations to out. context is a human-readable path for messages.
func compareExprToSchema(file string, line int, context string, expr hclsyntax.Expression, schema TypeSchema, locals map[string]LocalInfo, checks CheckSet, out *[]Violation) {
	if schema.Kind == SchemaUnknown {
		return
	}
	if schema.Kind == SchemaObject {
		if obj, ok := unwrapExpr(expr).(*hclsyntax.ObjectConsExpr); ok {
			compareObjectToSchema(file, line, context, obj, schema, locals, checks, out)
			return
		}
	}
	// Recursive element-type checking for list/set/map. The kind-mismatch
	// fall-through below still catches "passed an object where list expected"
	// type errors; this branch only fires when the kinds match and we have
	// an Elem schema to validate the contents against.
	if schema.Elem != nil && schema.Elem.Kind != SchemaUnknown {
		switch schema.Kind {
		case SchemaList, SchemaSet:
			if tup, ok := unwrapExpr(expr).(*hclsyntax.TupleConsExpr); ok {
				for i, elemExpr := range tup.Exprs {
					compareExprToSchema(file, line,
						fmt.Sprintf("%s[%d]", context, i),
						elemExpr, *schema.Elem, locals, checks, out)
				}
				return
			}
		case SchemaMap:
			if obj, ok := unwrapExpr(expr).(*hclsyntax.ObjectConsExpr); ok {
				for _, item := range obj.Items {
					key := objectKeyName(item.KeyExpr)
					if key == "" {
						key = "?"
					}
					compareExprToSchema(file, line,
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
	if exprKind != SchemaUnknown && kindIsSequence(exprKind) != kindIsSequence(schema.Kind) {
		*out = append(*out, Violation{
			Code:     "E006",
			Severity: "error",
			File:     file,
			Line:     line,
			Message:  context + ": declared " + schema.label() + ", got " + schemaKindLabel(exprKind),
		})
	}
}

// schemaKindToVarType maps a scalar SchemaKind to its VarType equivalent.
func schemaKindToVarType(k SchemaKind) VarType {
	switch k {
	case SchemaString:
		return TypeString
	case SchemaNumber:
		return TypeNumber
	case SchemaBool:
		return TypeBool
	}
	return TypeUnknown
}

// varTypeToSchemaKind infers the structural SchemaKind from an expression's AST shape,
// following local references through the locals map. seen prevents infinite loops.
func varTypeToSchemaKind(expr hclsyntax.Expression, locals map[string]LocalInfo, seen map[string]struct{}) SchemaKind {
	e := unwrapExpr(expr)
	switch e.(type) {
	case *hclsyntax.TupleConsExpr:
		return SchemaList
	case *hclsyntax.ObjectConsExpr:
		return SchemaObject
	}
	ref, ok := e.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(ref.Traversal) != 2 || ref.Traversal.RootName() != "local" {
		return SchemaUnknown
	}
	attr, ok := ref.Traversal[1].(hcl.TraverseAttr)
	if !ok {
		return SchemaUnknown
	}
	if seen == nil {
		seen = make(map[string]struct{})
	}
	if _, cycle := seen[attr.Name]; cycle {
		return SchemaUnknown // cycle detected
	}
	seen[attr.Name] = struct{}{}
	if li, defined := locals[attr.Name]; defined && li.Expr != nil {
		return varTypeToSchemaKind(li.Expr, locals, seen)
	}
	return SchemaUnknown
}

func schemaKindLabel(k SchemaKind) string {
	switch k {
	case SchemaList:
		return "list"
	case SchemaMap:
		return "map"
	case SchemaSet:
		return "set"
	case SchemaObject:
		return "object"
	default:
		return "unknown"
	}
}

// kindIsSequence reports whether a SchemaKind is a sequence (list/set) vs a mapping (map/object).
func kindIsSequence(k SchemaKind) bool {
	return k == SchemaList || k == SchemaSet
}

// compareObjectToSchema checks each key of an object literal against a SchemaObject.
func compareObjectToSchema(file string, line int, context string, obj *hclsyntax.ObjectConsExpr, schema TypeSchema, locals map[string]LocalInfo, checks CheckSet, out *[]Violation) {
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

// resolveExprType infers the type of an expression, resolving local.X references
// through the locals map when direct inference returns TypeUnknown.
func resolveExprType(expr hclsyntax.Expression, locals map[string]LocalInfo) VarType {
	t := inferExprType(expr)
	if t != TypeUnknown {
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
	if li, defined := locals[attr.Name]; defined {
		return li.Type
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
			if lit, ok := e.Parts[0].(*hclsyntax.LiteralValueExpr); ok && lit.Val.Type().FriendlyName() == "string" {
				return lit.Val.AsString()
			}
		}
	case *hclsyntax.LiteralValueExpr:
		if e.Val.Type().FriendlyName() == "string" {
			return e.Val.AsString()
		}
	case *hclsyntax.TemplateWrapExpr:
		return stringLiteralValue(e.Wrapped)
	}
	return ""
}
