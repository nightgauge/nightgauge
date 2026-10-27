package github

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// shipEvidenceJSON renders a ShipEvidence GraphQL response from timeline
// nodes (oldest first) and the PRs closedByPullRequestsReferences lists.
func shipEvidenceJSON(nodes []string, closingRefs ...string) string {
	return fmt.Sprintf(`{"data":{"repository":{"issue":{
		"timelineItems":{"nodes":[%s]},
		"closedByPullRequestsReferences":{"nodes":[%s]}
	}}}}`, strings.Join(nodes, ","), strings.Join(closingRefs, ","))
}

// emptyShipEvidence is the evidence of an issue with no timeline at all.
var emptyShipEvidence = shipEvidenceJSON(nil)

// serveEmptyShipEvidence answers a ShipEvidence query with emptyShipEvidence
// and reports whether it did, for fakes that route on the request body.
func serveEmptyShipEvidence(w http.ResponseWriter, body []byte) bool {
	if !bytes.Contains(body, []byte("timelineItems(")) {
		return false
	}
	fmt.Fprint(w, emptyShipEvidence)
	return true
}

// closingRef is a closedByPullRequestsReferences node.
func closingRef(number int, merged bool) string {
	return fmt.Sprintf(`{"number":%d,"merged":%t,"headRefName":"feat/x"}`, number, merged)
}

func evPR(number int, merged bool, head string) string {
	return fmt.Sprintf(`{"__typename":"PullRequest","number":%d,"merged":%t,"headRefName":%q}`, number, merged, head)
}

// evClosed is a ClosedEvent; pr 0 is a close with no pull request behind it.
func evClosed(pr int, merged bool, head string) string {
	closer := "null"
	if pr > 0 {
		closer = evPR(pr, merged, head)
	}
	return fmt.Sprintf(`{"__typename":"ClosedEvent","closer":%s}`, closer)
}

func evCrossRef(pr int, merged, willClose bool) string {
	return fmt.Sprintf(`{"__typename":"CrossReferencedEvent","willCloseTarget":%t,"source":%s}`,
		willClose, evPR(pr, merged, "feat/x"))
}

func evConnected(pr int, merged bool) string {
	return fmt.Sprintf(`{"__typename":"ConnectedEvent","subject":%s}`, evPR(pr, merged, "feat/x"))
}

func evComment(body string) string {
	return fmt.Sprintf(`{"__typename":"IssueComment","body":%q}`, body)
}

// legacyOrphanComment is the comment the pre-#2602 orphan closer wrote.
const legacyOrphanComment = "Auto-closed: this sub-issue's work shipped as part of epic #100, which merged and closed as completed. " +
	"Closing the orphaned sub-issue so the autonomous picker does not re-spawn conflicting work. Reopen if scope remains."

func readShipEvidence(t *testing.T, resp string) *ShipEvidence {
	t.Helper()
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()
	ev, err := NewIssueService(client).ShipEvidence(context.Background(), "o", "r", 200)
	if err != nil {
		t.Fatalf("ShipEvidence: %v", err)
	}
	return ev
}

func TestShipEvidence_DecodesTimeline(t *testing.T) {
	ev := readShipEvidence(t, shipEvidenceJSON([]string{
		evComment("unrelated"),
		evCrossRef(10, true, false),    // a merged mention
		evCrossRef(11, false, true),    // an unmerged closing reference
		evConnected(12, true),          // a merged manual link
		evClosed(0, false, ""),         // an earlier manual close
		evClosed(13, true, "epic/7-x"), // the latest close, by a merged PR
	}, closingRef(14, true), closingRef(15, false)))

	if want := []int{10, 12, 13, 14}; !slices.Equal(ev.MergedPRs, want) {
		t.Errorf("MergedPRs = %v, want %v", ev.MergedPRs, want)
	}
	if want := []int{12, 13, 14}; !slices.Equal(ev.ClosingPRs, want) {
		t.Errorf("ClosingPRs = %v, want %v: a plain mention is not a closing reference", ev.ClosingPRs, want)
	}
	if ev.Closer == nil || ev.Closer.Number != 13 || ev.Closer.HeadRef != "epic/7-x" || !ev.Closer.Merged {
		t.Errorf("Closer = %+v, want merged #13 from epic/7-x", ev.Closer)
	}
	if ev.OrphanClosedWithoutEvidence {
		t.Error("OrphanClosedWithoutEvidence = true with no orphan-closer comment")
	}
}

func TestShipEvidence_LatestCloseWins(t *testing.T) {
	// Closed by a PR, reopened, then closed by hand: the PR no longer explains
	// the issue's current state.
	ev := readShipEvidence(t, shipEvidenceJSON([]string{
		evClosed(13, true, "feat/a"),
		evClosed(0, false, ""),
	}))
	if ev.Closer != nil {
		t.Errorf("Closer = %+v, want nil: the latest close had no PR", ev.Closer)
	}
}

func TestShipEvidence_OrphanCloseComments(t *testing.T) {
	tests := []struct {
		name     string
		comments []string
		want     bool
	}{
		{"no comments", nil, false},
		{"legacy orphan close", []string{legacyOrphanComment}, true},
		{"evidenced orphan close", []string{orphanCloseComment(100, "pr", 9)}, false},
		{"legacy then evidenced", []string{legacyOrphanComment, orphanCloseComment(100, "epic-branch", 9)}, false},
		{"prefix quoted mid-comment", []string{"> " + legacyOrphanComment}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nodes []string
			for _, c := range tt.comments {
				nodes = append(nodes, evComment(c))
			}
			if got := readShipEvidence(t, shipEvidenceJSON(nodes)).OrphanClosedWithoutEvidence; got != tt.want {
				t.Errorf("OrphanClosedWithoutEvidence = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShipEvidence_IssueNotFound(t *testing.T) {
	client, cleanup := mockGraphQLServer(t, `{"data":{"repository":{"issue":null}}}`)
	defer cleanup()
	if _, err := NewIssueService(client).ShipEvidence(context.Background(), "o", "r", 200); err == nil {
		t.Fatal("ShipEvidence returned no error for a missing issue")
	}
}

func TestClosedByEpicBranch(t *testing.T) {
	tests := []struct {
		name   string
		closer *ShipCloser
		wantPR int
		wantOK bool
	}{
		{"no closer", nil, 0, false},
		{"merged epic branch", &ShipCloser{Number: 9, HeadRef: "epic/100-thing", Merged: true}, 9, true},
		{"slice PR", &ShipCloser{Number: 9, HeadRef: "feat/123-slice", Merged: true}, 0, false},
		{"another epic's branch", &ShipCloser{Number: 9, HeadRef: "epic/1000-thing", Merged: true}, 0, false},
		{"unmerged epic branch", &ShipCloser{Number: 9, HeadRef: "epic/100-thing"}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr, ok := ClosedByEpicBranch(&ShipEvidence{Closer: tt.closer}, 100)
			if pr != tt.wantPR || ok != tt.wantOK {
				t.Errorf("ClosedByEpicBranch = (%d, %v), want (%d, %v)", pr, ok, tt.wantPR, tt.wantOK)
			}
		})
	}
}

func TestShippedBy(t *testing.T) {
	tests := []struct {
		name    string
		ev      *ShipEvidence
		exclude int
		wantPR  int
		wantOK  bool
	}{
		{"nil evidence", nil, 0, 0, false},
		{"nothing merged", &ShipEvidence{}, 0, 0, false},
		{"merged mention", &ShipEvidence{MergedPRs: []int{5}}, 0, 5, true},
		{"only a mention by the epic's slice PR", &ShipEvidence{MergedPRs: []int{5}}, 5, 0, false},
		{"another mention besides the slice PR", &ShipEvidence{MergedPRs: []int{4, 5}}, 5, 4, true},
		{"closing reference by the slice PR", &ShipEvidence{MergedPRs: []int{5}, ClosingPRs: []int{5}}, 5, 5, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr, ok := ShippedBy(tt.ev, tt.exclude)
			if pr != tt.wantPR || ok != tt.wantOK {
				t.Errorf("ShippedBy = (%d, %v), want (%d, %v)", pr, ok, tt.wantPR, tt.wantOK)
			}
		})
	}
}
