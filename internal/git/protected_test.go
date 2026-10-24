package git

import (
	"reflect"
	"strings"
	"testing"
)

func TestProtectedBranches(t *testing.T) {
	cases := []struct {
		base string
		want []string
	}{
		{"", []string{"main", "master"}},
		{"main", []string{"main", "master"}},
		{"develop", []string{"main", "master", "develop"}},
	}
	for _, c := range cases {
		if got := ProtectedBranches(c.base); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ProtectedBranches(%q) = %v, want %v", c.base, got, c.want)
		}
	}
}

func TestRefuseBranchMutation(t *testing.T) {
	r := setupLiveRunRepo(t)
	svc := r.service(t, r.primary)

	refused := []struct{ name, branch, base, cur, frag string }{
		{"trunk", "main", "", "", "protected"},
		{"master literal", "master", "", "", "protected"},
		{"base branch", "develop", "develop", "", "protected"},
		{"current branch", "feat/123-stale", "", "feat/123-stale", "current branch"},
		{"empty", "", "", "", "empty"},
	}
	for _, c := range refused {
		err := svc.RefuseBranchMutation(c.branch, c.base, c.cur)
		if err == nil || !strings.Contains(err.Error(), c.frag) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.frag)
		}
	}

	if err := svc.RefuseBranchMutation("feat/123-stale", "main", "feat/999-live"); err != nil {
		t.Errorf("ordinary feature branch refused: %v", err)
	}
}
