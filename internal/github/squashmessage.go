package github

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// WIPCommitTrailer is the trailer key a WIP-preservation commit carries
// (#2728). It mirrors wipCommitTrailer in internal/reclaim/wip.go and
// WIP_COMMIT_TRAILER in packages/nightgauge/src/utils/preserveWorkInProgress.ts;
// the three must not drift.
const WIPCommitTrailer = "Nightgauge-WIP"

// IsWIPPreservationMessage reports whether a commit message is a
// machine-authored WIP-preservation commit: one of its lines is the
// `Nightgauge-WIP:` trailer.
func IsWIPPreservationMessage(msg string) bool {
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), WIPCommitTrailer+":") {
			return true
		}
	}
	return false
}

// SquashCommitMessage builds the squash commit for a PR (#2728). The headline
// is always `<title> (#<number>)`, so a WIP-preservation commit can never
// become the subject on the base branch. The body follows GitHub's
// COMMIT_MESSAGES shape over the commits that are not WIP preservation: one
// remaining commit contributes its body, several contribute `* <headline>`
// plus body each, and none leaves the body empty. Any stray `Nightgauge-WIP:`
// line is removed last, so the trailer never reaches the base branch.
func SquashCommitMessage(title string, number int, commitMessages []string) (headline, body string) {
	headline = fmt.Sprintf("%s (#%d)", strings.TrimSpace(title), number)
	var kept []string
	for _, m := range commitMessages {
		if !IsWIPPreservationMessage(m) {
			kept = append(kept, strings.TrimSpace(m))
		}
	}
	switch len(kept) {
	case 0:
		body = ""
	case 1:
		_, rest, _ := strings.Cut(kept[0], "\n")
		body = rest
	default:
		parts := make([]string, 0, len(kept))
		for _, m := range kept {
			h, rest, _ := strings.Cut(m, "\n")
			entry := "* " + h
			if r := strings.TrimSpace(rest); r != "" {
				entry += "\n\n" + r
			}
			parts = append(parts, entry)
		}
		body = strings.Join(parts, "\n\n")
	}
	return headline, stripWIPTrailer(body)
}

func stripWIPTrailer(body string) string {
	lines := strings.Split(body, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), WIPCommitTrailer+":") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// PRSquashSource is what a squash message is built from: the PR's number and
// title, its base repository, and its commits (first page of 250 — pipeline
// PRs are far below it). The head and commit-graph fields let a sole WIP
// commit be reworded before an enqueue (#2988).
type PRSquashSource struct {
	Number   int
	Title    string
	Owner    string
	Repo     string
	Messages []string
	// RepoID is the base repository's node ID; HeadRefName and HeadRefOid
	// name the PR head; CrossRepository is true for a head in a fork.
	RepoID          string
	HeadRefName     string
	HeadRefOid      string
	CrossRepository bool
	Commits         []PRSquashCommit
}

// PRSquashCommit is one PR commit: its OID, tree, parents and message.
type PRSquashCommit struct {
	OID     string
	Tree    string
	Parents []string
	Message string
}

// HasWIPPreservation reports whether any commit on the PR is a
// WIP-preservation commit.
func (s PRSquashSource) HasWIPPreservation() bool {
	for _, m := range s.Messages {
		if IsWIPPreservationMessage(m) {
			return true
		}
	}
	return false
}

// SoleWIP returns the PR's only commit when it is a WIP-preservation commit
// that sits at the head with exactly one parent, the one shape the merge
// queue guard may reword (#2988). Mixed PRs and every other shape return false.
func (s PRSquashSource) SoleWIP() (PRSquashCommit, bool) {
	if len(s.Commits) != 1 {
		return PRSquashCommit{}, false
	}
	c := s.Commits[0]
	if !IsWIPPreservationMessage(c.Message) || c.OID == "" || c.OID != s.HeadRefOid ||
		c.Tree == "" || len(c.Parents) != 1 {
		return PRSquashCommit{}, false
	}
	return c, true
}

// RewordedWIPMessage is the message that replaces a sole WIP commit: the PR
// title, then the SquashMessage body (empty for a sole WIP PR), with no
// trailer. GitHub appends " (#N)" to a single commit's subject, so the queue
// lands it as "<title> (#N)" under COMMIT_OR_PR_TITLE.
func (s PRSquashSource) RewordedWIPMessage() string {
	_, body := s.SquashMessage()
	msg := strings.TrimSpace(s.Title)
	if body != "" {
		msg += "\n\n" + body
	}
	return msg
}

// SquashMessage is SquashCommitMessage over this source.
func (s PRSquashSource) SquashMessage() (headline, body string) {
	return SquashCommitMessage(s.Title, s.Number, s.Messages)
}

const prSquashSourceQuery = `query prSquashSource($id: ID!) {
  node(id: $id) {
    ... on PullRequest {
      number
      title
      headRefName
      headRefOid
      isCrossRepository
      baseRepository { id name owner { login } }
      commits(first: 250) { nodes { commit { oid message tree { oid } parents(first: 2) { nodes { oid } } } } }
    }
  }
}`

// SquashSource reads the PR's squash-message inputs by node ID.
func (q *MergeQueue) SquashSource(ctx context.Context, prID string) (PRSquashSource, error) {
	body, err := q.do(ctx, prSquashSourceQuery, map[string]any{"id": prID})
	if err != nil {
		return PRSquashSource{}, fmt.Errorf("squash source: %w", err)
	}
	var data struct {
		Node *struct {
			Number            int    `json:"number"`
			Title             string `json:"title"`
			HeadRefName       string `json:"headRefName"`
			HeadRefOid        string `json:"headRefOid"`
			IsCrossRepository bool   `json:"isCrossRepository"`
			BaseRepository    *struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Owner struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"baseRepository"`
			Commits struct {
				Nodes []struct {
					Commit struct {
						OID     string `json:"oid"`
						Message string `json:"message"`
						Tree    struct {
							OID string `json:"oid"`
						} `json:"tree"`
						Parents struct {
							Nodes []struct {
								OID string `json:"oid"`
							} `json:"nodes"`
						} `json:"parents"`
					} `json:"commit"`
				} `json:"nodes"`
			} `json:"commits"`
		} `json:"node"`
	}
	if err := decodeGraphQL(body, &data); err != nil {
		return PRSquashSource{}, fmt.Errorf("squash source: %w", err)
	}
	if data.Node == nil || data.Node.Number == 0 {
		return PRSquashSource{}, fmt.Errorf("squash source: pull request %s not found", prID)
	}
	n := data.Node
	src := PRSquashSource{Number: n.Number, Title: n.Title, HeadRefName: n.HeadRefName,
		HeadRefOid: n.HeadRefOid, CrossRepository: n.IsCrossRepository}
	if br := n.BaseRepository; br != nil {
		src.Owner, src.Repo, src.RepoID = br.Owner.Login, br.Name, br.ID
	}
	for _, c := range n.Commits.Nodes {
		src.Messages = append(src.Messages, c.Commit.Message)
		pc := PRSquashCommit{OID: c.Commit.OID, Tree: c.Commit.Tree.OID, Message: c.Commit.Message}
		for _, p := range c.Commit.Parents.Nodes {
			pc.Parents = append(pc.Parents, p.OID)
		}
		src.Commits = append(src.Commits, pc)
	}
	return src, nil
}

// updateRefCASMutation moves one ref only if it still points at beforeOid
// (#2988): a push that landed after the probe makes the swap fail.
const updateRefCASMutation = `mutation updateRefCAS($repo: ID!, $name: GitRefname!, $after: GitObjectID!, $before: GitObjectID!) {
  updateRefs(input: {repositoryId: $repo, refUpdates: [{name: $name, afterOid: $after, beforeOid: $before, force: true}]}) { clientMutationId }
}`

// rewordSoleWIP replaces the PR's sole WIP commit with one of the same tree
// and parent whose message is RewordedWIPMessage, and moves the head ref to it
// by compare-and-swap (#2988). It writes the PR branch only.
func (q *MergeQueue) rewordSoleWIP(ctx context.Context, src PRSquashSource, wip PRSquashCommit, opts MergeQueueWaitOptions) (string, error) {
	if opts.CreateCommit == nil {
		return "", fmt.Errorf("no commit writer configured to reword the WIP commit")
	}
	if src.CrossRepository {
		return "", fmt.Errorf("head branch is in a fork; the WIP commit cannot be reworded")
	}
	if src.RepoID == "" || src.HeadRefName == "" {
		return "", fmt.Errorf("head ref unknown; the WIP commit cannot be reworded")
	}
	oid, err := opts.CreateCommit(ctx, src.Owner, src.Repo, src.RewordedWIPMessage(), wip.Tree, wip.Parents)
	if err != nil {
		return "", fmt.Errorf("create reworded commit: %w", err)
	}
	if oid == "" {
		return "", fmt.Errorf("create reworded commit: no oid returned")
	}
	body, err := q.do(ctx, updateRefCASMutation, map[string]any{"repo": src.RepoID,
		"name": "refs/heads/" + src.HeadRefName, "after": oid, "before": wip.OID})
	if err == nil {
		err = decodeGraphQL(body, &struct{}{})
	}
	if err != nil {
		return "", fmt.Errorf("move %s from %s to %s: %w", src.HeadRefName, wip.OID, oid, err)
	}
	log.Printf("merge queue: PR #%d sole WIP commit %s reworded to %s", src.Number, wip.OID, oid)
	return oid, nil
}

// guardSquashDefaults runs before an enqueue (#2728). enqueuePullRequest takes
// no headline, so the queue builds the squash commit from the repository's
// squash defaults; opts.CheckSquashDefaults reads whether they are
// PR_TITLE/PR_BODY. When they are not (or cannot be read), a PR without a
// WIP-preservation commit is enqueued as before; a PR whose only commit is a
// WIP commit is reworded in place (#2988) and its new OID is returned so the
// caller waits for the PR to report it and for its checks; any other WIP-carrying PR (or one
// whose commits cannot be read) is refused, so the trailer never reaches the
// base branch. Repository settings are never changed here.
func (q *MergeQueue) guardSquashDefaults(ctx context.Context, status MergeQueueStatus, opts MergeQueueWaitOptions) (rewordedOID string, err error) {
	if opts.CheckSquashDefaults == nil {
		return "", nil
	}
	prTitled, err := opts.CheckSquashDefaults(ctx)
	if err == nil && prTitled {
		return "", nil
	}
	if err == nil {
		err = fmt.Errorf("squash defaults are not %s/%s", SquashTitlePRTitle, SquashMessagePRBody)
	}
	src, probeErr := q.SquashSource(ctx, status.PRID)
	if probeErr == nil && !src.HasWIPPreservation() {
		log.Printf("merge queue: PR #%d has no WIP commit, enqueueing: %v", status.Number, err)
		return "", nil
	}
	cause := err
	if probeErr != nil {
		cause = fmt.Errorf("%v; commit probe: %w", err, probeErr)
	} else if wip, ok := src.SoleWIP(); ok {
		oid, rerr := q.rewordSoleWIP(ctx, src, wip, opts)
		if rerr == nil {
			return oid, nil
		}
		cause = fmt.Errorf("%v; reword: %w", err, rerr)
	} else {
		cause = fmt.Errorf("%v; WIP commit is not the PR's sole head commit", err)
	}
	return "", &MergeQueueError{PRNumber: status.Number, Kind: MergeQueueSquashDefaults, Cause: cause}
}
