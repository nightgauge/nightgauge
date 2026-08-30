package state

// Go ⇄ TypeScript field-name parity for the history JSONL schema (Issue #682,
// AC 4). Go is the sole writer of every V2* record type in this package;
// packages/nightgauge-vscode/src/schemas/executionHistory.ts's Zod schemas are
// the sole validator. #682 happened because those two declarations drifted —
// TS declared `cost_source` and Go's V2StageTokens had no field for it at all
// — and nothing in the repo would have failed on that drift. This file is
// that guard: it reads the actual Zod source at test time (not a hand-copied
// literal, the same reasoning TestModelSelectionSourcesPinnedToSDK uses for
// the model_selection.source VALUE vocabulary) and compares each pinned Go
// struct's `json` tag field set against the corresponding TS `z.object({...
// })`'s property-name set.
//
// WHAT THIS DOES NOT COVER (read before adding a third pair): the extraction
// below is intentionally NOT a general recursive schema walker. It handles
// exactly the shape HistoryStageTokenUsageSchema and StageGateResultSchema
// both have — a FLAT z.object({...}) whose property values are leaf types
// (z.number(), z.string(), an .optional() enum, or a reference to another
// named schema) with no NESTED z.object({...}) literal inside the body. A
// schema with a nested inline z.object({...}) (V2Tokens.ptc_metrics's
// PTCMetricsSchema reference is fine — that is a NAME reference, not an
// inline nested literal — but files/routing on ExecutionHistoryRunRecordSchema
// use an inline `z.object({...}).optional()`) would have its nested body's
// keys mistaken for top-level keys by the brace-depth-0 extraction here. Pin
// nested schemas by naming them (as HistoryStageTokenUsageSchema and
// StageGateResultSchema already are) and adding a THIRD assertFieldParity
// call for the nested schema's own Go struct, not by trying to make this
// walker recurse.
//
// It also does not attempt V2Tokens/TokensSchema or V2RunRecord/
// ExecutionHistoryRunRecordV2Schema: a prerequisite scan (#682) found those
// two structs already disagree on fields unrelated to cost_source. Two of
// those — `adapter_source` and `adapter_fallback_chain_used` — were deleted
// from the TS schema by #693, having had neither a writer nor a reader in
// either language; `model` remains and is justified in the
// stageTokensKnownGaps allowlist below.

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// executionHistoryTSSchemaPath is the SDK/TS authority this test reads at
// test time. Path-coupled: if the file moves, move this constant with it —
// do not delete the pin (mirrors model_selection_source_test.go's own
// path-coupling comment).
const executionHistoryTSSchemaPath = "../../packages/nightgauge-vscode/src/schemas/executionHistory.ts"

// stageTokensKnownGaps lists TS-only HistoryStageTokenUsageSchema fields that
// predate #682 and are OUT OF SCOPE for it. It is a HOLDING PEN, not a
// resting place, and it held three entries. #693 emptied two of them the way
// the holding-pen comment always intended: by DELETING the
// declared-and-unused fields (`adapter_source`, `adapter_fallback_chain_used`)
// rather than inventing a Go writer for them. Neither had a writer OR a
// reader in either language, so wiring one would have repeated #682's
// cost_source mistake in reverse.
//
// The last survivor was `model`, justified on the grounds that
// V2StageDetail.ModelSelection.Model "covers stage-level attribution". It did
// not: the per-(stage, model) calibration loop reads
// tokens.per_stage[*].model, PostPipelineAnalyzer's
// `.filter(([, usage]) => usage.model)` therefore dropped every row, and
// stage-model-calibration.json did not exist in any workspace after hundreds
// of runs. #1213 emptied the pen the way the comment above intends — by
// wiring the real Go writer.
//
// The list is now EMPTY, and this test is what keeps it that way: dropping
// V2StageTokens.Model turns it red rather than quietly restoring the dead
// loop.
//
// Do NOT add cost_source-related entries here — that field's parity is the
// whole point of this test. Shrinking this list, by wiring a real Go writer or
// by deleting a dead field, is welcome; growing it to hide an unrelated new
// drift is exactly what this test exists to prevent, so a new addition needs
// the same justification the original three got, with a linked issue.
var stageTokensKnownGaps = map[string]bool{}

// goJSONFieldNames returns the `json` tag field names (pre-comma-options) of
// a Go struct type, in declaration order. Fails loudly on any exported field
// missing a json tag — an untagged field is invisible to this parity check by
// construction, and a schema-parity tool that can silently miss a field is
// worse than none.
func goJSONFieldNames(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("goJSONFieldNames: %s is not a struct", typ)
	}
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag, ok := f.Tag.Lookup("json")
		if !ok {
			t.Fatalf("field %s.%s has no `json` tag — this parity test cannot see it either way. "+
				"Add one (or `json:\"-\"` if it must never reach the wire).", typ, f.Name)
		}
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names
}

var (
	blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineCommentRe  = regexp.MustCompile(`//[^\n]*`)
	tsFieldNameRe  = regexp.MustCompile(`(?m)^\s*(\w+)\s*:\s`)
)

// extractZodObjectBody returns the source text between the outermost
// `{` and its matching `}` for `export const <constName> = z.object({...})`,
// found by brace-depth counting from the opening brace (so a schema whose
// body itself contains balanced `{}` — which the two pins used here do not —
// would still find the right end, though see the file header for why a
// NESTED z.object({...}) literal inside the body is unsupported at the
// field-extraction step regardless).
func extractZodObjectBody(t *testing.T, source []byte, constName string) string {
	t.Helper()
	re := regexp.MustCompile(`export const ` + regexp.QuoteMeta(constName) + `\s*=\s*z\.object\(\{`)
	loc := re.FindIndex(source)
	if loc == nil {
		t.Fatalf("no `export const %s = z.object({...})` declaration found in %s.\n"+
			"The const was renamed, deleted, or rewritten in a form this pin cannot read "+
			"(e.g. built from a spread or a `.extend()` chain rather than an inline literal). "+
			"A missing definition is a FAILURE, never a skip.",
			constName, executionHistoryTSSchemaPath)
	}
	start := loc[1] // just past the opening `{`
	depth := 1
	i := start
	for ; i < len(source) && depth > 0; i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	if depth != 0 {
		t.Fatalf("unbalanced braces scanning %s's body in %s — cannot locate its end", constName, executionHistoryTSSchemaPath)
	}
	return string(source[start : i-1])
}

// tsObjectFieldNames extracts the top-level property names from a named
// `z.object({...})` export, deduplicated in first-seen order. Comments are
// stripped first so a field name mentioned only in prose (e.g. this file's
// own `cost_source` doc comment enumerating sibling keys) is never mistaken
// for a declared property.
func tsObjectFieldNames(t *testing.T, source []byte, constName string) []string {
	t.Helper()
	body := extractZodObjectBody(t, source, constName)
	body = blockCommentRe.ReplaceAllString(body, "")
	body = lineCommentRe.ReplaceAllString(body, "")
	matches := tsFieldNameRe.FindAllStringSubmatch(body, -1)
	seen := make(map[string]bool, len(matches))
	var names []string
	for _, m := range matches {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		t.Fatalf("%s in %s parsed to zero fields; body was %q.\n"+
			"An empty extraction would make this pin pass against anything.",
			constName, executionHistoryTSSchemaPath, body)
	}
	return names
}

// assertFieldParity fails if goFields and tsFields (each a field-name set,
// order-independent) are not IDENTICAL modulo the given allowlist of
// documented pre-existing TS-only gaps. allowGapsTSOnly may be nil.
func assertFieldParity(t *testing.T, goDesc string, goFields []string, tsDesc string, tsFields []string, allowGapsTSOnly map[string]bool) {
	t.Helper()
	goSet := make(map[string]bool, len(goFields))
	for _, f := range goFields {
		goSet[f] = true
	}
	tsSet := make(map[string]bool, len(tsFields))
	for _, f := range tsFields {
		tsSet[f] = true
	}

	var goOnly, tsOnly []string
	for f := range goSet {
		if !tsSet[f] {
			goOnly = append(goOnly, f)
		}
	}
	for f := range tsSet {
		if !goSet[f] && !allowGapsTSOnly[f] {
			tsOnly = append(tsOnly, f)
		}
	}
	sort.Strings(goOnly)
	sort.Strings(tsOnly)

	if len(goOnly) > 0 || len(tsOnly) > 0 {
		t.Errorf("schema field DRIFT between %s and %s:\n"+
			"  fields on %s but not %s: %v\n"+
			"  fields on %s but not %s: %v\n"+
			"Go is the writer and TypeScript is the validator (or vice versa for a\n"+
			"reader-only field) — a field declared on only one side means either a\n"+
			"writer emits a key nothing ever validates, or a reader expects a key\n"+
			"nothing ever writes. This is the exact drift #682 exists to catch.",
			goDesc, tsDesc, goDesc, tsDesc, goOnly, tsDesc, goDesc, tsOnly)
	}
}

// TestHistorySchemaParity_StageTokens is the AC-4 pin for #682: it fails if
// V2StageTokens and HistoryStageTokenUsageSchema disagree on any field name
// other than the three documented pre-existing gaps in stageTokensKnownGaps.
// Before #682 this would have failed on cost_source alone (Go had no field;
// TS declared one) — that is precisely the regression this test now blocks.
func TestHistorySchemaParity_StageTokens(t *testing.T) {
	source, err := os.ReadFile(executionHistoryTSSchemaPath)
	if err != nil {
		t.Fatalf("cannot read the TS schema authority at %s: %v", executionHistoryTSSchemaPath, err)
	}
	goFields := goJSONFieldNames(t, reflect.TypeOf(V2StageTokens{}))
	tsFields := tsObjectFieldNames(t, source, "HistoryStageTokenUsageSchema")
	assertFieldParity(t,
		"state.V2StageTokens", goFields,
		"HistoryStageTokenUsageSchema", tsFields,
		stageTokensKnownGaps,
	)
}

// TestHistorySchemaParity_StageGateResult is the second pin proving the
// helper above is not hardcoded to one struct: state.StageGateResult and
// StageGateResultSchema are independently documented ("Mirrors
// `state.StageGateResult`" in the TS doc comment) to match field-for-field,
// with no known gaps — so this pair runs the STRICT comparison (nil
// allowlist).
func TestHistorySchemaParity_StageGateResult(t *testing.T) {
	source, err := os.ReadFile(executionHistoryTSSchemaPath)
	if err != nil {
		t.Fatalf("cannot read the TS schema authority at %s: %v", executionHistoryTSSchemaPath, err)
	}
	goFields := goJSONFieldNames(t, reflect.TypeOf(StageGateResult{}))
	tsFields := tsObjectFieldNames(t, source, "StageGateResultSchema")
	assertFieldParity(t,
		"state.StageGateResult", goFields,
		"StageGateResultSchema", tsFields,
		nil,
	)
}
