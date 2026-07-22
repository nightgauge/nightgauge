package forgecmd

import (
	"time"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// IssueJSON is the gh-aligned output shape for `forge issue view|list`.
// Field names mirror `gh issue view --json
// number,title,body,state,labels,assignees,url,author,createdAt,updatedAt,
// closedAt,milestone,comments` so jq pipelines parsing the gh output
// can be reused verbatim.
type IssueJSON struct {
	V         int           `json:"v"`
	Number    int           `json:"number"`
	NodeID    string        `json:"nodeId,omitempty"`
	Title     string        `json:"title"`
	Body      string        `json:"body"`
	State     string        `json:"state"`
	Labels    []LabelJSON   `json:"labels"`
	Assignees []ActorJSON   `json:"assignees"`
	URL       string        `json:"url"`
	Author    *ActorJSON    `json:"author"`
	Milestone *MilestoneRef `json:"milestone"`
	IsEpic    bool          `json:"isEpic,omitempty"`
	CreatedAt *time.Time    `json:"createdAt"`
	UpdatedAt *time.Time    `json:"updatedAt"`
	ClosedAt  *time.Time    `json:"closedAt"`
	Comments  []CommentJSON `json:"comments"`
}

// PRJSON is the gh-aligned output shape for `forge pr view|list`.
type PRJSON struct {
	V           int           `json:"v"`
	Number      int           `json:"number"`
	NodeID      string        `json:"nodeId,omitempty"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	State       string        `json:"state"`
	IsDraft     bool          `json:"isDraft"`
	HeadRefName string        `json:"headRefName"`
	BaseRefName string        `json:"baseRefName"`
	URL         string        `json:"url"`
	Mergeable   string        `json:"mergeable"`
	Author      *ActorJSON    `json:"author"`
	Labels      []LabelJSON   `json:"labels"`
	Assignees   []ActorJSON   `json:"assignees"`
	Additions   int           `json:"additions"`
	Deletions   int           `json:"deletions"`
	CreatedAt   *time.Time    `json:"createdAt"`
	UpdatedAt   *time.Time    `json:"updatedAt"`
	ClosedAt    *time.Time    `json:"closedAt"`
	MergedAt    *time.Time    `json:"mergedAt"`
	Comments    []CommentJSON `json:"comments"`
}

// LabelJSON is the gh-aligned label shape used inside IssueJSON / PRJSON.
type LabelJSON struct {
	V           int    `json:"v"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color,omitempty"`
}

// ActorJSON is the gh-aligned actor shape (used for assignees, authors).
type ActorJSON struct {
	Login string `json:"login"`
}

// MilestoneRef is a thin reference to a milestone. gh's `--json
// milestone` returns a shaped object so we mirror the field names.
type MilestoneRef struct {
	Title string     `json:"title"`
	State string     `json:"state,omitempty"`
	DueOn *time.Time `json:"dueOn,omitempty"`
}

// CommentJSON is the per-comment shape returned by gh; we leave it
// empty by default (the existing IssueService API does not return
// comments) but expose the field so consumers do not break when the
// adapter starts populating it.
type CommentJSON struct {
	Author    ActorJSON  `json:"author"`
	Body      string     `json:"body"`
	CreatedAt *time.Time `json:"createdAt"`
}

// CheckRollupJSON is the gh-aligned check rollup for `forge pr checks`.
// Schema is versioned (v: 1) so future bumps don't break the
// pipeline-audit skill.
type CheckRollupJSON struct {
	V                  int            `json:"v"`
	Number             int            `json:"number"`
	State              string         `json:"state"`
	Total              int            `json:"total"`
	Completed          int            `json:"completed"`
	Successful         int            `json:"successful"`
	Failed             int            `json:"failed"`
	Pending            int            `json:"pending"`
	IsTerminal         bool           `json:"isTerminal"`
	ElapsedSecs        int            `json:"elapsedSecs"`
	RequiredPassed     bool           `json:"requiredPassed"`
	RequiredCheckNames []string       `json:"requiredCheckNames"`
	Checks             []CheckRunJSON `json:"checks"`
}

// CheckRunJSON is one row of CheckRollupJSON.checks.
type CheckRunJSON struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Required   bool   `json:"required"`
}

// ProjectFieldJSON is the gh-aligned shape for `forge project field-list`.
type ProjectFieldJSON struct {
	V       int               `json:"v"`
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Options map[string]string `json:"options,omitempty"`
}

// ProjectFieldValueJSON is the shape returned by `forge project field-get`.
type ProjectFieldValueJSON struct {
	V     int    `json:"v"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// BoardItemJSON is the gh-aligned shape for `forge project item-list`.
type BoardItemJSON struct {
	V             int      `json:"v"`
	Number        int      `json:"number"`
	NodeID        string   `json:"nodeId,omitempty"`
	Title         string   `json:"title"`
	State         string   `json:"state"`
	Status        string   `json:"status"`
	Priority      string   `json:"priority"`
	Size          string   `json:"size"`
	PipelineStage string   `json:"pipelineStage,omitempty"`
	Labels        []string `json:"labels"`
	Repo          string   `json:"repo"`
	URL           string   `json:"url"`
	IsPR          bool     `json:"isPR"`
	IsEpic        bool     `json:"isEpic"`
}

// AuthStatusJSON is the JSON shape for `forge auth status`.
type AuthStatusJSON struct {
	V             int      `json:"v"`
	Login         string   `json:"login"`
	Scopes        []string `json:"scopes"`
	Missing       []string `json:"missing"`
	MaskedToken   string   `json:"masked_token"`
	Source        string   `json:"source"`
	OrgMembership []string `json:"org_membership,omitempty"`
	Valid         bool     `json:"valid"`
}

// WhoamiJSON is the JSON shape for `forge auth whoami`. Mirrors
// `gh api user --jq '{login: .login}'`. Only the login is returned by
// design — `forge auth status` is the verb for the broader
// scope/source/validity surface.
type WhoamiJSON struct {
	V     int    `json:"v"`
	Login string `json:"login"`
}

// AuthAssertJSON is the JSON shape for `forge auth assert`. Reports whether the
// resolved per-repo identity matches the configured github_user and has the
// required access on the target repo. `ok` is the single boolean gate skills
// branch on; `reason` carries the specific blocker when ok=false.
type AuthAssertJSON struct {
	V             int    `json:"v"`
	OK            bool   `json:"ok"`
	Repo          string `json:"repo"`
	ExpectedLogin string `json:"expected_login"`
	ActualLogin   string `json:"actual_login"`
	HasPush       bool   `json:"has_push"`
	HasAdmin      bool   `json:"has_admin"`
	AdminRequired bool   `json:"admin_required"`
	Reason        string `json:"reason,omitempty"`
	Remediation   string `json:"remediation,omitempty"`
}

// RepoJSON is the JSON shape for `forge repo view`. Mirrors
// `gh repo view --json nameWithOwner,owner,name` so jq pipelines parsing
// the gh output can be reused verbatim.
type RepoJSON struct {
	V             int    `json:"v"`
	NameWithOwner string `json:"nameWithOwner"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
}

// RepoFromForge converts a forge-agnostic Repo into RepoJSON.
func RepoFromForge(r *forgetypes.Repo) RepoJSON {
	if r == nil {
		return RepoJSON{V: 1}
	}
	return RepoJSON{
		V:             1,
		NameWithOwner: r.NameWithOwner,
		Owner:         r.Owner,
		Name:          r.Name,
	}
}

// ActorFromForge converts a forge-agnostic Actor into a WhoamiJSON.
// Returns the zero-value (with v=1, login="") for nil input so the
// JSON envelope shape is stable.
func ActorFromForge(a *forgetypes.Actor) WhoamiJSON {
	if a == nil {
		return WhoamiJSON{V: 1}
	}
	return WhoamiJSON{V: 1, Login: a.Login}
}

// IssueFromForge converts a forge-agnostic Issue into the gh-aligned
// IssueJSON. Times are NOT populated by the existing IssueService —
// they remain nil so callers see `null` (matching gh's behaviour for
// fields it could not fetch).
func IssueFromForge(i *forgetypes.Issue) IssueJSON {
	if i == nil {
		return IssueJSON{V: 1, Labels: []LabelJSON{}, Assignees: []ActorJSON{}, Comments: []CommentJSON{}}
	}
	labels := make([]LabelJSON, 0, len(i.Labels))
	for _, name := range i.Labels {
		labels = append(labels, LabelJSON{V: 1, Name: name})
	}
	assignees := make([]ActorJSON, 0, len(i.Assignees))
	for _, login := range i.Assignees {
		assignees = append(assignees, ActorJSON{Login: login})
	}
	var milestone *MilestoneRef
	if i.Milestone != "" {
		milestone = &MilestoneRef{Title: i.Milestone}
	}
	return IssueJSON{
		V:         1,
		Number:    i.Number,
		NodeID:    i.NodeID,
		Title:     i.Title,
		Body:      i.Body,
		State:     i.State,
		Labels:    labels,
		Assignees: assignees,
		URL:       i.URL,
		Author:    nil,
		Milestone: milestone,
		IsEpic:    i.IsEpic,
		Comments:  []CommentJSON{},
	}
}

// PRFromForge converts a forge-agnostic PullRequest into PRJSON.
func PRFromForge(pr *forgetypes.PullRequest) PRJSON {
	if pr == nil {
		return PRJSON{V: 1, Labels: []LabelJSON{}, Assignees: []ActorJSON{}, Comments: []CommentJSON{}}
	}
	labels := make([]LabelJSON, 0, len(pr.Labels))
	for _, name := range pr.Labels {
		labels = append(labels, LabelJSON{V: 1, Name: name})
	}
	return PRJSON{
		V:           1,
		Number:      pr.Number,
		NodeID:      pr.NodeID,
		Title:       pr.Title,
		Body:        pr.Body,
		State:       pr.State,
		IsDraft:     pr.IsDraft,
		HeadRefName: pr.HeadRef,
		BaseRefName: pr.BaseRef,
		URL:         pr.URL,
		Mergeable:   pr.Mergeable,
		Labels:      labels,
		Assignees:   []ActorJSON{},
		Additions:   pr.Additions,
		Deletions:   pr.Deletions,
		Comments:    []CommentJSON{},
	}
}

// LabelFromForge converts a forge-agnostic Label into LabelJSON.
func LabelFromForge(l *forgetypes.Label) LabelJSON {
	if l == nil {
		return LabelJSON{V: 1}
	}
	return LabelJSON{V: 1, ID: l.ID, Name: l.Name, Description: l.Description, Color: l.Color}
}

// CheckRollupFromForge converts a forge-agnostic CheckStatus into the
// schema-versioned rollup DTO.
func CheckRollupFromForge(cs *forgetypes.CheckStatus) CheckRollupJSON {
	if cs == nil {
		return CheckRollupJSON{V: 1, RequiredCheckNames: []string{}, Checks: []CheckRunJSON{}}
	}
	checks := make([]CheckRunJSON, 0, len(cs.Checks))
	for _, c := range cs.Checks {
		checks = append(checks, CheckRunJSON{
			Name:       c.Name,
			Status:     c.Status,
			Conclusion: c.Conclusion,
			Required:   c.Required,
		})
	}
	required := cs.RequiredCheckNames
	if required == nil {
		required = []string{}
	}
	return CheckRollupJSON{
		V:                  1,
		Number:             cs.PRNumber,
		State:              cs.State,
		Total:              cs.Total,
		Completed:          cs.Completed,
		Successful:         cs.Successful,
		Failed:             cs.Failed,
		Pending:            cs.Pending,
		IsTerminal:         cs.IsTerminal,
		ElapsedSecs:        cs.ElapsedSecs,
		RequiredPassed:     cs.RequiredPassed,
		RequiredCheckNames: required,
		Checks:             checks,
	}
}

// BoardItemFromForge converts a forge-agnostic BoardItem into BoardItemJSON.
func BoardItemFromForge(b *forgetypes.BoardItem) BoardItemJSON {
	if b == nil {
		return BoardItemJSON{V: 1, Labels: []string{}}
	}
	labels := b.Labels
	if labels == nil {
		labels = []string{}
	}
	return BoardItemJSON{
		V:             1,
		Number:        b.Number,
		NodeID:        b.NodeID,
		Title:         b.Title,
		State:         b.State,
		Status:        b.Status,
		Priority:      string(b.Priority),
		Size:          string(b.Size),
		PipelineStage: b.PipelineStage,
		Labels:        labels,
		Repo:          b.Repo,
		URL:           b.URL,
		IsPR:          b.IsPR,
		IsEpic:        b.IsEpic,
	}
}

// MaskToken returns a fixed-format masked rendering of a token —
// "<first4>****<last4>" — so logs never carry the raw value. Tokens
// shorter than 8 characters are masked entirely.
func MaskToken(token string) string {
	if len(token) <= 8 {
		return "****"
	}
	return token[:4] + "****" + token[len(token)-4:]
}
