package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// Deterministic issue-pickup (#1904).
//
// issue-pickup resolves an issue, names a branch and writes a JSON file — all
// deterministic work. On the Go scheduler it used to run an LLM for it (2–5
// minutes and a per-dispatch chance of the model writing `branch` as an
// object), for every adapter. This runner is now the canonical path: it reads
// the issue, derives the branch name with git.ComposeBranchName (the same
// derivation `nightgauge git branch-create --issue` uses, #1901), creates and
// pushes the branch through git.Service.EnsureIssueBranch (the same code
// branch-create runs), and writes issue-{N}.json atomically from a typed
// struct, so `branch` is a string by construction and the file is never
// observable empty or partial. Routing comes from routing.Derive — the same
// Decision the scheduler already applies to the run's stage list — rather
// than from a model. The skill remains only as the punt fallback, and an LLM
// run of this stage is flagged by the atomic-LLM-overrun anomaly.

// issueContextSchemaVersion is the schema_version the skill's step 8.2 writes.
const issueContextSchemaVersion = "1.5"

// IssuePickupInput is everything the runner needs, already resolved by the
// scheduler: the fetched issue, the directory the run's stages execute in, and
// the run's routing Decision.
type IssuePickupInput struct {
	Issue    *types.Issue
	Dir      string // the run's worktree (or workspace root for an in-place run)
	Routing  routing.Decision
	DevModel string // the router's feature-dev model, "" when unknown
	// EpicTitle resolves the parent's title when the epic branch must be
	// created; may be nil.
	EpicTitle func() (string, error)
}

// IssuePickupResult reports what the runner produced.
type IssuePickupResult struct {
	Branch      string
	BaseBranch  string
	Action      string
	ContextPath string
	Pushed      bool
	DurationMs  int64
}

// IssuePickupRunner is the deterministic-first hook for issue-pickup.
type IssuePickupRunner interface {
	Run(ctx context.Context, in IssuePickupInput) (IssuePickupResult, error)
}

// deterministicIssuePickup is the production IssuePickupRunner. ensure and
// push are seams for tests; production wires git.Service.
type deterministicIssuePickup struct {
	ensure func(dir, branch string, parent int, epicTitle func() (string, error)) (git.IssueBranchResult, error)
	push   func(dir, branch string) error
	now    func() time.Time
}

// NewDeterministicIssuePickupRunner returns the production runner.
func NewDeterministicIssuePickupRunner() IssuePickupRunner {
	return &deterministicIssuePickup{
		ensure: func(dir, branch string, parent int, epicTitle func() (string, error)) (git.IssueBranchResult, error) {
			svc, err := git.NewService(dir)
			if err != nil {
				return git.IssueBranchResult{}, err
			}
			return svc.EnsureIssueBranch(branch, parent, epicTitle)
		},
		push: func(dir, branch string) error {
			svc, err := git.NewService(dir)
			if err != nil {
				return err
			}
			return svc.PushBranch(branch)
		},
		now: time.Now,
	}
}

func (r *deterministicIssuePickup) Run(_ context.Context, in IssuePickupInput) (IssuePickupResult, error) {
	start := r.now()
	var res IssuePickupResult
	if in.Issue == nil || in.Issue.Number <= 0 {
		return res, fmt.Errorf("no issue to pick up")
	}
	if in.Dir == "" {
		return res, fmt.Errorf("no working directory for issue #%d", in.Issue.Number)
	}
	name, err := git.ComposeBranchName(in.Issue.Labels, in.Issue.Number, in.Issue.Title)
	if err != nil {
		return res, fmt.Errorf("derive branch name: %w", err)
	}
	br, err := r.ensure(in.Dir, name, in.Issue.ParentIssueNumber, in.EpicTitle)
	if err != nil {
		return res, fmt.Errorf("create branch %s: %w", name, err)
	}
	res.Branch, res.BaseBranch, res.Action = br.Branch, br.BaseBranch, br.Action
	if res.Branch == "" {
		res.Branch = name
	}
	// Publishing the branch is best-effort: pr-create pushes again before it
	// opens the PR, so a push failure here (e.g. a transient credential fault,
	// #878) must not cost the run its pickup.
	if r.push != nil {
		if perr := r.push(in.Dir, res.Branch); perr != nil {
			log.Printf("#%d: issue-pickup could not push %s (non-fatal; pr-create pushes again): %v",
				in.Issue.Number, res.Branch, perr)
		} else {
			res.Pushed = true
		}
	}

	path := stagecontext.ContextPath(in.Dir, in.Issue.Number, "issue")
	doc := buildIssueContext(in, res.Branch, res.BaseBranch, r.now())
	if err := writeIssueContextMerged(path, doc); err != nil {
		return res, err
	}
	res.ContextPath = path
	res.DurationMs = r.now().Sub(start).Milliseconds()
	return res, nil
}

// issueContextDoc is the typed shape the deterministic writer emits. Typed
// fields are what make the gate's shape unviolable: Branch is a string.
type issueContextDoc struct {
	SchemaVersion string              `json:"schema_version"`
	IssueNumber   int                 `json:"issue_number"`
	Title         string              `json:"title"`
	Body          string              `json:"body"`
	Branch        string              `json:"branch"`
	BaseBranch    string              `json:"base_branch"`
	Type          string              `json:"type"`
	Requirements  issueRequirements   `json:"requirements"`
	Labels        []string            `json:"labels"`
	ParentIssue   *int                `json:"parent_issue"`
	Routing       issueContextRouting `json:"routing"`
	CreatedAt     string              `json:"created_at"`
	CreatedBy     string              `json:"created_by"`
}

type issueRequirements struct {
	Summary            string   `json:"summary"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	UserStory          *string  `json:"user_story"`
	TechnicalNotes     []string `json:"technical_notes"`
}

type issueContextRouting struct {
	ChangeType           string               `json:"change_type"`
	TaskType             string               `json:"task_type"`
	ComplexityScore      int                  `json:"complexity_score"`
	SuggestedRoute       string               `json:"suggested_route"`
	SkipStages           []string             `json:"skip_stages"`
	FoundationTask       bool                 `json:"foundation_task"`
	DocumentationScope   string               `json:"documentation_scope,omitempty"`
	Rationale            string               `json:"rationale"`
	EstimatedTimeMinutes int                  `json:"estimated_time_minutes"`
	RiskHigh             bool                 `json:"risk_high"`
	RiskReasons          []string             `json:"risk_reasons"`
	MatchedChangeRule    string               `json:"matched_change_rule,omitempty"`
	PickupRecommendation pickupRecommendation `json:"pickup_recommendation"`
}

type pickupRecommendation struct {
	Complexity        int      `json:"complexity"`
	RecommendedStages []string `json:"recommended_stages"`
	SkippedStages     []string `json:"skipped_stages"`
	SkipRationale     string   `json:"skip_rationale"`
	DevModel          string   `json:"dev_model,omitempty"`
	ValidateModel     *string  `json:"validate_model"`
}

var acceptanceLine = regexp.MustCompile(`^\s*-\s*\[`)

func buildIssueContext(in IssuePickupInput, branch, base string, now time.Time) issueContextDoc {
	iss := in.Issue
	labels := append([]string{}, iss.Labels...)

	// Same extraction as the skill's step 8.2: the body's first five lines as
	// the summary, its checkbox lines as acceptance criteria.
	lines := strings.Split(iss.Body, "\n")
	head := lines
	if len(head) > 5 {
		head = head[:5]
	}
	ac := []string{}
	for _, l := range lines {
		if acceptanceLine.MatchString(l) {
			ac = append(ac, l)
		}
	}

	d := in.Routing
	skips := append([]string{}, d.SkipStages...)
	recommended := []string{}
	skipSet := map[string]bool{}
	for _, s := range skips {
		skipSet[s] = true
	}
	for _, st := range []state.PipelineStage{
		state.StageIssuePickup, state.StageFeaturePlanning, state.StageFeatureDev,
		state.StageFeatureValidate, state.StagePRCreate, state.StagePRMerge,
	} {
		if !skipSet[string(st)] {
			recommended = append(recommended, string(st))
		}
	}
	riskReasons := append([]string{}, d.RiskReasons...)

	var parent *int
	if iss.ParentIssueNumber > 0 {
		p := iss.ParentIssueNumber
		parent = &p
	}
	if base == "" {
		base = "main"
	}

	return issueContextDoc{
		SchemaVersion: issueContextSchemaVersion,
		IssueNumber:   iss.Number,
		Title:         iss.Title,
		Body:          iss.Body,
		Branch:        branch,
		BaseBranch:    base,
		Type:          issueTypeFromBranch(branch),
		Requirements: issueRequirements{
			Summary:            strings.TrimSpace(strings.Join(head, " ")),
			AcceptanceCriteria: ac,
		},
		Labels:      labels,
		ParentIssue: parent,
		Routing: issueContextRouting{
			ChangeType:           d.ChangeType,
			TaskType:             d.TaskType,
			ComplexityScore:      d.ComplexityScore,
			SuggestedRoute:       d.SuggestedRoute,
			SkipStages:           skips,
			FoundationTask:       d.FoundationTask,
			DocumentationScope:   d.DocumentationScope,
			Rationale:            d.Rationale,
			EstimatedTimeMinutes: estimatedMinutes(d.ComplexityScore),
			RiskHigh:             d.RiskHigh,
			RiskReasons:          riskReasons,
			MatchedChangeRule:    d.MatchedChangeRule,
			PickupRecommendation: pickupRecommendation{
				Complexity:        d.ComplexityScore,
				RecommendedStages: recommended,
				SkippedStages:     skips,
				SkipRationale:     d.Rationale,
				DevModel:          in.DevModel,
			},
		},
		CreatedAt: now.UTC().Format(time.RFC3339),
		CreatedBy: "deterministic-issue-pickup",
	}
}

// issueTypeFromBranch mirrors the skill's step 8.2: the type follows the
// branch prefix, which ComposeBranchName already derived from the labels.
func issueTypeFromBranch(branch string) string {
	prefix, _, _ := strings.Cut(branch, "/")
	switch prefix {
	case "fix":
		return "bug"
	case "docs", "chore", "refactor":
		return prefix
	default:
		return "feature"
	}
}

func estimatedMinutes(score int) int {
	switch {
	case score <= 1:
		return 15
	case score <= 2:
		return 20
	case score <= 3:
		return 30
	case score <= 5:
		return 60
	default:
		return 120
	}
}

// writeIssueContextMerged writes doc to path atomically (temp file in the same
// directory + rename), merging over any existing file so keys this runner does
// not author — knowledge_path from a prior attempt, dependency_analysis — are
// preserved. The runner's own keys always win: they are the deterministic
// answer. Readers never observe an empty or partial file.
func writeIssueContextMerged(path string, doc issueContextDoc) error {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal issue context: %w", err)
	}
	var fresh map[string]interface{}
	if err := json.Unmarshal(encoded, &fresh); err != nil {
		return fmt.Errorf("re-read issue context: %w", err)
	}
	merged := map[string]interface{}{
		"dependency_analysis": nil,
		"knowledge_path":      nil,
	}
	if data, rerr := os.ReadFile(path); rerr == nil && len(data) > 0 {
		var existing map[string]interface{}
		if json.Unmarshal(data, &existing) == nil {
			for k, v := range existing {
				merged[k] = v
			}
		}
	}
	for k, v := range fresh {
		merged[k] = v
	}
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal issue context: %w", err)
	}
	return atomicWriteFile(path, append(out, '\n'))
}

// atomicWriteFile writes data to a uniquely named temp file beside path and
// renames it into place, so concurrent writers never share a temp file and a
// reader sees either the old file or the new one.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// WithIssuePickupRunner overrides the deterministic-first runner for the
// issue-pickup stage (#1904). Tests inject a fake; nil disables the hook.
func (s *Scheduler) WithIssuePickupRunner(r IssuePickupRunner) {
	s.issuePickupRunner = r
}

// tryDeterministicIssuePickup runs the deterministic-first hook for
// issue-pickup. On success it records execution_path="deterministic" and
// returns true so the caller skips the skill; otherwise it records "llm" and
// the punt reason, and the skill runs as the fallback.
func (s *Scheduler) tryDeterministicIssuePickup(
	ctx context.Context,
	stage state.PipelineStage,
	runtime *state.RuntimeState,
	item types.BoardItem,
	workspaceRoot string,
) bool {
	if stage != state.StageIssuePickup || s.issuePickupRunner == nil {
		return false
	}
	punt := func(reason string) bool {
		runtime.RecordExecutionPath(stage, "llm")
		runtime.RecordStagePuntReason(stage, reason)
		log.Printf("#%d: issue-pickup deterministic path punted (%s) — falling through to LLM", item.Number, reason)
		s.emitStagePunt(ctx, runtime, stage, item.Number, reason)
		return false
	}

	dir, reason := s.runWorktree(runtime, item)
	if reason != "" {
		return punt(reason)
	}

	owner, repo := s.linkRepoFor(item.Repo)
	issues := s.issueServiceFor(ctx, owner, repo)
	if issues == nil {
		return punt("no-issue-service")
	}
	iss, err := issues.GetIssueWithRelations(ctx, owner, repo, item.Number, gh.NoRelations)
	if err != nil || iss == nil {
		return punt(fmt.Sprintf("issue-fetch: %v", err))
	}
	if iss.Number == 0 {
		iss.Number = item.Number
	}

	decision := deriveRoutingDecision(workspaceRoot, item)
	devModel := routing.NewRouter(nil, workspaceRoot).
		Route(ctx, string(state.StageFeatureDev), complexity.Score{Value: decision.ComplexityScore}).Model

	parent := iss.ParentIssueNumber
	in := IssuePickupInput{
		Issue:    iss,
		Dir:      dir,
		Routing:  decision,
		DevModel: devModel,
		EpicTitle: func() (string, error) {
			epic, eerr := issues.GetIssueWithRelations(ctx, owner, repo, parent, gh.NoRelations)
			if eerr != nil {
				return "", eerr
			}
			return epic.Title, nil
		},
	}
	res, err := s.issuePickupRunner.Run(ctx, in)
	if err != nil {
		return punt(fmt.Sprintf("%s: %v", "unexpected", err))
	}
	runtime.RecordExecutionPath(stage, "deterministic")
	runtime.SetBranch(res.Branch)
	log.Printf("#%d: issue-pickup deterministic path: branch %s (%s, base %s, pushed=%t, %dms)",
		item.Number, res.Branch, res.Action, res.BaseBranch, res.Pushed, res.DurationMs)
	return true
}

// runWorktree resolves the run's worktree, provisioning it when this is the
// first stage, or returns a reason there is none. The deterministic
// issue-pickup runner creates and checks out the branch in it, and the stage
// prompt's context paths are rooted in it.
//
// The hook runs BEFORE the first stage dispatch, and the run's worktree is
// provisioned inside that dispatch (execution.Manager.RunStage), so at pickup
// runtime.WorktreeDir is still empty and stageWorkspace falls back to the
// workspace root — the operator's primary checkout. Checking the branch out
// there switched the primary checkout off main mid-run and left the worktree
// the later stages use on a detached HEAD (#2170). So the worktree is
// provisioned here, through the same EnsureWorktree RunStage reuses, and
// stamped on the runtime.
//
// The same gap rooted the first stages' prompt context paths
// (.nightgauge/pipeline/issue-{N}.json) in the primary checkout, so the
// scheduler also calls this before it builds a prompt.
//
// Without a Go-side adapter (IPC mode) the extension owns the worktree and
// the Go side cannot name it; the hook punts to the skill rather than mutate
// the primary checkout.
func (s *Scheduler) runWorktree(runtime *state.RuntimeState, item types.BoardItem) (string, string) {
	if runtime != nil && runtime.WorktreeDir != "" {
		return runtime.WorktreeDir, ""
	}
	if s.execMgr == nil || !s.execMgr.HasAdapter() {
		return "", "no-run-worktree"
	}
	dir, err := s.execMgr.EnsureWorktree(item.Repo, item.Number)
	if dir != "" && runtime != nil {
		runtime.SetWorktree(dir)
	}
	if err != nil {
		return "", fmt.Sprintf("worktree-setup: %v", err)
	}
	if dir == "" {
		return "", "no-run-worktree"
	}
	return dir, ""
}
