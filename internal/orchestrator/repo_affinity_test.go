package orchestrator

import (
	"context"
	"errors"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// twoRepoWorkspace is a workspace of o/r (the filed repo) and o/ext (a
// sibling). shared.go exists in both.
func twoRepoDeps(bodies map[int]string) affinityDeps {
	return affinityDeps{
		repoPaths: func() map[string]string { return map[string]string{"o/r": "/r", "o/ext": "/ext"} },
		body: func(_ context.Context, _, _ string, n int) (string, error) {
			b, ok := bodies[n]
			if !ok {
				return "", errors.New("forge down")
			}
			return b, nil
		},
		files: func(root string) (map[string]bool, error) {
			if root == "/r" {
				return map[string]bool{"internal/a.go": true, "docs/A.md": true, "shared.go": true, "cmd/x/shared.go": true}, nil
			}
			return map[string]bool{"src/ext/one.ts": true, "src/ext/two.ts": true, "lib/three.ts": true, "cmd/x/shared.go": true}, nil
		},
	}
}

func TestRepoAffinity(t *testing.T) {
	bodies := map[int]string{
		// AC1: every resolving path lives only in the sibling.
		1: "Fix `src/ext/one.ts` and src/ext/two.ts.\n## Acceptance criteria\n- [ ] lib/three.ts works",
		// AC2: own references dispatch.
		2: "Change `internal/a.go`.\n## Acceptance criteria\n- [ ] docs/A.md updated",
		// AC2 guard: a sibling path mentioned only as context.
		3: "Change internal/a.go.\n## Acceptance criteria\n- [ ] works\n## Notes\nlike src/ext/one.ts does",
		// AC3: acceptance criteria name both repos.
		4: "Change internal/a.go.\n## Acceptance criteria\n- [ ] internal/a.go done\n- [ ] src/ext/one.ts done",
		// AC4: reference-free and unresolved bodies dispatch.
		5: "Make the scheduler faster.",
		6: "Touch nowhere/missing.go and gone/x.ts",
		// A path tracked in both repos counts as own.
		7: "Fix cmd/x/shared.go and src/ext/one.ts",
		// A single sibling reference is not enough to call it misfiled.
		8: "Fix src/ext/one.ts",
	}
	cases := []struct {
		n      int
		action string
		owner  string
	}{
		{1, affinityMisfiled, "o/ext"},
		{2, affinityDispatch, ""},
		{3, affinityDispatch, ""},
		{4, affinityCrossRepo, "o/ext"},
		{5, affinityDispatch, ""},
		{6, affinityDispatch, ""},
		{7, affinityDispatch, ""},
		{8, affinityDispatch, ""},
		{99, affinityDispatch, ""}, // body read fails: fail open
	}
	for _, c := range cases {
		s := &Scheduler{affinityDeps: twoRepoDeps(bodies)}
		v := s.repoAffinity(context.Background(), "o/r", c.n, "")
		if v.Action != c.action {
			t.Errorf("#%d: action = %s (%s), want %s", c.n, v.Action, v.Reason, c.action)
			continue
		}
		if c.owner == "" {
			continue
		}
		found := false
		for _, r := range v.OwningRepos {
			found = found || r == c.owner
		}
		if !found {
			t.Errorf("#%d: owning repos %v do not name %s", c.n, v.OwningRepos, c.owner)
		}
	}
}

// AC4: a git failure fails open.
func TestRepoAffinity_FileSetErrorDispatches(t *testing.T) {
	deps := twoRepoDeps(map[int]string{1: "Fix src/ext/one.ts and src/ext/two.ts"})
	deps.files = func(string) (map[string]bool, error) { return nil, errors.New("not a git repo") }
	s := &Scheduler{affinityDeps: deps}
	if v := s.repoAffinity(context.Background(), "o/r", 1, ""); v.Refused() {
		t.Errorf("refused on a file-set error: %+v", v)
	}
}

// AC5: a single-repo workspace never reads the body.
func TestRepoAffinity_SingleRepoIsNoOp(t *testing.T) {
	s := &Scheduler{affinityDeps: affinityDeps{
		repoPaths: func() map[string]string { return map[string]string{"o/r": "/r"} },
		body: func(context.Context, string, string, int) (string, error) {
			t.Fatal("body read in a single-repo workspace")
			return "", nil
		},
	}}
	if v := s.repoAffinity(context.Background(), "o/r", 1, ""); v.Refused() {
		t.Errorf("refused in a single-repo workspace: %+v", v)
	}
}

// AC1 end to end: PickNext skips a misfiled Ready issue and returns the next.
func TestPickNext_SkipsMisfiledIssue(t *testing.T) {
	srv := readyItemsServer(t)
	defer srv.Close()
	client := gh.NewClientWithURL("test-token", srv.URL)
	gate, _ := levelsGate(map[string]trust.Level{"stranger": trust.LevelAdmin, "maintainer": trust.LevelAdmin})
	s := &Scheduler{
		client:      client,
		boardSvc:    gh.NewBoardService(client, "o", 1),
		repoRunning: map[string]int{},
		trustGate:   gate,
		affinityDeps: twoRepoDeps(map[int]string{
			1: "Fix src/ext/one.ts and src/ext/two.ts",
			2: "Fix internal/a.go",
		}),
	}
	item, err := s.PickNext(context.Background())
	if err != nil {
		t.Fatalf("PickNext: %v", err)
	}
	if item == nil || item.Number != 2 {
		t.Fatalf("PickNext = %+v, want #2 (#1 is misfiled)", item)
	}
}
