// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// Command extract walks a corpus of Terraform (.tf) files and extracts literal
// attribute values matching per-family attribute-name patterns. Output is
// sorted, unique, one value per line, per category.
//
// The extractor uses hclsyntax so it respects HCL structure — heredocs,
// nested blocks, list expressions — and skips values with interpolation
// (${...} / %{...}) because those cannot be statically validated.
//
// Categories and their attribute-name patterns are intentionally wider than
// the eventual check trigger lists: the point of the corpus is to seed
// benchmarks with as much real input as possible, not to define check scope.
//
// Usage:
//
//	go run bench/attr-corpus/cmd/extract/main.go <corpus-dir> <output-dir>
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// category maps a family name to a regexp matching attribute names to harvest.
type category struct {
	name    string
	pattern *regexp.Regexp
}

var categories = []category{
	{name: "cidr", pattern: regexp.MustCompile(`(^|_)cidr(_blocks?)?s?$`)},
	{name: "arn", pattern: regexp.MustCompile(`(^|_)arns?$`)},
	{name: "region", pattern: regexp.MustCompile(`^(aws_)?region$`)},
	{name: "account_id", pattern: regexp.MustCompile(`^(allowed_|aws_|master_|caller_)?account_ids?$`)},
}

// maxValueLen bounds captured strings so a stray large heredoc cannot dominate
// the corpus. Anything longer is a policy document or config blob, not a
// grammar-family value.
const maxValueLen = 1024

// extractString returns the literal string value of an expression together
// with a boolean indicating whether the expression is a valid literal string.
// The bool avoids conflating "not a literal" with "literal empty string": a
// list `[..., ""]` should keep its non-empty elements rather than being
// dropped entirely on the empty one. HCL strings are always template
// expressions; a template with a single literal part is a plain string.
func extractString(e hclsyntax.Expression) (string, bool) {
	tpl, ok := e.(*hclsyntax.TemplateExpr)
	if !ok || len(tpl.Parts) != 1 {
		return "", false
	}
	lit, ok := tpl.Parts[0].(*hclsyntax.LiteralValueExpr)
	if !ok {
		return "", false
	}
	val, diags := lit.Value(nil)
	if diags.HasErrors() || val.IsNull() || !val.Type().Equals(cty.String) {
		return "", false
	}
	return val.AsString(), true
}

// extractStringList returns literal strings from a tuple/list expression. If
// any element is not a valid literal string the whole list is dropped — we
// prefer an empty return to a partial capture that would silently omit
// interpolated or non-string neighbours.
func extractStringList(e hclsyntax.Expression) []string {
	tuple, ok := e.(*hclsyntax.TupleConsExpr)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(tuple.Exprs))
	for _, sub := range tuple.Exprs {
		v, ok := extractString(sub)
		if !ok {
			return nil
		}
		out = append(out, v)
	}
	return out
}

// walk visits every attribute in body (and every nested block) and buckets
// literal values by category. body is expected non-nil (hclsyntax guarantees
// this for a well-formed parse); the guard is defensive against future
// contract changes.
func walk(body *hclsyntax.Body, buckets map[string]map[string]struct{}) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		for _, cat := range categories {
			if !cat.pattern.MatchString(attr.Name) {
				continue
			}
			if v, ok := extractString(attr.Expr); ok {
				buckets[cat.name][v] = struct{}{}
				continue
			}
			for _, v := range extractStringList(attr.Expr) {
				buckets[cat.name][v] = struct{}{}
			}
		}
	}
	for _, block := range body.Blocks {
		walk(block.Body, buckets)
	}
}

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("usage: extract <corpus-dir> <output-dir>")
	}
	corpusDir, outDir := os.Args[1], os.Args[2]

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	buckets := make(map[string]map[string]struct{}, len(categories))
	for _, c := range categories {
		buckets[c.name] = make(map[string]struct{})
	}

	var scanned int
	var parseErrPaths []string
	walkErr := filepath.WalkDir(corpusDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".tf" {
			return nil
		}
		scanned++
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, diags := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			parseErrPaths = append(parseErrPaths, path)
			return nil
		}
		// hclsyntax normally returns a non-nil file+body when diags is clean,
		// but guard against a contract regression that would panic mid-walk.
		if file == nil || file.Body == nil {
			return nil
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			return nil
		}
		walk(body, buckets)
		return nil
	})
	if walkErr != nil {
		log.Fatal(walkErr)
	}

	for _, c := range categories {
		values := make([]string, 0, len(buckets[c.name]))
		for v := range buckets[c.name] {
			if v == "" || len(v) > maxValueLen || strings.ContainsAny(v, "\n\r") {
				continue
			}
			values = append(values, v)
		}
		sort.Strings(values)

		outPath := filepath.Join(outDir, c.name+".txt")
		f, err := os.Create(outPath)
		if err != nil {
			log.Fatal(err)
		}
		for _, v := range values {
			fmt.Fprintln(f, v)
		}
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
	}

	fmt.Fprintf(os.Stderr, "scanned %d .tf files (%d parse errors)\n", scanned, len(parseErrPaths))
	for _, c := range categories {
		fmt.Fprintf(os.Stderr, "  %-12s %d unique values\n", c.name, len(buckets[c.name]))
	}

	// Parse errors indicate the corpus is not being fully harvested, so the
	// resulting values/ may be missing content and shouldn't be trusted for
	// a benchmark refresh. Log the offending paths and exit non-zero so
	// `make bench-corpus-refresh` surfaces the problem rather than silently
	// producing an incomplete corpus.
	if len(parseErrPaths) > 0 {
		fmt.Fprintln(os.Stderr, "\nfiles that failed to parse:")
		for _, p := range parseErrPaths {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		os.Exit(1)
	}
}
