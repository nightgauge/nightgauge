package deliverable

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StatusPassedUnverified is the verdict for a run whose gates all passed but
// whose own test deliverable was never executed.
//
// It is deliberately NOT a failure. Demanding that a stage stand up a
// multi-repo Docker stack, a booted emulator and a mail catcher before it may
// report anything would block legitimate work, and the pipeline would learn to
// route around the gate. What was missing was never a veto — it was a verdict
// that does not read as clean, so a human can filter on it and a card can be
// raised. `passed` said the opposite of what happened.
const StatusPassedUnverified = "passed_unverified"

// StatusPassed is the clean verdict this package may supersede.
const StatusPassed = "passed"

// Finding is the derived result for one run.
type Finding struct {
	// Artifacts are the introduced test files whose tier never executed.
	Artifacts []TestArtifact
	// Tiers are the distinct idle tiers, in canonical order.
	Tiers []Tier
	// TierReasons carries each idle tier's own `reason` string from the
	// validate artifact. This is where the stage put the truth — the reported
	// run said "no E2E framework detected by deterministic binary; suite
	// unwired from CI per plan scope", which is a complete explanation that
	// simply never reached the verdict. Surfacing it beats inventing a
	// remediation command the pipeline cannot actually verify.
	TierReasons map[Tier]string
	// SupersededStatus is the verdict the skill rolled up, retained so a retro
	// can see the divergence rather than only the correction.
	SupersededStatus string
}

// Detected reports whether anything was found.
func (f Finding) Detected() bool { return len(f.Artifacts) > 0 }

// Paths returns the finding's file paths.
func (f Finding) Paths() []string {
	out := make([]string, 0, len(f.Artifacts))
	for _, a := range f.Artifacts {
		out = append(out, a.Path)
	}
	return out
}

// Summary is the one-line form used in gate details, the PR annotation and the
// decision-request title.
func (f Finding) Summary() string {
	if !f.Detected() {
		return "all introduced test files were exercised"
	}
	tiers := make([]string, 0, len(f.Tiers))
	for _, t := range f.Tiers {
		tiers = append(tiers, string(t))
	}
	noun := "files"
	if len(f.Artifacts) == 1 {
		noun = "file"
	}
	return fmt.Sprintf("%d test %s added but never executed (%s tier did not run)",
		len(f.Artifacts), noun, strings.Join(tiers, "/"))
}

// validatePath is the per-issue validate context written by feature-validate.
func validatePath(workspace string, issueNumber int) string {
	return filepath.Join(workspace, ".nightgauge", "pipeline",
		fmt.Sprintf("validate-%d.json", issueNumber))
}

// ExecutionFromArtifact reads the per-tier `ran` flags out of a parsed validate
// context.
//
// It reads the sub-fields, never `validation_status`. The roll-up is the thing
// under suspicion; the sub-fields are the stage's factual record of what it
// did, and in the run that produced #152 they were entirely accurate.
func ExecutionFromArtifact(doc map[string]any) Execution {
	return Execution{
		Unit:        tierRan(doc, "unit_tests"),
		Integration: tierRan(doc, "integration_tests"),
		E2E:         tierRan(doc, "e2e_tests"),
	}
}

func tierRan(doc map[string]any, key string) bool {
	block, ok := doc[key].(map[string]any)
	if !ok {
		return false
	}
	ran, _ := block["ran"].(bool)
	return ran
}

func tierReason(doc map[string]any, key string) string {
	block, ok := doc[key].(map[string]any)
	if !ok {
		return ""
	}
	r, _ := block["reason"].(string)
	return r
}

var tierArtifactKey = map[Tier]string{
	TierUnit:        "unit_tests",
	TierIntegration: "integration_tests",
	TierE2E:         "e2e_tests",
}

// Derive computes the finding for a run from its validate artifact and the
// change's own file set.
//
// changedFiles must come from git (`git diff --name-only base...head`), not
// from a stage's self-report of what it wrote. Both inputs to the decision are
// then outside the plan-writes-then-validates-against-its-own-plan loop.
func Derive(doc map[string]any, changedFiles []string) Finding {
	ran := ExecutionFromArtifact(doc)
	artifacts := Unexercised(changedFiles, ran)
	if len(artifacts) == 0 {
		return Finding{}
	}

	tiers := Tiers(artifacts)
	reasons := make(map[Tier]string, len(tiers))
	for _, t := range tiers {
		if r := tierReason(doc, tierArtifactKey[t]); r != "" {
			reasons[t] = r
		}
	}
	status, _ := doc["validation_status"].(string)

	return Finding{
		Artifacts:        artifacts,
		Tiers:            tiers,
		TierReasons:      reasons,
		SupersededStatus: status,
	}
}

// Apply writes the finding back into the validate context: the verdict becomes
// `passed_unverified` and an `unverified_deliverable` block records the
// evidence.
//
// Only a `passed` verdict is superseded. A run that already failed keeps its
// failure — an unexercised deliverable is strictly less urgent than a gate that
// actually caught something, and overwriting `failed` would lose the more
// severe signal.
func Apply(doc map[string]any, f Finding) bool {
	if !f.Detected() {
		return false
	}
	status, _ := doc["validation_status"].(string)
	if !strings.EqualFold(strings.TrimSpace(status), StatusPassed) {
		return false
	}

	tiers := make([]string, 0, len(f.Tiers))
	for _, t := range f.Tiers {
		tiers = append(tiers, string(t))
	}
	reasons := make(map[string]string, len(f.TierReasons))
	for t, r := range f.TierReasons {
		reasons[string(t)] = r
	}

	doc["validation_status"] = StatusPassedUnverified
	doc["unverified_deliverable"] = map[string]any{
		"detected":          true,
		"tiers":             tiers,
		"files":             f.Paths(),
		"tier_reasons":      reasons,
		"superseded_status": f.SupersededStatus,
		"summary":           f.Summary(),
	}
	return true
}

// ReadValidateContext loads the validate artifact as a generic document.
//
// Generic on purpose: Apply writes the corrected verdict back, and decoding
// into a typed struct would silently drop every field this package does not
// know about — which is most of them, and which downstream stages depend on.
func ReadValidateContext(workspace string, issueNumber int) (map[string]any, error) {
	data, err := os.ReadFile(validatePath(workspace, issueNumber))
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse validate context: %w", err)
	}
	return doc, nil
}

// WriteValidateContext persists a validate document, replacing the file.
func WriteValidateContext(workspace string, issueNumber int, doc map[string]any) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode validate context: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(validatePath(workspace, issueNumber), data, 0o644)
}

// FindingFromArtifact re-reads a previously applied finding out of a validate
// document, for consumers (pr-create, the attention sweep) that see the
// artifact after the gate corrected it and must not re-derive it — re-deriving
// needs the git diff, which those consumers may be running too late to obtain.
func FindingFromArtifact(doc map[string]any) Finding {
	block, ok := doc["unverified_deliverable"].(map[string]any)
	if !ok {
		return Finding{}
	}
	if detected, _ := block["detected"].(bool); !detected {
		return Finding{}
	}

	var f Finding
	for _, raw := range toStrings(block["files"]) {
		f.Artifacts = append(f.Artifacts, TestArtifact{Path: raw})
	}
	for _, raw := range toStrings(block["tiers"]) {
		f.Tiers = append(f.Tiers, Tier(raw))
	}
	f.SupersededStatus, _ = block["superseded_status"].(string)
	if reasons, ok := block["tier_reasons"].(map[string]any); ok {
		f.TierReasons = make(map[Tier]string, len(reasons))
		for k, v := range reasons {
			if s, ok := v.(string); ok {
				f.TierReasons[Tier(k)] = s
			}
		}
	}
	// Re-attach tiers to artifacts so Summary() reads the same before and after
	// a round-trip.
	for i := range f.Artifacts {
		f.Artifacts[i].Tiers = f.Tiers
	}
	return f
}

// toStrings reads a string list that may arrive in either of two shapes.
//
// Apply writes `[]string`; a JSON decode of the same document yields `[]any`.
// Both are the same value, and a reader that handles only one is dead against
// the other — the exact failure mode #169 was, where a consumer keyed on the
// delivery shape instead of the data and silently observed nothing.
func toStrings(v any) []string {
	var out []string
	switch raw := v.(type) {
	case []string:
		out = append(out, raw...)
	case []any:
		for _, item := range raw {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	default:
		return nil
	}
	// Stored order is preserved deliberately: files were sorted when written,
	// and tiers were written in canonical unit → integration → e2e order.
	// Re-sorting here would render "integration/e2e" as "e2e/integration".
	return out
}
