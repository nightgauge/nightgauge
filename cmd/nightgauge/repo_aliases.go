package main

import (
	"os"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// workspaceRepoAliases is the alias map through which every body-declared
// dependency this binary parses resolves a short repository name (#2349): the
// pickup gate, the promote sweep, `hook check-deps`, the PR-merge blocker
// guard, the scheduler's graph, `graph build` and `next` all read the same
// prose, so they must all resolve "Blocked by widget-api #12" to the same
// issue. Before #2349 each passed nil, which resolved short names through the
// documentation's example acme/* repositories.
//
// The map covers every repository the workspace knows about: repos (the
// scheduler's or the command's repo set), extra (slugs the caller already
// holds, such as the repository a gate was asked about), and whatever
// config.WorkspaceRepoPaths discovers from root — the workspace manifest's
// members, the sibling checkouts, and root itself. root is first canonicalized
// to its main checkout: a pipeline stage runs in a linked worktree under
// .nightgauge/worktrees/, whose filesystem siblings are other worktrees of the
// same repository, not the workspace's other repositories.
func workspaceRepoAliases(root string, repos []depgraph.RepoConfig, extra ...string) map[string]string {
	slugs := make([]string, 0, len(repos)+len(extra))
	for _, rc := range repos {
		if rc.Owner != "" && rc.Name != "" {
			slugs = append(slugs, rc.FullName())
		}
	}
	slugs = append(slugs, extra...)
	if root != "" {
		if main := config.MainCheckoutRoot(root); main != "" {
			root = main
		}
		for slug := range config.WorkspaceRepoPaths(root) {
			slugs = append(slugs, slug)
		}
	}
	return depgraph.WorkspaceRepoAliases(slugs)
}

// launchRepoAliases is workspaceRepoAliases for a one-shot command: the
// workspace is discovered from the working directory it was launched in.
func launchRepoAliases(extra ...string) map[string]string {
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}
	return workspaceRepoAliases(wd, nil, extra...)
}
