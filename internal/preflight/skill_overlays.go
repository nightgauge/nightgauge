// Skill-overlay gate (#80). ADR 016 §1 splits every stage skill into a
// model-neutral base SKILL.md and per-model _overlays/ fragments. This gate
// makes the split mechanical:
//
//   - overlay-key: every fragment is keyed to something skillrender.OverlayKeys
//     can emit — a registry provider or a non-deprecated concrete id, or (under
//     hosts/) a known adapter. A band name, a deprecated id or a typo is a
//     fragment that can never render.
//   - overlay-budget: every fragment fits OverlayBudgetBytes.
//   - overlay-registry-fact: a fragment does not restate a registry behavior
//     fact (#544); the registry is the single source.
//   - thinking-effort: the resolved stage (model, effort) pairs pass the
//     thinking/effort interlock (delegates to RunThinkingEffortCheck).
//   - base-skill-purity: stage SKILL.md files carry no model-conditional
//     prose. Warn-only until #82 triages the existing hits (purityBlocking).
//   - overlay-override: each whole-file <key>.SKILL.md override is listed as a
//     drift liability (warn).
//
// Deterministic: the registry is the embedded models.All(); no network.
//
// Schema version 1 — field names are stable.
package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/state"
)

// SkillOverlaysSchemaVersion is the JSON envelope version.
const SkillOverlaysSchemaVersion = 1

// OverlayBudgetBytes is the per-fragment size budget (ADR-001 of #80): the
// largest vetted fragment (claude-fable-5-1, 4183 B) plus ~10% headroom.
const OverlayBudgetBytes = 4608

// purityBlocking makes base-skill-purity findings fail the gate. False until
// #82 triages and extracts the existing model-conditional prose.
const purityBlocking = false

// Severities.
const (
	OverlaySeverityFail = "fail"
	OverlaySeverityWarn = "warn"
)

// Rule ids.
const (
	RuleOverlayKey          = "overlay-key"
	RuleOverlayBudget       = "overlay-budget"
	RuleOverlayRegistryFact = "overlay-registry-fact"
	RuleThinkingEffort      = "thinking-effort"
	RuleBaseSkillPurity     = "base-skill-purity"
	RuleOverlayOverride     = "overlay-override"
)

// overlayExemptRe matches the marker that exempts a base-skill line (or the line after it) from
// the purity pass. The reason after the colon must be non-empty.
var overlayExemptRe = regexp.MustCompile(`<!--\s*overlay-exempt:(.*?)-->`)

// SkillOverlayFinding is one gate finding.
type SkillOverlayFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
}

// SkillOverlayExempt is one purity hit suppressed by an exempt marker.
type SkillOverlayExempt struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// SkillOverlaysResult is the stable JSON output.
type SkillOverlaysResult struct {
	V            int                   `json:"v"`
	SkillsRoot   string                `json:"skills_root"`
	FilesChecked int                   `json:"files_checked"`
	BudgetBytes  int                   `json:"budget_bytes"`
	Findings     []SkillOverlayFinding `json:"findings"`
	Warnings     []string              `json:"warnings"`
	Exempt       []SkillOverlayExempt  `json:"exempt"`
	Overrides    []string              `json:"overrides"`
}

// Blocking reports whether any finding fails the gate.
func (r *SkillOverlaysResult) Blocking() bool {
	for _, f := range r.Findings {
		if f.Severity == OverlaySeverityFail {
			return true
		}
	}
	return false
}

// SkillOverlaysOptions parameterizes the gate.
type SkillOverlaysOptions struct {
	// SkillsRoot is the skill tree (e.g. "skills").
	SkillsRoot string
	// Efforts are the resolved (model, effort) pairs for the thinking-effort rule.
	Efforts map[string]ModelEffort
	// ThinkingDisabledOverride forces the env answer (tests).
	ThinkingDisabledOverride *bool
	// EffortsSource labels the file thinking-effort findings point at.
	EffortsSource string
}

// overlayModelKeys returns provider and non-deprecated id keys, computed from
// the registry — the exact set OverlayKeys can emit for a model position.
func overlayModelKeys() map[string]bool {
	keys := map[string]bool{}
	for _, m := range models.All() {
		keys[m.Provider] = true
		if !m.Deprecated {
			keys[m.ID] = true
		}
	}
	return keys
}

// overlayHostKeys returns the adapters a host position can carry.
func overlayHostKeys() map[string]bool {
	keys := map[string]bool{}
	for _, a := range doctor.AllAdapterNames() {
		keys[a] = true
	}
	return keys
}

// decodeOverlayKey inverts skillrender's overlayKeySafe.
func decodeOverlayKey(name string) string {
	return strings.ReplaceAll(name, "__", "/")
}

// RunSkillOverlaysCheck runs every rule over opts.SkillsRoot.
func RunSkillOverlaysCheck(_ context.Context, opts SkillOverlaysOptions) (*SkillOverlaysResult, error) {
	root := opts.SkillsRoot
	if root == "" {
		root = "skills"
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("skills root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skills root %q is not a directory", root)
	}
	r := &SkillOverlaysResult{
		V: SkillOverlaysSchemaVersion, SkillsRoot: root, BudgetBytes: OverlayBudgetBytes,
		Findings: []SkillOverlayFinding{}, Warnings: []string{},
		Exempt: []SkillOverlayExempt{}, Overrides: []string{},
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read skills root: %w", err)
	}
	modelKeys, hostKeys := overlayModelKeys(), overlayHostKeys()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name(), "_overlays")
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		r.checkOverlayDir(dir, false, modelKeys)
		hosts := filepath.Join(dir, "hosts")
		if st, err := os.Stat(hosts); err == nil && st.IsDir() {
			r.checkOverlayDir(hosts, true, hostKeys)
		}
	}

	for _, stage := range state.AllPipelineStages {
		path := filepath.Join(root, "nightgauge-"+string(stage), "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		r.checkPurity(path)
	}

	te := RunThinkingEffortCheck(ThinkingEffortOptions{
		Efforts: opts.Efforts, ThinkingDisabledOverride: opts.ThinkingDisabledOverride,
	})
	src := opts.EffortsSource
	if src == "" {
		src = ".nightgauge/config.yaml"
	}
	for _, f := range te.Findings {
		r.Findings = append(r.Findings, SkillOverlayFinding{
			File: src, Rule: RuleThinkingEffort, Severity: OverlaySeverityFail,
			Message: f.Message,
			Fix:     fmt.Sprintf("unset %s or lower %s to %q or below", DisableThinkingEnvVar, f.Source, f.MaxAllowed),
		})
	}

	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return r, nil
}

func displayPath(path string) string { return filepath.ToSlash(path) }

// unreadable records a file the gate could not inspect as a blocking finding:
// a check that could not run must not pass.
func (r *SkillOverlaysResult) unreadable(path string, err error) {
	r.Findings = append(r.Findings, SkillOverlayFinding{
		File: displayPath(path), Rule: "unreadable", Severity: OverlaySeverityFail,
		Message: fmt.Sprintf("could not read: %v", err),
		Fix:     "fix the file's permissions or remove it",
	})
}

func (r *SkillOverlaysResult) checkOverlayDir(dir string, host bool, valid map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		r.unreadable(dir, err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(dir, name)
		file := displayPath(path)
		r.FilesChecked++
		override := !host && strings.HasSuffix(name, ".SKILL.md")
		key := decodeOverlayKey(strings.TrimSuffix(strings.TrimSuffix(name, ".md"), ".SKILL"))

		if !valid[key] {
			r.Findings = append(r.Findings, SkillOverlayFinding{
				File: file, Rule: RuleOverlayKey, Severity: OverlaySeverityFail,
				Message: overlayKeyMessage(key, host),
				Fix:     "rename the fragment to a live key, or delete it",
			})
		}
		if override {
			r.Overrides = append(r.Overrides, file)
			r.Findings = append(r.Findings, SkillOverlayFinding{
				File: file, Rule: RuleOverlayOverride, Severity: OverlaySeverityWarn,
				Message: "whole-file override replaces the base skill and drifts from it",
				Fix:     "express the difference as an _overlays/<key>.md fragment instead",
			})
			continue // overrides are whole skills, not fragments: no budget/fact rules
		}

		body, err := os.ReadFile(path)
		if err != nil {
			r.unreadable(path, err)
			continue
		}
		if len(body) > OverlayBudgetBytes {
			r.Findings = append(r.Findings, SkillOverlayFinding{
				File: file, Rule: RuleOverlayBudget, Severity: OverlaySeverityFail,
				Message: fmt.Sprintf("fragment is %d bytes, budget is %d", len(body), OverlayBudgetBytes),
				Fix:     "move procedure into the base skill, or split the fragment by a narrower key",
			})
		}
		if !host {
			r.checkRegistryFacts(file, key, string(body))
		}
	}
}

func overlayKeyMessage(key string, host bool) string {
	if host {
		return fmt.Sprintf("host key %q is not a known adapter (%s)", key,
			strings.Join(doctor.AllAdapterNames(), ", "))
	}
	for _, m := range models.All() {
		if m.ID == key && m.Deprecated {
			return fmt.Sprintf("key %q is a deprecated registry id (deprecated: true); it never resolves", key)
		}
		for _, t := range m.Tiers {
			if t == key {
				return fmt.Sprintf("key %q is a tier band, not a provider or concrete id; bands never key an overlay (#582)", key)
			}
		}
	}
	return fmt.Sprintf("key %q is neither a registry provider nor a live model id (typo?)", key)
}

// factModels returns the live models a key covers.
func factModels(key string) []models.ModelDescriptor {
	var out []models.ModelDescriptor
	for _, m := range models.All() {
		if m.Deprecated {
			continue
		}
		if m.ID == key || m.Provider == key {
			out = append(out, m)
		}
	}
	return out
}

func (r *SkillOverlaysResult) checkRegistryFacts(file, key, body string) {
	type fact struct{ name, value string }
	seen := map[string]bool{}
	var facts []fact
	for _, m := range factModels(key) {
		if m.Behavior == nil {
			continue
		}
		for _, f := range []fact{
			{"thinking_default", m.Behavior.ThinkingDefault},
			{"effort_default", m.Behavior.EffortDefault},
			{"thinking_disable_max_effort", m.Behavior.ThinkingDisableMaxEffort},
		} {
			if f.value != "" && !seen[f.name+"="+f.value] {
				seen[f.name+"="+f.value] = true
				facts = append(facts, f)
			}
		}
	}
	lines := strings.Split(body, "\n")
	for _, f := range facts {
		re := factPattern(f.name, f.value)
		if re == nil {
			continue
		}
		for i, line := range lines {
			if re.MatchString(line) {
				r.Findings = append(r.Findings, SkillOverlayFinding{
					File: file, Line: i + 1, Rule: RuleOverlayRegistryFact, Severity: OverlaySeverityFail,
					Message: fmt.Sprintf("restates registry fact %s=%s (#544)", f.name, f.value),
					Fix:     "delete the sentence; the registry behavior block is the source",
				})
			}
		}
	}
}

func factPattern(name, value string) *regexp.Regexp {
	v := regexp.QuoteMeta(value)
	switch name {
	case "thinking_default":
		words := v
		if value == "on" {
			words = `(?:on|enabled)`
		} else if value == "off" {
			words = `(?:off|disabled)`
		}
		return regexp.MustCompile(`(?i)\bthinking\s+(?:is\s+)?` + words + `\s+by\s+default\b|\bthinking\s+defaults\s+to\s+` + words + `\b`)
	case "effort_default":
		return regexp.MustCompile(`(?i)\bdefault\s+effort\s+(?:is\s+)?` + "`?" + v + "`?" + `\b|\beffort\s+defaults\s+to\s+` + "`?" + v + `\b`)
	case "thinking_disable_max_effort":
		if value == models.ThinkingDisableNever {
			return regexp.MustCompile(`(?i)\bthinking\s+(?:can(?:not|'t)|may\s+not)\s+be\s+disabled\b`)
		}
		return regexp.MustCompile(`(?i)\bdisabl\w*\s+thinking\b.*\b(?:above|at\s+or\s+below|up\s+to)\s+` + "`?" + v + `\b`)
	}
	return nil
}

// purityPatterns flag model-conditional prose in a base skill.
var purityPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:Opus|Sonnet|Haiku|Fable|GPT|Gemini|Grok)\b`),
	regexp.MustCompile(`(?i)\bif you are a (?:large|small|smaller|frontier) model\b`),
	regexp.MustCompile(`(?i)\bsmaller models\b`),
}

var purityIDRe = purityIDPattern()

func purityIDPattern() *regexp.Regexp {
	var ids []string
	for _, m := range models.All() {
		ids = append(ids, regexp.QuoteMeta(m.ID))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return regexp.MustCompile(`\b(?:` + strings.Join(ids, "|") + `)\b`)
}

func (r *SkillOverlaysResult) checkPurity(path string) {
	body, err := os.ReadFile(path)
	if err != nil {
		r.unreadable(path, err)
		return
	}
	file := displayPath(path)
	r.FilesChecked++
	severity := OverlaySeverityWarn
	if purityBlocking {
		severity = OverlaySeverityFail
	}
	patterns := append([]*regexp.Regexp{purityIDRe}, purityPatterns...)
	lines := strings.Split(string(body), "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		// An exempt marker with an empty reason is itself a finding.
		if m := overlayExemptRe.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[1]) == "" {
			r.Findings = append(r.Findings, SkillOverlayFinding{
				File: file, Line: i + 1, Rule: RuleBaseSkillPurity, Severity: severity,
				Message: "overlay-exempt marker has no reason",
				Fix:     "write the reason after the colon: <!-- overlay-exempt: <why> -->",
			})
		}
		hit := ""
		stripped := overlayExemptRe.ReplaceAllString(line, "")
		for _, p := range patterns {
			if s := p.FindString(stripped); s != "" {
				hit = s
				break
			}
		}
		if hit == "" {
			continue
		}
		if reason := exemptReason(line); reason == "" && i > 0 {
			if reason = exemptReason(lines[i-1]); reason != "" {
				r.Exempt = append(r.Exempt, SkillOverlayExempt{File: file, Line: i + 1, Reason: reason})
				continue
			}
		} else if reason != "" {
			r.Exempt = append(r.Exempt, SkillOverlayExempt{File: file, Line: i + 1, Reason: reason})
			continue
		}
		r.Findings = append(r.Findings, SkillOverlayFinding{
			File: file, Line: i + 1, Rule: RuleBaseSkillPurity, Severity: severity,
			Message: fmt.Sprintf("base skill names %q: model-conditional prose belongs in an overlay", hit),
			Fix:     "move the sentence into _overlays/<provider-or-id>.md, or mark it <!-- overlay-exempt: <reason> -->",
		})
	}
}

func exemptReason(line string) string {
	if m := overlayExemptRe.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
