package main

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

// vendorVocabFields is the AC's explicit no-`enum:` list (P3-01): six
// vendor-supplied vocabulary fields named in docs/PLAN.md's ticket text.
// It is deliberately the same list specvalidate's own
// checkNoEnumOnVendorVocab enforces at the parsed-document level; this test
// is the "grep-based test" the AC calls for, run directly against the YAML
// source so a $ref alias or a future refactor of main.go can't silently
// stop enforcing it.
var vendorVocabFields = []string{
	"query_source",
	"decision_source",
	"tool_source",
	"terminal_type",
	"start_type",
	"permission_mode",
}

// propertyKeyPattern matches a YAML mapping key line, e.g. `    tool_source:`
// possibly followed by an inline scalar value on the same line (which this
// test ignores — only a nested block can contain a sibling `enum:`).
var propertyKeyPattern = regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*):(\s|$)`)

// enumKeyPattern matches an `enum:` mapping key at any indentation.
var enumKeyPattern = regexp.MustCompile(`^\s*enum:(\s|$)`)

// TestNoEnumOnVendorVocabFields is the AC's "grep-based test": for each of
// the six named fields, no line `<indent><field>:` in openapi.yaml is
// followed — before a line at the same or shallower indentation ends its
// block — by a nested `enum:` line. This is the YAML-structural equivalent
// of "grep enum: under query_source:", robust to the field name also
// appearing as a plain string value elsewhere in the file (descriptions,
// examples).
func TestNoEnumOnVendorVocabFields(t *testing.T) {
	lines := readSpecLines(t)

	forbidden := make(map[string]bool, len(vendorVocabFields))
	for _, f := range vendorVocabFields {
		forbidden[f] = true
	}

	for i, line := range lines {
		m := propertyKeyPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent, key := m[1], m[2]
		if !forbidden[key] {
			continue
		}

		if enumLine := findNestedEnum(lines, i+1, len(indent)); enumLine != -1 {
			t.Errorf("openapi.yaml:%d: property %q must never declare enum: (SPEC §0 forbids "+
				"enum on vendor-supplied vocabulary — a generated TS union would make an unseen "+
				"value a type error); found enum: at line %d", i+1, key, enumLine+1)
		}
	}
}

// findNestedEnum scans lines[start:] for an `enum:` key whose indentation is
// strictly greater than parentIndent, stopping as soon as it meets a
// non-blank line at parentIndent or shallower (the parent block has ended).
// Returns the 0-based line index of the enum, or -1 if none is found.
func findNestedEnum(lines []string, start, parentIndent int) int {
	for i := start; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent <= parentIndent {
			return -1
		}
		if enumKeyPattern.MatchString(line) {
			return i
		}
	}
	return -1
}

func readSpecLines(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(specPath)
	if err != nil {
		t.Fatalf("open %s: %v", specPath, err)
	}
	defer func() { _ = f.Close() }()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", specPath, err)
	}
	return lines
}
