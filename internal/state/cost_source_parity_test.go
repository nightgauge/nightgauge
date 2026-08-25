package state

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// tsCostSourceEnumRegexp reads the `cost_source: z.enum([...])` literal out of
// the TypeScript execution-history schema. Deliberately anchored on the
// property name and an INLINE array literal: Go is the sole writer of this
// field and that Zod enum is its only validator, so the vocabulary must be
// readable from both languages in one place. Rewriting the enum as a reference
// to a shared const (or splitting it across lines with comments interleaved)
// breaks this pin — move the pin with the literal rather than deleting it.
var tsCostSourceEnumRegexp = regexp.MustCompile(`(?m)^\s*cost_source:\s*z\.enum\(\[([^\]]*)\]\)`)

var tsEnumMemberRegexp = regexp.MustCompile(`"([^"]*)"`)

// TestCostSourcesPinnedToTSSchema is the value-level pin the cost_source.go
// package comment noted was missing (#682) and that #890 made load-bearing by
// adding a fourth member. TestHistorySchemaParity_StageTokens pins the FIELD
// NAME across both languages; nothing pinned the VALUE vocabulary, so a value
// added on the Go side alone would write records the TypeScript reader rejects
// at parse time — silently, in a reader that treats a rejected record as an
// absent one.
func TestCostSourcesPinnedToTSSchema(t *testing.T) {
	source, err := os.ReadFile(executionHistoryTSSchemaPath)
	if err != nil {
		t.Fatalf("cannot read the TS authority at %s: %v\n"+
			"This pin is path-coupled: if executionHistory.ts moved or was renamed, move "+
			"executionHistoryTSSchemaPath with it — do NOT delete the pin.", executionHistoryTSSchemaPath, err)
	}

	matches := tsCostSourceEnumRegexp.FindAllStringSubmatch(string(source), -1)
	if len(matches) != 1 {
		t.Fatalf("found %d inline `cost_source: z.enum([...])` literals in %s, want exactly 1.\n"+
			"The enum was renamed, deleted, or rewritten in a form this pin cannot read "+
			"(a reference to a shared const, say). Keep it an inline literal so both "+
			"languages read the vocabulary from one place.", len(matches), executionHistoryTSSchemaPath)
	}

	var ts []string
	for _, m := range tsEnumMemberRegexp.FindAllStringSubmatch(matches[0][1], -1) {
		ts = append(ts, m[1])
	}
	sort.Strings(ts)

	goValues := append([]string(nil), CostSources...)
	sort.Strings(goValues)

	if strings.Join(ts, ",") != strings.Join(goValues, ",") {
		t.Errorf("cost_source vocabulary drift.\n  Go (state.CostSources): %v\n  TS (%s):                %v\n"+
			"Go writes this field and TypeScript validates it — a value on one side only is "+
			"either a record TS rejects or a value nothing ever writes. Add it to both.",
			goValues, executionHistoryTSSchemaPath, ts)
	}
}
