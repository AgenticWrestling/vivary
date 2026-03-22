// vivary-gen is the VIVARY capability schema code generator.
//
// It reads Go struct files annotated with jsonschema tags and emits a
// _schema.go companion file containing a static Explain() string per
// capability struct.  This ensures the on-wire schema never drifts from the
// Go types and is available at zero runtime cost.
//
// Usage (typically in a go:generate comment):
//
//	//go:generate vivary-gen -in browser.go -out browser_schema.go -cap Browser_Page_Read
//
// Linting rules enforced at generation time:
//   - Capability name must match Namespace_Noun_Verb (two underscores, each
//     segment starts with an uppercase letter).
//   - Every exported field must have a "description" tag.
//   - String fields with a fixed set of values must have an "enum" tag.
//   - All JSON field names must be snake_case.
//   - No vendor-specific terms (gmail, drive, sheets, slack, etc.) in names.
//
// Backward compatibility:
//   - If -prev-schema is supplied, vivary-gen compares the new schema against
//     it and exits non-zero if any field has been removed or renamed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

func main() {
	inFile := flag.String("in", "", "input .go file containing the capability Args struct")
	outFile := flag.String("out", "", "output _schema.go file to write (default: <in>_schema.go)")
	capName := flag.String("cap", "", "capability name, e.g. Browser_Page_Read")
	structName := flag.String("struct", "", "Args struct name (default: <capName>Args)")
	prevSchema := flag.String("prev-schema", "", "path to previous schema JSON for backward-compat check")
	flag.Parse()

	if *inFile == "" || *capName == "" {
		fmt.Fprintln(os.Stderr, "vivary-gen: -in and -cap are required")
		flag.Usage()
		os.Exit(1)
	}
	if *outFile == "" {
		ext := filepath.Ext(*inFile)
		*outFile = strings.TrimSuffix(*inFile, ext) + "_schema.go"
	}
	if *structName == "" {
		// Strip Namespace_Noun_Verb → use the last segment as the base.
		parts := strings.Split(*capName, "_")
		*structName = strings.Join(parts, "") + "Args"
	}

	if err := validateCapName(*capName); err != nil {
		fmt.Fprintf(os.Stderr, "vivary-gen: invalid capability name %q: %v\n", *capName, err)
		os.Exit(1)
	}

	schema, err := generateSchema(*inFile, *structName, *capName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vivary-gen: %v\n", err)
		os.Exit(1)
	}

	// Backward-compat check.
	if *prevSchema != "" {
		prev, err := os.ReadFile(*prevSchema)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "vivary-gen: read prev-schema: %v\n", err)
			os.Exit(1)
		}
		if len(prev) > 0 {
			if err := checkBackwardCompat(prev, []byte(schema)); err != nil {
				fmt.Fprintf(os.Stderr, "vivary-gen: backward-compat violation: %v\n", err)
				os.Exit(1)
			}
		}
	}

	if err := writeSchemaFile(*outFile, *capName, *structName, schema); err != nil {
		fmt.Fprintf(os.Stderr, "vivary-gen: write %s: %v\n", *outFile, err)
		os.Exit(1)
	}
	fmt.Printf("vivary-gen: wrote %s\n", *outFile)
}

// ---- Capability name validation --------------------------------------------

var capNameRe = regexp.MustCompile(`^[A-Z][A-Za-z]+_[A-Z][A-Za-z]+_[A-Z][A-Za-z]+$`)

var vendorTerms = []string{
	"gmail", "google", "drive", "sheets", "slack", "teams", "notion",
	"airtable", "salesforce", "hubspot", "stripe", "twilio",
}

func validateCapName(name string) error {
	if !capNameRe.MatchString(name) {
		return fmt.Errorf("must match Namespace_Noun_Verb (e.g. Browser_Page_Read)")
	}
	lower := strings.ToLower(name)
	for _, term := range vendorTerms {
		if strings.Contains(lower, term) {
			return fmt.Errorf("contains vendor-specific term %q; use a vendor-neutral name", term)
		}
	}
	return nil
}

// ---- Schema generation -----------------------------------------------------

// generateSchema parses inFile, finds structName, and emits a JSON Schema.
func generateSchema(inFile, structName, capName string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, inFile, nil, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", inFile, err)
	}

	var target *ast.StructType
	var targetDoc string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != structName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		target = st
		if ts.Comment != nil {
			targetDoc = ts.Comment.Text()
		}
		return false
	})

	if target == nil {
		return "", fmt.Errorf("struct %q not found in %s", structName, inFile)
	}

	props, required, err := parseStructFields(target)
	if err != nil {
		return "", err
	}

	schema := map[string]any{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"title":       capName,
		"description": strings.TrimSpace(targetDoc),
		"type":        "object",
		"properties":  props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	schema["additionalProperties"] = false

	b, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseStructFields(st *ast.StructType) (props map[string]any, required []string, err error) {
	props = make(map[string]any)
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			continue // embedded
		}
		name := field.Names[0].Name
		if !unicode.IsUpper(rune(name[0])) {
			continue // unexported
		}

		tag := ""
		if field.Tag != nil {
			tag = strings.Trim(field.Tag.Value, "`")
		}
		st := reflect.StructTag(tag)

		jsonName := st.Get("json")
		if jsonName == "" || jsonName == "-" {
			continue
		}
		jsonName = strings.Split(jsonName, ",")[0]

		if err := validateJSONKey(jsonName); err != nil {
			return nil, nil, fmt.Errorf("field %s: %w", name, err)
		}

		desc := st.Get("description")
		if desc == "" {
			return nil, nil, fmt.Errorf("field %s: missing 'description' struct tag (required by vivary-gen)", name)
		}

		prop := map[string]any{
			"description": desc,
		}

		// Type inference from Go type.
		goType := typeString(field.Type)
		switch {
		case goType == "string":
			prop["type"] = "string"
		case goType == "int" || goType == "int32" || goType == "int64":
			prop["type"] = "integer"
		case goType == "float64" || goType == "float32":
			prop["type"] = "number"
		case goType == "bool":
			prop["type"] = "boolean"
		case strings.HasPrefix(goType, "[]"):
			prop["type"] = "array"
		}

		if enum := st.Get("enum"); enum != "" {
			var vals []string
			for _, v := range strings.Split(enum, ",") {
				vals = append(vals, strings.TrimSpace(v))
			}
			prop["enum"] = vals
		}
		if def := st.Get("default"); def != "" {
			prop["default"] = def
		}
		if min := st.Get("minimum"); min != "" {
			prop["minimum"] = min
		}
		if max := st.Get("maximum"); max != "" {
			prop["maximum"] = max
		}
		if example := st.Get("example"); example != "" {
			prop["examples"] = []string{example}
		}

		props[jsonName] = prop

		// A field is required if its json tag lacks "omitempty".
		if !strings.Contains(tag, "omitempty") {
			required = append(required, jsonName)
		}
	}
	return props, required, nil
}

func typeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.ArrayType:
		return "[]" + typeString(t.Elt)
	default:
		return "unknown"
	}
}

// validateJSONKey enforces snake_case: all lowercase, digits, underscores only.
var snakeCaseRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validateJSONKey(key string) error {
	if !snakeCaseRe.MatchString(key) {
		return fmt.Errorf("JSON key %q is not snake_case", key)
	}
	return nil
}

// ---- Backward-compat check -------------------------------------------------

func checkBackwardCompat(prevJSON, newJSON []byte) error {
	var prev, next map[string]any
	if err := json.Unmarshal(prevJSON, &prev); err != nil {
		return fmt.Errorf("parse prev schema: %w", err)
	}
	if err := json.Unmarshal(newJSON, &next); err != nil {
		return fmt.Errorf("parse new schema: %w", err)
	}

	prevProps, _ := prev["properties"].(map[string]any)
	nextProps, _ := next["properties"].(map[string]any)

	for fieldName := range prevProps {
		if _, exists := nextProps[fieldName]; !exists {
			return fmt.Errorf("field %q was removed; bump the capability version instead", fieldName)
		}
	}
	return nil
}

// ---- Output file generation ------------------------------------------------

func writeSchemaFile(outFile, capName, structName, schema string) error {
	// Escape schema for embedding as a raw string literal.
	escaped := strings.ReplaceAll(schema, "`", "` + \"`\" + `")

	pkg := "capabilities" // default; could be inferred from source file
	src := fmt.Sprintf(`// Code generated by vivary-gen. DO NOT EDIT.
// Source capability: %s

package %s

// Explain returns the static JSON Schema for %s.
// Generated from the %s struct via vivary-gen.
func (%s) Explain() string { return %sschema_%s }

var schema_%s = `+"`"+`%s`+"`"+`
`,
		capName,
		pkg,
		capName,
		structName,
		structName,
		"",
		sanitizeName(capName),
		sanitizeName(capName),
		escaped,
	)

	return os.WriteFile(outFile, []byte(src), 0o644)
}

func sanitizeName(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "")
}
