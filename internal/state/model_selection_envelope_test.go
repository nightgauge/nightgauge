package state

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// ---------------------------------------------------------------------------
// The #528 evidence gap (Issue #580).
//
// #528's live grok matrix run produced a stage record whose model_selection
// read only {model: "sonnet", source: "scheduler"} — a routing-tier alias
// with no adapter, no served concrete id, no effort, no thinking. Nothing in
// that record could distinguish it from a claude run dispatched at the same
// tier. This test builds the same shape of run through the real writer path
// (RuntimeState → BuildV2Record) and pins that the envelope now carries every
// field Go has evidence for.

// TestBuildV2Record_GrokEvidenceGapClosed pins the #528 defect's fix at the
// builder level: a grok-served stage's model_selection now expresses
// adapter, served model, effort, and thinking — not just model and source.
func TestBuildV2Record_GrokEvidenceGapClosed(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 528, "item-528", testRunID())

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageAdapter(StageFeatureDev, "grok")
	rs.RecordStageModel(StageFeatureDev, "sonnet")
	rs.RecordStageServedModel(StageFeatureDev, "grok-4.6")
	rs.RecordStageEffort(StageFeatureDev, "high")
	rs.RecordStageThinking(StageFeatureDev, "on")
	rs.RecordStageModelSelectionMode(StageFeatureDev, "automatic")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "grok-4.6", "grok")

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	stage, ok := record.Stages[string(StageFeatureDev)]
	if !ok {
		t.Fatal("feature-dev stage missing from record")
	}
	sel := stage.ModelSelection
	if sel == nil {
		t.Fatal("ModelSelection missing — the #528 gap this test exists to close")
	}

	want := V2ModelSelect{
		Model:       "sonnet",
		Source:      ModelSourceScheduler,
		Adapter:     "grok",
		ServedModel: "grok-4.6",
		Effort:      "high",
		Thinking:    "on",
		Mode:        "automatic",
	}
	if *sel != want {
		t.Errorf("ModelSelection = %+v, want %+v\n"+
			"Before #580 this record read only {model: %q, source: %q} — the "+
			"#528 defect: a grok run was indistinguishable from a claude run "+
			"dispatched at the same routing tier.", *sel, want, want.Model, want.Source)
	}
}

// TestBuildV2Record_ModelSelectionEnvelopeWireKeys is the envelope's key-axis
// pin (the #446 lesson: pin both the key axis and the value axis), the
// companion to the pre-#580 TestBuildV2Record_ModelSelectionWireKeys — that
// test proves the key set when NONE of the new fields are populated; this one
// proves it when ALL of them are.
func TestBuildV2Record_ModelSelectionEnvelopeWireKeys(t *testing.T) {
	stage := buildOneStageRecord(t, func(rs *RuntimeState) {
		rs.RecordStageAdapter(StageFeatureDev, "grok")
		rs.RecordStageServedModel(StageFeatureDev, "grok-4.6")
		rs.RecordStageEffort(StageFeatureDev, "high")
		rs.RecordStageThinking(StageFeatureDev, "on")
		rs.RecordStageModelSelectionMode(StageFeatureDev, "automatic")
	})

	b, err := json.Marshal(stage.ModelSelection)
	if err != nil {
		t.Fatalf("marshal ModelSelection: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal ModelSelection: %v", err)
	}

	want := []string{"adapter", "effort", "mode", "model", "served_model", "source", "thinking"}
	keys := slices.Sorted(maps.Keys(got))
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("model_selection wire keys = %q, want %q.\n"+
			"packages/nightgauge-vscode/src/schemas/executionHistory.ts "+
			"(HistoryStageDetailSchema.model_selection) must declare exactly these keys; a "+
			"renamed or dropped json tag on V2ModelSelect (internal/state/history.go) costs "+
			"every record with a populated envelope its strict parse.",
			keys, want)
	}
}

// ---------------------------------------------------------------------------
// Cross-language vocabulary pin for the dispatch envelope's effort and
// thinking axes (Issue #580, the #446 lesson applied to BOTH axes spike #568
// §3 adds).

// tsExecutionHistorySchemaPath is the TypeScript schema that declares
// model_selection, relative to this package directory (go test runs with
// cwd = the package dir).
var tsExecutionHistorySchemaPath = filepath.Join(
	"..", "..", "packages", "nightgauge-vscode", "src", "schemas", "executionHistory.ts",
)

// tsModelSelectionEffortFieldRegexp requires the model_selection.effort field
// to be declared as a DERIVATION from EFFORT_LEVELS — not a re-listed
// literal — which is the #434 drift class (a hand-copied enum stops at
// "high", two rungs behind the ladder, and nothing catches the next rung
// EFFORT_LEVELS gains). EFFORT_LEVELS itself is already pinned to Go's
// models.EffortOrder by internal/models/registry_test.go's
// TestEffortOrderPinnedToSDKEffortLevels (#394/#578), so this structural
// check closes the loop: model_selection.effort → EFFORT_LEVELS →
// models.EffortOrder, with no independent value list anywhere in that chain.
var tsModelSelectionEffortFieldRegexp = regexp.MustCompile(
	`(?m)^\s*effort:\s*z\.enum\(EFFORT_LEVELS\)\.optional\(\),\s*$`)

// tsEffortLevelsSdkImportRegexp requires EFFORT_LEVELS to be imported from
// @nightgauge/sdk in this file. Without this check, the field-derivation
// regexp above is satisfied by the TEXT `z.enum(EFFORT_LEVELS)` regardless of
// what `EFFORT_LEVELS` actually resolves to — a local re-declaration would
// pass the same way a real import does.
var tsEffortLevelsSdkImportRegexp = regexp.MustCompile(
	`(?m)^import\s*\{[^}]*\bEFFORT_LEVELS\b[^}]*\}\s*from\s*["']@nightgauge/sdk["'];?\s*$`)

// tsLocalEffortLevelsDeclRegexp catches a local const/let/var named
// EFFORT_LEVELS, which would shadow the imported binding the derivation
// regexp above depends on being the real cross-language authority.
var tsLocalEffortLevelsDeclRegexp = regexp.MustCompile(
	`(?m)^\s*(?:export\s+)?(?:const|let|var)\s+EFFORT_LEVELS\b`)

func TestModelSelectionEffortDerivesFromEffortLevelsAuthority(t *testing.T) {
	source, err := os.ReadFile(tsExecutionHistorySchemaPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v\n"+
			"This pin is path-coupled: if executionHistory.ts moved, move "+
			"tsExecutionHistorySchemaPath in this test with it — do NOT delete the pin.",
			tsExecutionHistorySchemaPath, err)
	}

	matches := tsModelSelectionEffortFieldRegexp.FindAllIndex(source, -1)
	switch len(matches) {
	case 1:
		// pinned shape
	case 0:
		t.Fatalf("model_selection.effort in %s is not declared as "+
			"`effort: z.enum(EFFORT_LEVELS).optional(),`.\n"+
			"This is the #434 drift class: a hand-copied literal (e.g. "+
			"`z.enum([\"low\", \"medium\", \"high\"])`) silently falls behind the "+
			"EFFORT_LEVELS ladder the moment a new rung is added. The field must derive "+
			"from EFFORT_LEVELS (imported from @nightgauge/sdk in this file), never "+
			"re-list its members.", tsExecutionHistorySchemaPath)
	default:
		t.Fatalf("found %d `effort: z.enum(EFFORT_LEVELS).optional(),` fields in %s; expected "+
			"exactly 1.\nAmbiguous: this pin cannot tell which one is the actual "+
			"model_selection.effort declaration — the check is not scoped to inside the "+
			"model_selection block, so an unrelated field with the identical shape elsewhere "+
			"in the file would make this ambiguous rather than silently passing.",
			len(matches), tsExecutionHistorySchemaPath)
	}

	if tsLocalEffortLevelsDeclRegexp.Match(source) {
		t.Fatalf("found a local `const/let/var EFFORT_LEVELS` declaration in %s.\n"+
			"That would shadow the @nightgauge/sdk import the model_selection.effort "+
			"derivation above depends on being the real cross-language authority "+
			"(#394/#578) — the field would silently derive from a local list instead.",
			tsExecutionHistorySchemaPath)
	}
	if !tsEffortLevelsSdkImportRegexp.Match(source) {
		t.Fatalf("no `import { EFFORT_LEVELS, ... } from \"@nightgauge/sdk\";` found in %s.\n"+
			"model_selection.effort derives from EFFORT_LEVELS, but without this import this "+
			"pin cannot confirm that name resolves to the SDK authority (#394/#578) rather "+
			"than an unrelated local binding.", tsExecutionHistorySchemaPath)
	}
}

// tsModelSelectionThinkingFieldRegexp lifts the model_selection.thinking
// field's literal enum. Unlike effort, thinking has no pre-existing
// cross-language authority to derive from — this issue introduces the axis —
// so the TypeScript literal here IS the authority, and Go's ThinkingStates
// (model_selection_envelope.go) is what gets pinned against it.
var tsModelSelectionThinkingFieldRegexp = regexp.MustCompile(
	`(?m)^\s*thinking:\s*z\.enum\(\[([^\]]*)\]\)\.optional\(\),\s*$`)

var tsQuotedMemberRegexp = regexp.MustCompile(`["']([^"']+)["']`)

func TestModelSelectionThinkingPinnedToExecutionHistorySchema(t *testing.T) {
	source, err := os.ReadFile(tsExecutionHistorySchemaPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v\n"+
			"This pin is path-coupled: if executionHistory.ts moved, move "+
			"tsExecutionHistorySchemaPath in this test with it — do NOT delete the pin.",
			tsExecutionHistorySchemaPath, err)
	}

	matches := tsModelSelectionThinkingFieldRegexp.FindAllStringSubmatch(string(source), -1)
	switch len(matches) {
	case 1:
		// pinned shape
	case 0:
		t.Fatalf("no `thinking: z.enum([...]).optional(),` field found inside "+
			"model_selection in %s.\n"+
			"The field was renamed, deleted, or rewritten in a form this pin cannot read. "+
			"A missing definition is a FAILURE, never a skip.", tsExecutionHistorySchemaPath)
	default:
		t.Fatalf("found %d `thinking: z.enum([...])` fields in %s; expected exactly 1.\n"+
			"Ambiguous: this pin cannot tell which one the schema actually declares.",
			len(matches), tsExecutionHistorySchemaPath)
	}

	var want []string
	for _, m := range tsQuotedMemberRegexp.FindAllStringSubmatch(matches[0][1], -1) {
		want = append(want, m[1])
	}
	if len(want) == 0 {
		t.Fatalf("model_selection.thinking in %s parsed to zero members from body %q.\n"+
			"An empty extraction would make this pin pass against anything.",
			tsExecutionHistorySchemaPath, matches[0][1])
	}
	t.Logf("extracted model_selection.thinking enum from %s: %q", tsExecutionHistorySchemaPath, want)

	if !reflect.DeepEqual(ThinkingStates, want) {
		t.Errorf("the thinking vocabulary has DRIFTED between Go and TypeScript:\n"+
			"  Go   ThinkingStates                    = %q\n"+
			"  TS   model_selection.thinking z.enum    = %q\n"+
			"(internal/state/model_selection_envelope.go vs %s)\n"+
			"resolveDispatchThinking (internal/orchestrator/dispatch_envelope.go) can only "+
			"emit members of ThinkingStates; a TS member Go's list is missing would be "+
			"unreachable, and a Go member TS does not list would fail strict validation.",
			ThinkingStates, want, tsExecutionHistorySchemaPath)
	}
}

// ---------------------------------------------------------------------------
// Cross-language vocabulary pin for the dispatch envelope's mode axis (Issue
// #580, resolves #462 — the mode value axis, the third of the envelope's
// value axes alongside effort and thinking above).
//
// Unlike thinking, mode has a pre-existing cross-language authority to derive
// from: ModelRoutingModeSchema in
// packages/nightgauge-vscode/src/config/schema.ts, the same enum
// `model_routing.mode` config validation already uses. executionHistory.ts's
// model_selection.mode field derives from it directly (see the `mode` field
// comment there), so the only remaining hand-listed copy on the Go side is
// state.ModelRoutingModes (model_selection_envelope.go) — this test pins that
// against the TypeScript authority, exactly as
// TestEffortOrderPinnedToSDKEffortLevels (internal/models/registry_test.go)
// pins EffortOrder against EFFORT_LEVELS.

// tsModelRoutingModeSchemaPath is the config schema that declares
// ModelRoutingModeSchema, relative to this package directory (go test runs
// with cwd = the package dir).
var tsModelRoutingModeSchemaPath = filepath.Join(
	"..", "..", "packages", "nightgauge-vscode", "src", "config", "schema.ts",
)

// tsModelRoutingModeLiteralRegexp lifts `export const ModelRoutingModeSchema
// = z.enum([...]);` out of the TypeScript source. The `(?m)^` anchor requires
// the declaration at column 0 — a module-scope `export const` always is — so
// a comment-embedded copy can never satisfy the pin.
//
// Capture 1 is the raw bracket contents; tsQuotedMemberRegexp splits it.
var tsModelRoutingModeLiteralRegexp = regexp.MustCompile(
	`(?m)^export const ModelRoutingModeSchema\s*=\s*z\.enum\(\[([^\]]*)\]\)\s*;`)

func TestModelSelectionModePinnedToModelRoutingModeSchema(t *testing.T) {
	source, err := os.ReadFile(tsModelRoutingModeSchemaPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v\n"+
			"This pin is path-coupled: if schema.ts moved, move "+
			"tsModelRoutingModeSchemaPath in this test with it — do NOT delete the pin.",
			tsModelRoutingModeSchemaPath, err)
	}

	matches := tsModelRoutingModeLiteralRegexp.FindAllStringSubmatch(string(source), -1)
	switch len(matches) {
	case 1:
		// pinned shape
	case 0:
		t.Fatalf("no `export const ModelRoutingModeSchema = z.enum([...]);` literal found "+
			"in %s.\n"+
			"The const was renamed, deleted, or rewritten in a form this pin cannot read. "+
			"A missing definition is a FAILURE, never a skip.", tsModelRoutingModeSchemaPath)
	default:
		t.Fatalf("found %d `ModelRoutingModeSchema = z.enum([...])` literals in %s; expected "+
			"exactly 1.\nAmbiguous: this pin cannot tell which one is the actual authority.",
			len(matches), tsModelRoutingModeSchemaPath)
	}

	var want []string
	for _, m := range tsQuotedMemberRegexp.FindAllStringSubmatch(matches[0][1], -1) {
		want = append(want, m[1])
	}
	if len(want) == 0 {
		t.Fatalf("ModelRoutingModeSchema in %s parsed to zero members from body %q.\n"+
			"An empty extraction would make this pin pass against anything.",
			tsModelRoutingModeSchemaPath, matches[0][1])
	}
	t.Logf("extracted ModelRoutingModeSchema from %s: %q", tsModelRoutingModeSchemaPath, want)

	if !reflect.DeepEqual(ModelRoutingModes, want) {
		t.Errorf("the mode vocabulary has DRIFTED between Go and TypeScript:\n"+
			"  Go   ModelRoutingModes         = %q\n"+
			"  TS   ModelRoutingModeSchema    = %q\n"+
			"(internal/state/model_selection_envelope.go vs %s)\n"+
			"modelRoutingMode (internal/orchestrator/dispatch_routing.go) — the same function "+
			"resolveDispatchSelectionMode (dispatch_envelope.go) calls to populate "+
			"model_selection.mode — validates env and config values against "+
			"state.IsModelRoutingMode; a TS member Go's list is missing would be unreachable "+
			"from config/env, and a Go member TS does not list would fail strict validation "+
			"the instant a record carrying it is parsed (the #446 lesson).",
			ModelRoutingModes, want, tsModelRoutingModeSchemaPath)
	}
}
