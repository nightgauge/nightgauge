package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// The run tools answer "how is the pipeline doing" and "what is running"
// from this machine's own state (#1587): the runtime snapshots and the
// execution history the daemon writes for each repository of the workspace.
// They reach no forge, no hosted service and no model provider, and they
// start, approve, cancel or edit nothing. The host's operator sees every run
// of their own workspace. A teammate sees only runs of the repositories the
// turn command lists as readable, never a private run (#2400: a private run
// is its creator's alone, and every private run on this machine is the
// operator's), and never a run the state cannot attribute to a repository.
// Nothing they return names a local path or a process id.

const (
	// defaultRunLimit and maxRunLimit bound pipeline_status's recent runs per
	// repository.
	defaultRunLimit = 10
	maxRunLimit     = 50
	// maxRunTitleChars bounds a run's title as the tools show it.
	maxRunTitleChars = 200
)

// liveArms are the liveness evidence that a run is executing now, as the
// snapshot scan names it (state.ActiveIssues.Protected). The other arms
// protect a paused, finished or unreadable snapshot, not a running job.
var liveArms = []string{"live-sidecar", "stage-child", "timestamp-lease"}

// RunRecord is one run as the run tools report it.
type RunRecord struct {
	Repo  string `json:"repo"`
	Issue int    `json:"issue"`
	Title string `json:"title,omitempty"`
	RunID string `json:"run_id,omitempty"`
	// State is running, paused, stalled (not finished, with no evidence that
	// anything executes it), abandoned, or a finished run's outcome.
	State          string `json:"state"`
	Stage          string `json:"stage,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	StageStartedAt string `json:"stage_started_at,omitempty"`
	FinishedAt     string `json:"finished_at,omitempty"`
	Branch         string `json:"branch,omitempty"`
	PullRequest    string `json:"pull_request,omitempty"`
	// Evidence is what shows a running run is live: its stage process
	// (stage-child), the orchestrator's sidecar (live-sidecar), or a
	// snapshot written within the liveness window (timestamp-lease).
	Evidence string `json:"evidence,omitempty"`
	// Private marks a run its creator chose private (#2400). Only the host's
	// operator, its creator, is ever shown one.
	Private bool `json:"private,omitempty"`
	// unattributed marks a run the state names without its repository or
	// visibility: only the operator is shown one.
	unattributed bool
}

// RepoRuns is one repository's runs.
type RepoRuns struct {
	Repo string
	// Current are the runs with a snapshot: in flight, paused or not yet
	// cleaned up. Newest first.
	Current []RunRecord
	// Recent are finished runs from the execution history, most recent
	// first.
	Recent []RunRecord
	// Warnings are what the read could not account for.
	Warnings []string
}

// RunSource reads one repository's runs from local state.
type RunSource interface {
	Runs(slug, root string, recent int) (RepoRuns, error)
}

// LocalRuns is the RunSource of this machine: the snapshot directory and the
// execution history of each checkout, read and never written.
type LocalRuns struct{}

// Runs reads root's snapshots and its last recent finished runs.
func (l LocalRuns) Runs(slug, root string, recent int) (RepoRuns, error) {
	out := RepoRuns{Repo: slug}
	dir, err := state.PipelineStateDir(root)
	if err != nil {
		return out, err
	}
	active, err := state.ActiveIssuesFromSnapshots(dir)
	if err != nil {
		return out, err
	}
	// The scan's own warnings name files on this machine; only their number
	// leaves it.
	if n := len(active.Warnings); n > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d pipeline state file(s) could not be fully read", n))
	}
	unreadable := 0
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	seen := map[int]bool{}
	for _, e := range entries {
		issue, runID, ok := state.ParseSnapshotFilename(e.Name())
		if e.IsDir() || !ok {
			continue
		}
		rs, err := state.LoadSnapshotByIdentity(dir, issue, runID)
		if err != nil {
			unreadable++
			continue
		}
		rec := RunRecord{
			Repo: firstNonEmpty(rs.Repo, slug), Issue: rs.IssueNumber, Title: clip(rs.Title, maxRunTitleChars),
			RunID: rs.RunID, Stage: string(rs.Stage), Branch: rs.Branch, PullRequest: rs.PrUrl,
			StartedAt: stamp(rs.StartedAt), StageStartedAt: stamp(rs.StageStart),
			Private: rs.Visibility == state.VisibilityPrivate,
		}
		arm := active.Protected[issue]
		switch {
		case rs.Terminal:
			rec.State = firstNonEmpty(rs.TerminalOutcome, "finished")
			if rs.TerminalAt != nil {
				rec.FinishedAt = stamp(*rs.TerminalAt)
			}
		case rs.Abandoned:
			rec.State = "abandoned"
		case rs.Paused:
			rec.State = "paused"
		case isLive(arm):
			rec.State, rec.Evidence = "running", armName(arm)
			seen[issue] = true
		default:
			rec.State = "stalled"
		}
		out.Current = append(out.Current, rec)
	}
	if unreadable > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d run snapshot(s) could not be read", unreadable))
	}
	// An orchestrator's sidecar names a live issue whose snapshot is not on
	// disk yet: it is running all the same. The sidecar names neither the
	// run's repository nor its visibility, so only the operator sees it.
	for issue, arm := range active.Protected {
		if !seen[issue] && strings.HasPrefix(arm, "live-sidecar") && !hasIssue(out.Current, issue) {
			out.Current = append(out.Current, RunRecord{Repo: slug, Issue: issue, State: "running",
				Evidence: armName(arm), unattributed: true})
		}
	}
	sort.SliceStable(out.Current, func(i, j int) bool {
		if out.Current[i].StartedAt != out.Current[j].StartedAt {
			return out.Current[i].StartedAt > out.Current[j].StartedAt
		}
		return out.Current[i].RunID > out.Current[j].RunID
	})
	finished, err := state.RecentRuns(root, recent)
	if err != nil {
		out.Warnings = append(out.Warnings, "the execution history is unreadable")
	}
	for _, e := range finished {
		out.Recent = append(out.Recent, RunRecord{
			Repo: firstNonEmpty(e.Repo, slug), Issue: e.IssueNumber, Title: clip(e.Title, maxRunTitleChars), RunID: e.RunID,
			State: firstNonEmpty(e.Outcome, "finished"), StartedAt: e.StartedAt,
			FinishedAt: firstNonEmpty(e.CompletedAt, e.RecordedAt), Branch: e.Branch,
			Private: e.Visibility == state.VisibilityPrivate,
		})
	}
	return out, nil
}

// armName is the liveness arm without its evidence, which can name a
// process id on this machine.
func armName(arm string) string {
	name, _, _ := strings.Cut(arm, ",")
	return name
}

func isLive(arm string) bool {
	for _, a := range liveArms {
		if arm == a || strings.HasPrefix(arm, a+",") {
			return true
		}
	}
	return false
}

func hasIssue(runs []RunRecord, issue int) bool {
	for _, r := range runs {
		if r.Issue == issue {
			return true
		}
	}
	return false
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// readableRepoRoots are the workspace's checkouts the poster may see runs
// of, sorted by slug, narrowed to one repository when owner and repo name
// it.
func (s *Toolset) readableRepoRoots(owner, repo string) ([][2]string, *Error) {
	if (owner == "") != (repo == "") {
		return nil, fail(CodeInvalidArguments, "name both owner and repo, or neither")
	}
	var out [][2]string
	for slug, root := range s.env.RepoRoots {
		o, r, _ := strings.Cut(slug, "/")
		if owner != "" && !(strings.EqualFold(o, owner) && strings.EqualFold(r, repo)) {
			continue
		}
		if !s.turn.PosterIsOperator && !s.canReadRepo(o, r) {
			continue
		}
		out = append(out, [2]string{slug, root})
	}
	if owner != "" && len(out) == 0 {
		if !s.turn.PosterIsOperator && !s.canReadRepo(owner, repo) {
			return nil, errRepoNotReadable
		}
		return nil, fail(CodeRepoNotInWorkspace, "this workspace has no checkout of that repository")
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out, nil
}

// runArgs are the run tools' arguments.
type runArgs struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Limit *int   `json:"limit"`
}

// pipelineStatus reports each readable repository's current and recent runs.
func (s *Toolset) pipelineStatus(raw json.RawMessage) (string, *Error) {
	var a runArgs
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	limit := defaultRunLimit
	if a.Limit != nil {
		if *a.Limit < 0 || *a.Limit > maxRunLimit {
			return "", fail(CodeInvalidArguments, fmt.Sprintf("limit must be 0 to %d", maxRunLimit))
		}
		limit = *a.Limit
	}
	repos, ferr := s.readRuns(a.Owner, a.Repo, limit)
	if ferr != nil {
		return "", ferr
	}
	type repoStatus struct {
		Repo     string      `json:"repo"`
		Running  int         `json:"running"`
		Paused   int         `json:"paused"`
		Current  []RunRecord `json:"current"`
		Recent   []RunRecord `json:"recent"`
		Warnings []string    `json:"warnings,omitempty"`
	}
	out := []repoStatus{}
	for _, r := range repos {
		st := repoStatus{Repo: r.Repo, Current: nonNil(r.Current), Recent: nonNil(r.Recent), Warnings: r.Warnings}
		for _, c := range r.Current {
			switch c.State {
			case "running":
				st.Running++
			case "paused":
				st.Paused++
			}
		}
		out = append(out, st)
	}
	return untrusted("this machine's pipeline state", map[string]any{"repositories": out}), nil
}

// listRunningJobs reports the runs executing now in the readable
// repositories.
func (s *Toolset) listRunningJobs(raw json.RawMessage) (string, *Error) {
	var a struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
	}
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	repos, ferr := s.readRuns(a.Owner, a.Repo, 0)
	if ferr != nil {
		return "", ferr
	}
	running := []RunRecord{}
	var warnings []string
	for _, r := range repos {
		for _, c := range r.Current {
			if c.State == "running" {
				running = append(running, c)
			}
		}
		warnings = append(warnings, r.Warnings...)
	}
	content := map[string]any{"running": running}
	if len(warnings) > 0 {
		content["warnings"] = warnings
	}
	return untrusted("this machine's pipeline state", content), nil
}

// readRuns reads the runs of every readable repository, keeping only the
// runs a teammate may read when a checkout's state holds another
// repository's run.
func (s *Toolset) readRuns(owner, repo string, recent int) ([]RepoRuns, *Error) {
	if s.env.Runs == nil {
		return nil, fail(CodeStateUnavailable, "this workspace's pipeline state is not available to this turn")
	}
	roots, ferr := s.readableRepoRoots(owner, repo)
	if ferr != nil {
		return nil, ferr
	}
	out := make([]RepoRuns, 0, len(roots))
	for _, sr := range roots {
		rr, err := s.env.Runs.Runs(sr[0], sr[1], recent)
		if err != nil {
			rr = RepoRuns{Repo: sr[0], Warnings: []string{"the pipeline state of " + sr[0] + " could not be read"}}
		}
		rr.Current = s.keepReadable(rr.Current, owner, repo)
		rr.Recent = s.keepReadable(rr.Recent, owner, repo)
		out = append(out, rr)
	}
	return out, nil
}

// keepReadable drops, when owner and repo name one, every other
// repository's runs; and for anyone but the host's operator, the runs of a
// repository the poster may not read, every private run and every run the
// state does not attribute.
func (s *Toolset) keepReadable(runs []RunRecord, owner, repo string) []RunRecord {
	kept := runs[:0]
	for _, r := range runs {
		o, n, _ := strings.Cut(r.Repo, "/")
		if owner != "" && !(strings.EqualFold(o, owner) && strings.EqualFold(n, repo)) {
			continue
		}
		if s.turn.PosterIsOperator || (s.canReadRepo(o, n) && !r.Private && !r.unattributed) {
			kept = append(kept, r)
		}
	}
	return kept
}

func nonNil(r []RunRecord) []RunRecord {
	if r == nil {
		return []RunRecord{}
	}
	return r
}
