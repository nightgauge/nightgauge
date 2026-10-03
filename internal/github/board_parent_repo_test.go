package github

import (
	"context"
	"strings"
	"testing"
)

// Both board readers record the parent epic's repository with its number
// (#2350): a number alone names the right issue only when the epic lives in
// the item's own repository, and the epic cascade keyed on it read the
// sub-issue's own same-numbered issue for every cross-repository epic.
func TestBoardScan_ParentCarriesItsRepository(t *testing.T) {
	var node projectItemNode
	node.Content.TypeName = "Issue"
	node.Content.IssueFields.Number = 21
	node.Content.IssueFields.Repository.NameWithOwner = "acme/web"
	node.Content.IssueFields.Parent.Number = 20
	node.Content.IssueFields.Parent.Title = "the epic"
	node.Content.IssueFields.Parent.Repository.NameWithOwner = "acme/platform"

	item := (&BoardService{}).nodeToItem(node, AllRelations)
	if item == nil {
		t.Fatal("nodeToItem returned nil for an Issue")
	}
	if item.ParentNumber != 20 || item.ParentRepo != "acme/platform" || item.ParentTitle != "the epic" {
		t.Errorf("parent = %d %q in %q, want 20 %q in acme/platform", item.ParentNumber, item.ParentTitle, item.ParentRepo, "the epic")
	}
}

func TestRESTBoard_ParentCarriesItsRepository(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/orgs/acme/projectsV2/3/fields"] = restBoardFields
	// The Parent issue field's value is the parent's REST issue object.
	item := strings.Replace(restIssueItem("PVTI_1", 21, "Ready", nil, 0, 0, 0, 20),
		`{"number":20,"title":"the epic"}`,
		`{"number":20,"title":"the epic","url":"https://api.github.com/repos/acme/platform/issues/20",`+
			`"repository_url":"https://api.github.com/repos/acme/platform","html_url":"https://github.com/acme/platform/issues/20"}`, 1)
	f.bodies["/orgs/acme/projectsV2/3/items"] = "[" + item + "]"
	b := NewBoardService(restClient(f, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg)

	items, _, err := b.ListOpenItemsSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if got := items[0]; got.Repo != "acme/web" || got.ParentNumber != 20 || got.ParentRepo != "acme/platform" {
		t.Errorf("item %s#%d parent = %d in %q, want 20 in acme/platform", got.Repo, got.Number, got.ParentNumber, got.ParentRepo)
	}
}
