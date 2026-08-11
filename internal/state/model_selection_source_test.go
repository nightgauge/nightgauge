package state

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// ---------------------------------------------------------------------------
// Cross-language pin for the model-selection source vocabulary (#446).
//
// Go is the SOLE writer of `model_selection.source` — BuildV2Record is the only
// V2ModelSelect construction in the tree — and TypeScript is the only reader
// that validates it. Before #446 the two sides shared no value at all: Go wrote
// "scheduler", every TypeScript copy of the enum listed nine values from a
// retired extension-side cascade, and so every real record failed strict
// validation and fell into the reader's lenient raw cast. Drift here is
// therefore not cosmetic — it silently disables the schema that exists to
// catch it.
//
// #446 made MODEL_SELECTION_SOURCES in the SDK the single authority (every
// TypeScript surface derives from it) and left Go with a genuine second
// definition, because Go cannot import TypeScript. This test reads the SDK
// source, extracts the array literal, and requires ModelSelectionSources to
// equal it element-for-element IN ORDER. It never skips: an unreadable file, or
// a const renamed or rewritten into a form this extractor cannot read, is a
// FAILURE, because a pin that quietly stops checking hides exactly the drift it
// exists to catch.

// tsModelSelectionSourcePath is the SDK-side authority, relative to this
// package directory (go test runs with cwd = the package dir).
var tsModelSelectionSourcePath = filepath.Join(
	"..", "..", "packages", "nightgauge-sdk", "src", "analysis", "types.ts",
)

// tsModelSelectionLiteralRegexp lifts
// `export const MODEL_SELECTION_SOURCES = [...] as const;` out of the
// TypeScript source. Whitespace is elastic because prettier reflows the array
// whenever its width changes.
//
// What the `(?m)^` anchor actually buys: the declaration must start at column
// 0, so a `//`- or ` * `-prefixed copy (a line comment, or a jsdoc body line)
// cannot satisfy it. It does NOT exclude every commented-out copy — a
// `/* … */` block comment whose contents begin at column 0 matches. That case
// does not pass silently: two declarations trip the `default:` >1-match arm,
// and a lone commented-out one is compared like any other and fails the
// DeepEqual unless it happens to be correct.
//
// Capture 1 is the raw bracket contents; tsModelSelectionMemberRegexp splits
// it. A derived form (`[...LEGACY, "scheduler", …]`) still matches and yields
// only the literal members after the spread, which is a PARTIAL list rather
// than zero matches — so the extraction is rejected explicitly below rather
// than silently compared.
var tsModelSelectionLiteralRegexp = regexp.MustCompile(
	`(?m)^export const MODEL_SELECTION_SOURCES\s*=\s*\[([^\]]*)\]\s*as const;`)

// tsModelSelectionMemberRegexp pulls the quoted members out of the array body,
// in source order. Both quote styles are accepted because prettier's choice is
// a formatting decision, not a semantic one.
var tsModelSelectionMemberRegexp = regexp.MustCompile(`["']([^"']+)["']`)

// modelSelectionRealignHint is appended to every failure: the fixer needs to
// know which two sites are peers and which one is the authority.
const modelSelectionRealignHint = "re-align the two definition sites: " +
	"internal/state/model_selection_source.go (ModelSelectionSources) and " +
	"packages/nightgauge-sdk/src/analysis/types.ts (MODEL_SELECTION_SOURCES). " +
	"MODEL_SELECTION_SOURCES is the authority (#446) — every TypeScript surface " +
	"derives from it — so Go follows it, in the same order."

func TestModelSelectionSourcesPinnedToSDK(t *testing.T) {
	source, err := os.ReadFile(tsModelSelectionSourcePath)
	if err != nil {
		t.Fatalf("cannot read the SDK authority at %s: %v\n"+
			"This pin is path-coupled: if analysis/types.ts moved or was renamed, move "+
			"tsModelSelectionSourcePath in this test with it — do NOT delete the pin. %s",
			tsModelSelectionSourcePath, err, modelSelectionRealignHint)
	}

	matches := tsModelSelectionLiteralRegexp.FindAllStringSubmatch(string(source), -1)
	switch len(matches) {
	case 1:
		// The pinned shape. Fall through to the comparison below.
	case 0:
		t.Fatalf("no `export const MODEL_SELECTION_SOURCES = [...] as const;` array literal found in %s.\n"+
			"The const was renamed, deleted, or rewritten in a form this pin cannot read "+
			"(e.g. `satisfies` in place of `as const`, or a re-export from another module — "+
			"keep the inline literal so both languages can read the vocabulary from one "+
			"place). The literal must be a top-level declaration, at column 0, in THIS file. "+
			"A missing definition is a FAILURE, never a skip. %s",
			tsModelSelectionSourcePath, modelSelectionRealignHint)
	default:
		t.Fatalf("found %d `export const MODEL_SELECTION_SOURCES = [...]` literals in %s; expected exactly 1.\n"+
			"Ambiguous: this pin cannot tell which one the SDK actually exports, and #446 "+
			"exists precisely to keep the vocabulary to a single declaration. %s",
			len(matches), tsModelSelectionSourcePath, modelSelectionRealignHint)
	}

	// A spread (`[...LEGACY, "scheduler", …]`) is the one degenerate shape that
	// can pass QUIETLY: the extractor sees only the literal members after the
	// spread, so if those happen to equal Go's list the DeepEqual is green while
	// the exported TypeScript enum accepts strictly more values than Go writes.
	// Reject the shape instead of comparing a partial list.
	if strings.Contains(matches[0][1], "...") {
		t.Fatalf("MODEL_SELECTION_SOURCES in %s is built with a spread: body %q.\n"+
			"This pin can only read the literal members, so a spread would compare a "+
			"PARTIAL list and could pass while the TypeScript enum accepts values Go "+
			"never writes. Keep the vocabulary as one inline literal. %s",
			tsModelSelectionSourcePath, matches[0][1], modelSelectionRealignHint)
	}

	var want []string
	for _, m := range tsModelSelectionMemberRegexp.FindAllStringSubmatch(matches[0][1], -1) {
		want = append(want, m[1])
	}
	if len(want) == 0 {
		t.Fatalf("MODEL_SELECTION_SOURCES in %s parsed to zero members from body %q.\n"+
			"An empty extraction would make this pin pass against anything. %s",
			tsModelSelectionSourcePath, matches[0][1], modelSelectionRealignHint)
	}
	t.Logf("extracted MODEL_SELECTION_SOURCES from %s: %q", tsModelSelectionSourcePath, want)

	if !reflect.DeepEqual(ModelSelectionSources, want) {
		t.Errorf("model-selection source vocabulary has DRIFTED between Go and the SDK:\n"+
			"  Go   ModelSelectionSources    = %q\n"+
			"  TS   MODEL_SELECTION_SOURCES  = %q\n"+
			"(internal/state/model_selection_source.go vs %s)\n"+
			"Go is the only writer of this field and TypeScript is the only validator of it, "+
			"so a value Go emits that TS does not list costs the whole record its strict parse. %s",
			ModelSelectionSources, want, tsModelSelectionSourcePath, modelSelectionRealignHint)
	}
}

// tsAutomaticModelSelectionSourceRegexp lifts
// `export const AUTOMATIC_MODEL_SELECTION_SOURCE: ModelSelectionSource = "…";`
// out of the same SDK file. Same column-0 anchor and same fail-never-skip
// discipline as the array pin above.
var tsAutomaticModelSelectionSourceRegexp = regexp.MustCompile(
	`(?m)^export const AUTOMATIC_MODEL_SELECTION_SOURCE: ModelSelectionSource =\s*"([^"]+)";`)

// TestAutomaticModelSelectionSourcePinnedToGoDefault closes the gap the array
// pin leaves open: TestModelSelectionSourcesPinnedToSDK proves the two
// vocabularies hold the SAME MEMBERS in the same order, and nothing else. It
// says nothing about WHICH member means "the scheduler resolved this model and
// nothing substituted it" — the value every SDK routing analytic filters on
// (isAutoSelected, analyzeAutoSelectionOutcomes, detectUnder/OverRouting,
// generateThresholdRecommendations).
//
// Without this arm, adding a fifth member to both lists and making it
// BuildV2Record's default for a plain completed stage leaves both existing pins
// green while every routing metric silently matches nothing again — the exact
// failure #446 exists to close, reached from the other end. The TS-side
// assertion in modelRouting.test.ts cannot cover it: its GO_WRITTEN_SOURCE is a
// hand-copied literal in a TypeScript file, so it compares "scheduler" to
// "scheduler".
//
// The Go side of the comparison is the WRITER, not a constant: whatever
// BuildV2Record actually stamps on an unremarkable completed stage is what the
// SDK must call automatic.
func TestAutomaticModelSelectionSourcePinnedToGoDefault(t *testing.T) {
	source, err := os.ReadFile(tsModelSelectionSourcePath)
	if err != nil {
		t.Fatalf("cannot read the SDK authority at %s: %v\n"+
			"This pin is path-coupled: if analysis/types.ts moved or was renamed, move "+
			"tsModelSelectionSourcePath in this test with it — do NOT delete the pin. %s",
			tsModelSelectionSourcePath, err, modelSelectionRealignHint)
	}

	matches := tsAutomaticModelSelectionSourceRegexp.FindAllStringSubmatch(string(source), -1)
	switch len(matches) {
	case 1:
		// The pinned shape. Fall through to the comparison below.
	case 0:
		t.Fatalf("no `export const AUTOMATIC_MODEL_SELECTION_SOURCE: ModelSelectionSource = \"…\";` "+
			"declaration found in %s.\n"+
			"It was renamed, deleted, retyped, or rewritten in a form this pin cannot read "+
			"(a derived value, a different type annotation, single quotes). Keep it a "+
			"top-level string literal at column 0 so Go can read which member means "+
			"automatic. A missing definition is a FAILURE, never a skip. %s",
			tsModelSelectionSourcePath, modelSelectionRealignHint)
	default:
		t.Fatalf("found %d `export const AUTOMATIC_MODEL_SELECTION_SOURCE` declarations in %s; "+
			"expected exactly 1.\nAmbiguous: this pin cannot tell which one the SDK exports. %s",
			len(matches), tsModelSelectionSourcePath, modelSelectionRealignHint)
	}
	tsAutomatic := matches[0][1]
	t.Logf("extracted AUTOMATIC_MODEL_SELECTION_SOURCE from %s: %q",
		tsModelSelectionSourcePath, tsAutomatic)

	goDefault := buildOneStageRecord(t, nil).ModelSelection.Source
	if tsAutomatic != goDefault {
		t.Errorf("the SDK's \"automatic\" member does not match what Go writes for a plain "+
			"completed stage:\n"+
			"  Go   BuildV2Record default          = %q\n"+
			"  TS   AUTOMATIC_MODEL_SELECTION_SOURCE = %q\n"+
			"(internal/state/history.go vs %s)\n"+
			"Every SDK routing analytic filters on the TS constant, so a mismatch makes them "+
			"all match zero records without any other test going red. %s",
			goDefault, tsAutomatic, tsModelSelectionSourcePath, modelSelectionRealignHint)
	}
}

// TestEscalationReasonModelUnavailableValue pins the STRING, not the constant.
// "model_unavailable" is what both scheduler construction sites have written
// onto EscalationRecord.Reason since #42, and RuntimeState is persisted, so the
// value is on-disk vocabulary. #446 named it; renaming the value it holds is a
// separate decision with its own migration question.
func TestEscalationReasonModelUnavailableValue(t *testing.T) {
	if EscalationReasonModelUnavailable != "model_unavailable" {
		t.Errorf("EscalationReasonModelUnavailable = %q, want %q — the value is existing "+
			"telemetry vocabulary carried in persisted RuntimeState (EscalationHistory), "+
			"not a label free to change with the constant's name",
			EscalationReasonModelUnavailable, "model_unavailable")
	}
}

// TestModelSelectionSourceForEscalationReason_IsTotal is the closure guard for
// the mapping that replaced the raw `source = esc.Reason` passthrough. Every
// input — including reasons that do not exist yet — must land inside
// ModelSelectionSources, because whatever this returns is written verbatim to
// disk and validated against the SDK enum.
func TestModelSelectionSourceForEscalationReason_IsTotal(t *testing.T) {
	inVocabulary := func(s string) bool {
		for _, v := range ModelSelectionSources {
			if v == s {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{"the only reason any writer emits today", EscalationReasonModelUnavailable, ModelSourceModelUnavailableDowngrade},
		{"literal value, in case the constant is re-pointed", "model_unavailable", ModelSourceModelUnavailableDowngrade},
		{"a reason no writer emits yet", "context_exhausted", ModelSourceEscalation},
		{"empty reason", "", ModelSourceEscalation},
		{"a reason that looks like a source", "scheduler", ModelSourceEscalation},
		{"a retry-engine reason from the OTHER vocabulary", "escalation_ceiling_reached", ModelSourceEscalation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := modelSelectionSourceForEscalationReason(tc.reason)
			if got != tc.want {
				t.Errorf("modelSelectionSourceForEscalationReason(%q) = %q, want %q", tc.reason, got, tc.want)
			}
			if !inVocabulary(got) {
				t.Errorf("modelSelectionSourceForEscalationReason(%q) = %q, which is NOT in "+
					"ModelSelectionSources %q — an out-of-vocabulary source reaches disk and "+
					"costs the record its strict parse, which is the whole defect #446 closed",
					tc.reason, got, ModelSelectionSources)
			}
		})
	}
}

// reasonsDeliberatelyBucketed lists the EscalationReasons members whose
// attribution to the ModelSourceEscalation catch-all is a DECISION, not an
// oversight. It is empty today: the one reason any writer emits
// (EscalationReasonModelUnavailable) has its own label. Adding an entry here is
// how you record "this cause does not deserve its own source label" — and doing
// so is a real choice, because the record keeps no other copy of the reason.
var reasonsDeliberatelyBucketed = map[string]string{}

// TestEscalationReasonsAreDeliberatelyLabeled is the closure guard the totality
// test cannot be. TestModelSelectionSourceForEscalationReason_IsTotal proves
// nothing ESCAPES the vocabulary; this proves nothing is silently MERGED into
// it. Those are different failures: the mapping's `default:` arm is total by
// construction, so a new reason plus a scheduler site that writes it compiles,
// passes every other test, and lands on "escalation" with nothing red — and
// because `model_selection.source` is the only place an escalation reason
// reaches the history record (no V2/V3 field carries the raw string), the
// distinct cause is destroyed rather than merely coarsened.
//
// Declaring the reason in EscalationReasons is what forces the decision here.
func TestEscalationReasonsAreDeliberatelyLabeled(t *testing.T) {
	if len(EscalationReasons) == 0 {
		t.Fatal("EscalationReasons is empty — internal/state/runtime_state.go must list " +
			"every reason a writer puts on EscalationRecord.Reason, or this guard checks nothing")
	}

	for _, reason := range EscalationReasons {
		t.Run(reason, func(t *testing.T) {
			got := modelSelectionSourceForEscalationReason(reason)
			if got != ModelSourceEscalation {
				return
			}
			if why, ok := reasonsDeliberatelyBucketed[reason]; ok {
				t.Logf("reason %q buckets into %q on purpose: %s", reason, got, why)
				return
			}
			t.Errorf("escalation reason %q maps to the %q catch-all, and nothing says that was "+
				"intended.\n"+
				"DECIDE ONE:\n"+
				"  (a) it deserves its own attribution — add a case to "+
				"modelSelectionSourceForEscalationReason in "+
				"internal/state/model_selection_source.go, a matching Source constant there, "+
				"and the matching member to MODEL_SELECTION_SOURCES in "+
				"packages/nightgauge-sdk/src/analysis/types.ts (one commit, both files); or\n"+
				"  (b) the catch-all is right — record that in reasonsDeliberatelyBucketed in "+
				"this file.\n"+
				"Silence is not an option: the reason string reaches the history record ONLY "+
				"through this mapping, so an unlabelled reason is unrecoverable from the "+
				"record afterwards. (Reason declared in internal/state/runtime_state.go, "+
				"EscalationReasons.)", reason, got)
		})
	}
}

// ---------------------------------------------------------------------------
// BuildV2Record behaviour arms. The mapping above is only worth anything if the
// writer routes through it; the plain and refusal-fallback arms are restated
// here so the four sources are pinned as ONE decision table rather than three
// tests in three files.

func buildOneStageRecord(t *testing.T, mutate func(rs *RuntimeState)) V2StageDetail {
	t.Helper()
	rs := NewRuntimeState("o/r", 446, "item-1", testRunID())
	rs.StartedAt = time.Now()
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "claude-sonnet-4-6")
	if mutate != nil {
		mutate(rs)
	}
	rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 200}, "claude-sonnet-4-6")

	hw := NewHistoryWriter(t.TempDir())
	rec := hw.BuildV2Record(rs.Snapshot(), true, "", V2RunInput{}, time.Now())
	stage, ok := rec.Stages[string(StageFeatureDev)]
	if !ok {
		t.Fatal("feature-dev stage missing from record")
	}
	if stage.ModelSelection == nil {
		t.Fatal("ModelSelection missing")
	}
	return stage
}

func TestBuildV2Record_ModelSelectionSourceVocabulary(t *testing.T) {
	escalate := func(reason string) func(*RuntimeState) {
		return func(rs *RuntimeState) {
			rs.AppendEscalation(EscalationRecord{
				Stage:     StageFeatureDev,
				FromModel: "claude-sonnet-4-6",
				ToModel:   "claude-haiku-4-5",
				Reason:    reason,
				At:        time.Now(),
			})
		}
	}

	cases := []struct {
		name   string
		mutate func(*RuntimeState)
		want   string
	}{
		{
			name:   "plain stage",
			mutate: nil,
			want:   ModelSourceScheduler,
		},
		{
			name: "CLI refusal fallback",
			mutate: func(rs *RuntimeState) {
				rs.RecordModelRefusalFallback(StageFeatureDev, "claude-fable-5", "claude-opus-4-8", "reasoning_extraction")
			},
			want: ModelSourceCLIRefusalFallback,
		},
		{
			name:   "model-unavailable downgrade",
			mutate: escalate(EscalationReasonModelUnavailable),
			want:   ModelSourceModelUnavailableDowngrade,
		},
		{
			name:   "escalation with a reason that has no dedicated label",
			mutate: escalate("some_future_reason"),
			want:   ModelSourceEscalation,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stage := buildOneStageRecord(t, tc.mutate)
			if stage.ModelSelection.Source != tc.want {
				t.Errorf("ModelSelection.Source = %q, want %q", stage.ModelSelection.Source, tc.want)
			}
		})
	}
}

// TestBuildV2Record_ModelSelectionWireKeys closes the OTHER axis of the same
// cross-language contract. Every other pin here is about the VALUE of
// `model_selection.source`; this one is about the KEYS the record is written
// under. Renaming a struct tag — `json:"source"` → `json:"src"` — builds clean
// and survives internal/state, internal/pipeline, internal/platform and the IPC
// suites, because the vocabulary pins compare string slices lifted from source
// text and never a marshaled record, and the TypeScript fixture pins parse a
// frozen capture rather than fresh Go output.
//
// The consequence is the regression #446 just removed, restored silently and
// wholesale: HistoryStageDetailSchema requires both keys, so a renamed tag
// drops every record back into executionHistoryReader's lenient raw cast, where
// no zod default runs and the routing analytics go quiet again. Before #446 the
// key name was inert — every record already failed strict parse on the value —
// which is exactly why this needs its own guard now that the strict path is
// live for the whole corpus.
//
// Marshaling is deliberate: it asserts the wire shape, not the Go field names.
func TestBuildV2Record_ModelSelectionWireKeys(t *testing.T) {
	stage := buildOneStageRecord(t, nil)

	b, err := json.Marshal(stage.ModelSelection)
	if err != nil {
		t.Fatalf("marshal ModelSelection: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal ModelSelection: %v", err)
	}

	// Both directions by construction: a sorted key-set equality catches a
	// renamed key, a dropped key and an added key alike.
	want := []string{"model", "source"}
	keys := slices.Sorted(maps.Keys(got))
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("model_selection wire keys = %q, want %q.\n"+
			"packages/nightgauge-vscode/src/schemas/executionHistory.ts "+
			"(HistoryStageDetailSchema) requires exactly these keys; renaming or dropping a "+
			"json tag on V2ModelSelect (internal/state/history.go) costs every record its "+
			"strict parse, the same as writing a value the enum does not list. %s",
			keys, want, modelSelectionRealignHint)
	}
	if got["source"] != ModelSourceScheduler {
		t.Errorf("wire source = %v, want %q — the key must carry the value, not merely exist",
			got["source"], ModelSourceScheduler)
	}
}

// TestBuildV2Record_NeverWritesAnUnlistedSource is the property the four arms
// above imply but do not state: whatever combination of runtime state reaches
// the writer, the source it emits is a member of the vocabulary the SDK enum
// validates. The escalation arm is what used to break it — `source = esc.Reason`
// wrote the raw snake_case reason straight onto the record.
func TestBuildV2Record_NeverWritesAnUnlistedSource(t *testing.T) {
	reasons := []string{
		EscalationReasonModelUnavailable,
		"model_not_in_registry",
		"max_escalations_per_stage_exceeded",
		"",
	}
	for _, reason := range reasons {
		stage := buildOneStageRecord(t, func(rs *RuntimeState) {
			rs.AppendEscalation(EscalationRecord{
				Stage:  StageFeatureDev,
				Reason: reason,
				At:     time.Now(),
			})
		})
		got := stage.ModelSelection.Source
		found := false
		for _, v := range ModelSelectionSources {
			if v == got {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("escalation reason %q produced source %q, which is not in ModelSelectionSources %q",
				reason, got, ModelSelectionSources)
		}
	}
}
