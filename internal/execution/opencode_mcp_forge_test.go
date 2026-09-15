package execution

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/internal/models"
)

// canaryStubProviderCleanup removes stubProviderCanaryBinary's os.MkdirTemp
// directory (#1639 round 4 low: it was never removed, so every canary-tagged
// test run leaked one nightgauge-canary-stub-provider-* dir under $TMPDIR).
// It is only ever set, under the `canary` build tag, by
// opencode_canary_test.go's own package-level initializer — package
// execution has exactly one TestMain (this one, untagged, so it always
// compiles), so build-tag-specific cleanup registers into this hook instead
// of adding a second TestMain, which would not compile alongside this one.
var canaryStubProviderCleanup func()

// TestMain makes the forge an OpenCode stage reads its MCP servers from
// refuse every read for the whole test binary: the manager's OpenCode
// dispatches record a repository, and none of them may reach GitHub. A test
// that reads servers swaps in a forge of its own (adapters.
// SwapOpenCodeMcpForgeForTest). Discovery of a local model's limits finds
// nothing for the same reason: no test may ask whatever model server this
// machine runs.
func TestMain(m *testing.M) {
	restore := adapters.SwapOpenCodeMcpForgeForTest(openCodeMapForge{err: errors.New("the execution test binary reads no forge")})
	restoreDiscovery := adapters.SwapOpenCodeLocalDiscoveryForTest(func(adapters.OpenCodeEndpoint, string) (models.LocalDescriptor, error) {
		return models.LocalDescriptor{}, errors.New("the execution test binary asks no model server")
	})
	code := m.Run()
	if canaryStubProviderCleanup != nil {
		canaryStubProviderCleanup()
	}
	restoreDiscovery()
	restore()
	os.Exit(code)
}

// openCodeMapForge serves files as the head of main on a forge, or fails with
// err.
type openCodeMapForge struct {
	files map[string]string
	err   error
}

func (f openCodeMapForge) DefaultBranchFiles(_ context.Context, _, _ string, paths []string) (*forgetypes.DefaultBranchFiles, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &forgetypes.DefaultBranchFiles{Branch: "main", Commit: "0123456789abcdef0123456789abcdef01234567", Files: map[string]forgetypes.RepoFile{}}
	for _, p := range paths {
		if content, ok := f.files[p]; ok {
			out.Files[p] = forgetypes.RepoFile{Regular: true, Content: []byte(content)}
		}
	}
	return out, nil
}
