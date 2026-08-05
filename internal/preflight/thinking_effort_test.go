package preflight

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

func boolPtr(b bool) *bool { return &b }

// The gate must be inert unless the operator actually disabled thinking —
// otherwise it would fail runs over a combination that cannot occur.
func TestThinkingEffort_NoopWhenThinkingEnabled(t *testing.T) {
	r := RunThinkingEffortCheck(ThinkingEffortOptions{
		Efforts:                  map[string]ModelEffort{"stage_efforts.feature-dev": {Model: "claude-opus-5", Effort: "max"}},
		ThinkingDisabledOverride: boolPtr(false),
	})
	if r.ThinkingDisabled {
		t.Fatal("ThinkingDisabled = true, want false")
	}
	if len(r.Findings) != 0 {
		t.Errorf("findings = %d, want 0 when thinking is enabled", len(r.Findings))
	}
}

func TestThinkingEffort_FlagsConflictAboveLimit(t *testing.T) {
	for _, effort := range []string{"xhigh", "max"} {
		r := RunThinkingEffortCheck(ThinkingEffortOptions{
			Efforts:                  map[string]ModelEffort{"stage_efforts.feature-dev": {Model: "claude-opus-5", Effort: effort}},
			ThinkingDisabledOverride: boolPtr(true),
		})
		if len(r.Findings) != 1 {
			t.Fatalf("effort %s: findings = %d, want 1", effort, len(r.Findings))
		}
		f := r.Findings[0]
		if f.MaxAllowed != "high" {
			t.Errorf("effort %s: MaxAllowed = %q, want high", effort, f.MaxAllowed)
		}
		// The message has to be actionable on its own — it is the only thing
		// an operator sees before the run would have 400'd mid-stage.
		for _, want := range []string{"claude-opus-5", effort, DisableThinkingEnvVar, "400"} {
			if !contains(f.Message, want) {
				t.Errorf("effort %s: message %q missing %q", effort, f.Message, want)
			}
		}
	}
}

func TestThinkingEffort_AllowsAtOrBelowLimit(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high"} {
		r := RunThinkingEffortCheck(ThinkingEffortOptions{
			Efforts:                  map[string]ModelEffort{"s": {Model: "claude-opus-5", Effort: effort}},
			ThinkingDisabledOverride: boolPtr(true),
		})
		if len(r.Findings) != 0 {
			t.Errorf("effort %s: findings = %d, want 0", effort, len(r.Findings))
		}
	}
}

// Opus 4.8 declares no constraint — the two settings were independent there.
// A model without the field must never produce a finding, which is what keeps
// this registry-driven rather than a model-name check.
func TestThinkingEffort_ModelWithoutConstraintNeverConflicts(t *testing.T) {
	r := RunThinkingEffortCheck(ThinkingEffortOptions{
		Efforts:                  map[string]ModelEffort{"s": {Model: "claude-opus-4-8", Effort: "xhigh"}},
		ThinkingDisabledOverride: boolPtr(true),
	})
	if len(r.Findings) != 0 {
		t.Errorf("findings = %d, want 0 for a model with no declared constraint", len(r.Findings))
	}
}

// Local models (ollama/lm-studio) have no registry entry by design. Rejecting
// them would break local runs, so they are silently skipped.
func TestThinkingEffort_UnknownModelSkipped(t *testing.T) {
	r := RunThinkingEffortCheck(ThinkingEffortOptions{
		Efforts:                  map[string]ModelEffort{"s": {Model: "qwen3-coder:32b", Effort: "max"}},
		ThinkingDisabledOverride: boolPtr(true),
	})
	if len(r.Findings) != 0 {
		t.Errorf("findings = %d, want 0 for an unknown model", len(r.Findings))
	}
}

// Tier aliases resolve through the registry, so a stage pinned to "opus"
// inherits whichever model currently serves the band.
func TestThinkingEffort_ResolvesTierAlias(t *testing.T) {
	r := RunThinkingEffortCheck(ThinkingEffortOptions{
		Efforts:                  map[string]ModelEffort{"s": {Model: "opus", Effort: "max"}},
		ThinkingDisabledOverride: boolPtr(true),
	})
	if len(r.Findings) != 1 {
		t.Fatalf("findings = %d, want 1 (opus band resolves to a constrained model)", len(r.Findings))
	}
	if r.Findings[0].Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the resolved concrete id", r.Findings[0].Model)
	}
}

func TestIsTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " yes ", "on"} {
		if !isTruthy(v) {
			t.Errorf("isTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "maybe"} {
		if isTruthy(v) {
			t.Errorf("isTruthy(%q) = true, want false", v)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// TestThinkingEffort_NeverCeilingOffersNoEffortRemedy covers the model that
// rejects disabled thinking at every effort (fable). The generic message ends
// with "lower the effort to %q or below" — rendered with a "never" ceiling that
// reads "lower the effort to \"never\" or below", which is not an action an
// operator can take and points away from the only fix.
//
// Asserting the remedy TEXT rather than just "a finding was produced" is
// deliberate: the finding fires either way, so an assertion on count or on
// error-ness passes against the broken message.
func TestThinkingEffort_NeverCeilingOffersNoEffortRemedy(t *testing.T) {
	disabled := true
	res := RunThinkingEffortCheck(ThinkingEffortOptions{
		ThinkingDisabledOverride: &disabled,
		Efforts: map[string]ModelEffort{
			"stage_efforts.feature-dev": {Model: "claude-fable-5", Effort: "low"},
		},
	})
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding for fable + disabled thinking, got %d", len(res.Findings))
	}
	f := res.Findings[0]
	if f.MaxAllowed != models.ThinkingDisableNever {
		t.Errorf("MaxAllowed = %q, want %q", f.MaxAllowed, models.ThinkingDisableNever)
	}
	if strings.Contains(f.Message, "lower the effort") {
		t.Errorf("message offers an effort remedy that cannot work: %q", f.Message)
	}
	if !strings.Contains(f.Message, "every effort") {
		t.Errorf("message should say the ceiling applies at every effort, got: %q", f.Message)
	}
	if !strings.Contains(f.Message, DisableThinkingEnvVar) {
		t.Errorf("message should name the env var to unset, got: %q", f.Message)
	}
}

// TestThinkingEffort_NeverCeilingFiresAtLowestEffort pins the rung an
// Opus-5-shaped implementation would miss: fable conflicts even at "low".
func TestThinkingEffort_NeverCeilingFiresAtLowestEffort(t *testing.T) {
	disabled := true
	for _, effort := range models.EffortOrder {
		res := RunThinkingEffortCheck(ThinkingEffortOptions{
			ThinkingDisabledOverride: &disabled,
			Efforts: map[string]ModelEffort{
				"s": {Model: "claude-fable-5", Effort: effort},
			},
		})
		if len(res.Findings) != 1 {
			t.Errorf("effort %q: expected a finding, got %d", effort, len(res.Findings))
		}
	}
}
