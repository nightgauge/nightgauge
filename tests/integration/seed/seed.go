// Package seed provides a deterministic GitLab CE fixture seeder for the
// integration test harness (#3366). Resources are created idempotently —
// each Seed call first checks for existing fixtures by deterministic name
// and reuses them instead of erroring out. This makes the harness safe to
// re-run after partial failures during local development and in CI.
//
// The seeder talks to GitLab REST directly via net/http rather than going
// through internal/gitlab — ADR-002 in
// .nightgauge/knowledge/features/3366-.../decisions.md documents why
// (avoids a package-import cycle for the seeder binary).
package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	defaultProjectName  = "nightgauge-ci-test"
	defaultPATName      = "nightgauge-test"
	defaultIssueCount   = 5
	defaultLabelBug     = "type:bug"
	defaultLabelFeature = "type:feature"
)

// Seeder holds a GitLab REST client and creates deterministic fixtures.
type Seeder struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewSeeder constructs a Seeder against the given GitLab instance. Token is
// a PRIVATE-TOKEN — must have admin scope to create the deploy token, PAT,
// and project.
func NewSeeder(baseURL, token string) *Seeder {
	return &Seeder{
		BaseURL: baseURL,
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Fixtures is the JSON-serializable output of a seed run. Tests read this
// to learn the IDs and tokens they need.
type Fixtures struct {
	BaseURL     string `json:"base_url"`
	ProjectID   int    `json:"project_id"`
	ProjectPath string `json:"project_path"`
	DeployToken string `json:"deploy_token,omitempty"`
	PAT         string `json:"pat"`
	LabelBugID  int    `json:"label_bug_id"`
	LabelFeatID int    `json:"label_feat_id"`
	BoardID     int    `json:"board_id"`
	IssueIIDs   []int  `json:"issue_iids"`
	MRIID       int    `json:"mr_iid"`
}

// Seed performs the full fixture creation. Idempotent — re-running against
// the same instance returns the existing fixtures rather than erroring.
func (s *Seeder) Seed(ctx context.Context) (*Fixtures, error) {
	if s.BaseURL == "" {
		return nil, errors.New("seed: BaseURL is required")
	}
	if s.Token == "" {
		return nil, errors.New("seed: Token is required")
	}

	out := &Fixtures{BaseURL: s.BaseURL, IssueIIDs: make([]int, 0, defaultIssueCount)}

	proj, err := s.ensureProject(ctx, defaultProjectName)
	if err != nil {
		return nil, fmt.Errorf("ensure project: %w", err)
	}
	out.ProjectID = proj.ID
	out.ProjectPath = proj.PathWithNamespace

	pat, err := s.ensurePAT(ctx, proj.OwnerID, defaultPATName)
	if err != nil {
		return nil, fmt.Errorf("ensure pat: %w", err)
	}
	out.PAT = pat

	bugID, err := s.ensureLabel(ctx, proj.ID, defaultLabelBug, "#d73a4a")
	if err != nil {
		return nil, fmt.Errorf("ensure label bug: %w", err)
	}
	out.LabelBugID = bugID

	featID, err := s.ensureLabel(ctx, proj.ID, defaultLabelFeature, "#a2eeef")
	if err != nil {
		return nil, fmt.Errorf("ensure label feature: %w", err)
	}
	out.LabelFeatID = featID

	boardID, err := s.ensureBoard(ctx, proj.ID, "nightgauge-board")
	if err != nil {
		return nil, fmt.Errorf("ensure board: %w", err)
	}
	out.BoardID = boardID

	for i := 0; i < defaultIssueCount; i++ {
		title := fmt.Sprintf("ci-test-issue-%d", i+1)
		labels := defaultLabelBug
		if i%2 == 1 {
			labels = defaultLabelFeature
		}
		iid, err := s.ensureIssue(ctx, proj.ID, title, "Fixture issue created by the integration seeder.", labels)
		if err != nil {
			return nil, fmt.Errorf("ensure issue %d: %w", i+1, err)
		}
		out.IssueIIDs = append(out.IssueIIDs, iid)
	}

	mrIID, err := s.ensureMR(ctx, proj.ID, "feature/ci-test-mr", proj.DefaultBranch, "ci-test-mr")
	if err != nil {
		return nil, fmt.Errorf("ensure mr: %w", err)
	}
	out.MRIID = mrIID

	return out, nil
}

// ---------- project ----------

type project struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	OwnerID           int    `json:"-"`
}

func (s *Seeder) ensureProject(ctx context.Context, name string) (*project, error) {
	q := url.Values{}
	q.Set("search", name)
	q.Set("owned", "true")

	var existing []project
	if err := s.doJSON(ctx, http.MethodGet, "/api/v4/projects?"+q.Encode(), nil, &existing); err != nil {
		return nil, err
	}
	for i := range existing {
		if existing[i].Name == name {
			// Find owner id via /user; we need it for PAT creation.
			owner, err := s.currentUserID(ctx)
			if err != nil {
				return nil, err
			}
			existing[i].OwnerID = owner
			if existing[i].DefaultBranch == "" {
				existing[i].DefaultBranch = "main"
			}
			return &existing[i], nil
		}
	}

	body := map[string]any{
		"name":                   name,
		"visibility":             "private",
		"initialize_with_readme": true,
		"default_branch":         "main",
	}
	var created project
	if err := s.doJSON(ctx, http.MethodPost, "/api/v4/projects", body, &created); err != nil {
		return nil, err
	}
	owner, err := s.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	created.OwnerID = owner
	if created.DefaultBranch == "" {
		created.DefaultBranch = "main"
	}
	return &created, nil
}

func (s *Seeder) currentUserID(ctx context.Context) (int, error) {
	var u struct {
		ID int `json:"id"`
	}
	if err := s.doJSON(ctx, http.MethodGet, "/api/v4/user", nil, &u); err != nil {
		return 0, err
	}
	if u.ID == 0 {
		return 0, errors.New("seed: /api/v4/user returned id=0")
	}
	return u.ID, nil
}

// ---------- PAT ----------

func (s *Seeder) ensurePAT(ctx context.Context, userID int, name string) (string, error) {
	// GitLab does not expose the raw token of an existing PAT, so when one
	// already exists with this name we revoke it and create a fresh one.
	var existing []struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Active bool   `json:"active"`
	}
	if err := s.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/users/%d/personal_access_tokens", userID), nil, &existing); err != nil {
		return "", err
	}
	for _, t := range existing {
		if t.Name == name && t.Active {
			if err := s.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/api/v4/personal_access_tokens/%d", t.ID), nil, nil); err != nil {
				return "", fmt.Errorf("revoke stale pat: %w", err)
			}
		}
	}

	body := map[string]any{
		"name":       name,
		"scopes":     []string{"api", "read_repository", "write_repository"},
		"expires_at": time.Now().AddDate(0, 0, 30).Format("2006-01-02"),
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := s.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/users/%d/personal_access_tokens", userID), body, &created); err != nil {
		return "", err
	}
	if created.Token == "" {
		return "", errors.New("seed: PAT creation returned empty token")
	}
	return created.Token, nil
}

// ---------- labels ----------

func (s *Seeder) ensureLabel(ctx context.Context, projectID int, name, color string) (int, error) {
	var existing []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := s.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/labels", projectID), nil, &existing); err != nil {
		return 0, err
	}
	for _, l := range existing {
		if l.Name == name {
			return l.ID, nil
		}
	}
	var created struct {
		ID int `json:"id"`
	}
	body := map[string]any{"name": name, "color": color}
	if err := s.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/labels", projectID), body, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

// ---------- board ----------

func (s *Seeder) ensureBoard(ctx context.Context, projectID int, name string) (int, error) {
	var existing []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := s.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/boards", projectID), nil, &existing); err != nil {
		return 0, err
	}
	for _, b := range existing {
		if b.Name == name {
			return b.ID, nil
		}
	}
	var created struct {
		ID int `json:"id"`
	}
	body := map[string]any{"name": name}
	if err := s.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/boards", projectID), body, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

// ---------- issues ----------

func (s *Seeder) ensureIssue(ctx context.Context, projectID int, title, body, labels string) (int, error) {
	q := url.Values{}
	q.Set("search", title)
	q.Set("in", "title")

	var existing []struct {
		IID   int    `json:"iid"`
		Title string `json:"title"`
	}
	if err := s.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/issues?%s", projectID, q.Encode()), nil, &existing); err != nil {
		return 0, err
	}
	for _, i := range existing {
		if i.Title == title {
			return i.IID, nil
		}
	}
	req := map[string]any{
		"title":       title,
		"description": body,
		"labels":      labels,
	}
	var created struct {
		IID int `json:"iid"`
	}
	if err := s.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/issues", projectID), req, &created); err != nil {
		return 0, err
	}
	return created.IID, nil
}

// ---------- MR ----------

func (s *Seeder) ensureMR(ctx context.Context, projectID int, sourceBranch, targetBranch, title string) (int, error) {
	// 1. Find existing MR with this title.
	q := url.Values{}
	q.Set("source_branch", sourceBranch)
	q.Set("state", "all")

	var existing []struct {
		IID   int    `json:"iid"`
		Title string `json:"title"`
	}
	if err := s.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/merge_requests?%s", projectID, q.Encode()), nil, &existing); err != nil {
		return 0, err
	}
	for _, m := range existing {
		if m.Title == title {
			return m.IID, nil
		}
	}

	// 2. Create branch off target (idempotent).
	branchReq := map[string]any{"branch": sourceBranch, "ref": targetBranch}
	if err := s.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/repository/branches", projectID), branchReq, nil); err != nil {
		// 400 is returned when the branch already exists — tolerate.
		var apiErr *apiError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			return 0, fmt.Errorf("create branch: %w", err)
		}
	}

	// 3. Commit a fixture file on the new branch so GitLab will accept the MR.
	commitReq := map[string]any{
		"branch":         sourceBranch,
		"commit_message": "ci-test-mr fixture",
		"actions": []map[string]any{
			{
				"action":    "create",
				"file_path": fmt.Sprintf("ci-test-%d.md", time.Now().UnixNano()),
				"content":   "fixture\n",
			},
		},
	}
	if err := s.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/repository/commits", projectID), commitReq, nil); err != nil {
		var apiErr *apiError
		if !errors.As(err, &apiErr) || apiErr.Status >= 500 {
			return 0, fmt.Errorf("create commit: %w", err)
		}
	}

	// 4. Create MR.
	mrReq := map[string]any{
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"title":         title,
		"description":   "Fixture MR created by the integration seeder.",
	}
	var created struct {
		IID int `json:"iid"`
	}
	if err := s.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/merge_requests", projectID), mrReq, &created); err != nil {
		return 0, err
	}
	return created.IID, nil
}

// ---------- low-level HTTP ----------

type apiError struct {
	Status int
	Body   string
	Op     string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("gitlab %s: status %d: %s", e.Op, e.Status, e.Body)
}

// doJSON issues a request and (optionally) decodes the JSON response. When
// out is nil the response body is drained and discarded. Non-2xx responses
// return an *apiError so callers can switch on status code.
func (s *Seeder) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.BaseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", s.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("do %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &apiError{Status: resp.StatusCode, Body: string(snippet), Op: method + " " + path}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// FormatJSON marshals a Fixtures struct to indented JSON suitable for stdout
// capture by the harness.
func FormatJSON(f *Fixtures) ([]byte, error) {
	return json.MarshalIndent(f, "", "  ")
}
