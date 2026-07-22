package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// PRService implements forge.PRService for GitLab merge requests.
type PRService struct {
	client *Client
}

// NewPRService constructs a GitLab PRService bound to the given REST client.
// Callers typically reach this via ForgeAdapter.PRs().
func NewPRService(client *Client) *PRService {
	return &PRService{client: client}
}

// rawPipelineSummary is the optional embedded pipeline shape returned by
// the single-MR GET endpoint (and absent from listing endpoints). Decoded
// best-effort so future callers can avoid the extra REST round trip when
// the head pipeline summary is already in hand. Currently unused by
// CIService.GetCheckStatus, which queries the pipelines list endpoint as
// the canonical source.
type rawPipelineSummary struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	SHA    string `json:"sha"`
}

// rawGitLabMR is the JSON shape returned by /projects/:id/merge_requests/:iid.
type rawGitLabMR struct {
	ID                   int64               `json:"id"`
	IID                  int                 `json:"iid"`
	ProjectID            int64               `json:"project_id"`
	Title                string              `json:"title"`
	Description          string              `json:"description"`
	State                string              `json:"state"` // "opened" | "closed" | "merged"
	SourceBranch         string              `json:"source_branch"`
	TargetBranch         string              `json:"target_branch"`
	WebURL               string              `json:"web_url"`
	Labels               []string            `json:"labels"`
	Draft                bool                `json:"draft"`
	WorkInProgress       bool                `json:"work_in_progress"`
	MergeStatus          string              `json:"merge_status"` // can_be_merged | cannot_be_merged | unchecked
	ChangesCount         string              `json:"changes_count"`
	Squash               bool                `json:"squash"`
	AllowForcePush       bool                `json:"allow_force_push"`
	ApprovalsBeforeMerge *int                `json:"approvals_before_merge,omitempty"`
	Assignees            []rawGitLabUser     `json:"assignees"`
	HeadPipeline         *rawPipelineSummary `json:"head_pipeline,omitempty"`
}

// translateMergeStatus maps GitLab's merge_status vocabulary into the
// GitHub-style values forge consumers already understand.
func translateMergeStatus(gitlabStatus string) string {
	switch gitlabStatus {
	case "can_be_merged":
		return "MERGEABLE"
	case "cannot_be_merged":
		return "CONFLICTING"
	case "unchecked", "checking", "":
		return "UNKNOWN"
	default:
		return strings.ToUpper(gitlabStatus)
	}
}

// toForgePR translates a raw GitLab MR into the forge-agnostic
// PullRequest shape.
func (r *rawGitLabMR) toForgePR(owner, repo string) *forgetypes.PullRequest {
	if r == nil {
		return nil
	}
	out := &forgetypes.PullRequest{
		NodeID:    strconv.FormatInt(r.ID, 10),
		Number:    r.IID,
		Title:     r.Title,
		Body:      r.Description,
		State:     normalisePRState(r.State),
		HeadRef:   r.SourceBranch,
		BaseRef:   r.TargetBranch,
		Repo:      owner + "/" + repo,
		URL:       r.WebURL,
		Mergeable: translateMergeStatus(r.MergeStatus),
		Labels:    append([]string(nil), r.Labels...),
		IsDraft:   r.Draft || r.WorkInProgress,
	}
	// Best-effort changes_count → Additions; GitLab returns it as a
	// stringified count when known, empty otherwise.
	if r.ChangesCount != "" {
		if n, err := strconv.Atoi(r.ChangesCount); err == nil {
			out.Additions = n
		}
	}
	return out
}

// normalisePRState mirrors GitHub's uppercase OPEN/CLOSED/MERGED state
// vocabulary so consumers can compare across forges without a shim.
func normalisePRState(state string) string {
	switch strings.ToLower(state) {
	case "opened":
		return "OPEN"
	case "closed":
		return "CLOSED"
	case "merged":
		return "MERGED"
	case "locked":
		return "LOCKED"
	default:
		return strings.ToUpper(state)
	}
}

// listMRsQuery returns the query parameters for the /merge_requests
// collection endpoint matching the (state, headRef) filter.
func listMRsQuery(state, headRef string) url.Values {
	q := url.Values{}
	if state != "" {
		q.Set("state", strings.ToLower(state))
	} else {
		q.Set("state", "opened")
	}
	if headRef != "" {
		q.Set("source_branch", headRef)
	}
	q.Set("per_page", "100")
	return q
}

// GetPR fetches a single merge request by iid.
func (s *PRService) GetPR(ctx context.Context, owner, repo string, number int) (*forgetypes.PullRequest, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(owner, repo), number)
	full := s.client.buildURL(path, nil)

	var raw rawGitLabMR
	if _, err := s.client.do(ctx, "GET", full, nil, &raw, fmt.Sprintf("get MR !%d", number)); err != nil {
		return nil, err
	}
	return raw.toForgePR(owner, repo), nil
}

// ListPRs fetches merge requests with the given filter, walking link-header
// pagination until exhausted.
func (s *PRService) ListPRs(ctx context.Context, owner, repo, state, headRef string) ([]forgetypes.PullRequest, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests", projectPath(owner, repo))
	full := s.client.buildURL(path, listMRsQuery(state, headRef))

	var all []forgetypes.PullRequest
	for full != "" {
		var page []rawGitLabMR
		resp, err := s.client.do(ctx, "GET", full, nil, &page, "list MRs")
		if err != nil {
			return nil, err
		}
		for i := range page {
			if pr := page[i].toForgePR(owner, repo); pr != nil {
				all = append(all, *pr)
			}
		}
		links := parseLinkHeader(resp.Header.Get("Link"))
		if links.Next == nil {
			break
		}
		full = links.Next.String()
	}
	return all, nil
}

// IteratePRs returns a streaming iterator that walks merge-request
// pagination without buffering the full result set.
func (s *PRService) IteratePRs(ctx context.Context, owner, repo, state, headRef string) forge.Iterator[forgetypes.PullRequest] {
	path := fmt.Sprintf("/projects/%s/merge_requests", projectPath(owner, repo))
	startURL := s.client.buildURL(path, listMRsQuery(state, headRef))
	return newPageIterator(s.client, startURL, "list MRs", owner, repo, decodeMRPage)
}

func decodeMRPage(body []byte, owner, repo string) ([]forgetypes.PullRequest, error) {
	var raws []rawGitLabMR
	if err := json.Unmarshal(body, &raws); err != nil {
		return nil, fmt.Errorf("decode MRs page: %w", err)
	}
	out := make([]forgetypes.PullRequest, 0, len(raws))
	for i := range raws {
		if v := raws[i].toForgePR(owner, repo); v != nil {
			out = append(out, *v)
		}
	}
	return out, nil
}

// CreatePR creates a new merge request. repoID encodes "owner/repo" in
// keeping with the GitHub adapter's opaque-repo-handle pattern.
func (s *PRService) CreatePR(ctx context.Context, repoID, title, body, headRef, baseRef string) (*forgetypes.PullRequest, error) {
	owner, repo := splitProjectID(repoID)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("gitlab create MR: repoID %q must be owner/repo", repoID)
	}
	path := fmt.Sprintf("/projects/%s/merge_requests", projectPath(owner, repo))
	full := s.client.buildURL(path, nil)

	payload := map[string]any{
		"title":         title,
		"description":   body,
		"source_branch": headRef,
		"target_branch": baseRef,
	}

	var raw rawGitLabMR
	if _, err := s.client.do(ctx, "POST", full, payload, &raw, "create MR"); err != nil {
		return nil, err
	}
	return raw.toForgePR(owner, repo), nil
}

// UpdatePR patches the documented attributes of a merge request identified
// by "owner/repo!iid". Forge-fields that GitLab supports natively
// (Title, Body, Draft, TargetBranch, Squash, AllowForcePush, Labels) are
// passed through. ApprovalsBeforeMerge is sent only when set; if GitLab
// CE rejects it, the call returns ErrUnsupportedOnEdition so callers can
// downgrade the failure to a warning.
func (s *PRService) UpdatePR(ctx context.Context, prID string, opts forge.UpdatePROptions) (*forgetypes.PullRequest, error) {
	owner, repo, iid, err := parseMRRef(prID)
	if err != nil {
		return nil, fmt.Errorf("gitlab update MR: %w", err)
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(owner, repo), iid)
	full := s.client.buildURL(path, nil)

	payload := map[string]any{}
	if opts.Title != nil {
		payload["title"] = *opts.Title
	}
	if opts.Body != nil {
		payload["description"] = *opts.Body
	}
	if opts.Draft != nil {
		// GitLab accepts both keys; sending `draft` matches the modern
		// schema while remaining compatible with older instances.
		payload["draft"] = *opts.Draft
	}
	if opts.TargetBranch != nil {
		payload["target_branch"] = *opts.TargetBranch
	}
	if opts.Squash != nil {
		payload["squash"] = *opts.Squash
	}
	if opts.AllowForcePush != nil {
		payload["allow_force_push"] = *opts.AllowForcePush
	}
	if opts.ApprovalsBeforeMerge != nil {
		payload["approvals_before_merge"] = *opts.ApprovalsBeforeMerge
	}
	if opts.Labels != nil {
		payload["labels"] = strings.Join(*opts.Labels, ",")
	}

	if len(payload) == 0 {
		return s.GetPR(ctx, owner, repo, iid)
	}

	var raw rawGitLabMR
	if _, err := s.client.do(ctx, "PUT", full, payload, &raw, "update MR"); err != nil {
		// CE rejects approvals_before_merge with HTTP 400 / specific error
		// strings; surface that as ErrUnsupportedOnEdition so callers can
		// downgrade.
		if opts.ApprovalsBeforeMerge != nil && isApprovalsCEError(err) {
			return nil, asEditionError("update MR", "approvals_before_merge", err)
		}
		return nil, err
	}
	return raw.toForgePR(owner, repo), nil
}

// isApprovalsCEError checks whether a GitLab error is the CE rejection of
// approvals_before_merge. The error text is matched conservatively so we
// don't misclassify unrelated 400s.
func isApprovalsCEError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 400") &&
		strings.Contains(strings.ToLower(msg), "approvals_before_merge")
}

// ClosePR closes a merge request without merging it.
func (s *PRService) ClosePR(ctx context.Context, prID string) error {
	owner, repo, iid, err := parseMRRef(prID)
	if err != nil {
		return fmt.Errorf("gitlab close MR: %w", err)
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(owner, repo), iid)
	full := s.client.buildURL(path, nil)
	_, err = s.client.do(ctx, "PUT", full, map[string]any{"state_event": "close"}, nil, "close MR")
	return err
}

// MergePR merges a merge request via the dedicated /merge endpoint.
func (s *PRService) MergePR(ctx context.Context, prID string) error {
	_, err := s.MergePRWithStrategy(ctx, prID, "")
	return err
}

// MergePRWithStrategy merges a merge request with the given strategy.
// GitLab uses squash booleans rather than named strategies; "SQUASH"
// flips the squash flag, anything else triggers a regular merge.
func (s *PRService) MergePRWithStrategy(ctx context.Context, prID, strategy string) (string, error) {
	owner, repo, iid, err := parseMRRef(prID)
	if err != nil {
		return "", fmt.Errorf("gitlab merge MR: %w", err)
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/merge", projectPath(owner, repo), iid)
	full := s.client.buildURL(path, nil)

	payload := map[string]any{}
	if strings.EqualFold(strategy, "SQUASH") {
		payload["squash"] = true
	}

	var resp struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
		SquashCommit   string `json:"squash_commit_sha"`
	}
	if _, err := s.client.do(ctx, "PUT", full, payload, &resp, "merge MR"); err != nil {
		return "", err
	}
	if resp.MergeCommitSHA != "" {
		return resp.MergeCommitSHA, nil
	}
	return resp.SquashCommit, nil
}

// DeleteBranch removes a branch via the GitLab Branches API.
func (s *PRService) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	path := fmt.Sprintf("/projects/%s/repository/branches/%s", projectPath(owner, repo), url.PathEscape(branch))
	full := s.client.buildURL(path, nil)
	_, err := s.client.do(ctx, "DELETE", full, nil, nil, "delete branch")
	return err
}

// CreateEpicPR is GitHub-specific composite logic; GitLab's epic flow is
// tracked in #3358 and currently unsupported.
func (s *PRService) CreateEpicPR(ctx context.Context, owner, repo string, epicNumber int, epicTitle, epicBranch, baseBranch string) (*forgetypes.EpicPRResult, error) {
	return nil, fmt.Errorf("gitlab.PRService.CreateEpicPR: %w (tracked: #3358)", forge.ErrUnsupported)
}

// MergeEpicPR is GitHub-specific composite logic; tracked in #3358.
func (s *PRService) MergeEpicPR(ctx context.Context, owner, repo string, prNodeID, epicBranch string) error {
	return fmt.Errorf("gitlab.PRService.MergeEpicPR: %w (tracked: #3358)", forge.ErrUnsupported)
}

// parseMRRef extracts (owner, repo, iid) from "owner/repo!iid" or
// "owner/repo#iid". Both separators are accepted because GitHub callers
// often hand back "#" while GitLab UIs prefer "!".
func parseMRRef(ref string) (string, string, int, error) {
	idx := strings.LastIndexAny(ref, "!#")
	if idx < 0 {
		return "", "", 0, fmt.Errorf("ref %q must be owner/repo!iid", ref)
	}
	repoPart := ref[:idx]
	iidPart := ref[idx+1:]
	owner, repo := splitProjectID(repoPart)
	if owner == "" || repo == "" {
		return "", "", 0, fmt.Errorf("ref %q has malformed owner/repo", ref)
	}
	iid, err := strconv.Atoi(iidPart)
	if err != nil {
		return "", "", 0, fmt.Errorf("ref %q has malformed iid: %w", ref, err)
	}
	return owner, repo, iid, nil
}
