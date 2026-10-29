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
	// The epic branch is the epic's own repository's: a parent in another
	// repository bases this branch on the default branch, never on
	// epic/<N>-* of this repository's own #N (#2377).
	parent := git.EpicBranchParent(in.Issue.Repo, in.Issue.ParentIssueNumber, in.Issue.ParentIssueRepo)
	br, err := r.ensure(in.Dir, name, parent, in.EpicTitle)
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

	path, err := stagecontext.ContextPath(in.Dir, in.Issue.Number, "issue")
	if err != nil {
		return res, err
	}
	doc := buildIssueContext(in, res.Branch, res.BaseBranch, parent, r.now())
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

// parent is the parent epic the branch was based on: EpicBranchParent's
// answer, 0 for a parent in another repository. parent_issue is read as a
// number in this repository (feature-planning finds the epic's batch file in
// this checkout by it), so a parent elsewhere is recorded as none, as
// `git branch-create` reports it on the skill path (#2377).
func buildIssueContext(in IssuePickupInput, branch, base string, parent int, now time.Time) issueContextDoc {
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

	var parentIssue *int
	if parent > 0 {
		parentIssue = &parent
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
		ParentIssue: parentIssue,
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

	in, reason := s.pickupInput(ctx, item, workspaceRoot, dir)
	if reason != "" {
		return punt(reason)
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

// pickupInput fetches the issue and assembles the runner's input for dir, or
// returns the reason it cannot. It is the one derivation behind both the
// deterministic runner and the recovery of a context the skill fallback left
// broken (#1919), so the two write the same document for the same issue.
func (s *Scheduler) pickupInput(ctx context.Context, item types.BoardItem, workspaceRoot, dir string) (IssuePickupInput, string) {
	owner, repo := s.linkRepoFor(item.Repo)
	issues := s.issueServiceFor(ctx, owner, repo)
	if issues == nil {
		return IssuePickupInput{}, "no-issue-service"
	}
	iss, err := issues.GetIssueWithRelations(ctx, owner, repo, item.Number, gh.NoRelations)
	if err != nil || iss == nil {
		return IssuePickupInput{}, fmt.Sprintf("issue-fetch: %v", err)
	}
	if iss.Number == 0 {
		iss.Number = item.Number
	}
	if iss.Repo == "" {
		iss.Repo = owner + "/" + repo
	}

	decision := deriveRoutingDecision(workspaceRoot, item)
	devModel := routing.NewRouter(nil, workspaceRoot).
		Route(ctx, string(state.StageFeatureDev), complexity.Score{Value: decision.ComplexityScore}).Model

	// Run creates an epic branch only for a parent in this repository, so the
	// title is read here, by this repository's number (#2377).
	parent := git.EpicBranchParent(iss.Repo, iss.ParentIssueNumber, iss.ParentIssueRepo)
	return IssuePickupInput{
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
	}, ""
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
// Provisioning needs no adapter: IPC mode gets the same worktree, and the
// scheduler provisions it before the first stage of every run (#2258), so a
// fast-tracked route never runs in the primary checkout.
func (s *Scheduler) runWorktree(runtime *state.RuntimeState, item types.BoardItem) (string, string) {
	if runtime != nil && runtime.WorktreeDir != "" {
		return runtime.WorktreeDir, ""
	}
	if s.execMgr == nil {
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

// Faults recoverIssueContextAtPickup repairs: each is a reason the issue-pickup
// gate would fail a context whose branch exists in git the whole time.
const (
	issueContextMissing     = "missing"
	issueContextInvalidJSON = "invalid-json"
	issueContextSchema      = "schema-mismatch"
	issueContextNoBranch    = "no-branch"
)

// issueContextFault classifies the context file at path the way
// gates.IssuePickupGate.Verify reads it, or returns "" when the gate would
// pass it (or fail it for a reason recovery must not paper over, such as an
// unreadable file).
func issueContextFault(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return issueContextMissing
		}
		return ""
	}
	if !json.Valid(data) {
		return issueContextInvalidJSON
	}
	var raw map[string]json.RawMessage
	var doc struct {
		Branch      string `json:"branch"`
		IssueNumber int    `json:"issue_number"`
	}
	if json.Unmarshal(data, &raw) != nil || raw == nil || json.Unmarshal(data, &doc) != nil {
		return issueContextSchema
	}
	if doc.Branch == "" {
		return issueContextNoBranch
	}
	return ""
}

// recoverIssueContextAtPickup repairs the issue context a skill-path
// issue-pickup left broken, before the post-condition gate reads it, and
// returns the fault it repaired ("" when it changed nothing) (#1919).
//
// #1930 recovered only an empty `branch`. The rest of #1919's failures were
// the model not producing the file at all — it ended its turn before the
// write — or producing one that was not valid JSON or did not match the
// schema. Escalating to a larger model cannot supply a structure the model
// was asked to serialise and did not. So the write leaves the model's hands:
//
//   - missing, unparsable or schema-invalid → the whole document is rebuilt
//     with the deterministic runner's own builder (pickupInput +
//     buildIssueContext), merged over whatever parsed, and written
//     atomically.
//   - valid but branch-less → only `branch` is stamped, so everything else
//     the skill wrote survives.
//   - valid with a branch → nothing; a value the skill wrote is never
//     overwritten, however wrong it looks (an empty slug is #1915's defect).
//
// It never invents a branch. Every repair requires HEAD in the run's worktree
// to be on a branch whose issue number is THIS issue — the actionable
// post-state the gate exists to check. A worktree still on the base branch, or
// detached, repairs nothing and the gate fails the stage as it should.
//
// Non-fatal throughout: every failure is logged and the gate remains the
// authority on whether the stage produced its post-state.
func (s *Scheduler) recoverIssueContextAtPickup(ctx context.Context, workspaceRoot, worktreeDir string, item types.BoardItem) string {
	repoDir := worktreeDir
	if repoDir == "" {
		repoDir = workspaceRoot
	}
	// The gate reads exactly this path (gates.contextFilePath on the same
	// stage workspace), so that is the file recovery inspects and writes.
	path, err := stagecontext.ContextPath(repoDir, item.Number, "issue")
	if err != nil {
		log.Printf("#%d: cannot resolve the issue context path in %s (non-fatal): %v", item.Number, repoDir, err)
		return ""
	}
	fault := issueContextFault(path)
	if fault == "" {
		return ""
	}

	gitSvc, err := git.NewService(repoDir)
	if err != nil {
		log.Printf("#%d: issue context %s and the worktree at %s could not be opened to recover it (non-fatal): %v",
			item.Number, fault, repoDir, err)
		return ""
	}
	branch, err := gitSvc.CurrentBranch()
	if err != nil {
		log.Printf("#%d: issue context %s and HEAD in %s names no branch — recovering nothing: %v",
			item.Number, fault, repoDir, err)
		return ""
	}
	if n, ok := git.ParseIssueNumberFromBranch(branch); !ok || n != item.Number {
		log.Printf("#%d: issue context %s and HEAD is on %q, which is not this issue's branch — recovering nothing",
			item.Number, fault, branch)
		return ""
	}

	if fault == issueContextNoBranch {
		if err := stampIssueContextBranch(path, branch); err != nil {
			log.Printf("#%d: could not write recovered branch %q into %s (non-fatal): %v", item.Number, branch, path, err)
			return ""
		}
		log.Printf("#%d: issue context carried no branch; recovered %q from the worktree's HEAD (#1919)", item.Number, branch)
		return fault
	}

	in, reason := s.pickupInput(ctx, item, workspaceRoot, repoDir)
	if reason != "" {
		log.Printf("#%d: issue context %s and the issue could not be read to rebuild it (%s) — recovering nothing",
			item.Number, fault, reason)
		return ""
	}
	parent := git.EpicBranchParent(in.Issue.Repo, in.Issue.ParentIssueNumber, in.Issue.ParentIssueRepo)
	doc := buildIssueContext(in, branch, recoveredBaseBranch(gitSvc, parent), parent, time.Now())
	doc.CreatedBy = "issue-pickup-recovery"
	if err := writeIssueContextMerged(path, doc); err != nil {
		log.Printf("#%d: could not write the recovered issue context to %s (non-fatal): %v", item.Number, path, err)
		return ""
	}
	log.Printf("#%d: issue context %s; rebuilt it deterministically on branch %q (#1919)", item.Number, fault, branch)
	return fault
}

// recoverPickupAfterSkill runs recoverIssueContextAtPickup for an issue-pickup
// the skill fallback ran and reported successful, and appends the repair to
// the stage's evidence so the run record says the orchestrator wrote the file
// (#1919). The deterministic runner wrote its file from a typed struct, and a
// failed stage never reaches the gate, so neither is touched. It returns the
// fault repaired, "" when nothing was.
func (s *Scheduler) recoverPickupAfterSkill(
	ctx context.Context,
	runtime *state.RuntimeState,
	stageErr error,
	exitCode int,
	workspaceRoot string,
	item types.BoardItem,
) string {
	stage := state.StageIssuePickup
	if runtime == nil || stageErr != nil || exitCode != 0 || runtime.StageExecutionPath(stage) == "deterministic" {
		return ""
	}
	fault := s.recoverIssueContextAtPickup(ctx, workspaceRoot, stageWorkspace(runtime, workspaceRoot), item)
	if fault != "" {
		runtime.AppendStageOutputTail(stage, fmt.Sprintf(
			"[orchestrator] issue context recovered (%s) from the worktree's HEAD and the issue (#1919)", fault))
	}
	return fault
}

// recoveredBaseBranch is the base EnsureIssueBranch would have recorded for a
// branch that already exists: the parent's epic branch when there is one, the
// repository default otherwise. "" lets buildIssueContext default to main.
func recoveredBaseBranch(gitSvc *git.Service, parent int) string {
	if parent > 0 {
		if epic, err := gitSvc.FindEpicBranch(parent); err == nil && epic != "" {
			return epic
		}
	}
	base, _ := gitSvc.DefaultBranch()
	return base
}

// stampIssueContextBranch sets `branch` in an otherwise-valid context file,
// preserving every other key, and writes it atomically.
func stampIssueContextBranch(path, branch string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	raw["branch"] = branch
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, append(out, '\n'))
}
