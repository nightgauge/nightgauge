package adapters

import (
	"context"
	"sync"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// Where an OpenCode stage's MCP servers are read from (ADR-022 § 8, #1626):
// the repository the pipeline records for the run, at the head of its default
// branch, as GitHub serves it. Nothing of the repository on this machine is
// read for them, so no stage can choose the servers of a later one
// (codexprovision.ReadForgeMcpServers).

// OpenCodeMcpForge is the forge an OpenCode dispatch reads its MCP servers
// from: GitHub, through the pipeline's GitHub client, as the identity the
// config in configRoot names for the repository's owner (the token chain of
// github.NewClientFromConfig, with its gh CLI fallback bounded by the read's
// deadline). configRoot is the launch root, or the directory a nightgauge
// command runs in, never a stage's worktree; "" uses no config, so
// GITHUB_TOKEN and then the default gh account.
func OpenCodeMcpForge(configRoot string) forge.DefaultBranchFileService {
	openCodeMcpForgeMu.RLock()
	defer openCodeMcpForgeMu.RUnlock()
	if openCodeMcpForgeOverride != nil {
		return openCodeMcpForgeOverride
	}
	return openCodeGitHubFiles{configRoot: configRoot}
}

var (
	openCodeMcpForgeMu       sync.RWMutex
	openCodeMcpForgeOverride forge.DefaultBranchFileService
)

// SwapOpenCodeMcpForgeForTest makes OpenCodeMcpForge return f until the
// returned function restores it, so no test reaches GitHub.
func SwapOpenCodeMcpForgeForTest(f forge.DefaultBranchFileService) (restore func()) {
	openCodeMcpForgeMu.Lock()
	prev := openCodeMcpForgeOverride
	openCodeMcpForgeOverride = f
	openCodeMcpForgeMu.Unlock()
	return func() {
		openCodeMcpForgeMu.Lock()
		openCodeMcpForgeOverride = prev
		openCodeMcpForgeMu.Unlock()
	}
}

// openCodeGitHubFiles reads a repository's default branch from GitHub.
type openCodeGitHubFiles struct {
	configRoot string
}

// DefaultBranchFiles builds the client under ctx, so resolving the identity's
// token is inside the read's bound, and reads the files through the GitHub
// adapter's RepoService.
func (g openCodeGitHubFiles) DefaultBranchFiles(ctx context.Context, owner, name string, paths []string) (*forgetypes.DefaultBranchFiles, error) {
	var resolver gh.TokenResolver
	if g.configRoot != "" {
		if cfg, err := config.Load(g.configRoot); err == nil && cfg != nil {
			resolver = cfg
		}
	}
	client, err := gh.NewClientFromConfigContext(ctx, resolver, owner)
	if err != nil {
		return nil, err
	}
	return gh.NewRepoService(client).DefaultBranchFiles(ctx, owner, name, paths)
}

// openCodeRunRepo is the repository, owner/name, the pipeline records for
// the dispatch: its target repository, else its repository.
func openCodeRunRepo(run RunOptions) string {
	if run.TargetRepo != "" {
		return run.TargetRepo
	}
	return run.Repo
}

// openCodeConfigContentStandIn stands in, when a variable's value is checked,
// for OPENCODE_CONFIG_CONTENT, the config itself: a JSON object, whose text
// always holds a quote.
const openCodeConfigContentStandIn = `{"model":""}`

// openCodeSpawnLookup reads the environment an OpenCode spawn of model is
// given: env, the variables the run sets (the isolation variables, and
// OPENCODE_CONFIG_CONTENT), laid over the inherited environment lookup reads,
// less every inherited variable the spawn withholds (OpenCodeWithholdsEnv).
// That is the environment OpenCode resolves each {env:NAME} of its config in,
// so it is the one a value is checked in.
func openCodeSpawnLookup(lookup func(string) (string, bool), env map[string]string, model string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if name == openCodeConfigContentEnvVar {
			return openCodeConfigContentStandIn, true
		}
		if v, ok := env[name]; ok {
			return v, true
		}
		if OpenCodeWithholdsEnv(model, name) {
			return "", false
		}
		return lookup(name)
	}
}
