package hooks

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// Shared by remainderComment (the writer) and ParsePartialDeliveryComment (the
// reader), so the two cannot drift apart (#3089).
const (
	partialDeliveryMarkerFormat = "<!-- nightgauge:partial-delivery pr=%d -->"
	// RemainingCriteriaHeading introduces the bulleted remainder list.
	RemainingCriteriaHeading = "Remaining acceptance criteria:"
)

var partialDeliveryMarkerRE = regexp.MustCompile(`<!-- nightgauge:partial-delivery pr=(\d+) -->`)

// PartialDeliveryReader is what PendingPartialDelivery reads: the issue's
// comments through the trust filter, and the newest merged PR that closes it.
// *github.IssueService plus a trust.Gate satisfy it (see NewPartialDeliveryReader).
type PartialDeliveryReader interface {
	TrustedComments(ctx context.Context, owner, repo string, number int) ([]trust.Comment, error)
	LatestClosingMerge(ctx context.Context, owner, repo string, number int) (*gh.ClosingMerge, error)
}

// PartialDeliveryStatus is the answer to "does this issue carry a recorded
// partial-delivery remainder that nothing merged since has closed?" (#3089).
type PartialDeliveryStatus struct {
	// Pending is true when the newest trusted marker comment is not followed
	// by a merged PR that closes the issue.
	Pending bool `json:"pending"`
	// PR is the partial delivery the newest marker names (0 when none).
	PR int `json:"pr,omitempty"`
	// Criteria are the remaining criteria that comment listed, as written.
	Criteria []string `json:"criteria"`
	// Unlisted is true when the comment listed no criteria: the remainder is
	// unknown, so it is not proven met.
	Unlisted bool `json:"unlisted,omitempty"`
	// CommentAt is when the newest marker comment was posted.
	CommentAt *time.Time `json:"commentAt,omitempty"`
	// ClosedBy is the closing PR merged after the comment (0 when none).
	ClosedBy int `json:"closedBy,omitempty"`
}

// ParsePartialDeliveryComment extracts the PR a remainder comment names and the
// criteria it listed. ok is false when body carries no marker.
func ParsePartialDeliveryComment(body string) (pr int, criteria []string, ok bool) {
	m := partialDeliveryMarkerRE.FindStringSubmatch(body)
	if m == nil {
		return 0, nil, false
	}
	pr, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, nil, false
	}
	idx := strings.Index(body, RemainingCriteriaHeading)
	if idx < 0 {
		return pr, nil, true
	}
	for _, line := range strings.Split(body[idx+len(RemainingCriteriaHeading):], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			if c := strings.TrimSpace(strings.TrimPrefix(line, "- ")); c != "" {
				criteria = append(criteria, c)
			}
		}
	}
	return pr, criteria, true
}

// PendingPartialDelivery finds the newest trusted partial-delivery marker on
// owner/repo#number and decides whether its remainder still stands: only a PR
// merged AFTER that comment that closes the issue retires it. Any read error
// is returned, so callers fail closed.
func PendingPartialDelivery(ctx context.Context, r PartialDeliveryReader, owner, repo string, number int) (PartialDeliveryStatus, error) {
	status := PartialDeliveryStatus{Criteria: []string{}}
	comments, err := r.TrustedComments(ctx, owner, repo, number)
	if err != nil {
		return status, fmt.Errorf("read comments of %s/%s#%d: %w", owner, repo, number, err)
	}
	var newest *trust.Comment
	var newestPR int
	var newestCriteria []string
	for i := range comments {
		pr, criteria, ok := ParsePartialDeliveryComment(comments[i].Body)
		if !ok {
			continue
		}
		if newest == nil || !comments[i].CreatedAt.Before(newest.CreatedAt) {
			newest, newestPR, newestCriteria = &comments[i], pr, criteria
		}
	}
	if newest == nil {
		return status, nil
	}
	at := newest.CreatedAt
	status.PR = newestPR
	status.CommentAt = &at
	if len(newestCriteria) > 0 {
		status.Criteria = newestCriteria
	} else {
		status.Unlisted = true
	}
	merge, err := r.LatestClosingMerge(ctx, owner, repo, number)
	if err != nil {
		return status, fmt.Errorf("read closing merges of %s/%s#%d: %w", owner, repo, number, err)
	}
	if merge != nil && merge.MergedAt.After(at) {
		status.ClosedBy = merge.Number
		return status, nil
	}
	status.Pending = true
	return status, nil
}

// partialDeliveryReader joins a trust gate (comments) with an issue service
// (closing merges).
type partialDeliveryReader struct {
	gate   *trust.Gate
	issues *gh.IssueService
}

func (p partialDeliveryReader) TrustedComments(ctx context.Context, owner, repo string, number int) ([]trust.Comment, error) {
	return p.gate.TrustedComments(ctx, owner, repo, number)
}

func (p partialDeliveryReader) LatestClosingMerge(ctx context.Context, owner, repo string, number int) (*gh.ClosingMerge, error) {
	return p.issues.LatestClosingMerge(ctx, owner, repo, number)
}

// NewPartialDeliveryReader is the production reader: comments through a trust
// gate trusting writers and trustedBots, merges through the issue service.
func NewPartialDeliveryReader(client *gh.Client, trustedBots []string) PartialDeliveryReader {
	return partialDeliveryReader{
		gate:   trust.NewGate(gh.NewTrustForge(client), trustedBots),
		issues: gh.NewIssueService(client),
	}
}
