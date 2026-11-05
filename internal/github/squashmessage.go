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
// title, its base repository, and its commit messages (first page of 250 —
// pipeline PRs are far below it).
type PRSquashSource struct {
	Number   int
	Title    string
	Owner    string
	Repo     string
	Messages []string
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

// SquashMessage is SquashCommitMessage over this source.
func (s PRSquashSource) SquashMessage() (headline, body string) {
	return SquashCommitMessage(s.Title, s.Number, s.Messages)
}

const prSquashSourceQuery = `query prSquashSource($id: ID!) {
  node(id: $id) {
    ... on PullRequest {
      number
      title
      baseRepository { name owner { login } }
      commits(first: 250) { nodes { commit { message } } }
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
			Number         int    `json:"number"`
			Title          string `json:"title"`
			BaseRepository *struct {
				Name  string `json:"name"`
				Owner struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"baseRepository"`
			Commits struct {
				Nodes []struct {
					Commit struct {
						Message string `json:"message"`
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
	src := PRSquashSource{Number: data.Node.Number, Title: data.Node.Title}
	if br := data.Node.BaseRepository; br != nil {
		src.Owner, src.Repo = br.Owner.Login, br.Name
	}
	for _, n := range data.Node.Commits.Nodes {
		src.Messages = append(src.Messages, n.Commit.Message)
	}
	return src, nil
}

// guardSquashDefaults runs before an enqueue (#2728). enqueuePullRequest takes
// no headline, so the queue builds the squash commit from the repository's
// squash defaults; opts.CheckSquashDefaults reads whether they are
// PR_TITLE/PR_BODY. When they are not (or cannot be read), a PR carrying a
// WIP-preservation commit (or one whose commits cannot be read) is refused, so
// the trailer never reaches the base branch; any other PR is enqueued as
// before. Repository settings are never changed here.
func (q *MergeQueue) guardSquashDefaults(ctx context.Context, status MergeQueueStatus, opts MergeQueueWaitOptions) error {
	if opts.CheckSquashDefaults == nil {
		return nil
	}
	prTitled, err := opts.CheckSquashDefaults(ctx)
	if err == nil && prTitled {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("squash defaults are not %s/%s", SquashTitlePRTitle, SquashMessagePRBody)
	}
	src, probeErr := q.SquashSource(ctx, status.PRID)
	if probeErr == nil && !src.HasWIPPreservation() {
		log.Printf("merge queue: PR #%d has no WIP commit, enqueueing: %v", status.Number, err)
		return nil
	}
	cause := err
	if probeErr != nil {
		cause = fmt.Errorf("%v; commit probe: %w", err, probeErr)
	}
	return &MergeQueueError{PRNumber: status.Number, Kind: MergeQueueSquashDefaults, Cause: cause}
}
