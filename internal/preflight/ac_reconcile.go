// Deterministic acceptance-criteria reconciliation (#193 / Issue #3003).
//
// `preflight ac-reconcile` was spec'd, documented (docs/CONTEXT_ARCHITECTURE.md
// §ac-reconcile-{N}.json), phase-registered, and invoked by feature-planning —
// but never implemented in the binary, so every run silently lost the
// deterministic short-circuit ("unknown command" swallowed by `|| echo`).
//
// This is the Go port of the SDK reconciler
// (packages/nightgauge-sdk/src/preflight/): parse Markdown checkbox ACs from
// the issue body, classify each via a first-match rule library evaluated
// against the working tree, and aggregate into a suggested route
// (verify-and-close / narrow-scope / standard). Zero LLM tokens; pure within
// a given body + working tree.
package preflight

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AC classification values (per-criterion).
const (
	ACSatisfied    = "satisfied"
	ACPartial      = "partial"
	ACUnsatisfied  = "unsatisfied"
	ACUndetectable = "undetectable"
)

// Aggregate status values.
const (
	AggAllSatisfied    = "all-satisfied"
	AggMostlySatisfied = "mostly-satisfied"
	AggPartial         = "partial"
	AggUnsatisfied     = "unsatisfied"
	AggUndetectable    = "undetectable"
	AggNoACsDetected   = "no-acs-detected"
)

// MostlySatisfiedThreshold mirrors the SDK reconciler: ≥80% satisfied with
// zero unsatisfied aggregates to mostly-satisfied / narrow-scope.
const MostlySatisfiedThreshold = 0.8

// ACReconcileResult is the ac-reconcile-{N}.json schema (v1.0), stable and
// consumed by feature-planning via fixed jq paths (.aggregate_status,
// .suggested_route.approach, .suggested_route.focus_acs).
type ACReconcileResult struct {
	SchemaVersion      string                `json:"schema_version"`
	IssueNumber        int                   `json:"issue_number"`
	MainSHA            string                `json:"main_sha"`
	EvaluatedAt        string                `json:"evaluated_at"`
	AcceptanceCriteria []ReconciledCriterion `json:"acceptance_criteria"`
	AggregateStatus    string                `json:"aggregate_status"`
	SuggestedRoute     SuggestedRoute        `json:"suggested_route"`
}

// ReconciledCriterion is one checkbox AC with its deterministic verdict.
type ReconciledCriterion struct {
	Index          int      `json:"index"`
	Text           string   `json:"text"`
	CheckboxState  string   `json:"checkbox_state"` // "checked" | "unchecked" — informational only
	RuleApplied    *string  `json:"rule_applied"`
	Classification string   `json:"classification"`
	Reason         string   `json:"reason"`
	Evidence       []string `json:"evidence"`
}

// SuggestedRoute tells the planner how to proceed.
type SuggestedRoute struct {
	Approach  string `json:"approach"` // "verify-and-close" | "narrow-scope" | "standard"
	FocusACs  []int  `json:"focus_acs"`
	Rationale string `json:"rationale"`
}

// acCheckbox is a parsed Markdown checkbox before rule evaluation.
type acCheckbox struct {
	index int
	text  string
	state string
}

// checkboxRE mirrors the SDK parser: `-`/`*` bullets, any indentation,
// `[ ]` / `[x]` / `[X]` states. The SDK bounds the text capture at 4096
// chars defensively; RE2 caps repeat counts at 1000 and guarantees linear
// time regardless, so the Go port bounds in code instead (see parseACs).
var checkboxRE = regexp.MustCompile(`(?m)^[ \t]*[-*][ \t]+\[([ xX])\][ \t]+(.+?)[ \t]*$`)

// parseACs extracts checkbox acceptance criteria from an issue body in
// order. Checkbox state is informational — satisfaction is verified against
// the working tree, not the box.
func parseACs(body string) []acCheckbox {
	var out []acCheckbox
	for _, m := range checkboxRE.FindAllStringSubmatch(body, -1) {
		text := strings.TrimSpace(m[2])
		if text == "" {
			continue
		}
		// Defensive cap mirroring the SDK's {1,4096} bound.
		if len(text) > 4096 {
			text = text[:4096]
		}
		state := "unchecked"
		if m[1] != " " {
			state = "checked"
		}
		out = append(out, acCheckbox{index: len(out), text: text, state: state})
	}
	return out
}

// acRule is one entry in the first-match rule library. applies returns
// extracted params (nil = rule does not apply); evaluate classifies against
// the working tree.
type acRule struct {
	name     string
	applies  func(text string) map[string]string
	evaluate func(workdir string, extracted map[string]string) (classification, reason string, evidence []string)
}

// acRules mirrors the SDK registry order — first applicable rule wins,
// most-specific first.
func acRules() []acRule {
	return []acRule{
		workflowJobNamedRule(),
		branchProtectionRule(),
		npmScriptDefinedRule(),
		docSectionPresentRule(),
		grepForSymbolRule(),
		fileExistsRule(),
	}
}

// ReconcileACs runs the deterministic reconciliation and returns the report.
func ReconcileACs(workdir string, issueNumber int, issueBody string) *ACReconcileResult {
	acs := parseACs(issueBody)
	rules := acRules()

	criteria := make([]ReconciledCriterion, 0, len(acs))
	for _, ac := range acs {
		rc := ReconciledCriterion{
			Index:          ac.index,
			Text:           ac.text,
			CheckboxState:  ac.state,
			Classification: ACUndetectable,
			Reason:         "No rule matched the acceptance-criterion text",
			Evidence:       []string{},
		}
		for _, rule := range rules {
			extracted := rule.applies(ac.text)
			if extracted == nil {
				continue
			}
			name := rule.name
			rc.RuleApplied = &name
			rc.Classification, rc.Reason, rc.Evidence = rule.evaluate(workdir, extracted)
			if rc.Evidence == nil {
				rc.Evidence = []string{}
			}
			break
		}
		criteria = append(criteria, rc)
	}

	status, route := deriveAggregate(criteria)

	return &ACReconcileResult{
		SchemaVersion:      "1.0",
		IssueNumber:        issueNumber,
		MainSHA:            resolveMainSHA(workdir),
		EvaluatedAt:        time.Now().UTC().Format(time.RFC3339),
		AcceptanceCriteria: criteria,
		AggregateStatus:    status,
		SuggestedRoute:     route,
	}
}

// WriteACReconcile serializes the report to outPath (creating parent dirs).
func WriteACReconcile(result *ACReconcileResult, outPath string) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ac-reconcile report: %w", err)
	}
	if dir := filepath.Dir(outPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output dir: %w", err)
		}
	}
	if err := os.WriteFile(outPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write ac-reconcile report: %w", err)
	}
	return nil
}

// deriveAggregate maps per-AC classifications to the aggregate status and
// suggested route. Mirrors the SDK's deriveAggregate exactly.
func deriveAggregate(criteria []ReconciledCriterion) (string, SuggestedRoute) {
	if len(criteria) == 0 {
		return AggNoACsDetected, SuggestedRoute{
			Approach:  "standard",
			FocusACs:  []int{},
			Rationale: "No acceptance-criterion checkboxes detected in issue body",
		}
	}

	counts := map[string]int{}
	for _, c := range criteria {
		counts[c.Classification]++
	}
	total := len(criteria)

	if counts[ACSatisfied] == total {
		return AggAllSatisfied, SuggestedRoute{
			Approach:  "verify-and-close",
			FocusACs:  []int{},
			Rationale: "All acceptance criteria satisfied — verify and close without further work",
		}
	}
	if counts[ACUnsatisfied] == total {
		focus := make([]int, 0, total)
		for _, c := range criteria {
			focus = append(focus, c.Index)
		}
		return AggUnsatisfied, SuggestedRoute{
			Approach:  "standard",
			FocusACs:  focus,
			Rationale: "No acceptance criteria satisfied — implement the full plan",
		}
	}
	if counts[ACUndetectable] == total {
		return AggUndetectable, SuggestedRoute{
			Approach:  "standard",
			FocusACs:  []int{},
			Rationale: "No acceptance criteria could be deterministically evaluated — proceed with the standard plan",
		}
	}

	focus := make([]int, 0, total)
	for _, c := range criteria {
		if c.Classification != ACSatisfied {
			focus = append(focus, c.Index)
		}
	}
	ratio := float64(counts[ACSatisfied]) / float64(total)
	if ratio >= MostlySatisfiedThreshold && counts[ACUnsatisfied] == 0 {
		return AggMostlySatisfied, SuggestedRoute{
			Approach:  "narrow-scope",
			FocusACs:  focus,
			Rationale: fmt.Sprintf("%d/%d criteria satisfied — narrow plan scope to the remaining %d", counts[ACSatisfied], total, len(focus)),
		}
	}
	return AggPartial, SuggestedRoute{
		Approach:  "standard",
		FocusACs:  focus,
		Rationale: fmt.Sprintf("%d/%d criteria satisfied; remaining work warrants standard plan", counts[ACSatisfied], total),
	}
}

// resolveMainSHA returns `git rev-parse main` (falling back to origin/main,
// then HEAD) in workdir, or "unknown" — worktrees may lack a local main ref.
func resolveMainSHA(workdir string) string {
	for _, ref := range []string{"main", "origin/main", "HEAD"} {
		cmd := exec.Command("git", "rev-parse", "--short=10", ref)
		cmd.Dir = workdir
		out, err := cmd.Output()
		if err == nil {
			if sha := strings.TrimSpace(string(out)); sha != "" {
				return sha
			}
		}
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// Rule library (Go port of packages/nightgauge-sdk/src/preflight/ac-rules/).
// ---------------------------------------------------------------------------

var (
	acPathRE         = regexp.MustCompile(`([A-Za-z0-9_./-]+\.(?:md|ts|tsx|js|jsx|json|sh|ya?ml|toml|go|py|rs))`)
	acPresenceVerbRE = regexp.MustCompile(`(?i)\b(exists|created|present|added|new\s+file)\b`)
)

func fileExistsRule() acRule {
	return acRule{
		name: "file-exists",
		applies: func(text string) map[string]string {
			if !acPresenceVerbRE.MatchString(text) {
				return nil
			}
			m := acPathRE.FindStringSubmatch(text)
			if m == nil {
				return nil
			}
			return map[string]string{"path": m[1]}
		},
		evaluate: func(workdir string, x map[string]string) (string, string, []string) {
			if _, err := os.Stat(filepath.Join(workdir, x["path"])); err == nil {
				return ACSatisfied, "File present: " + x["path"], []string{x["path"]}
			}
			return ACUnsatisfied, "File not found: " + x["path"], nil
		},
	}
}

var (
	acDeclRE       = regexp.MustCompile(`(?i)\b(?:function|class|const|interface|type|method)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	acBacktickRE   = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]{2,})`")
	acSymbolVerbRE = regexp.MustCompile(`(?i)\b(added|implemented|exported|defined|introduced)\b`)

	acExcludedDirs = map[string]bool{
		"node_modules": true, "dist": true, "build": true, "out": true,
		".git": true, "coverage": true, ".nightgauge": true,
		".next": true, ".turbo": true,
	}
	acSourceExts = map[string]bool{
		".ts": true, ".tsx": true, ".js": true, ".jsx": true,
		".mjs": true, ".cjs": true, ".go": true, ".py": true, ".rs": true,
	}
)

func grepForSymbolRule() acRule {
	return acRule{
		name: "grep-for-symbol",
		applies: func(text string) map[string]string {
			if !acSymbolVerbRE.MatchString(text) {
				return nil
			}
			if m := acDeclRE.FindStringSubmatch(text); m != nil {
				return map[string]string{"symbol": m[1]}
			}
			if m := acBacktickRE.FindStringSubmatch(text); m != nil {
				return map[string]string{"symbol": m[1]}
			}
			return nil
		},
		evaluate: func(workdir string, x map[string]string) (string, string, []string) {
			refs := findSymbolReferences(workdir, x["symbol"], 5)
			if len(refs) == 0 {
				return ACUnsatisfied, fmt.Sprintf("Symbol `%s` not found in workspace", x["symbol"]), nil
			}
			return ACSatisfied, fmt.Sprintf("Symbol `%s` found in %d file(s)", x["symbol"], len(refs)), refs
		},
	}
}

// findSymbolReferences walks workdir source files for word-boundary matches.
func findSymbolReferences(workdir, symbol string, limit int) []string {
	symbolRE, err := regexp.Compile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	if err != nil {
		return nil
	}
	var results []string
	_ = filepath.WalkDir(workdir, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(results) >= limit {
			if len(results) >= limit {
				return filepath.SkipAll
			}
			return nil
		}
		if d.IsDir() {
			if acExcludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !acSourceExts[filepath.Ext(d.Name())] {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > 1024*1024 {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if symbolRE.Match(data) {
			rel, relErr := filepath.Rel(workdir, path)
			if relErr != nil {
				rel = path
			}
			results = append(results, rel)
		}
		return nil
	})
	return results
}

var (
	acDocRE     = regexp.MustCompile(`(docs/[A-Za-z0-9_./-]+\.md|[A-Za-z0-9_/-]+\.md)`)
	acSectionRE = regexp.MustCompile("(?:section|heading|chapter)\\s+`([^`]+)`|##+\\s+([A-Za-z0-9 _-]{2,})")
	acDocVerbRE = regexp.MustCompile(`(?i)\b(documented|documentation)\b`)
)

func docSectionPresentRule() acRule {
	return acRule{
		name: "doc-section-present",
		applies: func(text string) map[string]string {
			if !acDocVerbRE.MatchString(text) {
				return nil
			}
			doc := acDocRE.FindStringSubmatch(text)
			if doc == nil {
				return nil
			}
			sec := acSectionRE.FindStringSubmatch(text)
			if sec == nil {
				return nil
			}
			section := strings.TrimSpace(sec[1])
			if section == "" {
				section = strings.TrimSpace(sec[2])
			}
			if section == "" {
				return nil
			}
			return map[string]string{"doc": doc[1], "section": section}
		},
		evaluate: func(workdir string, x map[string]string) (string, string, []string) {
			data, err := os.ReadFile(filepath.Join(workdir, x["doc"]))
			if err != nil {
				return ACUnsatisfied, "Doc file not found: " + x["doc"], nil
			}
			sectionRE, reErr := regexp.Compile(`(?im)^#+\s+` + regexp.QuoteMeta(x["section"]) + `\s*$`)
			if reErr != nil {
				return ACUndetectable, "Could not compile section pattern", nil
			}
			if sectionRE.Match(data) {
				return ACSatisfied, fmt.Sprintf("Section `%s` present in %s", x["section"], x["doc"]), []string{x["doc"]}
			}
			return ACUnsatisfied, fmt.Sprintf("Section `%s` not found in %s", x["section"], x["doc"]), nil
		},
	}
}

var acNpmRunRE = regexp.MustCompile("(?:`npm\\s+run\\s+([A-Za-z0-9:_-]+)`|\\bnpm\\s+run\\s+([A-Za-z0-9:_-]+)\\b|script\\s+`([A-Za-z0-9:_-]+)`)")

func npmScriptDefinedRule() acRule {
	return acRule{
		name: "npm-script-defined",
		applies: func(text string) map[string]string {
			m := acNpmRunRE.FindStringSubmatch(text)
			if m == nil {
				return nil
			}
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if name == "" {
				name = m[3]
			}
			if name == "" {
				return nil
			}
			return map[string]string{"script": name}
		},
		evaluate: func(workdir string, x map[string]string) (string, string, []string) {
			matches := findPackageJSONScripts(workdir, x["script"])
			if len(matches) == 0 {
				return ACUnsatisfied, fmt.Sprintf("npm script `%s` not defined in any package.json", x["script"]), nil
			}
			return ACSatisfied, fmt.Sprintf("npm script `%s` defined in %d package.json file(s)", x["script"], len(matches)), matches
		},
	}
}

func findPackageJSONScripts(workdir, scriptName string) []string {
	var evidence []string
	_ = filepath.WalkDir(workdir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if acExcludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "package.json" {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > 5*1024*1024 {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) != nil {
			return nil
		}
		if _, ok := pkg.Scripts[scriptName]; ok {
			rel, relErr := filepath.Rel(workdir, path)
			if relErr != nil {
				rel = path
			}
			evidence = append(evidence, rel)
		}
		return nil
	})
	return evidence
}

var (
	acWorkflowRE = regexp.MustCompile(`\.github/workflows/([A-Za-z0-9_.-]+\.ya?ml)`)
	acJobRE      = regexp.MustCompile("\\bjob(?:\\s+named)?\\s+`([A-Za-z0-9_-]+)`|\\bjob:?\\s+`([A-Za-z0-9_-]+)`")
)

func workflowJobNamedRule() acRule {
	return acRule{
		name: "workflow-job-named",
		applies: func(text string) map[string]string {
			wf := acWorkflowRE.FindStringSubmatch(text)
			if wf == nil {
				return nil
			}
			job := acJobRE.FindStringSubmatch(text)
			if job == nil {
				return nil
			}
			jobName := job[1]
			if jobName == "" {
				jobName = job[2]
			}
			if jobName == "" {
				return nil
			}
			return map[string]string{"workflow": wf[1], "job": jobName}
		},
		evaluate: func(workdir string, x map[string]string) (string, string, []string) {
			wfPath := filepath.Join(workdir, ".github", "workflows", x["workflow"])
			data, err := os.ReadFile(wfPath)
			if err != nil {
				return ACUnsatisfied, "Workflow file not found: .github/workflows/" + x["workflow"], nil
			}
			jobKeyRE, reErr := regexp.Compile(`(?m)^\s+` + regexp.QuoteMeta(x["job"]) + `:`)
			if reErr != nil {
				return ACUndetectable, "Could not compile job pattern", nil
			}
			if strings.Contains(string(data), "\njobs:") || strings.HasPrefix(string(data), "jobs:") {
				if jobKeyRE.Match(data) {
					return ACSatisfied,
						fmt.Sprintf("Workflow .github/workflows/%s defines job `%s`", x["workflow"], x["job"]),
						[]string{".github/workflows/" + x["workflow"]}
				}
			}
			return ACUnsatisfied,
				fmt.Sprintf("Workflow .github/workflows/%s does not define job `%s`", x["workflow"], x["job"]),
				nil
		},
	}
}

var (
	acProtectionRE    = regexp.MustCompile(`(?i)\bbranch\s+protection\b.*?\bmain\b`)
	acRequiredCheckRE = regexp.MustCompile("(?i)required\\s+(?:check|status\\s+check)\\s+`([^`]+)`")
)

// branchProtectionRule matches branch-protection ACs so they are not
// misrouted to a later rule, but classifies them undetectable: verifying
// branch protection requires a forge API call, which this offline
// deterministic gate does not make (the SDK rule shells out to `gh`).
func branchProtectionRule() acRule {
	return acRule{
		name: "branch-protection-rule-present",
		applies: func(text string) map[string]string {
			if !acProtectionRE.MatchString(text) {
				return nil
			}
			x := map[string]string{}
			if m := acRequiredCheckRE.FindStringSubmatch(text); m != nil {
				x["check"] = m[1]
			}
			return x
		},
		evaluate: func(_ string, _ map[string]string) (string, string, []string) {
			return ACUndetectable,
				"Branch-protection state requires a forge API call — not evaluated by the offline deterministic gate",
				nil
		},
	}
}
