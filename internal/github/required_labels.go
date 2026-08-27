package github

import (
	"context"
	"fmt"
)

// RequiredLabel is a label the Go layer READS at runtime. The distinction
// matters: the `nightgauge-repo-init` skill provisions a much larger taxonomy
// (component:*, size:*, priority:*), but those are conventions for humans and
// for board field mapping. The three below are load-bearing — a control-flow
// decision is made on their presence, so a repo missing one does not merely
// look untidy, it misbehaves.
type RequiredLabel struct {
	Name        string
	Description string
	Color       string
	// BlocksRefinement marks a label whose ABSENCE makes the autonomous
	// refinement loop misbehave rather than merely degrade, so the refinement
	// preflight must refuse the repo instead of proceeding.
	//
	// Only pipeline:refined qualifies, and the distinction is load-bearing: a
	// preflight that refuses on the whole registry disables refinement on a
	// brand-new repo that has simply never had an epic, which trades a runaway
	// loop for a silent stall. The others are provisioned by `label ensure` and
	// reported, but they do not gate the loop — auto-process absent means the
	// board target falls back to Backlog, and type:epic absent means epics are
	// not excluded from candidacy. Both are visible and recoverable; neither
	// rewrites an issue body.
	BlocksRefinement bool
}

// RequiredLabels is the single source of truth for labels the pipeline itself
// depends on. It exists because #993 traced a runaway refinement loop to a
// missing label: MarkRefined hard-errors without `pipeline:refined`
// (issues.go:939), and the candidate query excludes issues that CARRY the
// label (autonomous.go), so a label that does not exist in the repo can never
// be present, the exclusion is inert, and every issue stays a candidate
// forever.
//
// Provisioning previously lived only in the repo-init SKILL — an LLM step with
// no deterministic gate confirming it ran. A registry the code reads is what
// lets `nightgauge label ensure` and the refinement preflight agree on the
// same list without either restating it.
//
// Deliberately NOT here: `priority:p3` and the rest of the priority set. No Go
// code reads a `priority:*` LABEL — priority is read from the project board's
// single-select field (internal/intelligence/routing/derive.go), and
// internal/config/init.go's priorityKeys list only shapes the config template.
// Adding them here would provision labels nothing consumes.
var RequiredLabels = []RequiredLabel{
	{
		Name:             LabelRefined,
		Description:      "Issue has been refined and is ready for development",
		Color:            "0969da",
		BlocksRefinement: true,
	},
	{
		Name:        LabelAutoProcess,
		Description: "Issue is queued for automatic pipeline processing",
		Color:       "8957e5",
	},
	{
		Name:        LabelEpic,
		Description: "Parent issue with sub-issues",
		Color:       "8957e5",
	},
	{
		// DefaultExcludeLabels (internal/config/config.go) and
		// defaultExcludeLabels (internal/orchestrator/autonomous.go) both refuse
		// to dispatch an issue carrying this. It fails in the OPPOSITE direction
		// from pipeline:refined and is the more dangerous of the two: a label
		// that does not exist cannot be applied, so human-only work has no way
		// to mark itself and the pipeline dispatches it.
		Name:        "owner-action",
		Description: "Requires owner action (real account / external state)",
		Color:       "FBCA04",
	},
	{
		// DefaultArchitectureApprovalLabel (internal/config/config.go:1302),
		// read as a gate by cmd/nightgauge/approval_gate.go. The Action Center
		// verb self-creates it (internal/ipc/attention.go), but that is one path
		// of two — a repo where the label is absent has an architecture gate
		// that cannot be satisfied by the mechanism its own docs describe.
		// Description matches the verb's byte for byte so the two creators
		// cannot drift.
		Name:        LabelArchitectureApproved,
		Description: "Human-approved architectural decision — architecture gate passes",
		Color:       "0e8a16",
	},
}

// LabelArchitectureApproved mirrors config.DefaultArchitectureApprovalLabel by
// VALUE, not by import: internal/config reaches internal/github transitively
// (config → intelligence/routing → platform → state → github), so importing it
// here is an import cycle, not a style preference. The pairing is asserted by
// TestRequiredLabels_ArchitectureApprovalMatchesConfig in the config package,
// which can see both — so the copy cannot drift silently.
const LabelArchitectureApproved = "approved:architecture"

// RequiredLabelNames returns just the names, for callers that only need to
// test presence (the refinement preflight) rather than create anything.
func RequiredLabelNames() []string {
	names := make([]string, 0, len(RequiredLabels))
	for _, l := range RequiredLabels {
		names = append(names, l.Name)
	}
	return names
}

// LabelEnsurer is the subset of LabelService that EnsureRequiredLabels needs.
// Narrow on purpose: it makes the function unit-testable without a GitHub
// client, which is the difference between a guard that can go red and one that
// can only be exercised against the live API.
type LabelEnsurer interface {
	List(ctx context.Context) ([]*Label, error)
	Create(ctx context.Context, name, description, color string) (*Label, error)
}

// EnsureRequiredLabels creates any of RequiredLabels missing from the
// repository and returns the names it created, in registry order. A repo that
// already has all three yields an empty slice and issues zero mutations.
//
// It lists ONCE and creates only the gaps rather than calling Create for each
// name and leaning on Create's own idempotency check: that check is itself a
// List (labels.go), so the naive form costs one list per required label on
// every invocation. This runs on a preflight path, and the GitHub request
// budget is a tracked concern (epic #842).
//
// Errors are returned on the first failure with the labels created so far, so
// a caller can report partial progress instead of losing it. Re-running after
// a failure is safe.
func EnsureRequiredLabels(ctx context.Context, svc LabelEnsurer) ([]string, error) {
	if svc == nil {
		return nil, fmt.Errorf("label service is required")
	}

	existing, err := svc.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list existing labels: %w", err)
	}

	present := make(map[string]bool, len(existing))
	for _, l := range existing {
		if l != nil {
			present[l.Name] = true
		}
	}

	created := make([]string, 0, len(RequiredLabels))
	for _, want := range RequiredLabels {
		if present[want.Name] {
			continue
		}
		if _, err := svc.Create(ctx, want.Name, want.Description, want.Color); err != nil {
			return created, fmt.Errorf("create required label %q: %w", want.Name, err)
		}
		created = append(created, want.Name)
	}

	return created, nil
}

// MissingRequiredLabels returns every required label absent from repoLabels, a
// name→nodeID map as returned by IssueService.GetRepoLabels. Separate from
// EnsureRequiredLabels because a caller must be able to REPORT a miss without
// acquiring the write permission that creating implies.
func MissingRequiredLabels(repoLabels map[string]string) []string {
	return missingWhere(repoLabels, func(RequiredLabel) bool { return true })
}

// MissingRefinementBlockers returns only the absent labels whose absence makes
// the refinement loop misbehave. This — not MissingRequiredLabels — is what the
// refinement preflight refuses on.
func MissingRefinementBlockers(repoLabels map[string]string) []string {
	return missingWhere(repoLabels, func(l RequiredLabel) bool { return l.BlocksRefinement })
}

func missingWhere(repoLabels map[string]string, want func(RequiredLabel) bool) []string {
	missing := make([]string, 0, len(RequiredLabels))
	for _, l := range RequiredLabels {
		if !want(l) {
			continue
		}
		if _, ok := repoLabels[l.Name]; !ok {
			missing = append(missing, l.Name)
		}
	}
	return missing
}
