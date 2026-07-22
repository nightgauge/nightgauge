package focus_test

// Integration tests for the focus lens system verifying end-to-end behavior
// across the four integration points:
//
//  1. Focus Manager — set/load/clear round-trips and custom lens resolution
//  2. Autonomous Scheduler — focus lens read from focus.yaml at prioritize time
//  3. Release-Watch Assessment — keyword→dimension mapping produces score boosts
//  4. Continuous-Improvement — focus-aligned proposals promoted in output
//
// Tests in this file use real file I/O (no mocks) to validate the actual
// persistence contract that all consumers depend on.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/focus"
)

// ---------------------------------------------------------------------------
// Integration Point 1: Focus Manager — persistence and resolution
// ---------------------------------------------------------------------------

// TestFocusIntegration_SetPersistsAndLoads verifies the full set→persist→load
// round-trip. This is the contract all consumers depend on: when a component
// sets a focus lens, other components reading focus.yaml see the same lens.
func TestFocusIntegration_SetPersistsAndLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := focus.NewManager(dir)

	// Set focus to security
	_, err := m.Set("security", "cli")
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	// A second manager instance simulates a different process reading the file
	m2 := focus.NewManager(dir)
	state, err := m2.Load()
	if err != nil {
		t.Fatalf("Load() (second manager) error: %v", err)
	}

	if state.ActiveLens != "security" {
		t.Errorf("expected persisted lens 'security', got %q", state.ActiveLens)
	}
	if state.SetBy != "cli" {
		t.Errorf("expected SetBy 'cli', got %q", state.SetBy)
	}
	if state.SetAt.IsZero() {
		t.Error("expected non-zero SetAt timestamp")
	}
	if time.Since(state.SetAt) > 5*time.Second {
		t.Errorf("SetAt is too old: %v (expected within 5s)", state.SetAt)
	}
}

// TestFocusIntegration_ClearResetsToGeneral verifies that Clear() writes
// active_lens: general to the file so all consumers see the cleared state.
func TestFocusIntegration_ClearResetsToGeneral(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := focus.NewManager(dir)

	if _, err := m.Set("quality", "vscode"); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Clear("cli"); err != nil {
		t.Fatalf("Clear() error: %v", err)
	}

	// A fresh manager should see general
	m2 := focus.NewManager(dir)
	state, lens, err := m2.Show()
	if err != nil {
		t.Fatalf("Show() error: %v", err)
	}
	if state.ActiveLens != "general" {
		t.Errorf("expected 'general' after clear, got %q", state.ActiveLens)
	}
	if lens.Name != "general" {
		t.Errorf("expected resolved lens 'general', got %q", lens.Name)
	}
	if len(lens.ScoringBoosts) != 0 {
		t.Errorf("general lens should have no scoring boosts, got %v", lens.ScoringBoosts)
	}
}

// TestFocusIntegration_MissingFileDefaultsToGeneral verifies that consumers
// reading a missing focus.yaml get the general (no-boost) lens — ensuring
// backward compatibility with repos that have never used focus mode.
func TestFocusIntegration_MissingFileDefaultsToGeneral(t *testing.T) {
	dir := t.TempDir()
	// Deliberately do NOT create .nightgauge/ or focus.yaml

	m := focus.NewManager(dir)
	state, err := m.Load()
	if err != nil {
		t.Fatalf("Load() on missing file error: %v", err)
	}
	if state.ActiveLens != "general" {
		t.Errorf("expected default 'general', got %q", state.ActiveLens)
	}

	lens := m.ResolveLens(state.ActiveLens, state)
	if lens.Name != "general" {
		t.Errorf("expected resolved 'general', got %q", lens.Name)
	}
	if len(lens.ScoringBoosts) != 0 {
		t.Errorf("default general lens should have no boosts")
	}
}

// TestFocusIntegration_CustomLensRoundTrip verifies that custom lenses defined
// in focus.yaml are persisted and resolved correctly — enabling teams to define
// project-specific lenses (e.g., "mobile") beyond the built-in set.
func TestFocusIntegration_CustomLensRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := focus.NewManager(dir)

	// Write a state with a custom lens directly (simulating manual YAML edit)
	customState := &focus.State{
		ActiveLens: "general",
		CustomLenses: []focus.Lens{
			{
				Name:        "mobile",
				Description: "Mobile app quality — Flutter, iOS, Android",
				ScoringBoosts: map[string]int{
					"cross_repo":           15,
					"developer_experience": 5,
				},
				Keywords: []string{"flutter", "ios", "android", "mobile"},
			},
		},
	}
	if err := m.Save(customState); err != nil {
		t.Fatalf("Save() custom state error: %v", err)
	}

	// Now activate the custom lens
	state, err := m.Set("mobile", "cli")
	if err != nil {
		t.Fatalf("Set() custom lens error: %v", err)
	}
	if state.ActiveLens != "mobile" {
		t.Errorf("expected 'mobile', got %q", state.ActiveLens)
	}

	// Resolve and verify boosts
	lens := m.ResolveLens(state.ActiveLens, state)
	if lens.Name != "mobile" {
		t.Errorf("expected resolved lens 'mobile', got %q", lens.Name)
	}
	if lens.ScoringBoosts["cross_repo"] != 15 {
		t.Errorf("expected cross_repo boost 15, got %d", lens.ScoringBoosts["cross_repo"])
	}
	if lens.ScoringBoosts["developer_experience"] != 5 {
		t.Errorf("expected developer_experience boost 5, got %d", lens.ScoringBoosts["developer_experience"])
	}

	// Verify AllLenses includes both built-ins and the custom lens
	all := m.AllLenses()
	var foundMobile bool
	for _, l := range all {
		if l.Name == "mobile" {
			foundMobile = true
		}
	}
	if !foundMobile {
		t.Error("AllLenses() should include custom 'mobile' lens")
	}
	if len(all) < 9 { // 8 built-in + 1 custom
		t.Errorf("expected at least 9 lenses (8 built-in + 1 custom), got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// Integration Point 2: Autonomous Scheduler — focus from focus.yaml
// ---------------------------------------------------------------------------

// TestFocusIntegration_AutonomousScheduler_ReadsYAML verifies the contract
// between the focus package and the autonomous scheduler: the scheduler reads
// focus.yaml via the focus.Manager and applies keyword-based boosts. This test
// validates the data flow by exercising the Manager directly with the same
// patterns the scheduler uses.
func TestFocusIntegration_AutonomousScheduler_ReadsYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := focus.NewManager(dir)

	// Simulate scheduler startup: set focus before scheduler cycle
	if _, err := m.Set("quality", "cli"); err != nil {
		t.Fatal(err)
	}

	// Simulate scheduler's prioritize() reading focus:
	// 1. Load state
	state, err := m.Load()
	if err != nil {
		t.Fatalf("scheduler Load() error: %v", err)
	}
	// 2. Resolve active lens
	activeLens := m.ResolveLens(state.ActiveLens, state)

	if activeLens.Name != "quality" {
		t.Fatalf("scheduler should see 'quality' lens, got %q", activeLens.Name)
	}

	// 3. Keyword matching (mirrors focusAlignmentScore logic in autonomous.go)
	issueLabels := []string{"coverage", "type:feature"}
	issueTitle := "Add test coverage for auth"

	score := computeAlignmentScore(issueLabels, issueTitle, activeLens)
	if score == 0 {
		t.Error("expected non-zero alignment score for quality-aligned issue")
	}
	if score > 20 {
		t.Errorf("alignment score should be capped at 20, got %d", score)
	}

	// Verify general lens produces zero score
	if _, err := m.Clear("cli"); err != nil {
		t.Fatal(err)
	}
	state2, _ := m.Load()
	generalLens := m.ResolveLens(state2.ActiveLens, state2)
	generalScore := computeAlignmentScore(issueLabels, issueTitle, generalLens)
	if generalScore != 0 {
		t.Errorf("general lens should produce zero score, got %d", generalScore)
	}
}

// TestFocusIntegration_AutonomousScheduler_MissingYAMLBackwardCompat verifies
// the scheduler's backward-compatibility contract: when focus.yaml does not
// exist, Load() returns a default state with ActiveLens="general" and zero
// alignment scores for all issues.
func TestFocusIntegration_AutonomousScheduler_MissingYAMLBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	// No .nightgauge/ directory and no focus.yaml

	m := focus.NewManager(dir)
	state, err := m.Load()
	if err != nil {
		t.Fatalf("Load() on missing focus.yaml should not error, got: %v", err)
	}
	if state.ActiveLens != "general" {
		t.Errorf("missing focus.yaml should default to 'general', got %q", state.ActiveLens)
	}

	activeLens := m.ResolveLens(state.ActiveLens, state)
	score := computeAlignmentScore(
		[]string{"security", "vulnerability"},
		"Fix security vulnerability in auth",
		activeLens,
	)
	if score != 0 {
		t.Errorf("missing focus.yaml: general lens should produce 0 alignment score, got %d", score)
	}
}

// ---------------------------------------------------------------------------
// Integration Point 3: Release-Watch Assessment — score boosts via lens boosts
// ---------------------------------------------------------------------------

// TestFocusIntegration_ReleaseWatch_ScoreBoost verifies that the lens's
// ScoringBoosts map is accessible and contains the expected values that the
// release-watch assessment engine uses to compute score boosts.
func TestFocusIntegration_ReleaseWatch_ScoreBoost(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := focus.NewManager(dir)
	if _, err := m.Set("security", "cli"); err != nil {
		t.Fatal(err)
	}

	state, err := m.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	lens := m.ResolveLens(state.ActiveLens, state)

	// Verify the boosts the release-watch assessment engine will read
	safetyBoost, ok := lens.ScoringBoosts["safety_reliability"]
	if !ok {
		t.Fatal("security lens must have safety_reliability boost")
	}
	if safetyBoost != 15 {
		t.Errorf("security lens safety_reliability boost should be 15, got %d", safetyBoost)
	}

	crossRepoBoost, ok := lens.ScoringBoosts["cross_repo"]
	if !ok {
		t.Fatal("security lens must have cross_repo boost")
	}
	if crossRepoBoost != 5 {
		t.Errorf("security lens cross_repo boost should be 5, got %d", crossRepoBoost)
	}

	// Verify keyword list matches expected values for release-watch filtering
	securityKeywords := map[string]bool{}
	for _, kw := range lens.Keywords {
		securityKeywords[kw] = true
	}
	for _, expected := range []string{"security", "vulnerability", "auth", "CVE"} {
		if !securityKeywords[expected] {
			t.Errorf("security lens missing expected keyword %q", expected)
		}
	}
}

// TestFocusIntegration_ReleaseWatch_GeneralNoBoost verifies that when general
// lens is active, the release-watch assessment receives zero boosts — ensuring
// backward-compatible scoring for projects that haven't set a focus lens.
func TestFocusIntegration_ReleaseWatch_GeneralNoBoost(t *testing.T) {
	dir := t.TempDir()
	m := focus.NewManager(dir)

	// No focus.yaml — defaults to general
	state, _ := m.Load()
	lens := m.ResolveLens(state.ActiveLens, state)

	if lens.Name != "general" {
		t.Fatalf("expected general lens, got %q", lens.Name)
	}
	if len(lens.ScoringBoosts) != 0 {
		t.Errorf("general lens should have no scoring boosts for release-watch, got %v", lens.ScoringBoosts)
	}

	// Simulate release-watch: apply boost for a security-related change
	baseScore := 42
	boost := computeReleaseWatchBoost(baseScore, []string{"security", "CVE"}, lens)
	if boost != baseScore {
		t.Errorf("general lens should not boost release-watch score; expected %d, got %d", baseScore, boost)
	}
}

// TestFocusIntegration_ReleaseWatch_PerformanceLensBoost verifies the
// performance lens boosts performance-related release features.
func TestFocusIntegration_ReleaseWatch_PerformanceLensBoost(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := focus.NewManager(dir)
	if _, err := m.Set("performance", "cli"); err != nil {
		t.Fatal(err)
	}

	state, _ := m.Load()
	lens := m.ResolveLens(state.ActiveLens, state)

	// A "token efficiency" release feature maps to automation_potential
	baseScore := 40
	keywords := []string{"token", "optimize", "cost"}
	boosted := computeReleaseWatchBoost(baseScore, keywords, lens)

	// automation_potential boost is 10 for performance lens
	expectedBoost := lens.ScoringBoosts["automation_potential"]
	if expectedBoost == 0 {
		t.Fatal("performance lens must have automation_potential boost")
	}
	if boosted <= baseScore {
		t.Errorf("performance lens should boost score above %d for token/optimize keywords, got %d", baseScore, boosted)
	}
	if boosted > 100 {
		t.Errorf("release-watch score capped at 100, got %d", boosted)
	}
}

// ---------------------------------------------------------------------------
// Integration Point 4: Continuous-Improvement — proposal weighting
// ---------------------------------------------------------------------------

// TestFocusIntegration_ContinuousImprovement_ProposalWeighting verifies that
// the focus lens's keywords can be used to classify proposals as focus-aligned.
// This mirrors Phase 4 of the continuous-improvement skill.
func TestFocusIntegration_ContinuousImprovement_ProposalWeighting(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := focus.NewManager(dir)
	if _, err := m.Set("reliability", "cli"); err != nil {
		t.Fatal(err)
	}

	state, _ := m.Load()
	lens := m.ResolveLens(state.ActiveLens, state)

	proposals := []struct {
		title     string
		keywords  []string
		wantAlign bool
	}{
		{
			title:     "Fix retry logic in RALPH loop for transient failures",
			keywords:  []string{"retry", "resilient", "recovery"},
			wantAlign: true,
		},
		{
			title:     "Add new dashboard chart feature",
			keywords:  []string{"feature", "dashboard", "chart"},
			wantAlign: false,
		},
		{
			title:     "Improve error handling in pipeline health monitor",
			keywords:  []string{"error", "health", "monitor"},
			wantAlign: true,
		},
		{
			title:     "Update npm dependencies to latest versions",
			keywords:  []string{"dependency", "npm", "update"},
			wantAlign: false,
		},
	}

	for _, p := range proposals {
		t.Run(p.title, func(t *testing.T) {
			aligned := isProposalFocusAligned(p.keywords, lens)
			if aligned != p.wantAlign {
				t.Errorf("proposal %q: expected aligned=%v, got %v (lens=%s, keywords=%v)",
					p.title, p.wantAlign, aligned, lens.Name, lens.Keywords)
			}
		})
	}
}

// TestFocusIntegration_ContinuousImprovement_GeneralNoEffect verifies that
// the general lens does not weight any proposals — all proposals are treated
// equally, maintaining backward compatibility.
func TestFocusIntegration_ContinuousImprovement_GeneralNoEffect(t *testing.T) {
	dir := t.TempDir()
	m := focus.NewManager(dir)

	// No focus.yaml — default general
	state, _ := m.Load()
	lens := m.ResolveLens(state.ActiveLens, state)

	reliabilityProposal := []string{"retry", "resilient", "error", "health"}
	securityProposal := []string{"auth", "vulnerability", "CVE"}
	featureProposal := []string{"feature", "dashboard", "new"}

	for _, kws := range [][]string{reliabilityProposal, securityProposal, featureProposal} {
		if isProposalFocusAligned(kws, lens) {
			t.Errorf("general lens should not mark any proposal as focus-aligned, keywords=%v", kws)
		}
	}
}

// TestFocusIntegration_ContinuousImprovement_MultiLensAlignment verifies that
// different lenses produce different alignment results for the same proposal set.
func TestFocusIntegration_ContinuousImprovement_MultiLensAlignment(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := focus.NewManager(dir)

	proposal := []string{"test", "coverage", "lint", "quality"}

	lensAlignments := map[string]bool{
		"quality":     true,  // quality lens keywords include "test", "coverage", "lint", "quality"
		"security":    false, // security keywords are "security", "vulnerability", "auth", ...
		"performance": false, // performance keywords are "performance", "speed", "token", ...
		"general":     false, // general has no keywords
	}

	for lensName, wantAligned := range lensAlignments {
		if _, err := m.Set(lensName, "test"); err != nil {
			// general can't be set via Set directly in some implementations; use Clear
			if lensName == "general" {
				if _, err := m.Clear("test"); err != nil {
					t.Fatalf("Clear() for general error: %v", err)
				}
			} else {
				t.Fatalf("Set(%q) error: %v", lensName, err)
			}
		}
		state, _ := m.Load()
		lens := m.ResolveLens(state.ActiveLens, state)
		aligned := isProposalFocusAligned(proposal, lens)
		if aligned != wantAligned {
			t.Errorf("lens=%q: expected aligned=%v for proposal keywords %v, got %v",
				lensName, wantAligned, proposal, aligned)
		}
	}
}

// ---------------------------------------------------------------------------
// Helper functions — mirror the logic in real consumers to test the contract
// ---------------------------------------------------------------------------

// computeAlignmentScore mirrors the focusAlignmentScore function from
// internal/orchestrator/autonomous.go. It computes a focus boost for an issue
// based on label and title keyword matching.
func computeAlignmentScore(labels []string, title string, lens *focus.Lens) int {
	if lens == nil || lens.Name == "general" || len(lens.Keywords) == 0 {
		return 0
	}

	score := 0
	titleLower := strings.ToLower(title)

	for _, kw := range lens.Keywords {
		kwLower := strings.ToLower(kw)
		// Labels: +2 per matching label
		for _, label := range labels {
			if strings.Contains(strings.ToLower(label), kwLower) {
				score += 2
			}
		}
		// Title: +1 per keyword found
		if strings.Contains(titleLower, kwLower) {
			score++
		}
	}

	if score > 20 {
		score = 20
	}
	return score
}

// computeReleaseWatchBoost mirrors the scoring boost logic in the
// release-watch assessment engine (Phase 5 of SKILL.md). It applies lens
// boosts based on keyword-to-dimension mapping.
func computeReleaseWatchBoost(baseScore int, changeKeywords []string, lens *focus.Lens) int {
	if lens == nil || lens.Name == "general" || len(lens.ScoringBoosts) == 0 {
		return baseScore
	}

	// Keyword → dimension mapping (mirrors assessment-engine.md)
	dimensionKeywords := map[string][]string{
		"safety_reliability":   {"auth", "permission", "security", "sandbox", "privacy", "vulnerability", "secret", "encrypt", "sanitize", "CVE"},
		"pipeline_stage":       {"tool", "mcp", "agent", "command", "skill", "context", "ability", "plugin", "server"},
		"automation_potential": {"performance", "speed", "token", "cost", "cache", "optimize", "efficient", "reduce"},
		"developer_experience": {"ux", "experience", "ergonomic", "friction", "ui", "usability", "onboard", "interface"},
		"cross_repo":           {"cross", "multi-repo", "workspace", "integration", "ecosystem"},
	}

	boostedDimensions := map[string]bool{}
	for _, ck := range changeKeywords {
		ckLower := strings.ToLower(ck)
		for dimension, dimKeywords := range dimensionKeywords {
			for _, dk := range dimKeywords {
				if strings.Contains(ckLower, dk) || strings.Contains(dk, ckLower) {
					boostedDimensions[dimension] = true
				}
			}
		}
	}

	totalBoost := 0
	for dimension := range boostedDimensions {
		if boost, ok := lens.ScoringBoosts[dimension]; ok {
			totalBoost += boost
		}
	}

	result := baseScore + totalBoost
	if result > 100 {
		result = 100
	}
	return result
}

// isProposalFocusAligned mirrors the proposal alignment check in Phase 4 of
// the continuous-improvement skill. Returns true if any of the proposal's
// keywords match any lens keyword.
func isProposalFocusAligned(proposalKeywords []string, lens *focus.Lens) bool {
	if lens == nil || lens.Name == "general" || len(lens.Keywords) == 0 {
		return false
	}

	lensKWSet := make(map[string]bool, len(lens.Keywords))
	for _, kw := range lens.Keywords {
		lensKWSet[strings.ToLower(kw)] = true
	}

	for _, pk := range proposalKeywords {
		if lensKWSet[strings.ToLower(pk)] {
			return true
		}
	}
	return false
}
