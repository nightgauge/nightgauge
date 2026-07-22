package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EvaluateStageGate is the PreToolUse:Bash fence that stops pipeline ANALYSIS
// stages from advancing git/forge state outside their mandate (#4145).
//
// Incident (#4142): an issue-pickup subagent edited a doc, opened a PR, and
// merged it to main — bypassing every stage gate — because each stage's
// SKILL.md grants unscoped `Bash`, so the subagent could run any git/gh command.
//
// The fence is keyed on the active pipeline stage (the NIGHTGAUGE_STAGE env
// var the adapters set when running a stage subagent). Outside a pipeline stage
// (env unset → interactive/non-pipeline use) it is a no-op, so it never gets in
// a developer's way. It fails OPEN on any parse error so it can never wedge a
// session.
//
// Scope was tightened from the original proposal after auditing what each
// analysis stage LEGITIMATELY does (the issue explicitly asked to stop and
// document if observed behavior differs):
//
//   - issue-pickup legitimately `git push`es the new feature branch (Step 6.1)
//     and self-assigns via `forge issue edit` — so a blanket git-push / issue-edit
//     block would break the pipeline.
//   - feature-validate legitimately `git commit`s + `git push`es the validated
//     work before pr-create (Step 5.2/5.3) — so a git-commit block would break it.
//   - NONE of the three analysis stages ever creates, merges, or marks-ready a
//     PR/MR, or closes an issue — those belong exclusively to pr-create / pr-merge
//     and the deterministic post-merge hook.
//
// Resulting two-tier fence:
//
//   - forge PR/MR lifecycle mutations (pr|mr create|merge|ready, issue close) are
//     blocked for ALL THREE analysis stages — the primary defense that neutralizes
//     the incident with zero false-positive risk;
//   - git commit/merge/rebase/cherry-pick/revert are ALSO blocked for the
//     pure-analysis stages (issue-pickup, feature-planning) that never author
//     commits — defense in depth against committing hallucinated changes.
//     feature-validate is exempt from this tier because it legitimately commits.
func EvaluateStageGate(inputJSON []byte) GateDecision {
	return evaluateStageGate(inputJSON, os.Getenv("NIGHTGAUGE_STAGE"))
}

// forgeFencedStages may not advance the forge PR/MR lifecycle or close issues.
var forgeFencedStages = map[string]bool{
	"issue-pickup":     true,
	"feature-planning": true,
	"feature-validate": true,
}

// commitFencedStages additionally may not author git commits / rewrite history.
// feature-validate is intentionally absent — it legitimately commits validated work.
var commitFencedStages = map[string]bool{
	"issue-pickup":     true,
	"feature-planning": true,
}

// evaluateStageGate is the pure, testable core: it takes the stage explicitly so
// tests don't depend on process env.
func evaluateStageGate(inputJSON []byte, stage string) GateDecision {
	if stage == "" {
		return Allow() // interactive/non-pipeline use → no-op
	}

	var input GateInput
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return Allow() // fail open on parse error
	}
	if input.ToolName != "Bash" {
		return Allow()
	}
	var ti BashToolInput
	if err := json.Unmarshal(input.ToolInput, &ti); err != nil || ti.Command == "" {
		return Allow()
	}

	segments := ExpandWrappers(SplitSegments(ti.Command))

	// Admin/auto merge bypass is banned in EVERY pipeline stage — including
	// pr-merge, whose agent once improvised `gh pr merge --admin` against
	// branch protection when the merge dead-ended (#186 / bowlsheet#233).
	// A blocked merge is terminal: escalate, never bypass.
	if reason := adminMergeBypassReason(segments); reason != "" {
		return Block(fmt.Sprintf(
			"admin/auto merge bypass is prohibited in pipeline sessions — blocked: %s. A merge blocked by branch protection or required checks is terminal: report the blocker and escalate (#186).",
			reason))
	}

	if !forgeFencedStages[stage] && !commitFencedStages[stage] {
		return Allow() // not a fenced stage → only the merge-bypass ban applies
	}

	if forgeFencedStages[stage] {
		if reason := mutatingForgeReason(segments); reason != "" {
			return Block(fmt.Sprintf(
				"the %s stage must not advance forge PR state — blocked: %s. Creating/merging PRs and closing issues belong to the pr-create / pr-merge stages, not an analysis stage (#4145).",
				stage, reason))
		}
	}
	if commitFencedStages[stage] {
		if reason := mutatingGitReason(segments); reason != "" {
			return Block(fmt.Sprintf(
				"the %s stage must not author commits or rewrite history — blocked: %s. Committing implementation belongs to feature-dev / feature-validate (#4145).",
				stage, reason))
		}
	}
	return Allow()
}

// adminMergeBypassReason returns a short reason when any segment runs a
// forge PR/MR merge with --admin or --auto, else "". Matches gh, glab, and
// the nightgauge binary's forge dispatch on the real argv (env prefixes
// stripped, wrappers expanded) so prohibition prose never trips it.
func adminMergeBypassReason(segments []Segment) string {
	for _, seg := range segments {
		argv := seg.CommandArgv()
		if len(argv) == 0 {
			continue
		}
		var scanFrom []string
		var label string
		switch baseName(argv[0]) {
		case "gh", "glab":
			scanFrom, label = argv[1:], baseName(argv[0])
		case "nightgauge", "ib":
			if len(argv) >= 2 && argv[1] == "forge" {
				scanFrom, label = argv[2:], "nightgauge forge"
			} else {
				scanFrom, label = argv[1:], "nightgauge"
			}
		default:
			continue
		}
		resource, verb := forgeResourceVerb(scanFrom)
		if (resource != "pr" && resource != "mr") || verb != "merge" {
			continue
		}
		for _, arg := range scanFrom {
			if arg == "--admin" || arg == "--auto" {
				return fmt.Sprintf("%s %s merge %s", label, resource, arg)
			}
		}
	}
	return ""
}

// mutatingForgeReason returns a short reason naming a forge PR/MR-lifecycle
// mutation if any segment performs one, else "". Detects gh / glab / the
// nightgauge Go binary (`forge` subcommand or direct), matching on the
// real argv (env prefixes stripped, wrappers expanded) so prose never trips it.
func mutatingForgeReason(segments []Segment) string {
	for _, seg := range segments {
		argv := seg.CommandArgv()
		if len(argv) == 0 {
			continue
		}
		var scanFrom []string
		var label string
		switch baseName(argv[0]) {
		case "gh", "glab":
			scanFrom, label = argv[1:], baseName(argv[0])
		case "nightgauge", "ib":
			if len(argv) >= 2 && argv[1] == "forge" {
				scanFrom, label = argv[2:], "nightgauge forge"
			} else {
				scanFrom, label = argv[1:], "nightgauge"
			}
		default:
			continue
		}
		resource, verb := forgeResourceVerb(scanFrom)
		if mv := forgeMutationVerb(resource, verb); mv != "" {
			return label + " " + mv
		}
	}
	return ""
}

// forgeResourceVerb returns the first two positional tokens (resource, verb) of
// a forge CLI argv, skipping leading global flags. gh/glab put the resource and
// verb as the first positionals (e.g. `gh pr create`), so this is robust to the
// real usage without a per-flag value table.
func forgeResourceVerb(args []string) (resource, verb string) {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		i++
	}
	if i < len(args) {
		resource = args[i]
		i++
	}
	if i < len(args) && !strings.HasPrefix(args[i], "-") {
		verb = args[i]
	}
	return resource, verb
}

// forgeMutationVerb names the blocked resource+verb combo, or "" when allowed.
// Read-only verbs (view, list, checks, status, diff) and harmless ones (comment,
// edit) are allowed — only lifecycle-advancing mutations are fenced.
func forgeMutationVerb(resource, verb string) string {
	switch resource {
	case "pr", "mr":
		switch verb {
		case "create", "merge", "ready":
			return resource + " " + verb
		}
	case "issue":
		if verb == "close" {
			return "issue close"
		}
	}
	return ""
}

// mutatingGitReason returns a short reason naming a git commit / history-rewrite
// operation if any segment performs one, else "". It reuses gitArgvs so the verb
// is read from the normalized argv (global options skipped). git push/checkout/
// branch/fetch/add are intentionally NOT fenced — issue-pickup pushes the feature
// branch and branch ops are legitimate.
func mutatingGitReason(segments []Segment) string {
	for _, argv := range gitArgvs(segments) {
		if len(argv) < 2 {
			continue
		}
		switch argv[1] {
		case "commit":
			return "git commit"
		case "merge":
			return "git merge"
		case "rebase":
			return "git rebase"
		case "cherry-pick":
			return "git cherry-pick"
		case "revert":
			return "git revert"
		}
	}
	return ""
}
