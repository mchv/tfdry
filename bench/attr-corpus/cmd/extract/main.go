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

// arnRegexp matches ARN-shape substrings anywhere in a literal string value.
// Deliberately strict: requires all 6 canonical fields with the following
// per-field character classes:
//
//	arn         literal, lowercase only (rejects `ARN:` / `Arn:`, matching
//	                 AWS's own SDK behaviour — see aws-sdk-go-v2/aws/arn).
//	partition   lowercase alphanumeric with dashes, must start with a letter
//	                 (matches `aws`, `aws-cn`, `aws-us-gov`, `aws-iso[-b]`).
//	service     lowercase alphanumeric with dashes, must start with a letter
//	                 (matches `iam`, `s3`, `ec2`, `apigatewayv2`, `route53`).
//	region      one of: empty, `*` (wildcard), or a lowercase alphanumeric
//	                 name starting with a letter (`us-east-1`, `eu-west-3`,
//	                 `ap-southeast-1`). Real AWS regions are always
//	                 letter-prefixed with dashes; the wildcard form appears
//	                 in IAM policies. Rejects `**`, digit-starts, etc.
//	account     one of: empty, `*` (wildcard), the literal `aws` (AWS-managed
//	                 policy convention: arn:aws:iam::aws:policy/...), or
//	                 exactly 12 digits (real account ID). These four exact
//	                 shapes are the only forms AWS emits. Rejects partial
//	                 digits, arbitrary lowercase words, etc.
//	resource    anything up to the next HCL/JSON syntax terminator
//	                 (whitespace, quote, comma, semicolon, closing bracket,
//	                 brace, or paren). Resource may itself contain colons
//	                 and slashes.
//
// The leading `\b` word boundary ensures we don't match an `arn:` that is
// mid-identifier — `notarn:aws:...` must not yield `arn:aws:...`.
//
// Complementary to the name-pattern harvest: name-pattern captures whole-
// value ARNs, this regex captures ARN literals embedded inside larger
// strings (IAM policy heredocs, description text, values under non-`arn`
// attribute names, etc.).
var arnRegexp = regexp.MustCompile(
	`\barn:[a-z][a-z0-9-]*:[a-z][a-z0-9-]*:(?:[a-z][a-z0-9-]*|\*)?:(?:aws|[0-9]{12}|\*)?:[^\s"'` + "`" + `,;)\]}]+`,
)

// trimTrailingPunct removes trailing terminal-sentence punctuation from an
// ARN-shape substring captured by arnRegexp. AWS resource fields can
// contain most characters (including interior dots — S3 bucket names may
// contain `.`) but never end with terminal-sentence punctuation. When
// Path E captures an ARN embedded in prose ("The bucket ARN is
// arn:aws:s3:::foo.") the trailing punctuation is prose, not part of the
// ARN, and stripping it improves signal without dropping valid ARNs.
// The regex's own terminator class already excludes commas and
// semicolons, so we strip the remaining sentence-ending characters here.
func trimTrailingPunct(s string) string {
	return strings.TrimRight(s, ".!?")
}

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
//
// Two harvests run per attribute:
//  1. Name-pattern harvest — the attribute name is matched against each
//     category's regex; on a hit the whole value (scalar or list element)
//     goes into that category's bucket.
//  2. ARN-shape substring harvest — every literal string value (whether or
//     not the attribute name matched anything) is scanned for ARN-shape
//     substrings via arnRegexp; matches go into the "arn" bucket. This
//     complements the name-pattern pass by capturing ARN literals embedded
//     inside inline IAM policy heredocs, description strings, values under
//     non-`arn` attribute names, and any other literal-string context.
func walk(body *hclsyntax.Body, buckets map[string]map[string]struct{}) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		// Cache the extracted values once — both harvests need them.
		strVal, strOK := extractString(attr.Expr)
		var listVals []string
		if !strOK {
			listVals = extractStringList(attr.Expr)
		}

		// Name-pattern harvest.
		for _, cat := range categories {
			if !cat.pattern.MatchString(attr.Name) {
				continue
			}
			if strOK {
				buckets[cat.name][strVal] = struct{}{}
				continue
			}
			for _, v := range listVals {
				buckets[cat.name][v] = struct{}{}
			}
		}

		// ARN-shape substring harvest, name-independent.
		if strOK {
			for _, arn := range arnRegexp.FindAllString(strVal, -1) {
				buckets["arn"][trimTrailingPunct(arn)] = struct{}{}
			}
		}
		for _, v := range listVals {
			for _, arn := range arnRegexp.FindAllString(v, -1) {
				buckets["arn"][trimTrailingPunct(arn)] = struct{}{}
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
			// Skip dot-prefixed and node_modules subdirectories, but never
			// skip the root of the walk itself — otherwise invoking with a
			// hidden corpus path (e.g. `.my-corpus`) or `.` would silently
			// return no files and produce an empty corpus.
			if path == corpusDir {
				return nil
			}
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

	// Harvest 12-digit account IDs embedded in ARN field 5
	// (arn:PARTITION:SERVICE:REGION:ACCOUNT:RESOURCE). This complements
	// the direct `account_id` attribute harvest, which is architecturally
	// near-zero because real Terraform modules use
	// `data.aws_caller_identity.current.account_id` interpolation rather
	// than hardcoded literals. ARN values, in contrast, do get committed
	// with real account numbers embedded, so ARN field 5 is the practical
	// source of account-ID diversity in the corpus.
	//
	// Validation is strict-by-design because the "arn" bucket collects
	// every literal string appearing in any attribute matching
	// `(^|_)arns?$`, which includes real-world placeholders and typos
	// (e.g. `YourPolicyARN`). The gate here rejects anything that isn't
	// a canonically-shaped ARN before scanning field 5:
	//   - Exactly 6 colon-delimited parts (the canonical ARN grammar).
	//     A 5-part malformed ARN could otherwise contribute a spurious
	//     12-digit value from the wrong field position.
	//   - Parts[0] must equal "arn". Rejects placeholder strings that
	//     happen to contain 5 colons.
	// Then the 12-digit-numeric gate on field 5 rejects:
	//   - "aws" (managed-policy convention: arn:aws:iam::aws:policy/...)
	//   - "*" (wildcard account fields)
	//   - "" (global-service ARNs like arn:aws:s3:::my-bucket)
	// Only literal 12-digit strings survive — matching the AWS account-ID
	// contract (12 digits, leading zeros permitted).
	for arn := range buckets["arn"] {
		parts := strings.SplitN(arn, ":", 6)
		if len(parts) != 6 || parts[0] != "arn" {
			continue
		}
		account := parts[4]
		if len(account) != 12 {
			continue
		}
		allDigits := true
		for _, r := range account {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if !allDigits {
			continue
		}
		buckets["account_id"][account] = struct{}{}
	}

	// Filter every bucket to what will actually be written: skip empty
	// strings (interpolated / null sentinels), values above the length cap,
	// and any value carrying embedded newlines. Storing the filtered slices
	// keeps the summary consistent with the values/*.txt files — reporting
	// raw bucket sizes would over-count anything the filter drops.
	filtered := make(map[string][]string, len(categories))
	for _, c := range categories {
		values := make([]string, 0, len(buckets[c.name]))
		for v := range buckets[c.name] {
			if v == "" || len(v) > maxValueLen || strings.ContainsAny(v, "\n\r") {
				continue
			}
			values = append(values, v)
		}
		sort.Strings(values)
		filtered[c.name] = values
	}

	for _, c := range categories {
		outPath := filepath.Join(outDir, c.name+".txt")
		f, err := os.Create(outPath)
		if err != nil {
			log.Fatal(err)
		}
		for _, v := range filtered[c.name] {
			fmt.Fprintln(f, v)
		}
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
	}

	fmt.Fprintf(os.Stderr, "scanned %d .tf files (%d parse errors)\n", scanned, len(parseErrPaths))
	for _, c := range categories {
		fmt.Fprintf(os.Stderr, "  %-12s %d unique values\n", c.name, len(filtered[c.name]))
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
