package github

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shurcooL/graphql"

	"github.com/nightgauge/nightgauge/internal/trust"
)

// TrustForge reads what internal/trust decides on (#2130): a user's
// permission on a repository, and an issue's author, labels, last body edit
// and label/title timeline. Every read goes through the Client's own rate
// limiting and retry; nothing here retries on its own.
type TrustForge struct {
	resolve func(ctx context.Context, owner, repo string) (*Client, error)
}

// NewTrustForge reads through one client.
func NewTrustForge(c *Client) *TrustForge {
	return &TrustForge{resolve: func(context.Context, string, string) (*Client, error) { return c, nil }}
}

// NewTrustForgeResolver reads each repository through the client resolve
// returns for it, so a repository whose configured identity differs from the
// startup client's is read with the right token.
func NewTrustForgeResolver(resolve func(ctx context.Context, owner, repo string) (*Client, error)) *TrustForge {
	return &TrustForge{resolve: resolve}
}

func (t *TrustForge) client(ctx context.Context, owner, repo string) (*Client, error) {
	c, err := t.resolve(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("resolve a client for %s/%s: %w", owner, repo, err)
	}
	if c == nil {
		return nil, fmt.Errorf("no client for %s/%s", owner, repo)
	}
	return c, nil
}

// RepoPermission asks GitHub for login's role on owner/repo
// (GET /repos/{owner}/{repo}/collaborators/{login}/permission). Any non-2xx
// answer, 403 and 404 included, is an error, which the gate treats as
// untrusted.
func (t *TrustForge) RepoPermission(ctx context.Context, owner, repo, login string) (trust.Level, error) {
	if owner == "" || repo == "" || login == "" {
		return "", fmt.Errorf("permission lookup needs an owner, a repository and a login")
	}
	c, err := t.client(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/repos/%s/%s/collaborators/%s/permission",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(login))
	body, status, err := c.restDoStatus(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", fmt.Errorf("permission of %s on %s/%s: %w", login, owner, repo, err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("permission of %s on %s/%s: REST %d: %s",
			login, owner, repo, status, restErrorSummary(body))
	}
	var resp struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
		User       *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("permission of %s on %s/%s: decode: %w", login, owner, repo, err)
	}
	// The answer must be about the login asked for. A proxy or a renamed
	// account answering for someone else must not grant anything.
	if resp.User != nil && resp.User.Login != "" && !strings.EqualFold(resp.User.Login, login) {
		return "", fmt.Errorf("permission of %s on %s/%s: answer names %s", login, owner, repo, resp.User.Login)
	}
	return trust.ParseLevel(resp.RoleName, resp.Permission), nil
}

// trustActor is a GraphQL actor; a Bot's login is reported without the
// "[bot]" suffix REST uses, so actorLogin restores it.
type trustActor struct {
	Login    graphql.String
	Typename graphql.String `graphql:"__typename"`
}

func actorLogin(a *trustActor) string {
	if a == nil {
		return ""
	}
	login := string(a.Login)
	if login != "" && string(a.Typename) == "Bot" && !strings.HasSuffix(login, "[bot]") {
		login += "[bot]"
	}
	return login
}

type trustSignalsQuery struct {
	Repository struct {
		Issue *struct {
			Author       *trustActor
			LastEditedAt *graphql.String
			Labels       struct {
				Nodes []struct {
					Name graphql.String
				}
			} `graphql:"labels(first: 100)"`
			TimelineItems struct {
				Nodes []struct {
					Typename     graphql.String `graphql:"__typename"`
					LabeledEvent struct {
						CreatedAt graphql.String
						Actor     *trustActor
						Label     struct{ Name graphql.String }
					} `graphql:"... on LabeledEvent"`
					UnlabeledEvent struct {
						CreatedAt graphql.String
						Actor     *trustActor
						Label     struct{ Name graphql.String }
					} `graphql:"... on UnlabeledEvent"`
					RenamedTitleEvent struct {
						CreatedAt graphql.String
						Actor     *trustActor
					} `graphql:"... on RenamedTitleEvent"`
				}
			} `graphql:"timelineItems(last: 100, itemTypes: [LABELED_EVENT, UNLABELED_EVENT, RENAMED_TITLE_EVENT])"`
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// IssueSignals reads one issue's author, labels, last body edit and its most
// recent 100 label and title events in one GraphQL query. Only an issue whose
// author cannot vouch for it and which carries the approval label is read.
func (t *TrustForge) IssueSignals(ctx context.Context, owner, repo string, number int) (trust.Signals, error) {
	// GraphQL's Int is 32 bits; a number outside it names no issue.
	if number <= 0 || number > math.MaxInt32 {
		return trust.Signals{}, fmt.Errorf("read %s/%s#%d: not an issue number", owner, repo, number)
	}
	c, err := t.client(ctx, owner, repo)
	if err != nil {
		return trust.Signals{}, err
	}
	var q trustSignalsQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphql.Int(number),
	}
	if err := c.Query(ctx, &q, vars); err != nil {
		return trust.Signals{}, fmt.Errorf("read %s/%s#%d: %w", owner, repo, number, err)
	}
	iss := q.Repository.Issue
	if iss == nil {
		return trust.Signals{}, fmt.Errorf("read %s/%s#%d: no such issue", owner, repo, number)
	}
	parse := func(s graphql.String) (time.Time, error) {
		return time.Parse(time.RFC3339, string(s))
	}
	sig := trust.Signals{Author: actorLogin(iss.Author)}
	if iss.LastEditedAt != nil && *iss.LastEditedAt != "" {
		at, err := parse(*iss.LastEditedAt)
		if err != nil {
			return trust.Signals{}, fmt.Errorf("read %s/%s#%d: lastEditedAt: %w", owner, repo, number, err)
		}
		sig.BodyEditedAt = at
	}
	for _, l := range iss.Labels.Nodes {
		sig.Labels = append(sig.Labels, string(l.Name))
	}
	for _, n := range iss.TimelineItems.Nodes {
		var e trust.Event
		var raw graphql.String
		switch string(n.Typename) {
		case "LabeledEvent":
			e = trust.Event{Kind: trust.EventLabeled, Label: string(n.LabeledEvent.Label.Name), Actor: actorLogin(n.LabeledEvent.Actor)}
			raw = n.LabeledEvent.CreatedAt
		case "UnlabeledEvent":
			e = trust.Event{Kind: trust.EventUnlabeled, Label: string(n.UnlabeledEvent.Label.Name), Actor: actorLogin(n.UnlabeledEvent.Actor)}
			raw = n.UnlabeledEvent.CreatedAt
		case "RenamedTitleEvent":
			e = trust.Event{Kind: trust.EventRenamed, Actor: actorLogin(n.RenamedTitleEvent.Actor)}
			raw = n.RenamedTitleEvent.CreatedAt
		default:
			continue
		}
		at, err := parse(raw)
		if err != nil {
			// An event the gate cannot place in time cannot be ordered against
			// an approval, so the whole read fails (and fails closed).
			return trust.Signals{}, fmt.Errorf("read %s/%s#%d: %s time: %w", owner, repo, number, e.Kind, err)
		}
		e.At = at
		sig.Events = append(sig.Events, e)
	}
	return sig, nil
}

// maxCommentPages bounds one issue's comment read at 1,000 comments.
const maxCommentPages = 10

// IssueComments lists an issue's comments, oldest first
// (GET /repos/{owner}/{repo}/issues/{number}/comments). A bot's login keeps
// the "[bot]" suffix REST gives it.
func (t *TrustForge) IssueComments(ctx context.Context, owner, repo string, number int) ([]trust.Comment, error) {
	c, err := t.client(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	var out []trust.Comment
	for page := 1; page <= maxCommentPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d",
			url.PathEscape(owner), url.PathEscape(repo), number, page)
		body, status, err := c.restDoStatus(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("comments of %s/%s#%d: %w", owner, repo, number, err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("comments of %s/%s#%d: REST %d: %s",
				owner, repo, number, status, restErrorSummary(body))
		}
		var batch []struct {
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"created_at"`
			User      *struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("comments of %s/%s#%d: decode: %w", owner, repo, number, err)
		}
		for _, b := range batch {
			cm := trust.Comment{Body: b.Body, CreatedAt: b.CreatedAt}
			if b.User != nil {
				cm.Author = b.User.Login
			}
			out = append(out, cm)
		}
		if len(batch) < 100 {
			return out, nil
		}
	}
	return out, nil
}

// OpenIssue is one open issue as the eligibility report reads it.
type OpenIssue struct {
	Number int
	Author string
	Labels []string
}

// maxOpenIssuePages bounds the eligibility report's read at 2,000 issues.
const maxOpenIssuePages = 20

// OpenIssues lists a repository's open issues, pull requests excluded, with
// each author's login and labels (GET /repos/{owner}/{repo}/issues).
func (t *TrustForge) OpenIssues(ctx context.Context, owner, repo string) ([]OpenIssue, error) {
	c, err := t.client(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	var out []OpenIssue
	for page := 1; page <= maxOpenIssuePages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues?state=open&per_page=100&page=%d",
			url.PathEscape(owner), url.PathEscape(repo), page)
		body, status, err := c.restDoStatus(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("open issues of %s/%s: %w", owner, repo, err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("open issues of %s/%s: REST %d: %s", owner, repo, status, restErrorSummary(body))
		}
		var batch []struct {
			Number int `json:"number"`
			User   *struct {
				Login string `json:"login"`
			} `json:"user"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
			PullRequest *json.RawMessage `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("open issues of %s/%s: decode: %w", owner, repo, err)
		}
		for _, b := range batch {
			if b.PullRequest != nil {
				continue
			}
			oi := OpenIssue{Number: b.Number}
			if b.User != nil {
				oi.Author = b.User.Login
			}
			for _, l := range b.Labels {
				oi.Labels = append(oi.Labels, l.Name)
			}
			out = append(out, oi)
		}
		if len(batch) < 100 {
			return out, nil
		}
	}
	return out, fmt.Errorf("open issues of %s/%s: more than %d; the report would be partial", owner, repo, maxOpenIssuePages*100)
}
