package github

import (
	"testing"

	"github.com/shurcooL/graphql"
)

func TestRefsFromOrgQuery(t *testing.T) {
	q := projectLinkedReposQuery{}
	q.Organization.ProjectV2.Repositories.Nodes = []struct {
		Name  graphql.String
		Owner struct {
			Login graphql.String
		}
	}{
		{Name: "nightgauge", Owner: struct{ Login graphql.String }{Login: "nightgauge"}},
		{Name: "acme-platform", Owner: struct{ Login graphql.String }{Login: "nightgauge"}},
	}

	refs := refsFromOrgQuery(q)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].Owner != "nightgauge" || refs[0].Name != "nightgauge" {
		t.Errorf("unexpected refs[0]: %+v", refs[0])
	}
	if refs[1].Name != "acme-platform" {
		t.Errorf("unexpected refs[1]: %+v", refs[1])
	}
}

func TestRefsFromUserQuery(t *testing.T) {
	q := userProjectLinkedReposQuery{}
	q.User.ProjectV2.Repositories.Nodes = []struct {
		Name  graphql.String
		Owner struct {
			Login graphql.String
		}
	}{
		{Name: "my-repo", Owner: struct{ Login graphql.String }{Login: "alice"}},
	}

	refs := refsFromUserQuery(q)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Owner != "alice" || refs[0].Name != "my-repo" {
		t.Errorf("unexpected ref: %+v", refs[0])
	}
}

func TestRefsFromOrgQuery_Empty(t *testing.T) {
	q := projectLinkedReposQuery{}
	refs := refsFromOrgQuery(q)
	if len(refs) != 0 {
		t.Fatalf("expected 0 refs for empty query, got %d", len(refs))
	}
}

func TestLinkedProjectsFromQuery(t *testing.T) {
	q := repositoryLinkedProjectsQuery{}
	q.Repository.ProjectsV2.Nodes = []struct {
		ID     graphql.ID
		Number graphql.Int
		Title  graphql.String
	}{
		{ID: "PVT_8", Number: 8, Title: "Community Roadmap"},
		{ID: "PVT_9", Number: 9, Title: "Engineering"},
	}
	refs := linkedProjectsFromQuery(q, "nightgauge")
	if len(refs) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(refs))
	}
	if refs[0].ID != "PVT_8" || refs[0].Owner != "nightgauge" || refs[0].Number != 8 {
		t.Fatalf("unexpected first project: %+v", refs[0])
	}
}
