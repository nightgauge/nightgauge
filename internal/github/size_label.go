package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ApplySizeLabel adds `size:<SIZE>` to an issue in ONE REST call (#1515).
//
// The pipeline's feature-planning stage assesses a size on every run and writes
// it to planning-{N}.json. Most issues in this workspace carry no `size:*`
// label, so that assessment was read once, at record time, and never reached
// the issue — which means the router scored the NEXT run of the same backlog
// from the same missing input, and the pre-flight cost estimate had nothing to
// join run history on (#112).
//
// `POST /repos/{o}/{r}/issues/{n}/labels` takes label NAMES, so this is one
// call on the near-idle `core` bucket: no `GetIssue` to resolve a node ID, no
// `GetRepoLabels` to resolve a label ID, and no GraphQL points at all. That
// matters because the caller is a per-run hook — a three-call version would
// bill three times per run forever for a write that is usually a no-op.
//
// ADDITIVE ONLY. It never removes a label and never replaces a disagreeing one:
// callers must establish that the issue has no `size:*` label before calling.
// A size label is a human's input to the router, and an agent silently
// overwriting it is a worse failure than the disagreement it would be papering
// over — the disagreement is recorded on the run record instead
// (`planner_size`).
//
// A label the repository does not define is a REST 422 and is returned as an
// error; the caller logs it and moves on, because a missing size label is the
// state the run was already in.
func (s *IssueService) ApplySizeLabel(ctx context.Context, owner, repo string, number int, size string) error {
	name, err := SizeLabelName(size)
	if err != nil {
		return err
	}
	if owner == "" || repo == "" || number <= 0 {
		return fmt.Errorf("apply size label: owner, repo and issue number are required")
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels",
		url.PathEscape(owner), url.PathEscape(repo), number)
	body, status, err := s.client.restDoStatus(ctx, http.MethodPost, path,
		map[string][]string{"labels": {name}})
	if err != nil {
		return fmt.Errorf("apply %s to %s/%s#%d: %w", name, owner, repo, number, err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("apply %s to %s/%s#%d: REST %d: %s",
			name, owner, repo, number, status, restErrorSummary(body))
	}
	return nil
}

// SizeLabelName renders a bucket as the `size:*` label the repositories use.
//
// Case is normalized UP because that is how the labels are spelled
// (`size:XS` … `size:XL`) and how routing.SizeBaseScore reads them back. An
// unrecognized bucket is an error rather than a label name: creating
// `size:medium` alongside `size:M` would split the router's own input in two.
func SizeLabelName(size string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(size))
	switch normalized {
	case "XS", "S", "M", "L", "XL":
		return "size:" + normalized, nil
	default:
		return "", fmt.Errorf("size label: %q is not one of XS, S, M, L, XL", size)
	}
}

// HasSizeLabel reports whether any of `labels` is a `size:*` label.
func HasSizeLabel(labels []string) bool {
	for _, l := range labels {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), "size:") {
			return true
		}
	}
	return false
}
