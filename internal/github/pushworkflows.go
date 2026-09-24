package github

// Whether anything can run on a push to the base branch (#2061). In a
// repository whose workflows run only on pull requests, schedules and
// dispatch, an empty check list on a tree-equal merge commit is final: there
// are no push workflows to wait for, so MergeCommitCheckGrace is skipped.
//
// The read is conservative in one direction only. A trigger the parser cannot
// rule out (a paths filter, a branch pattern it does not model, an unreadable
// or unparsable file) counts as "may run", which keeps the grace. Only a clean
// read in which no workflow can run on push lets the grace go.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// PushWorkflowReader is implemented by *CIService. Callers type-assert for it;
// a reader without it keeps the grace.
type PushWorkflowReader interface {
	// PushWorkflowsCanRun reports whether any workflow at sha can run on a
	// push to branch. An error means "unknown", never "no".
	PushWorkflowsCanRun(ctx context.Context, owner, repo, sha, branch string) (bool, error)
}

// NoPushWorkflows reports that the workflows at a tree-equal merge commit were
// read and none can run on a push to its base branch (#2061). Any failure, or a
// reader without the capability, is false: the grace stays.
func NoPushWorkflows(ctx context.Context, reader any, owner, repo string, prov *MergeProvenance) bool {
	pr, ok := reader.(PushWorkflowReader)
	if !ok || prov == nil || !prov.TreesMatch() || prov.BaseRef == "" {
		return false
	}
	can, err := pr.PushWorkflowsCanRun(ctx, owner, repo, prov.MergeSHA, prov.BaseRef)
	return err == nil && !can
}

// PushWorkflowsCanRun reads .github/workflows at sha through the contents API.
// A repository with no workflows directory has nothing that can run.
func (s *CIService) PushWorkflowsCanRun(ctx context.Context, owner, repo, sha, branch string) (bool, error) {
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	var files []entry
	dirURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/.github/workflows?ref=%s", owner, repo, sha)
	err := s.getAllPages(ctx, dirURL, notFoundEndsRead, func(body io.Reader) error {
		var page []entry
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return fmt.Errorf("decode workflows directory: %w", err)
		}
		files = append(files, page...)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("list workflows at %s: %w", shortRef(sha), err)
	}
	for _, f := range files {
		lower := strings.ToLower(f.Name)
		if f.Type != "file" || !(strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")) {
			continue
		}
		data, err := s.getFileContent(ctx, owner, repo, f.Path, sha)
		if err != nil {
			return false, err
		}
		can, err := WorkflowCanRunOnPush(data, branch)
		if err != nil {
			return false, fmt.Errorf("%s: %w", f.Path, err)
		}
		if can {
			return true, nil
		}
	}
	return false, nil
}

// getFileContent reads one file at ref. A missing file is an error here: it
// was just listed, so its absence means the read is not trustworthy.
func (s *CIService) getFileContent(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref)
	var data []byte
	err := s.getAllPages(ctx, url, func(resp *http.Response, body []byte) error {
		return checkRunsStatusError(resp, body)
	}, func(body io.Reader) error {
		var f struct {
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
		}
		if err := json.NewDecoder(body).Decode(&f); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		if f.Encoding != "base64" {
			return fmt.Errorf("%s: unexpected encoding %q", path, f.Encoding)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
		if err != nil {
			return fmt.Errorf("decode %s content: %w", path, err)
		}
		data = raw
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", path, shortRef(ref), err)
	}
	return data, nil
}

// WorkflowCanRunOnPush reports whether a workflow file can run on a push to
// branch. Unparsable YAML, or an `on:` it does not recognise, is an error.
func WorkflowCanRunOnPush(data []byte, branch string) (bool, error) {
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parse workflow: %w", err)
	}
	on, ok := doc["on"]
	if !ok {
		// A YAML 1.1 reader would see `true`; yaml.v3 keeps "on". Neither
		// spelling present is not a workflow GitHub will run.
		if _, isTrue := doc["true"]; isTrue {
			return false, fmt.Errorf("`on` parsed as a boolean key")
		}
		return false, fmt.Errorf("no `on` key")
	}
	switch on.Kind {
	case yaml.ScalarNode:
		return on.Value == "push", nil
	case yaml.SequenceNode:
		for _, ev := range on.Content {
			if ev.Kind != yaml.ScalarNode {
				return false, fmt.Errorf("unrecognised event in `on` list")
			}
			if ev.Value == "push" {
				return true, nil
			}
		}
		return false, nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			if on.Content[i].Value == "push" {
				return pushFilterCanMatch(on.Content[i+1], branch)
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("unrecognised `on` shape")
}

// pushFilterCanMatch applies a push trigger's filters to a branch push.
// GitHub runs a push workflow that defines only tag filters for tag pushes
// alone; paths filters are not evaluated, so they may match.
func pushFilterCanMatch(n *yaml.Node, branch string) (bool, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		// `push:` with no value, or `push: null`.
		return true, nil
	case yaml.MappingNode:
	default:
		return false, fmt.Errorf("unrecognised push filter")
	}
	filters := map[string][]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i].Value, n.Content[i+1]
		var list []string
		switch val.Kind {
		case yaml.SequenceNode:
			for _, item := range val.Content {
				list = append(list, item.Value)
			}
		case yaml.ScalarNode:
			list = []string{val.Value}
		default:
			return false, fmt.Errorf("unrecognised %s filter", key)
		}
		filters[key] = list
	}
	branches, hasBranches := filters["branches"]
	ignore, hasIgnore := filters["branches-ignore"]
	_, hasTags := filters["tags"]
	_, hasTagsIgnore := filters["tags-ignore"]
	switch {
	case hasBranches:
		return branchesMatch(branches, branch), nil
	case hasIgnore:
		for _, p := range ignore {
			if m, exact := globMatch(p, branch); exact && m {
				return false, nil
			}
		}
		return true, nil
	case hasTags || hasTagsIgnore:
		return false, nil
	}
	return true, nil
}

// branchesMatch evaluates a `branches` list in order, `!` negating. A pattern
// globMatch cannot model counts as matching when positive and as not
// excluding when negated, so the answer errs toward "may run".
func branchesMatch(patterns []string, branch string) bool {
	matched := false
	for _, p := range patterns {
		if neg := strings.HasPrefix(p, "!"); neg {
			if m, exact := globMatch(p[1:], branch); exact && m {
				matched = false
			}
			continue
		}
		if m, exact := globMatch(p, branch); m || !exact {
			matched = true
		}
	}
	return matched
}

// globMatch matches GitHub's `*` (no slash) and `**` (anything) filter
// patterns. exact is false for `?`, `+`, `[` or `\`, which it does not model.
func globMatch(pattern, name string) (matched, exact bool) {
	if strings.ContainsAny(pattern, "?+[]\\") {
		return false, false
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(pattern[i])))
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false, false
	}
	return re.MatchString(name), true
}
