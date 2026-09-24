package github

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"testing"
)

func TestWorkflowCanRunOnPush(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       bool
		wantErr    bool
	}{
		{"scalar push", "on: push\n", true, false},
		{"scalar pull_request", "on: pull_request\n", false, false},
		{"list with push", "on: [pull_request, push]\n", true, false},
		{"list without push", "on:\n  - pull_request\n  - workflow_dispatch\n", false, false},
		{"map, bare push", "on:\n  push:\n  pull_request:\n", true, false},
		{"map, push to main", "on:\n  push:\n    branches: [main]\n", true, false},
		{"map, push to another branch", "on:\n  push:\n    branches: [release]\n", false, false},
		{"map, push glob matches", "on:\n  push:\n    branches: ['ma*']\n", true, false},
		{"map, push double-star matches", "on:\n  push:\n    branches: ['**']\n", true, false},
		{"map, push negated after a match", "on:\n  push:\n    branches: ['**', '!main']\n", false, false},
		{"map, unmodelled pattern may run", "on:\n  push:\n    branches: ['ma[i]n']\n", true, false},
		{"map, unmodelled negation does not exclude", "on:\n  push:\n    branches: ['**', '!ma?n']\n", true, false},
		{"map, branches-ignore main", "on:\n  push:\n    branches-ignore: [main]\n", false, false},
		{"map, branches-ignore other", "on:\n  push:\n    branches-ignore: ['dependabot/**']\n", true, false},
		{"map, tags only", "on:\n  push:\n    tags: ['v*']\n", false, false},
		{"map, tags and branches", "on:\n  push:\n    tags: ['v*']\n    branches: [main]\n", true, false},
		{"map, paths filter may run", "on:\n  push:\n    branches: [main]\n    paths: ['docs/**']\n", true, false},
		{"map, schedule and dispatch only", "on:\n  schedule:\n    - cron: '0 7 * * *'\n  workflow_dispatch:\n", false, false},
		{"quoted on key", "\"on\":\n  push:\n", true, false},
		{"no on key", "name: x\njobs: {}\n", false, true},
		{"unparsable", "on: [push\n", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WorkflowCanRunOnPush([]byte(tc.yaml), "main")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("can run = %v, want %v", got, tc.want)
			}
		})
	}
}

func contentPage(yaml string) string {
	return `{"encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte(yaml)) + `"}`
}

func TestPushWorkflowsCanRun(t *testing.T) {
	const dir = "/repos/o/r/contents/.github/workflows"
	listing := `[{"name":"ci.yml","path":".github/workflows/ci.yml","type":"file"},{"name":"README.md","path":".github/workflows/README.md","type":"file"},{"name":"cache-warm.yml","path":".github/workflows/cache-warm.yml","type":"file"}]`
	run := func(pages map[string][]string) (bool, error) {
		srv := httptest.NewServer(PagedFixture{T: t, Pages: pages})
		defer srv.Close()
		return newCIServiceForRESTTest(srv).PushWorkflowsCanRun(context.Background(), "o", "r", "abc1234", "main")
	}

	can, err := run(map[string][]string{dir: {listing},
		"/repos/o/r/contents/.github/workflows/ci.yml":         {contentPage("on: [pull_request, workflow_dispatch]\n")},
		"/repos/o/r/contents/.github/workflows/cache-warm.yml": {contentPage("on:\n  push:\n    tags: ['v*']\n")},
	})
	if err != nil || can {
		t.Errorf("PR-only and tag-only workflows: can = %v, err = %v; want false, nil", can, err)
	}

	can, err = run(map[string][]string{dir: {listing},
		"/repos/o/r/contents/.github/workflows/ci.yml":         {contentPage("on: pull_request\n")},
		"/repos/o/r/contents/.github/workflows/cache-warm.yml": {contentPage("on:\n  push:\n    branches: [main]\n")},
	})
	if err != nil || !can {
		t.Errorf("a push-to-main deploy: can = %v, err = %v; want true, nil", can, err)
	}

	if can, err = run(map[string][]string{}); err != nil || can {
		t.Errorf("no workflows directory: can = %v, err = %v; want false, nil", can, err)
	}

	// A listed file that cannot be read is unknown, never "no".
	if _, err = run(map[string][]string{dir: {listing},
		"/repos/o/r/contents/.github/workflows/ci.yml": {contentPage("on: pull_request\n")},
	}); err == nil {
		t.Error("an unreadable workflow file: err = nil, want an error")
	}
	// So is one that does not parse.
	if _, err = run(map[string][]string{dir: {listing},
		"/repos/o/r/contents/.github/workflows/ci.yml":         {contentPage("on: [push\n")},
		"/repos/o/r/contents/.github/workflows/cache-warm.yml": {contentPage("on: pull_request\n")},
	}); err == nil {
		t.Error("an unparsable workflow file: err = nil, want an error")
	}
}

type fakePushReader struct {
	can bool
	err error
}

func (f fakePushReader) PushWorkflowsCanRun(context.Context, string, string, string, string) (bool, error) {
	return f.can, f.err
}

func TestNoPushWorkflows(t *testing.T) {
	ctx := context.Background()
	equal, differ := prov("t1", "t1"), prov("t1", "t2")
	if !NoPushWorkflows(ctx, fakePushReader{}, "o", "r", equal) {
		t.Error("clean read, nothing on push, trees equal: want true")
	}
	if NoPushWorkflows(ctx, fakePushReader{can: true}, "o", "r", equal) {
		t.Error("a push workflow exists: want false")
	}
	if NoPushWorkflows(ctx, fakePushReader{err: context.Canceled}, "o", "r", equal) {
		t.Error("read failed: want false")
	}
	if NoPushWorkflows(ctx, fakePushReader{}, "o", "r", differ) {
		t.Error("trees differ: want false")
	}
	if NoPushWorkflows(ctx, struct{}{}, "o", "r", equal) {
		t.Error("reader without the capability: want false")
	}
}
