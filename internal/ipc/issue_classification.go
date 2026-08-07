package ipc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/state"
)

// issueClassification is the pre-run metadata a run record carries alongside
// its cost: the raw labels, the issue type, the size bucket, and the routing
// decision the issue was picked up under.
//
// Size is not a display field. It is the join key the VSCode pre-flight cost
// estimator matches historical runs on, so a record written without it is
// unusable as calibration input and silently collapses the projection back to
// the raw static estimate (#112).
//
// ComplexityScore and PredictedModel are the routing *predictions*. They are
// what the learning corpus calibrates against — a corpus row whose
// complexity_score is 0 and whose predicted model is empty measures nothing,
// which is exactly the degenerate shape every pre-#304 outcome had. Both sit in
// the same issue-{N}.json this loader already opens for labels and size, one
// object away from the fields that were being read.
type issueClassification struct {
	Labels []string
	Type   string
	Size   string
	// ComplexityScore is routing.complexity_score. 0 means UNKNOWN — no
	// recognized size bucket scores 0 (routing.SizeBaseScore: XS=1 … XL=8), so
	// callers can tell "unscored" from any real score and must not spell it as
	// a real one.
	ComplexityScore int
	// PredictedModel is routing.pickup_recommendation.dev_model — the model
	// tier the router chose for the implementation stage. Empty when unknown;
	// deliberately NOT defaulted to "sonnet" the way the scheduler's
	// loadIssueContext does, because a fabricated prediction is worse for
	// calibration than an absent one.
	PredictedModel string
}

// loadIssueClassification reads the run's issue-{N}.json context file and
// projects it onto the fields V2RunInput needs. Best-effort: a missing or
// unparseable context file yields a zero value, which leaves the record's
// labels/size/type absent rather than inventing them.
//
// Stages execute in the run's isolated worktree and write their context files
// there — they are per-worktree local state that never lands in the canonical
// root (the same reason the deterministic pr-create hook resolves its stage
// context via stageWorkspace). So the worktree is searched first, then the
// extension's default `<repoRoot>/.worktrees/issue-{N}` layout for runs the Go
// side never learned a worktree path for, then the repo root for in-place runs.
func loadIssueClassification(repoRoot, worktreeDir string, issueNumber int) issueClassification {
	for _, path := range issueContextCandidates(repoRoot, worktreeDir, issueNumber) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ctx struct {
			Type    string            `json:"type"`
			Labels  []json.RawMessage `json:"labels"`
			Routing struct {
				ComplexityScore      int `json:"complexity_score"`
				PickupRecommendation struct {
					DevModel string `json:"dev_model"`
				} `json:"pickup_recommendation"`
			} `json:"routing"`
		}
		if err := json.Unmarshal(data, &ctx); err != nil {
			continue
		}
		labels := decodeContextLabels(ctx.Labels)
		issueType := ctx.Type
		if issueType == "" {
			issueType = state.ExtractTypeFromLabels(labels)
		}
		return issueClassification{
			Labels:          labels,
			Type:            issueType,
			Size:            state.ExtractSizeFromLabels(labels),
			ComplexityScore: ctx.Routing.ComplexityScore,
			PredictedModel:  ctx.Routing.PickupRecommendation.DevModel,
		}
	}
	return issueClassification{}
}

// issueContextCandidates returns the ordered issue-{N}.json paths to try,
// most-specific workdir first.
func issueContextCandidates(repoRoot, worktreeDir string, issueNumber int) []string {
	roots := make([]string, 0, 3)
	if worktreeDir != "" {
		roots = append(roots, worktreeDir)
	}
	if repoRoot != "" {
		roots = append(roots,
			filepath.Join(repoRoot, ".worktrees", fmt.Sprintf("issue-%d", issueNumber)),
			repoRoot,
		)
	}
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, filepath.Join(root, ".nightgauge", "pipeline",
			fmt.Sprintf("issue-%d.json", issueNumber)))
	}
	return paths
}

// decodeContextLabels normalizes the context file's labels array to plain
// slugs. The schema accepts both `"type:bug"` and `{"name": "type:bug"}`
// because agents write either; entries in any other shape are dropped.
func decodeContextLabels(raw []json.RawMessage) []string {
	labels := make([]string, 0, len(raw))
	for _, entry := range raw {
		var slug string
		if err := json.Unmarshal(entry, &slug); err == nil {
			if slug != "" {
				labels = append(labels, slug)
			}
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(entry, &obj); err == nil && obj.Name != "" {
			labels = append(labels, obj.Name)
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}
