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

// MergeQueueWaitOptions bounds WaitForMergeQueue. Zero fields take defaults.
type MergeQueueWaitOptions struct {
	Timeout      time.Duration
	PollInterval time.Duration
	DequeueGrace int
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
}

// Merge-queue failure kinds carried on MergeQueueError.
const (
	MergeQueueDequeued    = "dequeued"
	MergeQueueUnmergeable = "unmergeable"
	MergeQueueClosed      = "closed"
	MergeQueueTimeout     = "timeout"
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
}

func (e *MergeQueueError) Error() string {
	var b strings.Builder
	switch e.Kind {
	case MergeQueueTimeout:
		fmt.Fprintf(&b, "merge queue: PR #%d not merged before the queue wait budget expired", e.PRNumber)
	case MergeQueueClosed:
		fmt.Fprintf(&b, "merge queue: PR #%d was closed while queued", e.PRNumber)
	case MergeQueueUnmergeable:
		fmt.Fprintf(&b, "merge queue: PR #%d merge group is unmergeable", e.PRNumber)
	default:
		fmt.Fprintf(&b, "merge queue: PR #%d was removed from the queue", e.PRNumber)
	}
	if e.EntryHeadOID != "" {
		fmt.Fprintf(&b, " (merge-group commit %s)", shortOID(e.EntryHeadOID))
	}
	if len(e.FailedChecks) > 0 {
		fmt.Fprintf(&b, "; failed merge-group checks: %s", strings.Join(e.FailedChecks, ", "))
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
	MergeCommit         *struct {
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
func ClassifyMergeQueue(st MergeQueueStatus, seenEntry bool, absentPolls, grace int) (MergeQueueOutcome, string) {
	switch st.State {
	case "MERGED":
		return MergeQueueMerged, ""
	case "CLOSED":
		return MergeQueueFailed, MergeQueueClosed
	}
	if st.EntryState == "UNMERGEABLE" {
		return MergeQueueFailed, MergeQueueUnmergeable
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
func (q *MergeQueue) Wait(ctx context.Context, prID string, initial MergeQueueStatus, opts MergeQueueWaitOptions) (MergeQueueStatus, error) {
	opts = opts.withDefaults()
	deadline := q.now().Add(opts.Timeout)
	last := initial
	seenEntry := initial.InQueue
	headOID := initial.EntryHeadOID
	absent := 0
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
			if st.InQueue {
				seenEntry = true
				absent = 0
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

// EnqueueAndWait enqueues the PR when it is not already queued and waits for
// the outcome. status is the observation that established QueueRequired.
func (q *MergeQueue) EnqueueAndWait(ctx context.Context, status MergeQueueStatus, opts MergeQueueWaitOptions) (MergeQueueStatus, error) {
	if status.State == "MERGED" {
		return status, nil
	}
	if !status.InQueue {
		if err := q.Enqueue(ctx, status.PRID); err != nil {
			return status, err
		}
	}
	return q.Wait(ctx, status.PRID, status, opts)
}
