package adapters

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// TestMain for this package lives in opencode_preflight_test.go: it also
// swaps the MCP forge for a refusing stub, so no test reaches GitHub; a test
// that reads servers sets req.McpForge itself (withMcpForge).

// fixtureRepo is the repository the pipeline records for a fixture's run.
const fixtureRepo = "fixture-owner/fixture-repo"

// fixtureCommit is the head of the fixture forge's default branch.
const fixtureCommit = "0123456789abcdef0123456789abcdef01234567"

// mapForge serves files as the head of main on a forge, or fails with err.
type mapForge struct {
	files map[string]string
	err   error
}

func (f mapForge) DefaultBranchFiles(_ context.Context, _, _ string, paths []string) (*forgetypes.DefaultBranchFiles, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &forgetypes.DefaultBranchFiles{Branch: "main", Commit: fixtureCommit, Files: map[string]forgetypes.RepoFile{}}
	for _, p := range paths {
		if content, ok := f.files[p]; ok {
			out.Files[p] = forgetypes.RepoFile{Regular: true, Content: []byte(content)}
		}
	}
	return out, nil
}

// withMcpForge makes req's run record fixtureRepo and read its MCP servers
// from a forge serving files.
func withMcpForge(req OpenCodeRunRequest, files map[string]string) OpenCodeRunRequest {
	req.Run.TargetRepo = fixtureRepo
	req.McpForge = mapForge{files: files}
	return req
}
