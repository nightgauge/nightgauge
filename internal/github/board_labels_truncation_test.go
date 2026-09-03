package github

import (
	"testing"

	"github.com/shurcooL/graphql"
)

// labelNodes builds n label nodes named l0..l(n-1), none of them a
// human-only label — the visible set must look harmless for these tests to
// prove anything.
func labelNodes(n int) []labelNode {
	out := make([]labelNode, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, labelNode{Name: graphql.String("l" + string(rune('0'+i)))})
	}
	return out
}

// TestBoardScan_TruncatedLabelsMarkItem is the #998 guard: a board item whose
// labels connection reports more labels than the page returned must come back
// flagged truncated, for issues and PRs alike, and an item whose page held
// every label must not. Before the fix the connection selected no totalCount,
// so this could not be expressed at all — the short slice became item.Labels
// verbatim and every label-derived decision downstream read it as complete.
func TestBoardScan_TruncatedLabelsMarkItem(t *testing.T) {
	b := &BoardService{}

	t.Run("issue with 9 labels on an 8-label page", func(t *testing.T) {
		var node projectItemNode
		node.Content.TypeName = "Issue"
		node.Content.IssueFields.Number = 998
		node.Content.IssueFields.Labels.TotalCount = 9
		node.Content.IssueFields.Labels.Nodes = labelNodes(8)

		item := b.nodeToItem(node)
		if item == nil {
			t.Fatal("nodeToItem returned nil for an Issue")
		}
		if !item.LabelsTruncated {
			t.Fatalf("item with totalCount=9 and 8 nodes not flagged LabelsTruncated; Labels=%v", item.Labels)
		}
		if len(item.Labels) != 8 {
			t.Errorf("visible labels = %d, want the 8 that were returned", len(item.Labels))
		}
	})

	t.Run("PR with 9 labels on an 8-label page", func(t *testing.T) {
		var node projectItemNode
		node.Content.TypeName = "PullRequest"
		node.Content.PRFields.Number = 999
		node.Content.PRFields.Labels.TotalCount = 9
		node.Content.PRFields.Labels.Nodes = labelNodes(8)

		item := b.nodeToItem(node)
		if item == nil {
			t.Fatal("nodeToItem returned nil for a PullRequest")
		}
		if !item.LabelsTruncated {
			t.Fatal("PR item with totalCount=9 and 8 nodes not flagged LabelsTruncated")
		}
	})

	t.Run("issue whose labels all fit is not flagged", func(t *testing.T) {
		var node projectItemNode
		node.Content.TypeName = "Issue"
		node.Content.IssueFields.Labels.TotalCount = 3
		node.Content.IssueFields.Labels.Nodes = labelNodes(3)

		item := b.nodeToItem(node)
		if item.LabelsTruncated {
			t.Fatal("complete label page flagged as truncated")
		}
	})
}

// TestLabelPage_Truncated pins the one helper both scans share: truncation
// means "the connection holds more than the page returned", nothing else.
func TestLabelPage_Truncated(t *testing.T) {
	cases := []struct {
		total, nodes int
		want         bool
	}{
		{0, 0, false},
		{8, 8, false},
		{9, 8, true},
		{20, 8, true},
	}
	for _, c := range cases {
		p := labelPage{TotalCount: graphql.Int(c.total), Nodes: labelNodes(c.nodes)}
		if got := p.truncated(); got != c.want {
			t.Errorf("labelPage{total=%d,nodes=%d}.truncated() = %v, want %v", c.total, c.nodes, got, c.want)
		}
		if len(p.names()) != c.nodes {
			t.Errorf("labelPage{nodes=%d}.names() returned %d names", c.nodes, len(p.names()))
		}
	}
}
