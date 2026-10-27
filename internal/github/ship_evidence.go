package github

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shurcooL/graphql"
)

// Ship evidence (#2602): what an issue's timeline proves about whether its
// work merged. An epic closed as COMPLETED proves nothing about its sub-issues
// — any slice PR that says `Closes #<epic>` sets that state (#2595) — so the
// orphan-sub closer and the epic rollup read this instead.

// orphanCloseCommentPrefix starts every comment the orphan-sub closer writes,
// the comments it wrote before #2602 included. The rollup recognises an
// orphan close by it, so the writer and the reader share this one constant.
const orphanCloseCommentPrefix = "Auto-closed: this sub-issue's work shipped"

// orphanCloseMarkerPrefix opens the machine marker the orphan-sub closer adds
// since #2602, naming the evidence the close rested on. A legacy orphan close
// carries the comment prefix without it.
const orphanCloseMarkerPrefix = "<!-- nightgauge:orphan-close evidence="

// epicBranchPrefix is the head-branch prefix of an epic's integration branch,
// the same prefix git.Service.FindEpicBranch looks for.
const epicBranchPrefix = "epic/%d-"

// orphanCloseMarker renders the marker for an orphan close resting on kind
// ("epic-branch" or "pr") and pull request number pr.
func orphanCloseMarker(kind string, pr int) string {
	return fmt.Sprintf("%s%s:#%d -->", orphanCloseMarkerPrefix, kind, pr)
}

// ShipCloser is the pull request that most recently closed an issue.
type ShipCloser struct {
	Number  int    `json:"number"`
	HeadRef string `json:"headRef"`
	Merged  bool   `json:"merged"`
}

// ShipEvidence is what one issue's timeline proves about its work shipping.
type ShipEvidence struct {
	// MergedPRs are the merged pull requests that cross-reference, connect to,
	// or close the issue, ascending and deduplicated.
	MergedPRs []int `json:"mergedPRs,omitempty"`
	// ClosingPRs is the subset of MergedPRs that name the issue as closing:
	// a closing keyword, a manual link, or the PR that closed it. A plain
	// mention, such as a slice PR listing the sub-issues still to do, is not.
	ClosingPRs []int `json:"closingPRs,omitempty"`
	// Closer is the pull request on the issue's most recent close, nil when
	// that close had no pull request behind it (a manual or API close).
	Closer *ShipCloser `json:"closer,omitempty"`
	// OrphanClosedWithoutEvidence reports a comment from the orphan-sub closer
	// with no evidence marker on any comment: the issue was closed by the
	// pre-#2602 closer, on the epic's state alone.
	OrphanClosedWithoutEvidence bool `json:"orphanClosedWithoutEvidence,omitempty"`
}

// ClosedByEpicBranch reports whether ev's issue was closed by merging epic
// epicNumber's own integration branch (the #2560 path), and by which PR.
func ClosedByEpicBranch(ev *ShipEvidence, epicNumber int) (int, bool) {
	if ev == nil || ev.Closer == nil || !ev.Closer.Merged {
		return 0, false
	}
	if !strings.HasPrefix(ev.Closer.HeadRef, fmt.Sprintf(epicBranchPrefix, epicNumber)) {
		return 0, false
	}
	return ev.Closer.Number, true
}

// shipPR is the slice of a pull request the evidence reads.
type shipPR struct {
	Number      graphql.Int
	Merged      graphql.Boolean
	HeadRefName graphql.String
}

// shipPRRef is a timeline field that may hold a pull request.
type shipPRRef struct {
	Typename    graphql.String `graphql:"__typename"`
	PullRequest shipPR         `graphql:"... on PullRequest"`
}

type shipEvidenceQuery struct {
	Repository struct {
		Issue *struct {
			TimelineItems struct {
				Nodes []struct {
					Typename    graphql.String `graphql:"__typename"`
					ClosedEvent struct {
						Closer *shipPRRef
					} `graphql:"... on ClosedEvent"`
					CrossReferencedEvent struct {
						WillCloseTarget graphql.Boolean
						Source          *shipPRRef
					} `graphql:"... on CrossReferencedEvent"`
					ConnectedEvent struct {
						Subject *shipPRRef
					} `graphql:"... on ConnectedEvent"`
					IssueComment struct {
						Body graphql.String
					} `graphql:"... on IssueComment"`
				}
			} `graphql:"timelineItems(last: 100, itemTypes: [CLOSED_EVENT, CROSS_REFERENCED_EVENT, CONNECTED_EVENT, ISSUE_COMMENT])"`
			ClosedByPullRequestsReferences struct {
				Nodes []shipPR
			} `graphql:"closedByPullRequestsReferences(first: 20, includeClosedPrs: true)"`
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// ShipEvidence reads issue owner/repo#number's ship evidence in one query:
// its latest 100 close, cross-reference, connect and comment timeline items,
// and the pull requests that reference it as closing. Timeline items older
// than that window are not seen, which can only leave a sub-issue unclosed.
func (s *IssueService) ShipEvidence(ctx context.Context, owner, repo string, number int) (*ShipEvidence, error) {
	graphQLNumber, err := checkedGraphQLInt("issue number", number)
	if err != nil {
		return nil, err
	}
	var q shipEvidenceQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphQLNumber,
	}
	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("read ship evidence for %s/%s#%d: %w", owner, repo, number, err)
	}
	if q.Repository.Issue == nil {
		return nil, fmt.Errorf("read ship evidence for %s/%s#%d: issue not found", owner, repo, number)
	}
	qi := q.Repository.Issue

	ev := &ShipEvidence{}
	merged, closing := map[int]bool{}, map[int]bool{}
	addMerged := func(ref *shipPRRef, closes bool) {
		if ref != nil && ref.Typename == "PullRequest" && bool(ref.PullRequest.Merged) {
			merged[int(ref.PullRequest.Number)] = true
			if closes {
				closing[int(ref.PullRequest.Number)] = true
			}
		}
	}
	orphanComment, marker := false, false
	for _, n := range qi.TimelineItems.Nodes {
		switch n.Typename {
		case "ClosedEvent":
			// Nodes run oldest to newest, so the last close wins.
			ev.Closer = nil
			if c := n.ClosedEvent.Closer; c != nil && c.Typename == "PullRequest" {
				ev.Closer = &ShipCloser{
					Number:  int(c.PullRequest.Number),
					HeadRef: string(c.PullRequest.HeadRefName),
					Merged:  bool(c.PullRequest.Merged),
				}
			}
			addMerged(n.ClosedEvent.Closer, true)
		case "CrossReferencedEvent":
			addMerged(n.CrossReferencedEvent.Source, bool(n.CrossReferencedEvent.WillCloseTarget))
		case "ConnectedEvent":
			addMerged(n.ConnectedEvent.Subject, true)
		case "IssueComment":
			body := string(n.IssueComment.Body)
			if strings.HasPrefix(body, orphanCloseCommentPrefix) {
				orphanComment = true
			}
			if strings.Contains(body, orphanCloseMarkerPrefix) {
				marker = true
			}
		}
	}
	for _, pr := range qi.ClosedByPullRequestsReferences.Nodes {
		if bool(pr.Merged) {
			merged[int(pr.Number)] = true
			closing[int(pr.Number)] = true
		}
	}
	ev.MergedPRs, ev.ClosingPRs = sortedKeys(merged), sortedKeys(closing)
	ev.OrphanClosedWithoutEvidence = orphanComment && !marker
	return ev, nil
}

// sortedKeys returns m's keys in ascending order, nil when m is empty.
func sortedKeys(m map[int]bool) []int {
	var out []int
	for n := range m {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// ShippedBy returns the merged pull request that proves ev's issue shipped,
// ignoring a plain mention by pull request exclude. exclude is the PR that
// closed the parent epic: when that is not the epic's own branch, it is a
// slice PR, and its body may list sub-issues it did not implement (#2602).
// A closing reference counts even from exclude.
func ShippedBy(ev *ShipEvidence, exclude int) (int, bool) {
	if ev == nil {
		return 0, false
	}
	if n := len(ev.ClosingPRs); n > 0 {
		return ev.ClosingPRs[n-1], true
	}
	for i := len(ev.MergedPRs) - 1; i >= 0; i-- {
		if ev.MergedPRs[i] != exclude {
			return ev.MergedPRs[i], true
		}
	}
	return 0, false
}
