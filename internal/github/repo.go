package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/shurcooL/graphql"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// collaboratorPermission fetches login's permission level on owner/repo via the
// REST collaborator-permission endpoint. It returns found=false for a definitive
// 404 — the login is NOT a collaborator, a CONFIRMED absence of access (not an
// infra/visibility hiccup), so the identity preflight fails CLOSED on the
// dominant "lacks push" case instead of mistaking it for a transient error
// (#4068). Any other non-2xx / transport failure returns a non-nil error so the
// caller can decide (the preflight treats those as infra and fails open; `forge
// auth assert` fails closed).
func (c *Client) collaboratorPermission(ctx context.Context, login, owner, repo string) (permission string, found bool, err error) {
	path := fmt.Sprintf("/repos/%s/%s/collaborators/%s/permission", owner, repo, login)
	data, status, err := c.restDoStatus(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", false, fmt.Errorf("collaborator permission check: %w", err)
	}
	if status == http.StatusNotFound {
		return "", false, nil // not a collaborator → confirmed no access
	}
	if status < 200 || status >= 300 {
		return "", false, fmt.Errorf("collaborator permission check: status %d: %s", status, string(data))
	}
	var resp struct {
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", false, fmt.Errorf("collaborator permission decode: %w", err)
	}
	return resp.Permission, true, nil
}

// RepoService is the github adapter's implementation of forge.RepoService.
// It performs a single GraphQL query equivalent to `gh repo view --json
// nameWithOwner,owner,name`.
type RepoService struct {
	client *Client
}

// NewRepoService wraps a *Client as a RepoService.
func NewRepoService(client *Client) *RepoService {
	return &RepoService{client: client}
}

// RepoMetadata returns the canonical name/owner pair for the named
// repository. The implementation issues a single repository(owner,name)
// GraphQL query.
func (r *RepoService) RepoMetadata(ctx context.Context, owner, name string) (*forgetypes.Repo, error) {
	if owner == "" || name == "" {
		return nil, fmt.Errorf("repo metadata: owner and name are required")
	}
	var q struct {
		Repository struct {
			NameWithOwner graphql.String
			Owner         struct{ Login graphql.String }
			Name          graphql.String
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(name),
	}
	if err := r.client.Query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("repo view %s/%s: %w", owner, name, err)
	}
	return &forgetypes.Repo{
		NameWithOwner: string(q.Repository.NameWithOwner),
		Owner:         string(q.Repository.Owner.Login),
		Name:          string(q.Repository.Name),
	}, nil
}

// ExecuteGraphQL satisfies forge.GraphQLService — the github adapter
// exposes its raw GraphQL transport so the `forge graphql` pass-through
// subcommand can route ad-hoc queries (e.g. addSubIssue, addBlockedBy)
// through the same authenticated, rate-limited client used by every
// other operation. Returns the raw JSON envelope verbatim.
func (c *Client) ExecuteGraphQL(ctx context.Context, query string, variables map[string]interface{}) ([]byte, error) {
	return c.queryRaw(ctx, query, variables)
}

// HasRepoWriteAccess returns true when login has write (or admin) permission
// on the named repository. It calls the GitHub REST collaborator permission
// endpoint: GET /repos/{owner}/{repo}/collaborators/{username}/permission.
//
// On API error the caller should treat the result as denied (fail-closed).
// The method itself returns (false, err) so the caller can log the cause.
func (c *Client) HasRepoWriteAccess(ctx context.Context, login, owner, repo string) (bool, error) {
	permission, found, err := c.collaboratorPermission(ctx, login, owner, repo)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil // 404 → not a collaborator → confirmed no write
	}
	return permission == "admin" || permission == "write", nil
}

// HasRepoAdminAccess returns true when login has admin permission on the named
// repository — the level required to bypass a required-review ruleset / branch
// protection. It calls the same collaborator permission endpoint as
// HasRepoWriteAccess but only treats "admin" as a grant.
//
// On API error the caller should treat the result as denied (fail-closed); the
// method returns (false, err) so the caller can surface the cause.
func (c *Client) HasRepoAdminAccess(ctx context.Context, login, owner, repo string) (bool, error) {
	permission, found, err := c.collaboratorPermission(ctx, login, owner, repo)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil // 404 → not a collaborator → confirmed no admin
	}
	return permission == "admin", nil
}
