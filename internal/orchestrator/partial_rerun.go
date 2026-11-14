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
	"github.com/nightgauge/nightgauge/internal/preflight"
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
	PR       int      `json:"pr"`
	Criteria []string `json:"criteria"`
	// Dropped lists criteria the comment named that the current issue body
	// no longer contains (#3235).
	Dropped   []string   `json:"dropped,omitempty"`
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
	scope, dropped := s.reconcilePartialScope(ctx, item, ws, status.Criteria, status.PR)
	if len(scope) == 0 {
		// Every listed criterion left the body and none is new: the remainder
		// is resolved. Scope the run to nothing, never to the whole issue,
		// which would re-open what the partial PR delivered [#3235].
		log.Printf("#%d: partial-delivery remainder after PR #%d reconciles to no criteria — the run is scoped to no work [#3235]",
			item.Number, status.PR)
	}
	runtime.SetPartialRerun(status.PR, scope)
	if err := writePartialDeliveryContext(ws, item.Number, partialDeliveryFile{
		PR: status.PR, Criteria: scope, Dropped: dropped, CommentAt: status.CommentAt,
	}); err != nil {
		log.Printf("#%d: could not write the partial-delivery context: %v", item.Number, err)
	}
	log.Printf("#%d: partial-delivery re-run after PR #%d — scope is the %d remaining criteria [#3160]",
		item.Number, status.PR, len(scope))
}

// reconcilePartialScope builds the re-run brief from the comment's remainder
// and a criteria report regenerated from the current, vouched issue body
// (#3235). The comment is a snapshot: criteria added to the body after the
// partial PR would otherwise never reach the re-run, and criteria removed from
// it would linger. The brief is the listed criteria still in the body plus
// only criteria new since the partial PR: in the body, but neither listed nor
// named in that PR's met or unmet acceptance-criteria sections. It never
// re-adds every criterion the report fails to judge satisfied: prose criteria
// are always undetectable, so that would re-open what the partial PR already
// delivered. When the body cannot be read or has no criteria, the comment
// list stands unchanged.
func (s *Scheduler) reconcilePartialScope(ctx context.Context, item types.BoardItem, ws string, listed []string, pr int) (scope, dropped []string) {
	reads := PartialScopeReads{
		Body: func(ctx context.Context) (string, error) { return s.readACReconcileBody(ctx, item) },
		PR: func(ctx context.Context, pr int) (time.Time, string, error) {
			got, err := s.readPartialPR(ctx, item.Repo, pr)
			return got.CreatedAt, got.Body, err
		},
		BodyEditedAt: func(ctx context.Context) (*time.Time, error) {
			if s.issueBodyEditedAt != nil {
				return s.issueBodyEditedAt(ctx, item)
			}
			return forgeIssueBodyEditedAt(s, ctx, item)
		},
	}
	return ReconcilePartialScope(ctx, reads, ws, item.Number, listed, pr)
}

// PartialScopeReads are the forge reads behind ReconcilePartialScope. Body
// must return the current issue body only once it is vouched for; an error
// leaves the comment's list standing.
type PartialScopeReads struct {
	Body         func(ctx context.Context) (string, error)
	PR           func(ctx context.Context, pr int) (created time.Time, body string, err error)
	BodyEditedAt func(ctx context.Context) (*time.Time, error)
}

// ReconcilePartialScope is the one reconcile both the scheduler and
// `nightgauge issue partial-delivery --json` run (#3235, #3242): see
// Scheduler.reconcilePartialScope for the rules.
func ReconcilePartialScope(ctx context.Context, reads PartialScopeReads, ws string, issue int, listed []string, pr int) (scope, dropped []string) {
	body, err := reads.Body(ctx)
	if err != nil {
		log.Printf("#%d: partial-delivery scope not reconciled with the issue body (%v) — using the comment's list [#3235]",
			issue, err)
		return listed, nil
	}
	rec := preflight.ReconcileACs(ws, issue, body)
	if rec == nil || len(rec.AcceptanceCriteria) == 0 {
		log.Printf("#%d: issue body has no acceptance criteria — using the partial-delivery comment's list [#3235]", issue)
		return listed, nil
	}
	var prCriteria []string
	addNew := false
	created, prBody, err := reads.PR(ctx, pr)
	if err != nil {
		// Without the PR's met and unmet sections nothing can be told apart
		// from what it delivered (prose criteria are always undetectable), so
		// adding would re-open delivered work: add nothing new.
		log.Printf("#%d: partial PR #%d unreadable (%v) — no criteria are added beyond the comment's list [#3235]",
			issue, pr, err)
	} else {
		prCriteria = partialPRCriteria(prBody)
		addNew = bodyEditedSince(ctx, reads.BodyEditedAt, issue, created, pr)
	}
	scope, added, dropped := reconcilePartialRemainder(listed, prCriteria, rec, addNew)
	for _, a := range added {
		log.Printf("#%d: criterion new since partial PR #%d — added to the brief: %q [#3235]", issue, pr, a)
	}
	for _, d := range dropped {
		log.Printf("#%d: partial-delivery criterion no longer in the issue body — dropped: %q [#3235]", issue, d)
	}
	return scope, dropped
}

// partialPR is what the reconcile reads from the partial PR: when it was
// opened, the earliest moment its unmet list could have been computed, and
// its body, whose acceptance-criteria sections name what it covered.
type partialPR struct {
	CreatedAt time.Time
	Body      string
}

func (s *Scheduler) readPartialPR(ctx context.Context, repo string, pr int) (partialPR, error) {
	if s.partialPRRead != nil {
		return s.partialPRRead(ctx, repo, pr)
	}
	return forgePartialPR(s, ctx, repo, pr)
}

// bodyEditedSince reports whether the issue body changed after partial PR pr
// was opened. An unedited body holds no criteria new since the PR. An
// unreadable edit time answers true: the body may hold criteria the PR never
// saw.
func bodyEditedSince(ctx context.Context, editedAt func(context.Context) (*time.Time, error), issue int, opened time.Time, pr int) bool {
	edited, err := editedAt(ctx)
	if err != nil {
		log.Printf("#%d: issue body edit time unreadable (%v) — treating the body as edited since PR #%d [#3235]",
			issue, err, pr)
		return true
	}
	return edited != nil && edited.After(opened)
}

// forgePartialPR is the production read behind readPartialPR.
var forgePartialPR = func(s *Scheduler, ctx context.Context, repo string, pr int) (partialPR, error) {
	owner, name := splitRepo(repo)
	if owner == "" || name == "" || pr <= 0 {
		return partialPR{}, fmt.Errorf("no PR %s#%d", repo, pr)
	}
	client, err := s.repoClient(ctx, owner, name)
	if err != nil {
		return partialPR{}, err
	}
	created, body, err := client.PRCreatedAtAndBody(ctx, owner, name, pr)
	if err != nil {
		return partialPR{}, err
	}
	return partialPR{CreatedAt: created, Body: body}, nil
}

// forgeIssueBodyEditedAt is the production read behind bodyEditedSince.
var forgeIssueBodyEditedAt = func(s *Scheduler, ctx context.Context, item types.BoardItem) (*time.Time, error) {
	owner, name := splitRepo(item.Repo)
	if owner == "" || name == "" {
		return nil, fmt.Errorf("repo %q is not owner/name", item.Repo)
	}
	client, err := s.repoClient(ctx, owner, name)
	if err != nil {
		return nil, err
	}
	return client.IssueLastEditedAt(ctx, owner, name, item.Number)
}

// partialPRMetHeading heads the met criteria in a pr-create body
// (stages.RenderBody); each bullet reads `- <label>: <text> (<source>: …)`.
const partialPRMetHeading = "## Acceptance criteria"

// partialPRCriteria returns the criteria a partial PR's body names under its
// met and unmet acceptance-criteria sections. A met bullet keeps its trailing
// evidence, which matching by containment tolerates; its status label is cut.
func partialPRCriteria(body string) []string {
	out := hooks.UnmetCriteria(body)
	for _, c := range partialPRSectionBullets(body, partialPRMetHeading) {
		out = append(out, strings.TrimPrefix(c, partialPRMetLabel))
	}
	return out
}

// partialPRMetLabel prefixes a met bullet's text in a pr-create body.
const partialPRMetLabel = "✅ met: "

// partialPRSectionBullets returns the top-level bullets under heading, the
// way hooks.UnmetCriteria reads the unmet section.
func partialPRSectionBullets(body, heading string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			in = t == heading
			continue
		}
		if in && strings.HasPrefix(t, "- ") {
			out = append(out, strings.TrimSpace(t[2:]))
		}
	}
	return out
}

// partialRemainderKey normalises a criterion for matching. The comment and
// the PR body were rendered through CommentSafeACText or UntrustedACText, so
// the body text takes the same transform before whitespace is normalised
// (preflight.NormalizeACText) and case folded, and trailing punctuation is
// trimmed.
func partialRemainderKey(t string) string {
	k := strings.ToLower(preflight.NormalizeACText(pmstages.CommentSafeACText(t)))
	return strings.TrimRight(k, ".;:, ")
}

// partialRemainderMinOverlap is the shortest key that may match another by
// containment rather than equality.
const partialRemainderMinOverlap = 24

// partialRemainderMatch reports whether two keys name one criterion. The
// remainder list and the PR body may have been shortened or trimmed by a
// model or a person, and a met bullet carries trailing evidence, so a long
// enough key contained in the other matches too.
func partialRemainderMatch(a, b string) bool {
	if a == b {
		return true
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	return len(short) >= partialRemainderMinOverlap && strings.Contains(long, short)
}

func partialRemainderMatchAny(k string, keys []string) bool {
	for _, o := range keys {
		if partialRemainderMatch(k, o) {
			return true
		}
	}
	return false
}

// reconcilePartialRemainder returns the brief, in issue-body order, the
// criteria it added as new since the partial PR, and the listed criteria the
// body no longer contains. A listed criterion still in the body stays whatever
// its classification (#3089: code on main does not prove a listed criterion
// met). With addNew, a body criterion joins the brief only when it is new: it
// matches neither a listed entry nor a criterion the partial PR named (met or
// unmet), and the report does not judge it satisfied.
func reconcilePartialRemainder(listed, prCriteria []string, rec *preflight.ACReconcileResult, addNew bool) (scope, added, dropped []string) {
	listedKeys := make([]string, len(listed))
	found := make([]bool, len(listed))
	for i, l := range listed {
		listedKeys[i] = partialRemainderKey(l)
	}
	prKeys := make([]string, len(prCriteria))
	for i, p := range prCriteria {
		prKeys[i] = partialRemainderKey(p)
	}
	for _, c := range rec.AcceptanceCriteria {
		k := partialRemainderKey(c.Text)
		isListed := false
		for i, lk := range listedKeys {
			if partialRemainderMatch(lk, k) {
				found[i] = true
				isListed = true
			}
		}
		switch {
		case isListed:
			scope = append(scope, c.Text)
		case addNew && c.Classification != preflight.ACSatisfied && !partialRemainderMatchAny(k, prKeys):
			scope = append(scope, c.Text)
			added = append(added, c.Text)
		}
	}
	for i, l := range listed {
		if !found[i] {
			dropped = append(dropped, l)
		}
	}
	return scope, added, dropped
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

// PartialRerunScopeBlocks renders the scope block for each stage that
// carries one, keyed by stage name, for the extension's stage loop (#3095):
// `nightgauge issue partial-delivery --json` hands it over so the text has one
// source. scope is the reconciled brief (ReconcilePartialScope, #3242); an
// empty scope renders the "nothing remains" block, as on the Go path.
func PartialRerunScopeBlocks(pr int, scope []string) map[string]string {
	if pr <= 0 {
		return nil
	}
	rerun := &state.PartialRerun{PR: pr, Criteria: scope}
	return map[string]string{
		string(state.StageFeaturePlanning): renderPartialRerunPromptBlock(state.StageFeaturePlanning, rerun, false),
		string(state.StageFeatureDev):      renderPartialRerunPromptBlock(state.StageFeatureDev, rerun, false),
	}
}

// partialRerunPromptBlock is the prompt trailer for a partial re-run's
// planning and feature-dev stages. "" when scope is nil, so an unscoped
// run's prompt stays byte-identical.
func partialRerunPromptBlock(stage state.PipelineStage, scope *state.PartialRerun) string {
	return renderPartialRerunPromptBlock(stage, scope, true)
}

// renderPartialRerunPromptBlock renders the block. Both paths reconcile the
// list against the issue body (#3242). goPath adds the two
// sentences only the Go scheduler keeps: it splices the record into the
// planning context and stops a no-diff feature-dev before feature-validate.
// The extension's stage loop does neither, so its block omits them (#3095).
func renderPartialRerunPromptBlock(stage state.PipelineStage, scope *state.PartialRerun, goPath bool) string {
	if scope == nil || scope.PR <= 0 {
		return ""
	}
	if stage != state.StageFeaturePlanning && stage != state.StageFeatureDev {
		return ""
	}
	var b strings.Builder
	if len(scope.Criteria) == 0 {
		b.WriteString("\n\n" + partialRerunPromptHeading + "\n\n")
		b.WriteString(fmt.Sprintf("PR #%d already delivered the rest of this issue, and that work is already on `main`. "+
			"Every criterion its partial-delivery comment listed as still unmet has since been removed from "+
			"the issue body, and the body adds none, so nothing remains: plan nothing beyond what is "+
			"already delivered and write no code.\n", scope.PR))
		return b.String()
	}
	b.WriteString("\n\n" + partialRerunPromptHeading + "\n\n")
	b.WriteString(fmt.Sprintf("PR #%d already delivered the rest of this issue, and that work is already on `main`. "+
		"This run's whole scope is the acceptance criteria that PR's partial-delivery comment "+
		"listed as still unmet", scope.PR))
	b.WriteString(", reconciled against the current issue body:\n\n")
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
		b.WriteString(" `focus_acs` is exactly these criteria: do not widen it with criteria the regenerated " +
			"`ac-reconcile-{N}.json` reports unmet or undetectable. When that report's route differs, " +
			"say so in the stage output.\n")
	case state.StageFeatureDev:
		b.WriteString("Implement only these criteria and change nothing else. If they are already met on this " +
			"branch, write no code")
		if goPath {
			b.WriteString(": the run then ends after this stage, without feature-validate")
		}
		b.WriteString(". When the plan's focus criteria name criteria that this list does not, the plan wins: " +
			"implement them and say so in the stage output. Write no code only when every focus criterion " +
			"is already met on this branch.\n")
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

// stopNoDiffBeforePRCreate runs StopNoDiffBeforePRCreate for the scheduler,
// reading the issue's current body and commenting through the repo client.
func (s *Scheduler) stopNoDiffBeforePRCreate(ctx context.Context, item types.BoardItem, ws string) (string, bool) {
	owner, name := splitRepo(item.Repo)
	fetch := func(ctx context.Context) (*types.Issue, error) {
		svc := s.issueServiceFor(ctx, owner, name)
		if svc == nil {
			return nil, fmt.Errorf("no issue service")
		}
		return svc.GetIssueWithRelations(ctx, owner, name, item.Number, gh.NoRelations)
	}
	commenter := s.noDiffCommenter
	if commenter == nil {
		if c, err := s.repoClient(ctx, owner, name); err == nil {
			commenter = gh.NewIssueService(c)
		}
	}
	return StopNoDiffBeforePRCreate(ctx, ws, item.Number, fetch, commenter)
}
