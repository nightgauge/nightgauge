package state

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
// whenever its width changes. The `(?m)^` anchor requires the declaration at
// column 0 — a module-scope `export const` always is — so a `//`- or ` * `-
// prefixed COPY inside a comment can never satisfy the pin.
//
// Capture 1 is the raw bracket contents; tsModelSelectionMemberRegexp splits it.
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
			"(e.g. built from another array at runtime — keep the inline literal so both "+
			"languages can read the vocabulary from one place). The literal must be a top-level "+
			"declaration in THIS file: a commented-out copy or a re-export from another "+
			"module does not count. A missing definition is a FAILURE, never a skip. %s",
			tsModelSelectionSourcePath, modelSelectionRealignHint)
	default:
		t.Fatalf("found %d `export const MODEL_SELECTION_SOURCES = [...]` literals in %s; expected exactly 1.\n"+
			"Ambiguous: this pin cannot tell which one the SDK actually exports, and #446 "+
			"exists precisely to keep the vocabulary to a single declaration. %s",
			len(matches), tsModelSelectionSourcePath, modelSelectionRealignHint)
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
