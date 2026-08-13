// Command specvalidate lints server/api/openapi.yaml (SPEC §4.4, ticket
// P3-01): kin-openapi structural validation (including every response
// example against its schema) plus the project-specific rules the AC
// demands that kin-openapi itself has no opinion on — every operation
// carries an operationId, at least one 4xx response, and at least one
// example; and no vendor-supplied vocabulary field is pinned down with an
// `enum:` (SPEC §0 — a generated TS union would turn an unseen
// `query_source` into a type error, SPEC §4.4).
//
// It also builds a legacy-router (chi-style `{param}` paths) over the
// document, because P3-09's conformance harness routes ~50 real requests
// through this exact file and needs every operation to be reachable that
// way.
//
// Usage: go run ./internal/tools/specvalidate (run from server/, per the
// `openapi` CI job, SPEC §8.3).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/getkin/kin-openapi/openapi3"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
)

// specPath is the absolute path to server/api/openapi.yaml, resolved from
// this source file's own location (runtime.Caller) rather than the process
// working directory: `go run ./internal/tools/specvalidate` runs with cwd
// server/ (matching the `openapi` CI job's working-directory, SPEC §8.3),
// but `go test ./...` runs with cwd set to this package's own directory —
// a plain relative path would resolve correctly for one and not the other.
var specPath = func() string {
	_, thisFile, _, _ := runtime.Caller(0)
	// this file lives at server/internal/tools/specvalidate/main.go.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "api", "openapi.yaml")
}()

// forbiddenEnumFields are the vendor-supplied vocabulary fields the AC names
// explicitly. vendorVocabFields (main_test.go) is the broader, non-exhaustive
// set the org-wide hard rule actually covers; this tool enforces the AC's
// named subset so its failure messages match the ticket precisely.
var forbiddenEnumFields = []string{
	"query_source",
	"decision_source",
	"tool_source",
	"terminal_type",
	"start_type",
	"permission_mode",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "specvalidate:", err)
		os.Exit(1)
	}
}

func run() error {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", specPath, err)
	}

	ctx := context.Background()
	if err := doc.Validate(ctx); err != nil {
		return fmt.Errorf("openapi validation: %w", err)
	}

	if _, err := legacyrouter.NewRouter(doc); err != nil {
		return fmt.Errorf("build router (paths must be chi-style routable): %w", err)
	}

	var violations []string
	violations = append(violations, checkOperations(doc)...)
	violations = append(violations, checkNoEnumOnVendorVocab(doc, forbiddenEnumFields)...)

	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, "specvalidate:", v)
		}
		return fmt.Errorf("%d violation(s)", len(violations))
	}

	fmt.Printf("specvalidate: OK — %d paths, %d operations, %d schemas, 0 errors\n",
		len(doc.Paths.Map()), countOperations(doc), len(doc.Components.Schemas))
	return nil
}

func countOperations(doc *openapi3.T) int {
	n := 0
	for _, item := range doc.Paths.Map() {
		n += len(item.Operations())
	}
	return n
}

// checkOperations enforces the AC's three structural rules per operation:
// an operationId, at least one 4xx response, and at least one example
// (either on a response's media type or, failing that, on the media type
// of a request body — POST ingest endpoints carry theirs there).
func checkOperations(doc *openapi3.T) []string {
	var violations []string
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			label := fmt.Sprintf("%s %s", method, path)

			if op.OperationID == "" {
				violations = append(violations, label+": missing operationId")
			}

			if !hasStatusCodeClass(op, '4') {
				violations = append(violations, label+": no 4xx response defined")
			}

			if !hasExample(op) {
				violations = append(violations, label+": no example on any response or request body")
			}
		}
	}
	return violations
}

// hasStatusCodeClass reports whether op declares at least one response
// whose status code starts with class (e.g. '4' for 4xx). Non-numeric keys
// ("default") are ignored — SPEC §4.1's error convention is always a
// concrete status code.
func hasStatusCodeClass(op *openapi3.Operation, class byte) bool {
	if op.Responses == nil {
		return false
	}
	for code := range op.Responses.Map() {
		if len(code) == 3 && code[0] == class {
			return true
		}
	}
	return false
}

// hasExample reports whether op has at least one example, on any response's
// media type (Example or the Examples map) or, for operations whose only
// worked payload is the request (the OTLP/hook ingest POSTs), the request
// body's media type.
func hasExample(op *openapi3.Operation) bool {
	if op.Responses != nil {
		for _, respRef := range op.Responses.Map() {
			if respRef.Value == nil {
				continue
			}
			for _, mt := range respRef.Value.Content {
				if mt.Example != nil || len(mt.Examples) > 0 {
					return true
				}
			}
		}
	}
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		for _, mt := range op.RequestBody.Value.Content {
			if mt.Example != nil || len(mt.Examples) > 0 {
				return true
			}
		}
	}
	return false
}

// checkNoEnumOnVendorVocab walks every named schema component and reports
// a violation for each property in fields whose schema declares an `enum`
// — the Go-level mirror of main_test.go's grep test, run against the
// parsed document rather than the raw YAML text so a $ref indirection
// cannot hide a violation from it.
func checkNoEnumOnVendorVocab(doc *openapi3.T, fields []string) []string {
	forbidden := make(map[string]bool, len(fields))
	for _, f := range fields {
		forbidden[f] = true
	}

	var violations []string
	seen := map[*openapi3.Schema]bool{}
	for name, schemaRef := range doc.Components.Schemas {
		if schemaRef == nil || schemaRef.Value == nil {
			continue
		}
		walkProperties(name, schemaRef.Value, forbidden, seen, &violations)
	}
	return violations
}

func walkProperties(schemaName string, schema *openapi3.Schema, forbidden map[string]bool, seen map[*openapi3.Schema]bool, violations *[]string) {
	if schema == nil || seen[schema] {
		return
	}
	seen[schema] = true

	for propName, propRef := range schema.Properties {
		if propRef == nil || propRef.Value == nil {
			continue
		}
		if forbidden[propName] && len(propRef.Value.Enum) > 0 {
			*violations = append(*violations, fmt.Sprintf(
				"schema %s: property %q is vendor-supplied vocabulary but declares enum: %v (SPEC §0)",
				schemaName, propName, propRef.Value.Enum))
		}
		walkProperties(schemaName+"."+propName, propRef.Value, forbidden, seen, violations)
	}
	for _, sub := range schema.AllOf {
		if sub != nil && sub.Value != nil {
			walkProperties(schemaName, sub.Value, forbidden, seen, violations)
		}
	}
	for _, sub := range schema.OneOf {
		if sub != nil && sub.Value != nil {
			walkProperties(schemaName, sub.Value, forbidden, seen, violations)
		}
	}
	if schema.Items != nil && schema.Items.Value != nil {
		walkProperties(schemaName+"[]", schema.Items.Value, forbidden, seen, violations)
	}
}
