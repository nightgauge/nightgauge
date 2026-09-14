package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// DefaultBranchFiles is the forge read an OpenCode stage's MCP servers come
// from (#1626): one GraphQL answer gives the default branch, the commit at its
// head and each file at that commit. The answers below are the shape GitHub
// gave for a repository with a .claude/settings.json and no .mcp.json
// (observed 2026-09-13): the missing file is null, with a NOT_FOUND error on
// that path alone.

func TestDefaultBranchFiles_ReadsTheHeadAndLeavesOutAMissingFile(t *testing.T) {
	response := `{"data":{"repository":{"defaultBranchRef":{"name":"trunk","target":{
		"oid":"218a301ddacc4df5df6e0ee342fcd971bacd3a8a",
		"f0":{"mode":33188,"type":"blob","object":{"text":"{\"mcpServers\":{}}","isBinary":false,"isTruncated":false}},
		"f1":null,
		"f2":{"mode":40960,"type":"blob","object":{"text":"../outside.json","isBinary":false,"isTruncated":false}}
	}}}},"errors":[{"type":"NOT_FOUND","path":["repository","defaultBranchRef","target","f1"],"message":"Could not resolve file for path '.mcp.json'."}]}`
	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	got, err := NewRepoService(client).DefaultBranchFiles(context.Background(), "octocat", "acme", []string{".claude/settings.json", ".mcp.json", "linked.json"})
	if err != nil {
		t.Fatalf("DefaultBranchFiles: %v", err)
	}
	if got.Branch != "trunk" || got.Commit != "218a301ddacc4df5df6e0ee342fcd971bacd3a8a" {
		t.Errorf("head = %s at %s, want trunk at 218a301d...", got.Branch, got.Commit)
	}
	if f, ok := got.Files[".claude/settings.json"]; !ok || !f.Regular || string(f.Content) != `{"mcpServers":{}}` {
		t.Errorf(".claude/settings.json = %+v, %v; want a regular file with its text", f, ok)
	}
	if _, ok := got.Files[".mcp.json"]; ok {
		t.Error("a file the commit does not have is in Files")
	}
	if f, ok := got.Files["linked.json"]; !ok || f.Regular || f.Content != nil {
		t.Errorf("a symbolic link = %+v, %v; want present, not regular, no content", f, ok)
	}
}

func TestDefaultBranchFiles_FailsOnAnythingButAMissingFile(t *testing.T) {
	for name, tc := range map[string]struct{ response, want string }{
		"repository not found": {
			`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve to a Repository with the name 'octocat/gone'."}]}`,
			"Could not resolve to a Repository",
		},
		"no default branch": {
			`{"data":{"repository":{"defaultBranchRef":null}}}`,
			"no default branch",
		},
		"a truncated file": {
			`{"data":{"repository":{"defaultBranchRef":{"name":"main","target":{"oid":"218a301ddacc4df5df6e0ee342fcd971bacd3a8a","f0":{"mode":33188,"type":"blob","object":{"text":"{","isBinary":false,"isTruncated":true}}}}}}}`,
			"truncated",
		},
		"a null file with no error": {
			`{"data":{"repository":{"defaultBranchRef":{"name":"main","target":{"oid":"218a301ddacc4df5df6e0ee342fcd971bacd3a8a","f0":null}}}}}`,
			"without saying it is absent",
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, cleanup := mockGraphQLServer(t, tc.response)
			defer cleanup()
			got, err := NewRepoService(client).DefaultBranchFiles(context.Background(), "octocat", "gone", []string{".mcp.json"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("DefaultBranchFiles = %+v, %v; want an error saying %q", got, err, tc.want)
			}
		})
	}
}

// TestNewClientFromConfigContext_BoundsTheGhFallback: the OpenCode forge read
// builds its client under the read's deadline, so a gh that never answers,
// and leaves a child holding its output, holds the caller up for the deadline
// and WaitDelay, not until gh exits.
func TestNewClientFromConfigContext_BoundsTheGhFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n/bin/sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	client, err := NewClientFromConfigContext(ctx, nil, "octocat")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("NewClientFromConfigContext took %s with a 300ms deadline", elapsed)
	}
	if err == nil || client != nil {
		t.Fatalf("NewClientFromConfigContext = %v, %v; want the gh fallback to fail at the deadline", client, err)
	}
}
