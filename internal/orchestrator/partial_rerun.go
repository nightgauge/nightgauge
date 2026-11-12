package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// A partial-delivery re-run (#3160).
//
// A `Refs` merge leaves its issue open with a
// `<!-- nightgauge:partial-delivery pr=N -->` comment listing what is still
// unmet, and the autonomous reconcile re-admits the open issue. Before this
// file the new run planned the whole issue again, found it on main, wrote
// nothing, and spent a feature-validate lap before pr-create ended it. Three
// things change here:
//
//   - the listed remainder becomes the run's whole scope: it is written to
//     partial-delivery-{N}.json, spliced into planning-{N}.json as
//     `partial_delivery`, and named in the planning and feature-dev prompts;
//   - a partial re-run whose feature-dev leaves the branch level with its base
//     ends not_pipeline_actionable before feature-validate;
//   - partial_readmit.go vets a re-admit against main before it dispatches.

// partialDeliveryContextType is the stage-context prefix of the remainder
// file: partial-delivery-{N}.json in the run's pipeline state directory.
const partialDeliveryContextType = "partial-delivery"

// partialDeliveryReadTimeout bounds the forge reads behind one remainder read.
const partialDeliveryReadTimeout = 30 * time.Second

// partialDeliveryFile is the on-disk shape of partial-delivery-{N}.json and of
// planning-{N}.json's `partial_delivery` field.
type partialDeliveryFile struct {
	PR        int        `json:"pr"`
	Criteria  []string   `json:"criteria"`
	CommentAt *time.Time `json:"comment_at,omitempty"`
}

// readPartialDelivery reads repo#issue's pending partial-delivery remainder
// through the scheduler's trust gate, with the repo's own client. The
// partialDeliveryRead seam replaces it in tests. Any missing dependency is an
// error, which every caller treats as "no scope": the run proceeds as before.
func (s *Scheduler) readPartialDelivery(ctx context.Context, repo string, issue int) (hooks.PartialDeliveryStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, partialDeliveryReadTimeout)
	defer cancel()
	if s.partialDeliveryRead != nil {
		return s.partialDeliveryRead(ctx, repo, issue)
	}
	owner, name := splitRepo(repo)
	if owner == "" || name == "" {
		return hooks.PartialDeliveryStatus{}, fmt.Errorf("repo %q is not owner/name", repo)
	}
	if s.trustGate == nil {
		return hooks.PartialDeliveryStatus{}, fmt.Errorf("no trust gate")
	}
	client, err := s.repoClient(ctx, owner, name)
	if err != nil {
		return hooks.PartialDeliveryStatus{}, err
	}
	reader := hooks.NewPartialDeliveryReaderFromGate(s.trustGate.Fresh(), gh.NewIssueService(client))
	return hooks.PendingPartialDelivery(ctx, reader, owner, name, issue)
}

// repoClient is the client the scheduler reads owner/name with: the resolver's
// per-repo client, else the startup client.
func (s *Scheduler) repoClient(ctx context.Context, owner, name string) (*gh.Client, error) {
	if s.clientResolver != nil {
		if c, err := s.clientResolver(ctx, owner, name); err == nil && c != nil {
			return c, nil
		}
	}
	if s.client == nil {
		return nil, fmt.Errorf("no GitHub client configured")
	}
	return s.client, nil
}

// scopePartialRerun runs at run start. When the issue carries a pending,
// listed remainder, it records the scope on the runtime and writes
// partial-delivery-{N}.json into ws. A read error, an unlisted remainder or
// none at all leaves the run unscoped, exactly as before #3160.
func (s *Scheduler) scopePartialRerun(ctx context.Context, runtime *state.RuntimeState, item types.BoardItem, ws string) {
	status, err := s.readPartialDelivery(ctx, item.Repo, item.Number)
	if err != nil {
		log.Printf("#%d: partial-delivery remainder unreadable (%v) — the run is not scoped to it", item.Number, err)
		return
	}
	if !status.Pending || status.Unlisted || len(status.Criteria) == 0 {
		return
	}
	runtime.SetPartialRerun(status.PR, status.Criteria)
	if err := writePartialDeliveryContext(ws, item.Number, partialDeliveryFile{
		PR: status.PR, Criteria: status.Criteria, CommentAt: status.CommentAt,
	}); err != nil {
		log.Printf("#%d: could not write the partial-delivery context: %v", item.Number, err)
	}
	log.Printf("#%d: partial-delivery re-run after PR #%d — scope is the %d remaining criteria [#3160]",
		item.Number, status.PR, len(status.Criteria))
}

func writePartialDeliveryContext(ws string, issue int, f partialDeliveryFile) error {
	path, err := stagecontext.ContextPath(ws, issue, partialDeliveryContextType)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// splicePartialDelivery writes partial-delivery-{N}.json into planning-{N}.json
// as `partial_delivery`: the twin of spliceACReconcile. Absent file, no-op —
// an unscoped run's planning context is untouched. Best-effort, like that
// splice: a failure is logged and never fails a stage that succeeded.
func splicePartialDelivery(workspaceRoot string, issueNumber int, planningFile string) {
	path, err := stagecontext.ContextPath(workspaceRoot, issueNumber, partialDeliveryContextType)
	if err != nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var remainder map[string]any
	if err := json.Unmarshal(raw, &remainder); err != nil {
		log.Printf("#%d: partial-delivery context is not valid JSON, leaving planning context alone: %v", issueNumber, err)
		return
	}
	planningRaw, err := os.ReadFile(planningFile)
	if err != nil {
		return
	}
	var planning map[string]any
	if err := json.Unmarshal(planningRaw, &planning); err != nil {
		log.Printf("#%d: planning context is not valid JSON, cannot splice partial_delivery: %v", issueNumber, err)
		return
	}
	planning["partial_delivery"] = remainder
	merged, err := json.MarshalIndent(planning, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(planningFile, merged, 0o644); err != nil {
		log.Printf("#%d: could not persist the spliced planning context: %v", issueNumber, err)
	}
}

// partialRerunPromptHeading heads the scope block in the planning and
// feature-dev prompts.
const partialRerunPromptHeading = "## Partial-Delivery Re-run Scope"

// PartialRerunPromptBlocks renders the scope block for each stage that
// carries one, keyed by stage name, for the extension's stage loop (#3095):
// `nightgauge issue partial-delivery --json` hands it over so the text has one
// source. Nil when the remainder is not pending or lists no criteria.
func PartialRerunPromptBlocks(status hooks.PartialDeliveryStatus) map[string]string {
	if !status.Pending || status.Unlisted || len(status.Criteria) == 0 {
		return nil
	}
	scope := &state.PartialRerun{PR: status.PR, Criteria: status.Criteria}
	return map[string]string{
		string(state.StageFeaturePlanning): renderPartialRerunPromptBlock(state.StageFeaturePlanning, scope, false),
		string(state.StageFeatureDev):      renderPartialRerunPromptBlock(state.StageFeatureDev, scope, false),
	}
}

// partialRerunPromptBlock is the prompt trailer for a partial re-run's
// planning and feature-dev stages. "" when scope is nil, so an unscoped
// run's prompt stays byte-identical.
func partialRerunPromptBlock(stage state.PipelineStage, scope *state.PartialRerun) string {
	return renderPartialRerunPromptBlock(stage, scope, true)
}

// renderPartialRerunPromptBlock renders the block. goPath adds the two
// sentences only the Go scheduler keeps: it splices the record into the
// planning context and stops a no-diff feature-dev before feature-validate.
// The extension's stage loop does neither, so its block omits them (#3095).
func renderPartialRerunPromptBlock(stage state.PipelineStage, scope *state.PartialRerun, goPath bool) string {
	if scope == nil || len(scope.Criteria) == 0 {
		return ""
	}
	if stage != state.StageFeaturePlanning && stage != state.StageFeatureDev {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + partialRerunPromptHeading + "\n\n")
	b.WriteString(fmt.Sprintf("PR #%d already delivered the rest of this issue, and that work is already on `main`. "+
		"This run's whole scope is the acceptance criteria that PR's partial-delivery comment "+
		"listed as still unmet:\n\n", scope.PR))
	for _, c := range scope.Criteria {
		b.WriteString("- " + pmstages.CommentSafeACText(c) + "\n")
	}
	b.WriteString("\n")
	switch stage {
	case state.StageFeaturePlanning:
		b.WriteString("Plan only these criteria: use them as `focus_acs` and do not re-plan the rest of the issue.")
		if goPath {
			b.WriteString(" The planning context records them under `partial_delivery`.")
		}
		b.WriteString("\n")
	case state.StageFeatureDev:
		b.WriteString("Implement only these criteria and change nothing else. If they are already met on this " +
			"branch, write no code")
		if goPath {
			b.WriteString(": the run then ends after this stage, without feature-validate")
		}
		b.WriteString(".\n")
	}
	return b.String()
}

// stopNoDiffPartialRerun ends a partial re-run whose feature-dev left the
// branch level with its base (#3160). It posts the remainder on the issue,
// never closes it (closing over a recorded remainder is a human decision,
// #3089), and returns the not_pipeline_actionable detail. stopped is false
// for an unscoped run, a branch with a diff, or a diff that could not be read
// — each of which continues to feature-validate as before.
func (s *Scheduler) stopNoDiffPartialRerun(ctx context.Context, runtime *state.RuntimeState, item types.BoardItem, ws string) (detail string, stopped bool) {
	scope := runtime.PartialRerunScope()
	if scope == nil {
		return "", false
	}
	branch, base, level, err := pmstages.BranchNoDiff(ctx, ws, item.Number)
	if err != nil {
		log.Printf("#%d: partial re-run: could not compare the branch with its base (%v) — continuing to feature-validate", item.Number, err)
		return "", false
	}
	if !level {
		return "", false
	}
	if branch == "" {
		branch = runtime.FeatureBranch()
	}
	commenter := s.noDiffCommenter
	if commenter == nil {
		owner, name := splitRepo(item.Repo)
		if c, err := s.repoClient(ctx, owner, name); err == nil {
			commenter = gh.NewIssueService(c)
		}
	}
	if commenter != nil && item.NodeID != "" {
		body := pmstages.PartialRerunNoDiffComment(item.Number, scope.PR, branch, base, scope.Criteria)
		if err := commenter.AddComment(ctx, item.NodeID, body); err != nil {
			log.Printf("#%d: partial re-run: could not comment the remaining criteria: %v", item.Number, err)
		}
	}
	detail = pmstages.PartialRerunNoDiffDetail(item.Number, scope.PR, branch, base, len(scope.Criteria))
	log.Printf("#%d: %s", item.Number, detail)
	return detail, true
}

// stagesInclude reports whether stage is among stages.
func stagesInclude(stages []state.PipelineStage, stage state.PipelineStage) bool {
	for _, st := range stages {
		if st == stage {
			return true
		}
	}
	return false
}
