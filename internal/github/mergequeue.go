package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Merge-queue support (#2214).
//
// On a base branch whose ruleset requires GitHub's merge queue, a PR cannot be
// merged directly: GraphQL `mergePullRequest` is rejected, and `gh pr merge`
// silently ENQUEUES instead of merging. The PR only reaches MERGED once the
// merge group's required checks pass, which takes a full CI run; a failed
// group removes the PR from the queue and leaves it OPEN with no other signal.
//
// This file is the one implementation of "detect, enqueue, wait, classify".
// It is transport-agnostic: PRService drives it over the in-process GraphQL
// client and the deterministic pr-merge stage drives it over `gh api graphql`,
// so both paths classify a queue outcome identically.
//
// Enqueueing happens here, in deterministic Go. Agents stay barred from
// `gh pr merge --auto` (stage_gate.go, skill_anti_patterns.go): the queue is
// entered only by code that also waits for, and classifies, the outcome.

// GraphQLDoer executes one GraphQL document and returns the raw response body
// (`{"data": …, "errors": […]}`).
type GraphQLDoer func(ctx context.Context, query string, variables map[string]any) ([]byte, error)

// Merge-queue wait defaults. The budget is sized for a full CI run on the
// merge-group commit, not for eventual consistency: a queue wait that expires
// before CI does would report a failure on a PR that is about to merge.
const (
	DefaultMergeQueueWaitTimeout  = 90 * time.Minute
	DefaultMergeQueuePollInterval = 30 * time.Second
	// DefaultMergeQueueDequeueGrace is how many consecutive polls may show an
	// OPEN PR outside the queue, before its entry was ever observed, before
	// that is read as a removal. enqueuePullRequest returns the entry
	// synchronously, so this only absorbs read-after-write lag.
	DefaultMergeQueueDequeueGrace = 3
)

// MergeableUnknown is GitHub's "I have not computed this yet" answer for a
// pull request's `mergeable` field. It is returned for the first seconds after
// a push or a PR creation.
const MergeableUnknown = "UNKNOWN"

// DefaultMergeabilityPolls / DefaultMergeabilityPollInterval bound the
// pr-merge stage's wait for GitHub to compute mergeability (#1933).
//
// Its own budget, deliberately short: mergeability resolves in seconds, unlike
// CI, which takes minutes and gets the far larger CI budget below. A long
// budget would delay the punt on a PR whose mergeability never resolves.
const (
	DefaultMergeabilityPolls        = 10
	DefaultMergeabilityPollInterval = 2 * time.Second
	// DefaultMergeabilityBackoffCap caps the doubling mergeability delay
	// (#2730). With DefaultMergeabilityPolls that budgets 2+4+8+16x7 =
	// 126 s. The old flat 20 s was shorter than GitHub takes to recompute after
	// the base branch moves, so the wait expired on a PR that had no problem.
	DefaultMergeabilityBackoffCap = 16 * time.Second
)

// MergeabilityWaitBudget is the longest one mergeability wait can sleep: the
// sum of the doubling, capped delays over DefaultMergeabilityPolls polls. It
// mirrors stages.(*DeterministicRunner).mergeabilityDelay, and a test in that
// package pins the two together (#2835).
func MergeabilityWaitBudget() time.Duration {
	var total time.Duration
	d := DefaultMergeabilityPollInterval
	for i := 0; i < DefaultMergeabilityPolls; i++ {
		total += min(d, DefaultMergeabilityBackoffCap)
		if d < DefaultMergeabilityBackoffCap {
			d *= 2
		}
	}
	return total
}

// DefaultCIPollInterval / DefaultCIPollMax are the CI-wait budget (#297):
// 30 × 30s = 15 min. The pr-merge stage waits on CI with it, and the merge
// queue's readiness wait (#2584) is bounded by the same total.
const (
	DefaultCIPollInterval = 30 * time.Second
	DefaultCIPollMax      = 30
)

// MergeQueueWaitOptions bounds WaitForMergeQueue. Zero fields take defaults.
type MergeQueueWaitOptions struct {
	Timeout      time.Duration
	PollInterval time.Duration
	DequeueGrace int
	// ReadyTimeout and ReadyPollInterval bound the pre-enqueue readiness wait
	// (#2584). ReadyTimeout defaults to DefaultCIPollInterval x
	// DefaultCIPollMax; ReadyPollInterval, the poll spacing once mergeability
	// is known, defaults to DefaultCIPollInterval. While mergeability is
	// UNKNOWN the wait polls at the shorter of it and
	// DefaultMergeabilityPollInterval.
	ReadyTimeout      time.Duration
	ReadyPollInterval time.Duration
	// OnQueued, when set, is called once per wait with the status of a PR
	// that reached the queue: after its enqueue succeeded, or on the first
	// poll that observes its entry (#2770). It never fires for a PR that left
	// OPEN, failed readiness or was never enqueued, so a caller timing the
	// queue wait starts its clock only once the queue owns the PR.
	OnQueued func(MergeQueueStatus)
	// CheckSquashDefaults, when set, runs before an enqueue and reports
	// whether the repository's squash defaults are PR_TITLE/PR_BODY (#2728).
	// The queue takes no headline, so these defaults decide the squash subject
	// on the base branch. It reads only; it never changes repository settings.
	CheckSquashDefaults func(ctx context.Context) (prTitled bool, err error)
	// CreateCommit, when set, creates a commit object in owner/repo with the
	// given message, tree and parents and returns its OID (#2988). The guard
	// uses it to reword a sole WIP commit; the commit is then moved onto the
	// PR head branch by compare-and-swap. It writes the PR branch only, never
	// repository settings. Nil keeps the refusal.
	CreateCommit func(ctx context.Context, owner, repo, message, tree string, parents []string) (oid string, err error)
}

// onQueuedOnce wraps o.OnQueued so it fires at most once.
func (o MergeQueueWaitOptions) onQueuedOnce() MergeQueueWaitOptions {
	if o.OnQueued == nil {
		return o
	}
	fire, fired := o.OnQueued, false
	o.OnQueued = func(st MergeQueueStatus) {
		if !fired {
			fired = true
			fire(st)
		}
	}
	return o
}

func (o MergeQueueWaitOptions) withDefaults() MergeQueueWaitOptions {
	if o.Timeout <= 0 {
		o.Timeout = DefaultMergeQueueWaitTimeout
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultMergeQueuePollInterval
	}
	if o.DequeueGrace <= 0 {
		o.DequeueGrace = DefaultMergeQueueDequeueGrace
	}
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = DefaultCIPollInterval * DefaultCIPollMax
	}
	if o.ReadyPollInterval <= 0 {
		o.ReadyPollInterval = DefaultCIPollInterval
	}
	return o
}

// MergeQueueStatus is one observation of a PR's merge-queue position.
type MergeQueueStatus struct {
	PRID    string
	Number  int
	State   string // OPEN | MERGED | CLOSED
	BaseRef string
	// QueueRequired is true when the PR's base branch has a merge queue
	// (PullRequest.isMergeQueueEnabled). A direct merge is then rejected.
	QueueRequired  bool
	InQueue        bool
	MergeCommitOID string
	// EntryState is the MergeQueueEntryState (QUEUED, AWAITING_CHECKS,
	// MERGEABLE, UNMERGEABLE, LOCKED) while the PR has an entry, else "".
	EntryState    string
	EntryPosition int
	// EntryHeadOID is the merge-group commit the queue is testing. Kept after
	// the entry disappears so the failing checks can still be fetched.
	EntryHeadOID string
	// Mergeable is the PR's mergeability (MERGEABLE, CONFLICTING, UNKNOWN),
	// MergeStateStatus its merge state (CLEAN, BLOCKED, …), HeadOID its head
	// commit, and ChecksState the head commit's check rollup state (SUCCESS,
	// PENDING, FAILURE, …; "" when no check has been created).
	Mergeable        string
	MergeStateStatus string
	HeadOID          string
	ChecksState      string
}

// Merge-queue failure kinds carried on MergeQueueError.
const (
	MergeQueueDequeued    = "dequeued"
	MergeQueueUnmergeable = "unmergeable"
	MergeQueueClosed      = "closed"
	MergeQueueTimeout     = "timeout"
	// MergeQueueNotReady: the PR never became ready to enqueue (mergeability
	// or its checks still pending) within the readiness budget (#2584).
	MergeQueueNotReady = "not_ready"
	// MergeQueueChecksFailed: the PR's own checks are red, so it was not
	// enqueued (#2584).
	MergeQueueChecksFailed = "checks_failed"
	// MergeQueueSquashDefaults: the repository's squash defaults are not
	// PR_TITLE/PR_BODY (or could not be read) and the PR carries a WIP-preservation commit, so
	// it was not enqueued (#2728).
	MergeQueueSquashDefaults = "squash_defaults"
)

// MergeQueueError is the classified failure of a queued merge. It names the
// failing merge-group checks, when they could be read, so the pipeline's
// CI-failure handling and retro have something concrete to act on.
type MergeQueueError struct {
	PRNumber     int
	Kind         string
	EntryHeadOID string
	FailedChecks []string
	Cause        error
	// Detail qualifies a MergeQueueNotReady error: what was still pending.
	Detail string
}

func (e *MergeQueueError) Error() string {
	var b strings.Builder
	switch e.Kind {
	case MergeQueueNotReady:
		fmt.Fprintf(&b, "merge queue: PR #%d not ready to enqueue", e.PRNumber)
		if e.Detail != "" {
			fmt.Fprintf(&b, " (%s)", e.Detail)
		}
	case MergeQueueChecksFailed:
		fmt.Fprintf(&b, "merge queue: PR #%d checks failed; not enqueued", e.PRNumber)
	case MergeQueueSquashDefaults:
		fmt.Fprintf(&b, "merge queue: PR #%d carries a WIP-preservation commit and the repository squash defaults "+
			"are not PR_TITLE/PR_BODY; not enqueued (an admin sets squash_merge_commit_title=PR_TITLE and "+
			"squash_merge_commit_message=PR_BODY: gh api -X PATCH repos/{owner}/{repo} "+
			"-f squash_merge_commit_title=PR_TITLE -f squash_merge_commit_message=PR_BODY)", e.PRNumber)
	case MergeQueueTimeout:
		fmt.Fprintf(&b, "merge queue: PR #%d not merged before the queue wait budget expired", e.PRNumber)
	case MergeQueueClosed:
		fmt.Fprintf(&b, "merge queue: PR #%d was closed while queued", e.PRNumber)
	case MergeQueueUnmergeable:
		fmt.Fprintf(&b, "merge queue: PR #%d merge group is unmergeable", e.PRNumber)
	default:
		fmt.Fprintf(&b, "merge queue: PR #%d was removed from the queue", e.PRNumber)
	}
	if e.EntryHeadOID != "" && e.Kind != MergeQueueChecksFailed {
		fmt.Fprintf(&b, " (merge-group commit %s)", shortOID(e.EntryHeadOID))
	}
	if len(e.FailedChecks) > 0 {
		label := "merge-group checks"
		if e.Kind == MergeQueueChecksFailed {
			label = "checks"
		}
		fmt.Fprintf(&b, "; failed %s: %s", label, strings.Join(e.FailedChecks, ", "))
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, ": %v", e.Cause)
	}
	return b.String()
}

func (e *MergeQueueError) Unwrap() error { return e.Cause }

func shortOID(oid string) string {
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}

// MergeQueue drives the detect/enqueue/wait cycle over a GraphQLDoer.
type MergeQueue struct {
	do    GraphQLDoer
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
}

// NewMergeQueue builds a MergeQueue over the given transport.
func NewMergeQueue(do GraphQLDoer) *MergeQueue {
	return &MergeQueue{do: do, sleep: sleepCtx, now: time.Now}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

const mergeQueuePRFields = `id number state baseRefName isMergeQueueEnabled isInMergeQueue
mergeable mergeStateStatus headRefOid
commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
mergeCommit { oid }
mergeQueueEntry { state position headCommit { oid } }`

const mergeQueueStatusByIDQuery = `query($id: ID!) { node(id: $id) { ... on PullRequest { ` + mergeQueuePRFields + ` } } }`

const mergeQueueStatusByNumberQuery = `query($owner: String!, $repo: String!, $number: Int!) { repository(owner: $owner, name: $repo) { pullRequest(number: $number) { ` + mergeQueuePRFields + ` } } }`

const enqueuePullRequestMutation = `mutation($id: ID!) { enqueuePullRequest(input: {pullRequestId: $id}) { mergeQueueEntry { state position } } }`

const mergeGroupChecksQuery = `query($id: ID!, $oid: GitObjectID!) { node(id: $id) { ... on PullRequest { baseRepository { object(oid: $oid) { ... on Commit { statusCheckRollup { contexts(first: 100) { nodes { __typename ... on CheckRun { name status conclusion } ... on StatusContext { context state } } } } } } } } } }`

type mqPRNode struct {
	ID                  string `json:"id"`
	Number              int    `json:"number"`
	State               string `json:"state"`
	BaseRefName         string `json:"baseRefName"`
	IsMergeQueueEnabled bool   `json:"isMergeQueueEnabled"`
	IsInMergeQueue      bool   `json:"isInMergeQueue"`
	Mergeable           string `json:"mergeable"`
	MergeStateStatus    string `json:"mergeStateStatus"`
	HeadRefOID          string `json:"headRefOid"`
	Commits             *struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	MergeQueueEntry *struct {
		State      string `json:"state"`
		Position   int    `json:"position"`
		HeadCommit *struct {
			OID string `json:"oid"`
		} `json:"headCommit"`
	} `json:"mergeQueueEntry"`
}

func (n *mqPRNode) status() MergeQueueStatus {
	st := MergeQueueStatus{
		PRID:          n.ID,
		Number:        n.Number,
		State:         n.State,
		BaseRef:       n.BaseRefName,
		QueueRequired: n.IsMergeQueueEnabled,
		InQueue:       n.IsInMergeQueue,

		Mergeable:        n.Mergeable,
		MergeStateStatus: n.MergeStateStatus,
		HeadOID:          n.HeadRefOID,
	}
	if n.Commits != nil && len(n.Commits.Nodes) > 0 {
		if r := n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup; r != nil {
			st.ChecksState = r.State
		}
	}
	if n.MergeCommit != nil {
		st.MergeCommitOID = n.MergeCommit.OID
	}
	if e := n.MergeQueueEntry; e != nil {
		st.InQueue = true
		st.EntryState = e.State
		st.EntryPosition = e.Position
		if e.HeadCommit != nil {
			st.EntryHeadOID = e.HeadCommit.OID
		}
	}
	return st
}

// decodeGraphQL unwraps a GraphQL response body into out, turning a non-empty
// `errors` array into an error.
func decodeGraphQL(body []byte, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decode graphql response: %w", err)
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, 0, len(resp.Errors))
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("graphql: %s", strings.Join(msgs, "; "))
	}
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		return errors.New("graphql: empty data")
	}
	return json.Unmarshal(resp.Data, out)
}

// StatusByID reads the queue status of the PR with the given node ID.
func (q *MergeQueue) StatusByID(ctx context.Context, prID string) (MergeQueueStatus, error) {
	body, err := q.do(ctx, mergeQueueStatusByIDQuery, map[string]any{"id": prID})
	if err != nil {
		return MergeQueueStatus{}, fmt.Errorf("merge queue status: %w", err)
	}
	var data struct {
		Node *mqPRNode `json:"node"`
	}
	if err := decodeGraphQL(body, &data); err != nil {
		return MergeQueueStatus{}, fmt.Errorf("merge queue status: %w", err)
	}
	if data.Node == nil || data.Node.ID == "" {
		return MergeQueueStatus{}, fmt.Errorf("merge queue status: pull request %s not found", prID)
	}
	return data.Node.status(), nil
}

// StatusByNumber reads the queue status of owner/repo#number. The gh
// transport accepts the literal placeholders "{owner}" and "{repo}", which
// `gh api` resolves from the working directory's repository.
func (q *MergeQueue) StatusByNumber(ctx context.Context, owner, repo string, number int) (MergeQueueStatus, error) {
	body, err := q.do(ctx, mergeQueueStatusByNumberQuery, map[string]any{"owner": owner, "repo": repo, "number": number})
	if err != nil {
		return MergeQueueStatus{}, fmt.Errorf("merge queue status: %w", err)
	}
	var data struct {
		Repository *struct {
			PullRequest *mqPRNode `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := decodeGraphQL(body, &data); err != nil {
		return MergeQueueStatus{}, fmt.Errorf("merge queue status: %w", err)
	}
	if data.Repository == nil || data.Repository.PullRequest == nil {
		return MergeQueueStatus{}, fmt.Errorf("merge queue status: pull request #%d not found", number)
	}
	return data.Repository.PullRequest.status(), nil
}

// Enqueue adds the PR to its base branch's merge queue.
func (q *MergeQueue) Enqueue(ctx context.Context, prID string) error {
	body, err := q.do(ctx, enqueuePullRequestMutation, map[string]any{"id": prID})
	if err != nil {
		return fmt.Errorf("enqueue pull request: %w", err)
	}
	var data struct {
		EnqueuePullRequest *struct{} `json:"enqueuePullRequest"`
	}
	if err := decodeGraphQL(body, &data); err != nil {
		return fmt.Errorf("enqueue pull request: %w", err)
	}
	return nil
}

// ErrEnqueueRetriesExhausted marks an enqueue that still failed transiently
// after every retry (#2729). pr-merge maps only this error to the non-fault
// merge_queue_enqueue_failed kind; a permanent rejection keeps its own path.
var ErrEnqueueRetriesExhausted = errors.New("enqueue retries exhausted")

// maxEnqueueRetries bounds the retries of a transient enqueue failure (#2729).
const maxEnqueueRetries = 3

// enqueueRetryBase is the first backoff delay; each retry doubles it.
const enqueueRetryBase = 2 * time.Second

// isTransientForgeErr reports whether err is a forge-side hiccup worth
// retrying: a gateway status, GitHub's generic GraphQL failure, or a dropped
// connection. Rate limits are excluded (the client and pr-merge handle them),
// as is the not-ready rejection (#2584 waits for readiness instead).
func isTransientForgeErr(err error) bool {
	if err == nil || isRateLimited(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, enqueueNotReadyMessage) {
		return false
	}
	for _, s := range []string{
		"502", "503", "504", "bad gateway", "service unavailable", "gateway timeout",
		"something went wrong while executing your query",
		"timeout", "timed out", "connection reset", "connection refused", "eof",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// enqueueRetrying enqueues st's PR, retrying a transient failure with
// exponential backoff (#2729). Before each retry it re-reads the PR: a write
// can land behind a dropped response, so a PR already queued or merged counts
// as enqueued and no second mutation is sent.
func (q *MergeQueue) enqueueRetrying(ctx context.Context, st MergeQueueStatus) error {
	err := q.Enqueue(ctx, st.PRID)
	delay := enqueueRetryBase
	for attempt := 1; err != nil && isTransientForgeErr(err) && attempt <= maxEnqueueRetries; attempt++ {
		log.Printf("merge queue: transient enqueue failure for PR #%d (attempt %d/%d), retrying in %s: %v",
			st.Number, attempt, maxEnqueueRetries+1, delay, err)
		if serr := q.sleep(ctx, delay); serr != nil {
			return err
		}
		delay *= 2
		if cur, serr := q.StatusByID(ctx, st.PRID); serr == nil && (cur.InQueue || cur.State == "MERGED") {
			return nil
		}
		err = q.Enqueue(ctx, st.PRID)
		if err != nil && attempt == maxEnqueueRetries && isTransientForgeErr(err) {
			return fmt.Errorf("enqueue pull request: failed after %d attempts: %w: %w", maxEnqueueRetries+1, ErrEnqueueRetriesExhausted, err)
		}
	}
	return err
}

// enqueueNotReadyMessage is GitHub's rejection of an enqueue issued before
// the PR's mergeability was computed (#2584).
const enqueueNotReadyMessage = "mergeability check has not yet completed"

// ClassifyEnqueueReadiness reports whether a PR may be enqueued now. When it
// may not, kind names a terminal failure (MergeQueueUnmergeable,
// MergeQueueChecksFailed), or is "" while mergeability or checks are pending.
// A PR with no checks at all is ready only when GitHub reports it CLEAN: a
// missing rollup is otherwise read as checks not created yet (#1027).
func ClassifyEnqueueReadiness(st MergeQueueStatus) (ready bool, kind string) {
	switch st.Mergeable {
	case MergeableUnknown, "":
		return false, ""
	case "CONFLICTING":
		return false, MergeQueueUnmergeable
	}
	switch st.ChecksState {
	case "FAILURE", "ERROR":
		return false, MergeQueueChecksFailed
	case "SUCCESS":
		return st.Mergeable == "MERGEABLE", ""
	case "":
		return st.Mergeable == "MERGEABLE" && st.MergeStateStatus == "CLEAN", ""
	}
	return false, ""
}

// WaitReady polls the PR until it may be enqueued (returns its status), its
// checks fail or it conflicts (*MergeQueueError with MergeQueueChecksFailed or
// MergeQueueUnmergeable), or the readiness budget expires (MergeQueueNotReady).
// A PR that leaves OPEN or enters the queue meanwhile is returned as is.
// initial is checked first, so a ready PR costs no extra read.
func (q *MergeQueue) WaitReady(ctx context.Context, initial MergeQueueStatus, opts MergeQueueWaitOptions) (MergeQueueStatus, error) {
	opts = opts.withDefaults()
	deadline := q.now().Add(opts.ReadyTimeout)
	st := initial
	for {
		if st.State != "OPEN" || st.InQueue {
			return st, nil
		}
		ready, kind := ClassifyEnqueueReadiness(st)
		if ready {
			return st, nil
		}
		if kind != "" {
			e := &MergeQueueError{PRNumber: st.Number, Kind: kind}
			if kind == MergeQueueChecksFailed && st.HeadOID != "" {
				failed, err := q.FailedMergeGroupChecks(ctx, st.PRID, st.HeadOID)
				if err != nil {
					log.Printf("merge queue: could not read checks for PR #%d at %s: %v", st.Number, shortOID(st.HeadOID), err)
				}
				e.FailedChecks = failed
			}
			return st, e
		}
		interval := opts.ReadyPollInterval
		if (st.Mergeable == MergeableUnknown || st.Mergeable == "") && DefaultMergeabilityPollInterval < interval {
			interval = DefaultMergeabilityPollInterval
		}
		if !q.now().Add(interval).Before(deadline) {
			return st, &MergeQueueError{PRNumber: st.Number, Kind: MergeQueueNotReady, Detail: notReadyDetail(st)}
		}
		if err := q.sleep(ctx, interval); err != nil {
			return st, &MergeQueueError{PRNumber: st.Number, Kind: MergeQueueNotReady, Detail: notReadyDetail(st), Cause: err}
		}
		next, err := q.StatusByID(ctx, st.PRID)
		if err != nil {
			if isRateLimited(err) || ctx.Err() != nil {
				return st, err
			}
			continue
		}
		st = next
	}
}

func notReadyDetail(st MergeQueueStatus) string {
	if st.Mergeable == MergeableUnknown || st.Mergeable == "" {
		return "mergeability not yet computed"
	}
	if st.ChecksState == "" {
		return "no checks reported yet"
	}
	return "checks " + strings.ToLower(st.ChecksState)
}

// FailedMergeGroupChecks returns the names of the checks that failed on the
// merge-group commit oid, sorted. Pending and passing checks are omitted.
func (q *MergeQueue) FailedMergeGroupChecks(ctx context.Context, prID, oid string) ([]string, error) {
	body, err := q.do(ctx, mergeGroupChecksQuery, map[string]any{"id": prID, "oid": oid})
	if err != nil {
		return nil, fmt.Errorf("merge-group checks: %w", err)
	}
	var data struct {
		Node *struct {
			BaseRepository *struct {
				Object *struct {
					StatusCheckRollup *struct {
						Contexts struct {
							Nodes []struct {
								Typename   string `json:"__typename"`
								Name       string `json:"name"`
								Conclusion string `json:"conclusion"`
								Context    string `json:"context"`
								State      string `json:"state"`
							} `json:"nodes"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"object"`
			} `json:"baseRepository"`
		} `json:"node"`
	}
	if err := decodeGraphQL(body, &data); err != nil {
		return nil, fmt.Errorf("merge-group checks: %w", err)
	}
	if data.Node == nil || data.Node.BaseRepository == nil || data.Node.BaseRepository.Object == nil ||
		data.Node.BaseRepository.Object.StatusCheckRollup == nil {
		return nil, nil
	}
	var failed []string
	for _, n := range data.Node.BaseRepository.Object.StatusCheckRollup.Contexts.Nodes {
		switch n.Typename {
		case "CheckRun":
			switch n.Conclusion {
			case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
				failed = append(failed, n.Name)
			}
		case "StatusContext":
			if n.State == "FAILURE" || n.State == "ERROR" {
				failed = append(failed, n.Context)
			}
		}
	}
	sort.Strings(failed)
	return failed, nil
}

// MergeQueueOutcome is the pure classification of one queue observation.
type MergeQueueOutcome int

const (
	MergeQueuePending MergeQueueOutcome = iota
	MergeQueueMerged
	MergeQueueFailed
)

// ClassifyMergeQueue maps one observation to an outcome. absentPolls is the
// number of consecutive polls (including this one) that saw the PR OPEN and
// outside the queue; seenEntry is whether an entry was ever observed.
//
// An UNMERGEABLE entry is terminal only when the PR itself reads CONFLICTING
// (#2611). On a still-mergeable PR it means the entry is blocked behind an
// entry ahead (or the read is stale), so it stays pending and Wait handles it.
func ClassifyMergeQueue(st MergeQueueStatus, seenEntry bool, absentPolls, grace int) (MergeQueueOutcome, string) {
	switch st.State {
	case "MERGED":
		return MergeQueueMerged, ""
	case "CLOSED":
		return MergeQueueFailed, MergeQueueClosed
	}
	if st.EntryState == "UNMERGEABLE" {
		if st.Mergeable == "CONFLICTING" {
			return MergeQueueFailed, MergeQueueUnmergeable
		}
		return MergeQueuePending, ""
	}
	if st.InQueue {
		return MergeQueuePending, ""
	}
	if seenEntry || absentPolls >= grace {
		return MergeQueueFailed, MergeQueueDequeued
	}
	return MergeQueuePending, ""
}

// Wait polls the PR until it is MERGED (returns the final status) or the
// queue outcome is a failure / the budget expires (returns *MergeQueueError).
// initial may carry the status observed before the wait (its entry head is
// remembered); pass a zero value when there is none. Rate-limit errors are
// returned immediately; other read errors are retried until the budget ends.
//
// An entry last seen UNMERGEABLE on a non-conflicting PR is waited out
// (#2611): when GitHub removes it, a CONFLICTING PR fails with
// MergeQueueUnmergeable, a MERGEABLE PR is re-enqueued, and an UNKNOWN one
// stays pending. The deadline bounds the whole wait.
func (q *MergeQueue) Wait(ctx context.Context, prID string, initial MergeQueueStatus, opts MergeQueueWaitOptions) (MergeQueueStatus, error) {
	opts = opts.withDefaults().onQueuedOnce()
	deadline := q.now().Add(opts.Timeout)
	last := initial
	seenEntry := initial.InQueue
	if initial.InQueue && opts.OnQueued != nil {
		opts.OnQueued(initial)
	}
	headOID := initial.EntryHeadOID
	absent := 0
	blockedUnmergeable := false
	var lastErr error
	for {
		st, err := q.StatusByID(ctx, prID)
		if err != nil {
			if isRateLimited(err) || ctx.Err() != nil {
				return last, err
			}
			lastErr = err
		} else {
			lastErr = nil
			last = st
			if st.EntryHeadOID != "" {
				headOID = st.EntryHeadOID
			}
			if !st.InQueue && st.State == "OPEN" && blockedUnmergeable {
				switch st.Mergeable {
				case "CONFLICTING":
					return st, q.failure(ctx, prID, st.Number, MergeQueueUnmergeable, headOID, nil)
				case "MERGEABLE":
					if err := q.Enqueue(ctx, prID); err != nil {
						if isRateLimited(err) {
							return st, err
						}
						log.Printf("merge queue: re-enqueue of PR #%d failed, retrying next poll: %v", st.Number, err)
					} else {
						seenEntry = false
						absent = 0
						blockedUnmergeable = false
					}
				}
			} else {
				if st.InQueue {
					if opts.OnQueued != nil {
						opts.OnQueued(st)
					}
					seenEntry = true
					absent = 0
					blockedUnmergeable = st.EntryState == "UNMERGEABLE" && st.Mergeable != "CONFLICTING"
				} else if st.State == "OPEN" {
					absent++
				}
				outcome, kind := ClassifyMergeQueue(st, seenEntry, absent, opts.DequeueGrace)
				switch outcome {
				case MergeQueueMerged:
					return st, nil
				case MergeQueueFailed:
					return st, q.failure(ctx, prID, st.Number, kind, headOID, nil)
				}
			}
		}
		if !q.now().Add(opts.PollInterval).Before(deadline) {
			return last, q.failure(ctx, prID, last.Number, MergeQueueTimeout, headOID, lastErr)
		}
		if err := q.sleep(ctx, opts.PollInterval); err != nil {
			return last, &MergeQueueError{PRNumber: last.Number, Kind: MergeQueueTimeout, EntryHeadOID: headOID, Cause: err}
		}
	}
}

func (q *MergeQueue) failure(ctx context.Context, prID string, number int, kind, headOID string, cause error) error {
	e := &MergeQueueError{PRNumber: number, Kind: kind, EntryHeadOID: headOID, Cause: cause}
	if headOID != "" && kind != MergeQueueTimeout {
		failed, err := q.FailedMergeGroupChecks(ctx, prID, headOID)
		if err != nil {
			log.Printf("merge queue: could not read merge-group checks for PR #%d at %s: %v", number, shortOID(headOID), err)
		}
		e.FailedChecks = failed
	}
	return e
}

// rewordedReady re-reads a PR whose head was reworded to oid until the PR
// reports oid as its head, waits for it to be ready again and re-runs the
// squash guard once (#2988). GitHub syncs a PR's head after a ref move
// asynchronously; guarding or waiting on the stale head would retry a reword
// whose compare-and-swap must fail, or accept the old head's checks.
func (q *MergeQueue) rewordedReady(ctx context.Context, status MergeQueueStatus, oid string, opts MergeQueueWaitOptions) (MergeQueueStatus, error) {
	opts = opts.withDefaults()
	deadline := time.Now().Add(opts.ReadyTimeout)
	var cur MergeQueueStatus
	for {
		var err error
		cur, err = q.StatusByID(ctx, status.PRID)
		if err != nil {
			return status, fmt.Errorf("re-read PR #%d after reword: %w", status.Number, err)
		}
		if cur.InQueue || cur.State != "OPEN" || cur.HeadOID == oid {
			break
		}
		if !time.Now().Before(deadline) {
			return cur, &MergeQueueError{PRNumber: cur.Number, Kind: MergeQueueSquashDefaults,
				Cause: fmt.Errorf("PR head is %s, not reworded commit %s, after %s", cur.HeadOID, oid, opts.ReadyTimeout)}
		}
		if err := q.sleep(ctx, opts.ReadyPollInterval); err != nil {
			return cur, err
		}
	}
	if cur.InQueue || cur.State != "OPEN" {
		return cur, nil
	}
	ready, err := q.WaitReady(ctx, cur, opts)
	if err != nil || ready.InQueue || ready.State != "OPEN" {
		return ready, err
	}
	again, err := q.guardSquashDefaults(ctx, ready, opts)
	if err != nil {
		return ready, err
	}
	if again != "" {
		return ready, &MergeQueueError{PRNumber: ready.Number, Kind: MergeQueueSquashDefaults,
			Cause: fmt.Errorf("WIP commit still present after reword")}
	}
	return ready, nil
}

// EnqueueAndWait enqueues the PR when it is not already queued and waits for
// the outcome. status is the observation that established QueueRequired.
// Before enqueueing it waits for the PR to be ready (#2584): GitHub rejects an
// enqueue issued before mergeability is computed, and a PR whose checks are
// still running would only be enqueued to fail.
func (q *MergeQueue) EnqueueAndWait(ctx context.Context, status MergeQueueStatus, opts MergeQueueWaitOptions) (MergeQueueStatus, error) {
	if status.State == "MERGED" {
		return status, nil
	}
	opts = opts.onQueuedOnce()
	if !status.InQueue {
		ready, err := q.WaitReady(ctx, status, opts)
		if err != nil {
			return ready, err
		}
		status = ready
		if status.State == "MERGED" {
			return status, nil
		}
	}
	if !status.InQueue && status.State == "OPEN" {
		rewordedOID, err := q.guardSquashDefaults(ctx, status, opts)
		if err != nil {
			return status, err
		}
		if rewordedOID != "" {
			// The head moved to the reworded commit (#2988): wait for the PR to
			// report it and for its
			// checks, then guard again, which now passes through.
			if status, err = q.rewordedReady(ctx, status, rewordedOID, opts); err != nil || status.State == "MERGED" {
				return status, err
			}
			if status.InQueue || status.State != "OPEN" {
				return q.Wait(ctx, status.PRID, status, opts)
			}
		}
		if err := q.enqueueRetrying(ctx, status); err != nil {
			if strings.Contains(err.Error(), enqueueNotReadyMessage) {
				return status, &MergeQueueError{PRNumber: status.Number, Kind: MergeQueueNotReady, Detail: "mergeability not yet computed", Cause: err}
			}
			return status, err
		}
		if opts.OnQueued != nil {
			opts.OnQueued(status)
		}
	}
	return q.Wait(ctx, status.PRID, status, opts)
}
