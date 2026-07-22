package graduation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/internal/knowledge"
)

// AutoGraduateStatus enumerates the terminal statuses an AutoGraduate run can
// produce. Returned both at the per-candidate and aggregate levels so callers
// can distinguish "PR created" from "already idempotent" from "tied scores"
// without parsing strings.
const (
	AutoStatusCreated          = "created"
	AutoStatusAlreadyGraduated = "already_graduated"
	AutoStatusDryRun           = "dry_run"
	AutoStatusNoCandidates     = "no_candidates"
	AutoStatusTieUnresolved    = "tie_unresolved"
	AutoStatusError            = "error"
)

// Default graduation PR labels — applied to every graduation PR created by
// auto-mode. Per PRD AC: "PR labels applied: type:docs, priority:medium,
// size:S."
var DefaultAutoGraduateLabels = []string{"type:docs", "priority:medium", "size:S"}

// AutoGraduateInput is the orchestrator entrypoint argument set. All fields
// other than WorkspaceRoot, IssueNumber, Git, and Forge are optional and zero
// values map to AC defaults.
type AutoGraduateInput struct {
	WorkspaceRoot string
	IssueNumber   int
	// ADRIndex selects which ranked candidate to graduate. 0 = highest scoring.
	ADRIndex int
	// DryRun skips all filesystem and forge mutations and returns the planned
	// changes instead.
	DryRun bool
	// AllCandidates iterates every qualifying candidate rather than a single one.
	AllCandidates bool
	// BaseBranch is the branch the graduation branch is created from.
	// Defaults to "main" when empty.
	BaseBranch string
	// Owner and Repo are passed to Forge.GetRepoID/ListOpenPRsForBranch.
	Owner string
	Repo  string
	// ProjectStatusField names the single-select field set to Ready on the
	// project board. Defaults to "Status" when empty.
	ProjectStatusField string
	// ProjectStatusReady is the option name applied to ProjectStatusField for
	// graduation PRs. Defaults to "Ready" when empty.
	ProjectStatusReady string
	// Now lets tests inject a deterministic timestamp.
	Now func() time.Time
	// Git and Forge are required.
	Git   GitService
	Forge ForgeClient
}

// AutoGraduateResult is the orchestrator's return value. Status aggregates
// PerCandidate per the contract in auto_test.go.
type AutoGraduateResult struct {
	Issue         int                `json:"issue"`
	DecisionsPath string             `json:"decisions_path"`
	Status        string             `json:"status"`
	DryRun        bool               `json:"dry_run"`
	PerCandidate  []CandidateOutcome `json:"per_candidate"`
	// TiedADRIndexes lists the ADR indexes that tied for top score when the
	// caller did not pass ADRIndex and the tie could not be auto-resolved.
	TiedADRIndexes []int `json:"tied_adr_indexes,omitempty"`
}

// CandidateOutcome captures the outcome of processing one candidate ADR.
type CandidateOutcome struct {
	ADRIndex          int      `json:"adr_index"`
	ADRAnchor         string   `json:"adr_anchor"`
	ADRTitle          string   `json:"adr_title"`
	DestinationDoc    string   `json:"destination_doc"`
	DestinationAnchor string   `json:"destination_anchor"`
	Branch            string   `json:"branch"`
	PRNumber          int      `json:"pr_number,omitempty"`
	PRURL             string   `json:"pr_url,omitempty"`
	PRNodeID          string   `json:"pr_node_id,omitempty"`
	LabelsApplied     []string `json:"labels_applied,omitempty"`
	BoardSynced       bool     `json:"board_synced"`
	Status            string   `json:"status"`
	SkipReason        string   `json:"skip_reason,omitempty"`
	// PlannedAppend is the rendered markdown block that would be (or was)
	// appended to the destination doc. Populated for both DryRun and normal
	// runs so callers can show the diff.
	PlannedAppend string `json:"planned_append,omitempty"`
}

// GitService is the small interface AutoGraduate consumes. CLI glue wraps
// *git.Service in an adapter satisfying it.
type GitService interface {
	CurrentBranch() (string, error)
	LocalBranchExists(name string) (bool, error)
	BranchCreateFrom(name, base string) error
	BranchDelete(name string) error
	Checkout(branch string) error
	Commit(message string) (string, error)
	PushBranch(name string) error
}

// ForgeClient is the small interface AutoGraduate consumes. CLI glue wraps
// forge.ForgeClient (PRs, Project) in an adapter satisfying it.
type ForgeClient interface {
	GetRepoID(ctx context.Context, owner, repo string) (string, error)
	ListOpenPRsForBranch(ctx context.Context, owner, repo, head string) ([]forgetypes.PullRequest, error)
	CreatePR(ctx context.Context, repoID, title, body, head, base string) (*forgetypes.PullRequest, error)
	UpdatePR(ctx context.Context, prID string, opts forge.UpdatePROptions) (*forgetypes.PullRequest, error)
	AddProjectItem(ctx context.Context, contentNodeID string) (string, error)
	SetProjectStatus(ctx context.Context, itemID, fieldName, optionName string) error
}

// AutoGraduate orchestrates one or more end-to-end graduation flows. See
// docs/KNOWLEDGE_BASE.md#graduation-workflow for the workflow this implements
// and PLAN.md §3597 Step 2 for the algorithm.
func AutoGraduate(ctx context.Context, in AutoGraduateInput) (AutoGraduateResult, error) {
	if in.IssueNumber <= 0 {
		return AutoGraduateResult{}, fmt.Errorf("issue number must be positive")
	}
	if in.Git == nil {
		return AutoGraduateResult{}, fmt.Errorf("Git service is required")
	}
	if in.Forge == nil && !in.DryRun {
		return AutoGraduateResult{}, fmt.Errorf("Forge client is required for non-dry-run")
	}
	if in.Now == nil {
		in.Now = time.Now
	}
	if in.BaseBranch == "" {
		in.BaseBranch = "main"
	}
	if in.ProjectStatusField == "" {
		in.ProjectStatusField = "Status"
	}
	if in.ProjectStatusReady == "" {
		in.ProjectStatusReady = "Ready"
	}

	// --all-candidates falls back to the retro-default MinScore; explicit
	// --adr-index selection wants the full ranked list so a low-scoring
	// candidate can still be addressed by index.
	minScore := 1
	if in.AllCandidates {
		minScore = DefaultMinScore
	}
	cands, err := Candidates(in.WorkspaceRoot, in.IssueNumber, Options{MinScore: minScore})
	if err != nil {
		return AutoGraduateResult{}, err
	}

	result := AutoGraduateResult{
		Issue:         in.IssueNumber,
		DecisionsPath: cands.DecisionsPath,
		DryRun:        in.DryRun,
		PerCandidate:  []CandidateOutcome{},
	}

	if len(cands.Candidates) == 0 {
		result.Status = AutoStatusNoCandidates
		return result, nil
	}

	targets, tieIdx, err := selectTargets(cands.Candidates, in.ADRIndex, in.AllCandidates)
	if err != nil {
		return result, err
	}
	if len(tieIdx) > 0 {
		result.TiedADRIndexes = tieIdx
		result.Status = AutoStatusTieUnresolved
		return result, nil
	}

	decisionsAbs := cands.DecisionsPath
	if !filepath.IsAbs(decisionsAbs) {
		decisionsAbs = filepath.Join(in.WorkspaceRoot, cands.DecisionsPath)
	}

	for _, c := range targets {
		outcome := processCandidate(ctx, in, c, decisionsAbs)
		result.PerCandidate = append(result.PerCandidate, outcome)
	}

	result.Status = aggregateStatus(result.PerCandidate, in.DryRun)
	return result, nil
}

// selectTargets resolves which candidates to process. When adrIndex is set or
// allCandidates is true the selection is unambiguous. Otherwise the
// highest-scoring candidate wins; if two or more candidates share the top
// score the tied indices are returned so the caller can surface a
// disambiguation hint.
func selectTargets(cands []Candidate, adrIndex int, allCandidates bool) ([]Candidate, []int, error) {
	if allCandidates {
		return cands, nil, nil
	}
	if adrIndex > 0 {
		for _, c := range cands {
			if c.ADRIndex == adrIndex {
				return []Candidate{c}, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("ADR-%03d not found in candidate list (use graduate-candidates to inspect)", adrIndex)
	}
	if len(cands) == 0 {
		return nil, nil, nil
	}
	topScore := cands[0].Score
	tied := []int{}
	for _, c := range cands {
		if c.Score == topScore {
			tied = append(tied, c.ADRIndex)
		}
	}
	if len(tied) > 1 {
		sort.Ints(tied)
		return nil, tied, nil
	}
	return []Candidate{cands[0]}, nil, nil
}

// processCandidate runs one ADR through the full graduation flow. All errors
// are surfaced via CandidateOutcome.Status == AutoStatusError so a partial
// failure in one candidate does not abort the others when --all-candidates is
// set.
func processCandidate(ctx context.Context, in AutoGraduateInput, c Candidate, decisionsAbs string) CandidateOutcome {
	adrAnchor := fmt.Sprintf("ADR-%03d", c.ADRIndex)
	branch := fmt.Sprintf("docs/graduate-%d-adr-%03d", in.IssueNumber, c.ADRIndex)
	outcome := CandidateOutcome{
		ADRIndex:       c.ADRIndex,
		ADRAnchor:      adrAnchor,
		ADRTitle:       c.ADRTitle,
		DestinationDoc: c.SuggestedDest,
		Branch:         branch,
	}

	block, err := knowledge.ReadADRBlock(decisionsAbs, adrAnchor)
	if err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("read ADR block: %v", err)
		return outcome
	}

	graduatedToMarker := "<!-- graduated-to:"
	alreadyGraduated := strings.Contains(block, graduatedToMarker)
	if alreadyGraduated {
		outcome.Status = AutoStatusAlreadyGraduated
		if in.Forge != nil && in.Owner != "" && in.Repo != "" {
			prs, _ := in.Forge.ListOpenPRsForBranch(ctx, in.Owner, in.Repo, branch)
			if len(prs) > 0 {
				outcome.PRNumber = prs[0].Number
				outcome.PRURL = prs[0].URL
				outcome.PRNodeID = prs[0].NodeID
			}
		}
		return outcome
	}

	destDocRel := c.SuggestedDest
	destDocAbs := destDocRel
	if !filepath.IsAbs(destDocAbs) {
		destDocAbs = filepath.Join(in.WorkspaceRoot, destDocRel)
	}

	anchor, err := deriveAnchor(destDocAbs, c.ADRTitle)
	if err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("derive anchor: %v", err)
		return outcome
	}
	outcome.DestinationAnchor = anchor

	rendered := renderDestinationSection(c.ADRTitle, knowledge.FormatGraduatedFromComment(decisionsRelForComment(in.WorkspaceRoot, decisionsAbs), adrAnchor), block, in.IssueNumber)
	outcome.PlannedAppend = rendered

	if in.DryRun {
		outcome.Status = AutoStatusDryRun
		return outcome
	}

	// Stash the original branch so we can restore the caller's worktree
	// regardless of success or failure on the graduation branch.
	originalBranch, _ := in.Git.CurrentBranch()

	if exists, _ := in.Git.LocalBranchExists(branch); exists {
		// Delete and recreate — when the prior aborted run left an orphan
		// branch, the verbatim content in this run may differ.
		_ = in.Git.BranchDelete(branch)
	}
	if err := in.Git.BranchCreateFrom(branch, in.BaseBranch); err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("create branch %s from %s: %v", branch, in.BaseBranch, err)
		return outcome
	}

	if err := appendDestinationSection(destDocAbs, rendered); err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("append to %s: %v", destDocRel, err)
		_ = restoreBranch(in.Git, originalBranch)
		return outcome
	}

	// Write the source-side marker idempotently via the canonical helper.
	docsSection := destDocRel + "#" + anchor
	if err := knowledge.WriteBacklink(knowledge.GraduateInput{
		DecisionsPath: decisionsAbs,
		ADRAnchor:     adrAnchor,
		DocsSection:   docsSection,
	}); err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("write graduated-to marker: %v", err)
		_ = restoreBranch(in.Git, originalBranch)
		return outcome
	}

	commitMsg := fmt.Sprintf("docs(#%d): graduate %s to %s", in.IssueNumber, c.ADRTitle, destDocRel)
	if _, err := in.Git.Commit(commitMsg); err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("commit: %v", err)
		_ = restoreBranch(in.Git, originalBranch)
		return outcome
	}

	if err := in.Git.PushBranch(branch); err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("push branch %s: %v", branch, err)
		_ = restoreBranch(in.Git, originalBranch)
		return outcome
	}

	// Always restore the caller's branch so the worktree is unchanged on
	// success — the new branch lives only on origin from the caller's POV.
	defer func() { _ = restoreBranch(in.Git, originalBranch) }()

	repoID, err := in.Forge.GetRepoID(ctx, in.Owner, in.Repo)
	if err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("get repo id: %v", err)
		return outcome
	}

	title := fmt.Sprintf("docs(#%d): graduate %s", in.IssueNumber, c.ADRTitle)
	body := renderPRBody(in.IssueNumber, c, adrAnchor, destDocRel, anchor, decisionsRelForComment(in.WorkspaceRoot, decisionsAbs))
	pr, err := in.Forge.CreatePR(ctx, repoID, title, body, branch, in.BaseBranch)
	if err != nil {
		outcome.Status = AutoStatusError
		outcome.SkipReason = fmt.Sprintf("create PR: %v", err)
		return outcome
	}
	if pr == nil || pr.Number <= 0 {
		outcome.Status = AutoStatusError
		outcome.SkipReason = "create PR returned empty result"
		return outcome
	}
	outcome.PRNumber = pr.Number
	outcome.PRURL = pr.URL
	outcome.PRNodeID = pr.NodeID

	labels := append([]string{}, DefaultAutoGraduateLabels...)
	labelsCopy := labels
	if _, err := in.Forge.UpdatePR(ctx, pr.NodeID, forge.UpdatePROptions{Labels: &labelsCopy}); err != nil {
		// Label application is best-effort — surface as a warning via
		// SkipReason but still mark as created so the caller sees the PR.
		if !errors.Is(err, forge.ErrUnsupported) {
			outcome.SkipReason = fmt.Sprintf("apply labels (warning): %v", err)
		}
	} else {
		outcome.LabelsApplied = labels
	}

	itemID, err := in.Forge.AddProjectItem(ctx, pr.NodeID)
	if err != nil {
		outcome.Status = AutoStatusCreated
		outcome.SkipReason = strings.TrimSpace(outcome.SkipReason + "; add to project board (warning): " + err.Error())
		return outcome
	}
	if err := in.Forge.SetProjectStatus(ctx, itemID, in.ProjectStatusField, in.ProjectStatusReady); err != nil {
		outcome.Status = AutoStatusCreated
		outcome.SkipReason = strings.TrimSpace(outcome.SkipReason + "; set project status (warning): " + err.Error())
		return outcome
	}
	outcome.BoardSynced = true
	outcome.Status = AutoStatusCreated
	return outcome
}

func restoreBranch(git GitService, branch string) error {
	if branch == "" {
		return nil
	}
	return git.Checkout(branch)
}

// decisionsRelForComment returns a workspace-relative path suitable for
// embedding in a graduated-from comment. Falls back to the absolute path when
// relativization fails.
func decisionsRelForComment(workspaceRoot, decisionsAbs string) string {
	if rel, err := filepath.Rel(workspaceRoot, decisionsAbs); err == nil {
		return filepath.ToSlash(rel)
	}
	return decisionsAbs
}

// aggregateStatus collapses per-candidate statuses into the top-level result
// status.
func aggregateStatus(per []CandidateOutcome, dryRun bool) string {
	if len(per) == 0 {
		return AutoStatusNoCandidates
	}
	if dryRun {
		return AutoStatusDryRun
	}
	createdAny := false
	allIdempotent := true
	for _, o := range per {
		if o.Status == AutoStatusCreated {
			createdAny = true
			allIdempotent = false
		} else if o.Status != AutoStatusAlreadyGraduated {
			allIdempotent = false
		}
		if o.Status == AutoStatusError {
			return AutoStatusError
		}
	}
	if createdAny {
		return AutoStatusCreated
	}
	if allIdempotent {
		return AutoStatusAlreadyGraduated
	}
	return AutoStatusCreated
}

// renderDestinationSection composes the markdown block that gets appended to
// the destination doc. Layout:
//
//	## <ADR title>
//	<graduated-from comment>
//	<verbatim ADR block>
//	_Source: issue #N, ADR <title>_
func renderDestinationSection(title, graduatedFrom, block string, issueNumber int) string {
	var b strings.Builder
	b.WriteString("## ")
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString(graduatedFrom)
	b.WriteString("\n\n")
	body := block
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	b.WriteString(body)
	b.WriteString("\n_Source: issue #")
	b.WriteString(fmt.Sprintf("%d", issueNumber))
	b.WriteString(", ADR ")
	b.WriteString(title)
	b.WriteString("_\n")
	return b.String()
}

// renderPRBody builds a deterministic PR body that links the source ADR, the
// destination doc anchor, and includes a small reviewer checklist.
func renderPRBody(issueNumber int, c Candidate, adrAnchor, destDoc, destAnchor, decisionsPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Graduates `%s` from `%s` to `%s#%s`.\n\n",
		adrAnchor, decisionsPath, destDoc, destAnchor)
	fmt.Fprintf(&b, "**Source issue**: #%d\n", issueNumber)
	fmt.Fprintf(&b, "**ADR**: %s — %s\n", adrAnchor, c.ADRTitle)
	fmt.Fprintf(&b, "**Score**: %d (signals: %s)\n", c.Score, strings.Join(c.Signals, ", "))
	b.WriteString("\n## Reviewer checklist\n\n")
	b.WriteString("- [ ] Distilled prose for the long-lived audience (replace verbatim Decision block if appropriate).\n")
	b.WriteString("- [ ] `<!-- graduated-from: -->` marker preserved in the destination doc.\n")
	b.WriteString("- [ ] `<!-- graduated-to: -->` marker preserved in the source ADR.\n")
	b.WriteString("- [ ] Destination heading anchor matches links elsewhere in `docs/`.\n")
	return b.String()
}

// anchorRe captures `## ` headings used to detect collisions in the
// destination doc.
var anchorRe = regexp.MustCompile(`(?m)^##\s+(.+)$`)

// deriveAnchor returns a kebab-case anchor derived from title, suffixing
// `-2`, `-3`, … until it does not collide with any existing `## ` heading in
// destDocAbs. When the destination file does not exist yet, the base anchor
// is returned unchanged.
func deriveAnchor(destDocAbs, title string) (string, error) {
	base := kebab(title)
	if base == "" {
		base = "section"
	}
	raw, err := os.ReadFile(destDocAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return base, nil
		}
		return "", err
	}
	existing := map[string]bool{}
	for _, m := range anchorRe.FindAllStringSubmatch(string(raw), -1) {
		existing[kebab(strings.TrimSpace(m[1]))] = true
	}
	if !existing[base] {
		return base, nil
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !existing[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not derive a unique anchor for %q (1000 collisions)", title)
}

// kebab lowercases s and replaces runs of non-[a-z0-9] with single dashes;
// trims leading/trailing dashes.
func kebab(s string) string {
	var b strings.Builder
	last := byte('-')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c = c + 32
			b.WriteByte(c)
			last = c
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
			last = c
		default:
			if last != '-' {
				b.WriteByte('-')
				last = '-'
			}
		}
	}
	out := b.String()
	out = strings.Trim(out, "-")
	return out
}

// appendDestinationSection appends rendered to destDocAbs. When the file does
// not exist, creates it with the rendered content as the body (no leading
// title line is injected — rendered already begins with `## <title>`).
// Guarantees at least two newlines between existing content and the appended
// block when the file lacks a trailing blank line.
func appendDestinationSection(destDocAbs, rendered string) error {
	if err := os.MkdirAll(filepath.Dir(destDocAbs), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(destDocAbs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var combined []byte
	if len(existing) == 0 {
		combined = []byte(rendered)
	} else {
		prefix := existing
		if !strings.HasSuffix(string(prefix), "\n\n") {
			if strings.HasSuffix(string(prefix), "\n") {
				prefix = append(prefix, '\n')
			} else {
				prefix = append(prefix, '\n', '\n')
			}
		}
		combined = append(prefix, []byte(rendered)...)
	}
	return os.WriteFile(destDocAbs, combined, 0o644)
}
