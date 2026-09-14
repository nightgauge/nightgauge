package adapters

import (
	"context"
	"errors"
	"os"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// TestMain makes the forge an OpenCode dispatch reads its MCP servers from
// refuse every read for the whole test binary, so no test reaches GitHub; a
// test that reads servers sets req.McpForge itself (withMcpForge).
func TestMain(m *testing.M) {
	restore := SwapOpenCodeMcpForgeForTest(mapForge{err: errors.New("the adapters test binary reads no forge: set OpenCodeRunRequest.McpForge")})
	code := m.Run()
	restore()
	os.Exit(code)
}

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
