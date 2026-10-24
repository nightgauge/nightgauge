package main

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/forge"
)

// parseBlockerRef reads the blocker argument of `issue add-blocked-by` and
// `issue remove-blocked-by` (#1508). A bare number names an issue in the
// command's own owner/repo; owner/repo#N, repo#N (through the workspace alias
// map) and a GitHub issue URL name one in any repository.
func parseBlockerRef(arg, owner, repo string, repoAliases map[string]string) (forge.IssueRef, error) {
	slug, number, err := depgraph.ParseIssueArg(arg, owner+"/"+repo, repoAliases)
	if err != nil {
		return forge.IssueRef{}, fmt.Errorf("invalid blocker %q: %w", arg, err)
	}
	o, r, _ := strings.Cut(slug, "/")
	return forge.IssueRef{Owner: o, Repo: r, Number: number}, nil
}
