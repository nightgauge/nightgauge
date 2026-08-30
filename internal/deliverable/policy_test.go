package deliverable

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// corpusCase mirrors one entry of schemas/deliverable-policy-corpus-v1.json.
// The same file drives the TypeScript mirror's suite; a rule implemented on one
// side and not the other fails the other side here.
type corpusCase struct {
	ID     string `json:"id"`
	Stage  string `json:"stage"`
	Input  any    `json:"input"`
	Expect struct {
		Verdict       string   `json:"verdict"`
		Untrustworthy []string `json:"untrustworthy"`
		Notes         []struct {
			Field       string `json:"field"`
			Disposition string `json:"disposition"`
			Rule        string `json:"rule"`
		} `json:"notes"`
		Doc map[string]any `json:"doc"`
	} `json:"expect"`
}

func loadCorpus(t *testing.T) []corpusCase {
	t.Helper()
	path := filepath.Join(repoRoot(t), "schemas", "deliverable-policy-corpus-v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var doc struct {
		PolicyVersion string       `json:"policy_version"`
		Cases         []corpusCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if doc.PolicyVersion != PolicyVersion {
		t.Fatalf("corpus policy_version %q != PolicyVersion %q — the rule table changed without the corpus", doc.PolicyVersion, PolicyVersion)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc.Cases
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate repo root (no go.mod above cwd)")
	return ""
}

// TestPolicyConformanceCorpus is the shared contract. Every row of the #1182
// mismatch table has a stated, tested outcome here.
func TestPolicyConformanceCorpus(t *testing.T) {
	for _, c := range loadCorpus(t) {
		t.Run(c.ID, func(t *testing.T) {
			out := ApplyPolicy(c.Stage, c.Input)

			if got := out.Verdict(); got != c.Expect.Verdict {
				t.Errorf("verdict = %q, want %q (notes: %v)", got, c.Expect.Verdict, out.Summary())
			}

			if len(out.Notes) != len(c.Expect.Notes) {
				t.Fatalf("got %d notes, want %d: %v", len(out.Notes), len(c.Expect.Notes), out.Summary())
			}
			for i, want := range c.Expect.Notes {
				got := out.Notes[i]
				if got.Field != want.Field || string(got.Disposition) != want.Disposition || got.Rule != want.Rule {
					t.Errorf("note[%d] = {%s %s %s}, want {%s %s %s}",
						i, got.Field, got.Disposition, got.Rule, want.Field, want.Disposition, want.Rule)
				}
				if strings.TrimSpace(got.Detail) == "" {
					t.Errorf("note[%d] (%s) has no operator-facing detail", i, got.Rule)
				}
			}

			if c.Expect.Untrustworthy != nil {
				if got := out.Untrustworthy(); !reflect.DeepEqual(got, c.Expect.Untrustworthy) {
					t.Errorf("untrustworthy = %v, want %v", got, c.Expect.Untrustworthy)
				}
			}

			if c.Expect.Doc != nil {
				if !reflect.DeepEqual(out.Doc, c.Expect.Doc) {
					gotJSON, _ := json.Marshal(out.Doc)
					wantJSON, _ := json.Marshal(c.Expect.Doc)
					t.Errorf("normalized doc mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
				}
			}
		})
	}
}

// TestFatalOutcomeDoesNotProceed pins the disposition boundary: a repaired or
// quarantined deliverable proceeds, a fatal one does not — at BOTH validation
// points, because both branch on this one value.
func TestFatalOutcomeDoesNotProceed(t *testing.T) {
	fatal := ApplyPolicy("dev", map[string]any{
		"schema_version": "1.8",
		"files_changed":  []any{"a.ts"},
	})
	if fatal.OK() {
		t.Fatal("a deliverable with no sibling manifest must not proceed")
	}

	repaired := ApplyPolicy("dev", map[string]any{
		"schema_version": "1.8",
		"files_changed":  []any{"a.ts"},
		"files_created":  []any{"a.ts"},
	})
	if !repaired.OK() {
		t.Fatalf("a totally repairable deliverable must proceed: %v", repaired.Summary())
	}

	quarantined := ApplyPolicy("validate", map[string]any{
		"schema_version": "2.6",
		"gate_metrics":   []any{map[string]any{"result": "pass"}},
	})
	if !quarantined.OK() {
		t.Fatal("a quarantined telemetry field must not fail the stage")
	}
	if len(quarantined.Untrustworthy()) == 0 {
		t.Fatal("a quarantined field must be marked untrustworthy — that is the only thing standing between a dropped entry and a corrupted record")
	}
}

// TestRepairIsRecordedInTheDeliverable — #1176 AC: a bad emitter stays visible.
func TestRepairIsRecordedInTheDeliverable(t *testing.T) {
	out := ApplyPolicy("dev", map[string]any{
		"schema_version": "1.5",
		"files_changed":  []any{"a.ts"},
		"files_modified": []any{"a.ts"},
	})
	if !out.Changed {
		t.Fatal("a repaired deliverable must be marked changed so it is written back")
	}
	out.Stamp(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))

	marker, ok := out.Doc[PolicyMarkerField].(map[string]any)
	if !ok {
		t.Fatalf("no %s marker written", PolicyMarkerField)
	}
	if marker["policy_version"] != PolicyVersion {
		t.Errorf("marker policy_version = %v", marker["policy_version"])
	}
	if marker["applied_at"] != "2026-08-29T12:00:00Z" {
		t.Errorf("marker applied_at = %v", marker["applied_at"])
	}
	repairs, _ := marker["repairs"].([]any)
	if len(repairs) != 2 {
		t.Fatalf("expected the schema_version stamp and the files_changed rebuild to be recorded, got %d", len(repairs))
	}
	first, _ := repairs[0].(map[string]any)
	if first["rule"] != "any.schema_version.stamped" {
		t.Errorf("first recorded repair = %v", first)
	}
}

// TestCleanDeliverableGrowsNoMarker — a healthy file must not accumulate
// bookkeeping. The marker's presence is itself the signal.
func TestCleanDeliverableGrowsNoMarker(t *testing.T) {
	out := ApplyPolicy("dev", map[string]any{
		"schema_version": "1.8",
		"files_changed":  map[string]any{"created": []any{}, "modified": []any{"a.ts"}, "deleted": []any{}},
	})
	if out.Changed {
		t.Fatalf("clean deliverable reported changed: %v", out.Summary())
	}
	out.Stamp(time.Now())
	if _, present := out.Doc[PolicyMarkerField]; present {
		t.Fatal("clean deliverable grew a policy marker")
	}
}

// TestApplyPolicyDoesNotMutateInput — the caller's decoded document is evidence
// of what the stage actually wrote; the policy must not edit it in place.
func TestApplyPolicyDoesNotMutateInput(t *testing.T) {
	input := map[string]any{
		"schema_version": "1.5",
		"files_changed":  []any{"a.ts"},
		"files_modified": []any{"a.ts"},
	}
	ApplyPolicy("dev", input)
	if input["schema_version"] != "1.5" {
		t.Errorf("input schema_version mutated to %v", input["schema_version"])
	}
	if _, isArr := input["files_changed"].([]any); !isArr {
		t.Error("input files_changed mutated")
	}
}

// TestSkillIncludesRunTheInStageCheck — #1177's first acceptance criterion is
// "a stage cannot silently emit a deliverable in the wrong shape". That is a
// property of the include, not of this package, and the only thing standing
// between it and a silent regression is this assertion: delete the checker call
// from the include and the Go suite goes red.
//
// `jq . "$FILE"` proves the file is well-formed JSON, which is a different and
// much weaker claim than "matches the contract" — the #1177 deliverable passed
// it comfortably.
func TestSkillIncludesRunTheInStageCheck(t *testing.T) {
	root := repoRoot(t)
	for stage, path := range map[string]string{
		"dev":      filepath.Join(root, "skills", "nightgauge-feature-dev", "_includes", "context-and-epilogue.md"),
		"validate": filepath.Join(root, "skills", "nightgauge-feature-validate", "_includes", "context-and-board.md"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		want := "gate check-deliverable --stage " + stage
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s never runs %q — a stale shape would again be caught only at the gate, after the spend", path, want)
		}
	}
}

// TestSchemaVersionIsAssertedNotAuthored — #1177. The include's literal and the
// binary's constant are the same fact stated twice; this fails the build if
// they drift, which is the only reason it is safe to keep both.
func TestSchemaVersionMatchesSkillIncludes(t *testing.T) {
	root := repoRoot(t)
	includes := map[string]string{
		"dev":      filepath.Join(root, "skills", "nightgauge-feature-dev", "_includes", "context-and-epilogue.md"),
		"validate": filepath.Join(root, "skills", "nightgauge-feature-validate", "_includes", "context-and-board.md"),
	}
	for stage, path := range includes {
		canonical, ok := CanonicalSchemaVersion(stage)
		if !ok {
			t.Fatalf("no canonical schema version for stage %q", stage)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		want := "schema_version: \"" + canonical + "\""
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s does not author %s — the include and CanonicalSchemaVersion(%q) have drifted", path, want, stage)
		}
	}
}
