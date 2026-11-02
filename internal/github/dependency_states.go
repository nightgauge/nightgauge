package github

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// GetDependencyStates reads what each number in one repository is and where it
// stands, as forge.DepState values, in one request. A dependency key may name a
// pull request, which issue(number:) cannot read, so this selects
// issueOrPullRequest and only the fields the state needs (#2854).
//
// A number is DepStateMissing only when GitHub answers NOT_FOUND for that alias.
// Any other error fails the whole read, and a value this cannot interpret leaves
// its number out of the result, so doubt is never reported as a state.
func (s *IssueService) GetDependencyStates(ctx context.Context, owner, repo string, numbers []int) (map[int]string, error) {
	seen := make(map[int]struct{}, len(numbers))
	ordered := make([]int, 0, len(numbers))
	for _, n := range numbers {
		if n <= 0 {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		ordered = append(ordered, n)
	}
	if len(ordered) == 0 {
		return map[int]string{}, nil
	}
	sort.Ints(ordered)

	var sb strings.Builder
	sb.WriteString("query($owner: String!, $name: String!) {\n")
	sb.WriteString("  repository(owner: $owner, name: $name) {\n")
	for _, n := range ordered {
		fmt.Fprintf(&sb, "    i%d: issueOrPullRequest(number: %d) { __typename ... on Issue { state } ... on PullRequest { state merged } }\n", n, n)
	}
	sb.WriteString("  }\n}\n")

	raw, err := s.client.queryRaw(ctx, sb.String(), map[string]interface{}{"owner": owner, "name": repo})
	if err != nil {
		return nil, fmt.Errorf("read dependency states: %w", err)
	}

	type node struct {
		Typename string `json:"__typename"`
		State    string `json:"state"`
		Merged   bool   `json:"merged"`
	}
	var env struct {
		Data struct {
			Repository map[string]*node `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Type    string        `json:"type"`
			Message string        `json:"message"`
			Path    []interface{} `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode dependency states: %w", err)
	}

	notFound := make(map[string]bool, len(env.Errors))
	for _, e := range env.Errors {
		alias, ok := dependencyAlias(e.Path)
		if e.Type != "NOT_FOUND" || !ok {
			return nil, fmt.Errorf("read dependency states: %s: %s", e.Type, e.Message)
		}
		notFound[alias] = true
	}
	if len(env.Data.Repository) == 0 {
		return nil, fmt.Errorf("read dependency states: no repository data for %s/%s", owner, repo)
	}

	out := make(map[int]string, len(ordered))
	for _, n := range ordered {
		alias := fmt.Sprintf("i%d", n)
		v := env.Data.Repository[alias]
		if v == nil {
			if notFound[alias] {
				out[n] = forge.DepStateMissing
			}
			continue
		}
		if state, ok := dependencyState(v.Typename, v.State, v.Merged); ok {
			out[n] = state
		}
	}
	return out, nil
}

// dependencyAlias names the alias a GraphQL error's path points at. Only an
// error on one alias qualifies: a path that stops at the repository says nothing
// about any number.
func dependencyAlias(path []interface{}) (string, bool) {
	if len(path) != 2 || path[0] != "repository" {
		return "", false
	}
	alias, ok := path[1].(string)
	return alias, ok
}

// dependencyState maps one issueOrPullRequest result to its forge.DepState.
func dependencyState(typename, state string, merged bool) (string, bool) {
	switch {
	case typename == "Issue" && (state == forge.DepStateOpen || state == forge.DepStateClosed):
		return state, true
	case typename == "PullRequest" && merged:
		return forge.DepStatePRMerged, true
	case typename == "PullRequest" && state == forge.DepStateOpen:
		return forge.DepStatePROpen, true
	case typename == "PullRequest" && state == forge.DepStateClosed:
		return forge.DepStatePRClosed, true
	}
	return "", false
}
