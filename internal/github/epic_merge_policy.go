package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// EpicMergeMethod is the only strategy that keeps an epic's story commits on
// the default branch (docs/GIT_WORKFLOW.md § Epic Merge Strategy).
const EpicMergeMethod = "MERGE"

// EpicMergeCheck reports whether a repository's default branch accepts an epic
// merged with EpicMergeMethod (#3358).
type EpicMergeCheck struct {
	Allowed bool
	Method  string
	Base    string
	// Reason names the refusing setting; empty when Allowed.
	Reason string
}

// EpicMergeRefusedError is returned by MergeEpicPR when the default branch
// would not take a MERGE. Nothing is merged and the branch is kept.
type EpicMergeRefusedError struct{ Check EpicMergeCheck }

func (e *EpicMergeRefusedError) Error() string {
	return fmt.Sprintf("epic merge refused: %s — %s", e.Check.Method, e.Check.Reason)
}

// Rule types the epic merge check reads from rules/branches.
const (
	ruleTypePullRequest = "pull_request"
	ruleTypeMergeQueue  = "merge_queue"
)

// epicMergeRule is the subset of a rules/branches entry the epic check reads.
type epicMergeRule struct {
	Type       string `json:"type"`
	Parameters struct {
		AllowedMergeMethods []string `json:"allowed_merge_methods"`
		MergeMethod         string   `json:"merge_method"`
	} `json:"parameters"`
}

// evaluateEpicMerge is the pure decision: refused on the repository setting,
// then a pull_request rule's allowed methods, then the merge queue's method.
func evaluateEpicMerge(rs RepositorySettings, rules []epicMergeRule, method string) EpicMergeCheck {
	c := EpicMergeCheck{Allowed: true, Method: method, Base: rs.DefaultBranch}
	refuse := func(reason string) EpicMergeCheck {
		c.Allowed, c.Reason = false, reason
		return c
	}
	if !rs.AllowMergeCommit {
		return refuse("repository setting allow_merge_commit=false")
	}
	for _, r := range rules {
		if r.Type != ruleTypePullRequest || r.Parameters.AllowedMergeMethods == nil {
			continue
		}
		ok := false
		for _, m := range r.Parameters.AllowedMergeMethods {
			if strings.EqualFold(m, method) {
				ok = true
			}
		}
		if !ok {
			return refuse(fmt.Sprintf("%s pull_request rule allowed_merge_methods=[%s]", c.Base, strings.Join(r.Parameters.AllowedMergeMethods, ",")))
		}
	}
	for _, r := range rules {
		if r.Type == ruleTypeMergeQueue && r.Parameters.MergeMethod != "" && !strings.EqualFold(r.Parameters.MergeMethod, method) {
			return refuse(fmt.Sprintf("%s merge_queue rule merge_method=%s", c.Base, r.Parameters.MergeMethod))
		}
	}
	return c
}

// CheckEpicMerge reads the repository's merge settings and its default
// branch's rules and reports whether an epic can merge with EpicMergeMethod.
// A 403/404 on the rules read decides on the repository setting alone.
func (s *SettingsService) CheckEpicMerge(ctx context.Context, owner, repo string) (EpicMergeCheck, error) {
	rs, err := s.GetRepositorySettings(ctx, owner, repo)
	if err != nil {
		return EpicMergeCheck{}, fmt.Errorf("check epic merge for %s/%s: %w", owner, repo, err)
	}
	if rs.DefaultBranch == "" {
		return EpicMergeCheck{}, fmt.Errorf("check epic merge for %s/%s: repository reports no default branch", owner, repo)
	}
	path := fmt.Sprintf("/repos/%s/%s/rules/branches/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(rs.DefaultBranch))
	body, status, err := s.client.restDoStatus(ctx, http.MethodGet, path, nil)
	if err != nil {
		return EpicMergeCheck{}, fmt.Errorf("check epic merge for %s/%s: %w", owner, repo, err)
	}
	var rules []epicMergeRule
	switch {
	case status == http.StatusForbidden || status == http.StatusNotFound:
		log.Printf("[epic-merge] %s/%s: branch rules for %s unreadable (status %d); deciding on repository settings alone", owner, repo, rs.DefaultBranch, status)
	case status < 200 || status >= 300:
		return EpicMergeCheck{}, fmt.Errorf("check epic merge for %s/%s: rules GET %s: status %d", owner, repo, path, status)
	default:
		if err := json.Unmarshal(body, &rules); err != nil {
			return EpicMergeCheck{}, fmt.Errorf("check epic merge for %s/%s: decode rules: %w", owner, repo, err)
		}
	}
	return evaluateEpicMerge(*rs, rules, EpicMergeMethod), nil
}
