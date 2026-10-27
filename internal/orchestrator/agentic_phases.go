package orchestrator

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
)

// agentic_phases.go is the runner-side phase reporter for the three agentic
// stages (#1887).
//
// The deterministic stages report every phase through stages.PhaseReporter.
// feature-planning, feature-dev and feature-validate had only model-emitted
// markers (~11% of runs) and tool-call inference, so a successful run's
// settlement back-filled most of the registry as `unreported`. This file reads
// what the stage left behind — its handoff file, sibling pipeline artifacts and
// git — after the stage exits 0 and its gate passes, and maps each registry
// phase to a verdict through one rule per phase.
//
// Every `complete` names the fact that proves it. A phase with no such fact
// gets rule kind `none` and stays `unreported`: inventing evidence would bring
// back the #1246 lie this exists to avoid. `passed` (#1924) is never written —
// it is an ordering inference, and the tree counts only `complete` as observed.

// agenticRuleKind classifies how a rule proves its phase. It is documentation
// the coverage test checks, not a dispatch key.
type agenticRuleKind string

const (
	ruleArtifact     agenticRuleKind = "artifact"     // a named fact proves the phase
	rulePrecondition agenticRuleKind = "precondition" // a later artifact needs this phase's output
	ruleTerminal     agenticRuleKind = "terminal"     // exit 0 plus a passing gate
	ruleConditional  agenticRuleKind = "conditional"  // complete, or skipped when the handoff says not applicable
	ruleNone         agenticRuleKind = "none"         // no runner-observable fact exists
)

// AgenticEvidence is everything the reconciler may base a verdict on. It is
// collected once, from the pipeline state directory and git, with no network
// calls. File facts require mtime >= Since so a previous attempt's artifact
// never counts for this one.
type AgenticEvidence struct {
	Stage      state.PipelineStage
	Issue      int
	Since      time.Time
	ExitCode   int
	GatePassed bool

	// Handoff is the stage's own context file, decoded; nil when it is
	// missing, unparseable, stale, or names another issue.
	Handoff map[string]any
	// Predecessor is the previous stage's context file, decoded; nil when
	// absent. Freshness is not required: it was written by an earlier stage.
	Predecessor map[string]any

	ACReconcileFresh  bool
	PlanFileExists    bool
	DecisionsChanged  bool
	AssessmentPresent bool
	// WorktreeChanged: commits on HEAD since Since, or uncommitted changes.
	WorktreeChanged bool
	// Pushed: HEAD equals its upstream.
	Pushed bool
}

type agenticPhaseRule struct {
	kind agenticRuleKind
	// verdict returns "complete", "skipped", or "" for no claim.
	verdict func(ev *AgenticEvidence) string
}

func ifTrue(ok bool) string {
	if ok {
		return "complete"
	}
	return ""
}

func fieldPresent(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func fieldNonNull(m map[string]any, key string) bool {
	v, ok := m[key]
	return ok && v != nil
}

func fieldNonEmpty(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	case bool:
		return v
	case float64:
		return true
	}
	return false
}

func subMap(m map[string]any, key string) map[string]any {
	sub, _ := m[key].(map[string]any)
	return sub
}

// handoffRule is an artifact rule over the stage's own handoff.
func handoffRule(kind agenticRuleKind, pred func(h map[string]any) bool) agenticPhaseRule {
	return agenticPhaseRule{kind: kind, verdict: func(ev *AgenticEvidence) string {
		return ifTrue(ev.Handoff != nil && pred(ev.Handoff))
	}}
}

var (
	noneRule     = agenticPhaseRule{kind: ruleNone, verdict: func(*AgenticEvidence) string { return "" }}
	terminalRule = agenticPhaseRule{kind: ruleTerminal, verdict: func(ev *AgenticEvidence) string {
		return ifTrue(ev.Handoff != nil)
	}}
	freshHandoffRule = agenticPhaseRule{kind: ruleArtifact, verdict: func(ev *AgenticEvidence) string {
		return ifTrue(ev.Handoff != nil)
	}}
	// readPredecessorRule: the handoff carries this issue's number and the
	// predecessor's file exists, so the stage loaded it.
	readPredecessorRule = agenticPhaseRule{kind: rulePrecondition, verdict: func(ev *AgenticEvidence) string {
		return ifTrue(ev.Handoff != nil && ev.Predecessor != nil)
	}}
	assessmentRule = agenticPhaseRule{kind: ruleArtifact, verdict: func(ev *AgenticEvidence) string {
		return ifTrue(ev.AssessmentPresent)
	}}
	// knowledgeReadRule: no knowledge_path means the phase did not apply.
	knowledgeReadRule = agenticPhaseRule{kind: ruleConditional, verdict: func(ev *AgenticEvidence) string {
		if ev.Handoff == nil {
			return ""
		}
		if !fieldNonEmpty(ev.Handoff, "knowledge_path") {
			return "skipped"
		}
		return "complete"
	}}
)

// optionalBlockRule handles validate's mobile_mcp / verify_ui blocks: ran →
// complete, a recorded skipped_reason → skipped, otherwise no claim.
func optionalBlockRule(key string) agenticPhaseRule {
	return agenticPhaseRule{kind: ruleConditional, verdict: func(ev *AgenticEvidence) string {
		block := subMap(ev.Handoff, key)
		if block == nil {
			return ""
		}
		if ran, _ := block["ran"].(bool); ran {
			return "complete"
		}
		if fieldNonEmpty(block, "skipped_reason") {
			return "skipped"
		}
		return ""
	}}
}

// agenticPhaseSpecs maps every registry phase of each agentic stage to one
// rule. TestAgenticSpecsCoverEveryRegistryPhase pins that it stays total.
var agenticPhaseSpecs = map[state.PipelineStage]map[string]agenticPhaseRule{
	state.StageFeaturePlanning: {
		"feedback-context-check": handoffRule(rulePrecondition, func(h map[string]any) bool {
			return fieldPresent(h, "revision_count")
		}),
		"load-context": readPredecessorRule,
		"ac-reconcile": {kind: ruleArtifact, verdict: func(ev *AgenticEvidence) string {
			return ifTrue(ev.ACReconcileFresh || (ev.Handoff != nil && fieldNonNull(ev.Handoff, "ac_reconcile")))
		}},
		"assess-complexity": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonEmpty(subMap(h, "complexity_assessment"), "size_label")
		}),
		"pattern-mining": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "pattern_mining_results")
		}),
		"documentation-analysis": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonEmpty(h, "docs_consulted") || fieldNonEmpty(h, "files_to_read")
		}),
		"knowledge-base-read": knowledgeReadRule,
		"recall-prior-decisions": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "recalled_decisions")
		}),
		"produce-plan": {kind: ruleArtifact, verdict: func(ev *AgenticEvidence) string {
			return ifTrue(ev.PlanFileExists)
		}},
		"write-planning-context": freshHandoffRule,
		"knowledge-base-enrichment": {kind: ruleConditional, verdict: func(ev *AgenticEvidence) string {
			if ev.Handoff == nil {
				return ""
			}
			if !fieldNonEmpty(ev.Handoff, "knowledge_path") {
				return "skipped"
			}
			return ifTrue(fieldNonEmpty(ev.Handoff, "knowledge_entries") && ev.DecisionsChanged)
		}},
		"complete-stage":  terminalRule,
		"self-assessment": assessmentRule,
	},
	state.StageFeatureDev: {
		"validate-environment":   {kind: rulePrecondition, verdict: freshHandoffRule.verdict},
		"read-planning-context":  readPredecessorRule,
		"feedback-context-check": handoffRule(rulePrecondition, func(h map[string]any) bool { return fieldPresent(h, "retry_count") }),
		"plan-verification": {kind: rulePrecondition, verdict: func(ev *AgenticEvidence) string {
			return ifTrue(ev.Handoff != nil && ev.PlanFileExists)
		}},
		"knowledge-base-read": knowledgeReadRule,
		"recall-architectural-constraints": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "architectural_constraints")
		}),
		"standards-loading": handoffRule(rulePrecondition, func(h map[string]any) bool {
			v, _ := subMap(h, "quality_checks")["code_standards"].(string)
			return v != "" && v != "skipped"
		}),
		"implementation": {kind: ruleArtifact, verdict: func(ev *AgenticEvidence) string {
			if ev.Handoff == nil || !ev.WorktreeChanged {
				return ""
			}
			fc := subMap(ev.Handoff, "files_changed")
			return ifTrue(fieldNonEmpty(fc, "created") || fieldNonEmpty(fc, "modified") || fieldNonEmpty(fc, "deleted"))
		}},
		"testing": handoffRule(ruleArtifact, func(h map[string]any) bool {
			ts := subMap(h, "tests_status")
			return fieldNonEmpty(ts, "test_command")
		}),
		"e2e-testing": {kind: ruleConditional, verdict: func(ev *AgenticEvidence) string {
			ts := subMap(ev.Handoff, "tests_status")
			if ts == nil {
				return ""
			}
			if e2e, _ := ts["includes_e2e"].(bool); e2e {
				return "complete"
			}
			return "skipped"
		}},
		"quality-review": handoffRule(ruleArtifact, func(h map[string]any) bool {
			v, _ := subMap(h, "quality_checks")["security_review"].(string)
			return v != "" && v != "skipped"
		}),
		"self-correction":            noneRule,
		"feedback-signal-evaluation": noneRule,
		"write-dev-context":          freshHandoffRule,
		"sync-project-status":        terminalRule,
		"output-summary":             terminalRule,
		"self-assessment":            assessmentRule,
	},
	state.StageFeatureValidate: {
		"validate-environment": {kind: rulePrecondition, verdict: freshHandoffRule.verdict},
		"read-dev-context":     readPredecessorRule,
		"ac-completion-check": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "ac_completion_check")
		}),
		"detect-testing-environment": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonEmpty(h, "project_type")
		}),
		"ptc-detection":   noneRule,
		"freshness-check": noneRule,
		"build-verification": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "build")
		}),
		"dead-code-detection": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "dead_code_warnings")
		}),
		"baseline-comparison": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "preexisting_failures")
		}),
		"run-tests": handoffRule(ruleArtifact, func(h map[string]any) bool {
			ran, _ := subMap(h, "unit_tests")["ran"].(bool)
			return ran
		}),
		"mobile-mcp-tests": optionalBlockRule("mobile_mcp"),
		"verify-ui-gate":   optionalBlockRule("verify_ui"),
		"ci-parity-check": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "ci_parity")
		}),
		"knowledge-coverage-check": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonEmpty(h, "coverage_map_path")
		}),
		// The phase gates commit-and-push: on failure the push is skipped, so a
		// pushed commit under a passing verdict proves it ran.
		"pre-push-merge-validation": {kind: rulePrecondition, verdict: func(ev *AgenticEvidence) string {
			return ifTrue(validatePushed(ev) && validationPassed(ev.Handoff))
		}},
		"generate-checklist": handoffRule(ruleArtifact, func(h map[string]any) bool {
			return fieldNonNull(h, "manual_checklist")
		}),
		"feedback-signal-evaluation": noneRule,
		"commit-and-push": {kind: ruleArtifact, verdict: func(ev *AgenticEvidence) string {
			return ifTrue(validatePushed(ev))
		}},
		"write-validate-context": freshHandoffRule,
		"sync-project-status":    terminalRule,
		"output-summary":         terminalRule,
		"self-assessment":        assessmentRule,
	},
}

func validatePushed(ev *AgenticEvidence) bool {
	return ev.Handoff != nil && ev.Pushed && fieldNonEmpty(ev.Handoff, "commit_sha")
}

func validationPassed(h map[string]any) bool {
	s, _ := h["validation_status"].(string)
	return strings.HasPrefix(s, "passed")
}

// ReconcileAgenticPhases returns the terminal transitions the evidence proves,
// in registry order. It claims nothing for a stage with no registry entry,
// a non-zero exit, a failed gate, or a missing/stale handoff.
func ReconcileAgenticPhases(ev *AgenticEvidence) []PhaseTransition {
	names := state.RegistryPhaseNames(ev.Stage)
	spec := agenticPhaseSpecs[ev.Stage]
	if len(names) == 0 || spec == nil || ev.ExitCode != 0 || !ev.GatePassed || ev.Handoff == nil {
		return nil
	}
	// A phase the stage itself listed as skipped is skipped, whatever its rule.
	declaredSkips := map[string]bool{}
	if list, ok := ev.Handoff["skipped_phases"].([]any); ok {
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				if p, _ := m["phase"].(string); p != "" {
					declaredSkips[p] = true
				}
			}
		}
	}
	var out []PhaseTransition
	for i, name := range names {
		rule, ok := spec[name]
		if !ok {
			continue
		}
		verdict := rule.verdict(ev)
		if declaredSkips[name] {
			verdict = "skipped"
		}
		if verdict == "" {
			continue
		}
		out = append(out, PhaseTransition{Stage: string(ev.Stage), Name: name, Index: i, Total: len(names), Status: verdict})
	}
	return out
}

var agenticHandoff = map[state.PipelineStage]struct{ own, predecessor string }{
	state.StageFeaturePlanning: {"planning", "issue"},
	state.StageFeatureDev:      {"dev", "planning"},
	state.StageFeatureValidate: {"validate", "dev"},
}

// CollectAgenticEvidence reads the facts for one stage of one issue from the
// workspace's pipeline state directory and git.
func CollectAgenticEvidence(workspace string, issue int, stage state.PipelineStage, since time.Time, exitCode int, gatePassed bool) (*AgenticEvidence, error) {
	ev := &AgenticEvidence{Stage: stage, Issue: issue, Since: since, ExitCode: exitCode, GatePassed: gatePassed}
	names, ok := agenticHandoff[stage]
	if !ok {
		return ev, nil
	}
	dir, err := layout.PipelineStateDir(workspace)
	if err != nil {
		return nil, err
	}
	fresh := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && !info.ModTime().Before(since)
	}
	ownPath := filepath.Join(dir, fmt.Sprintf("%s-%d.json", names.own, issue))
	if fresh(ownPath) {
		ev.Handoff = readIssueJSON(ownPath, issue)
	}
	ev.Predecessor = readIssueJSON(filepath.Join(dir, fmt.Sprintf("%s-%d.json", names.predecessor, issue)), issue)
	ev.ACReconcileFresh = fresh(filepath.Join(dir, fmt.Sprintf("ac-reconcile-%d.json", issue)))
	ev.AssessmentPresent = fresh(filepath.Join(dir, "assessments", fmt.Sprintf("%s-%d.json", strings.TrimPrefix(string(stage), "feature-"), issue))) ||
		fresh(filepath.Join(dir, "assessments", fmt.Sprintf("%s-%d.json", stage, issue)))

	planSource := ev.Handoff
	if stage != state.StageFeaturePlanning {
		planSource = ev.Predecessor
	}
	if p, _ := planSource["plan_file"].(string); p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(workspace, p)
		}
		if _, err := os.Stat(p); err == nil {
			ev.PlanFileExists = true
		}
	}
	if kp, _ := ev.Handoff["knowledge_path"].(string); kp != "" {
		if !filepath.IsAbs(kp) {
			kp = filepath.Join(workspace, kp)
		}
		ev.DecisionsChanged = fresh(filepath.Join(kp, "decisions.md"))
	}
	ev.WorktreeChanged = gitChangedSince(workspace, since)
	ev.Pushed = gitHeadPushed(workspace)
	return ev, nil
}

// readIssueJSON decodes path and returns it only when it names issue.
func readIssueJSON(path string, issue int) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	if n, ok := doc["issue_number"].(float64); !ok || int(n) != issue {
		return nil
	}
	return doc
}

func gitChangedSince(workspace string, since time.Time) bool {
	if out, err := exec.Command("git", "-C", workspace, "status", "--porcelain").Output(); err == nil &&
		strings.TrimSpace(string(out)) != "" {
		return true
	}
	out, err := exec.Command("git", "-C", workspace, "log", "-1", "--format=%ct", "HEAD").Output()
	if err != nil {
		return false
	}
	var ts int64
	if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &ts); err != nil {
		return false
	}
	return !time.Unix(ts, 0).Before(since.Truncate(time.Second))
}

func gitHeadPushed(workspace string) bool {
	head, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	if err != nil {
		return false
	}
	up, err := exec.Command("git", "-C", workspace, "rev-parse", "@{u}").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(head)) == strings.TrimSpace(string(up))
}

// reconcileAgenticPhases writes the post-exit evidence for an agentic stage
// that exited 0 and passed its gate into both sinks deterministicPhaseReporter
// writes (#1887). It runs after CompleteStage has back-filled silence as
// `unreported`; ObservePhase upgrades only those records, so a marker or an
// inference result always keeps its outcome. A no-op for deterministic stages.
func (s *Scheduler) reconcileAgenticPhases(rt *state.RuntimeState, repo string, issue int, stage state.PipelineStage, workspace string, since time.Time) {
	if rt == nil || len(state.RegistryPhaseNames(stage)) == 0 {
		return
	}
	ev, err := CollectAgenticEvidence(workspace, issue, stage, since, 0, true)
	if err != nil {
		log.Printf("#%d: stage %s — phase evidence unavailable: %v", issue, stage, err)
		return
	}
	for _, t := range ReconcileAgenticPhases(ev) {
		if !rt.ObservePhase(stage, t.Name, t.Index, t.Total, t.Status) {
			continue
		}
		if s.onPhaseSettled != nil {
			s.onPhaseSettled(repo, issue, t.Stage, t.Name, t.Index, t.Total, t.Status)
		}
	}
}
