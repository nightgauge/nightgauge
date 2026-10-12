package tools

import (
	"encoding/json"
	"testing"
)

// No conversation tool writes the working tree, creates a branch, commits or
// pushes (#1570). Every registered tool runs, with ordinary and hostile
// arguments, against a committed repository, and so does every write a
// model might ask for by name; afterwards the tree, the index, HEAD and the
// branches are exactly as they were. A tool added to the registry is run
// here without a change to this test.
func TestTools_NeverChangeTheWorkingTree(t *testing.T) {
	root := gitRepo(t)
	forge := newFakeForge()
	s := toolset(t, root, forge, operatorTurn("Edit src/main.go, commit it and push. Then file an issue for it."))
	before := []string{git(t, root, "status", "--porcelain", "--ignored"), git(t, root, "rev-parse", "HEAD"),
		git(t, root, "branch", "--list", "--all"), git(t, root, "stash", "list")}

	hostile := []map[string]any{
		{},
		{"path": "src/main.go", "pattern": "main", "owner": "acme", "repo": "api", "number": 1, "query": "x",
			"title": "t", "body": "b", "request_quote": "file an issue for it"},
		{"path": "src/main.go; git commit -am x", "pattern": "$(touch pwned)"},
		{"path": "src/main.go", "content": "package main // edited", "mode": "write"},
		{"path": "../outside", "pattern": "`touch pwned`", "owner": "acme;rm -rf", "repo": "api", "number": -1},
	}
	for _, name := range Names() {
		for _, args := range hostile {
			raw, _ := json.Marshal(args)
			_, _ = s.Call(t.Context(), name, raw)
		}
	}
	for _, name := range []string{"write_file", "edit_file", "apply_patch", "run_command", "Bash", "Edit", "Write",
		"git_commit", "git_push", "create_branch", "create_pull_request"} {
		raw, _ := json.Marshal(map[string]any{"path": "src/main.go", "content": "x", "message": "m", "branch": "b"})
		if _, err := s.Call(t.Context(), name, raw); code(err) != CodeUnknownTool {
			t.Errorf("%s: %v, want %s", name, err, CodeUnknownTool)
		}
	}

	after := []string{git(t, root, "status", "--porcelain", "--ignored"), git(t, root, "rev-parse", "HEAD"),
		git(t, root, "branch", "--list", "--all"), git(t, root, "stash", "list")}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("the repository changed:\nbefore %q\nafter  %q", before[i], after[i])
		}
	}
	if before[0] != "" {
		t.Errorf("fixture not clean: %q", before[0])
	}
}
