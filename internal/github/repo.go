package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
type RepoService struct {
	client *Client
}

// NewRepoService wraps a *Client as a RepoService.
func NewRepoService(client *Client) *RepoService {
	return &RepoService{client: client}
}

// RepoMetadata returns the canonical name/owner pair and default branch for
// the named repository, over REST with conditional requests: `GET
// /repos/{o}/{r}` plus `GET /repos/{o}/{r}/branches/{default}`. Both carry
// ETags, so the attention sweep asking every repository every pass pays
// nothing while neither changes — and the answer survives a daemon restart
// through the persistent ConditionalStore.
//
// Why TWO requests. REST's `default_branch` names a branch even for a
// repository that has none — measured on one repository before and after its
// first commit (2026-08-26): REST said "main" both times, while GraphQL's
// defaultBranchRef was null until the commit landed. The empty string is a
// load-bearing signal: attention/sweep.DefaultBranchHealth.Evaluate treats an
// empty DefaultBranch as "decline to observe", because guessing a branch that
// does not exist produces a 404 that reads as a producer failing forever. So
// the branch is asked for by name, and a 404 means exactly what a null
// defaultBranchRef meant: the ref does not exist, DefaultBranch stays "".
// (`size` and `pushed_at` were checked as cheaper guards and neither is one:
// `size` is 0 on a repository WITH a commit, and `pushed_at` did not move on
// push.) The second request used to be the argument against this migration —
// it doubled a one-point read. Conditional, both are free when unchanged.
//
// Pinned by TestRepoMetadata_EmptyRepoHasNoDefaultBranch.
func (r *RepoService) RepoMetadata(ctx context.Context, owner, name string) (*forgetypes.Repo, error) {
	if owner == "" || name == "" {
		return nil, fmt.Errorf("repo metadata: owner and name are required")
	}
	base := fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(name))
	type repoReduced struct {
		FullName      string `json:"fullName"`
		Owner         string `json:"owner"`
		Name          string `json:"name"`
		DefaultBranch string `json:"defaultBranch"`
	}
	resp, err := r.client.condGet(ctx, base, func(body []byte) (any, error) {
		var raw struct {
			FullName string `json:"full_name"`
			Name     string `json:"name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
			DefaultBranch string `json:"default_branch"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		return repoReduced{FullName: raw.FullName, Owner: raw.Owner.Login, Name: raw.Name, DefaultBranch: raw.DefaultBranch}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("repo view %s/%s: %w", owner, name, err)
	}
	if resp.Status != http.StatusOK && resp.Status != http.StatusNotModified {
		return nil, fmt.Errorf("repo view %s/%s: REST %d: %s", owner, name, resp.Status, restErrorSummary(resp.Body))
	}
	var meta repoReduced
	if err := json.Unmarshal(resp.Payload, &meta); err != nil {
		return nil, fmt.Errorf("repo view %s/%s: decode: %w", owner, name, err)
	}
	out := &forgetypes.Repo{NameWithOwner: meta.FullName, Owner: meta.Owner, Name: meta.Name}
	if meta.DefaultBranch == "" {
		return out, nil
	}
	br, err := r.client.condGet(ctx, base+"/branches/"+url.PathEscape(meta.DefaultBranch), func(body []byte) (any, error) {
		var raw struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		return raw.Name, nil
	})
	if err != nil {
		return nil, fmt.Errorf("repo view %s/%s: default branch: %w", owner, name, err)
	}
	switch br.Status {
	case http.StatusOK, http.StatusNotModified:
		out.DefaultBranch = meta.DefaultBranch
	case http.StatusNotFound:
		// No such ref: a repository with no commits. DefaultBranch stays "".
	default:
		return nil, fmt.Errorf("repo view %s/%s: default branch: REST %d: %s", owner, name, br.Status, restErrorSummary(br.Body))
	}
	return out, nil
}

// defaultBranchFilesLimit caps how many paths one DefaultBranchFiles query
// asks for, each an alias of its own in the query.
const defaultBranchFilesLimit = 16

// treeEntryModeRegular and treeEntryModeExecutable are the modes GraphQL's
// TreeEntry.mode reports for a regular file (0100644 and 0100755); a symbolic
// link is 0120000.
const (
	treeEntryModeRegular    = 0o100644
	treeEntryModeExecutable = 0o100755
)

// DefaultBranchFiles satisfies forge.DefaultBranchFileService: one GraphQL
// query reads the default branch's name, the commit at its head and each of
// paths at that commit (Commit.file), so every file comes from the commit the
// answer names. A path the commit does not have comes back null with a
// NOT_FOUND error on that path alone, and is left out of Files. A file GitHub
// serves truncated is an error, because it cannot be read whole; a binary
// file is Regular with no Content.
func (r *RepoService) DefaultBranchFiles(ctx context.Context, owner, name string, paths []string) (*forgetypes.DefaultBranchFiles, error) {
	if owner == "" || name == "" {
		return nil, fmt.Errorf("default branch files: owner and name are required")
	}
	if len(paths) == 0 || len(paths) > defaultBranchFilesLimit {
		return nil, fmt.Errorf("default branch files: %d paths asked for, want 1 to %d", len(paths), defaultBranchFilesLimit)
	}
	var decl, fields strings.Builder
	vars := map[string]interface{}{"owner": owner, "name": name}
	for i, p := range paths {
		fmt.Fprintf(&decl, ", $p%d: String!", i)
		fmt.Fprintf(&fields, " f%d: file(path: $p%d) { mode type object { ... on Blob { text isBinary isTruncated } } }", i, i)
		vars[fmt.Sprintf("p%d", i)] = p
	}
	query := "query($owner: String!, $name: String!" + decl.String() + ") { repository(owner: $owner, name: $name) { defaultBranchRef { name target { oid ... on Commit {" + fields.String() + " } } } } }"
	raw, err := r.client.queryRaw(ctx, query, vars)
	if err != nil {
		return nil, fmt.Errorf("default branch files of %s/%s: %w", owner, name, err)
	}
	var resp struct {
		Data struct {
			Repository *struct {
				DefaultBranchRef *struct {
					Name   string                     `json:"name"`
					Target map[string]json.RawMessage `json:"target"`
				} `json:"defaultBranchRef"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Type    string        `json:"type"`
			Path    []interface{} `json:"path"`
			Message string        `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("default branch files of %s/%s: decode: %w", owner, name, err)
	}
	absent := map[string]bool{}
	for _, e := range resp.Errors {
		if alias, ok := missingFileAlias(e.Type, e.Path); ok {
			absent[alias] = true
			continue
		}
		return nil, fmt.Errorf("default branch files of %s/%s: %s", owner, name, e.Message)
	}
	repo := resp.Data.Repository
	if repo == nil {
		return nil, fmt.Errorf("default branch files of %s/%s: the repository is not visible", owner, name)
	}
	ref := repo.DefaultBranchRef
	if ref == nil || ref.Name == "" {
		return nil, fmt.Errorf("default branch files of %s/%s: the repository has no default branch", owner, name)
	}
	var oid string
	if err := json.Unmarshal(ref.Target["oid"], &oid); err != nil || oid == "" {
		return nil, fmt.Errorf("default branch files of %s/%s: no commit at the head of %s", owner, name, ref.Name)
	}
	out := &forgetypes.DefaultBranchFiles{Branch: ref.Name, Commit: oid, Files: map[string]forgetypes.RepoFile{}}
	for i, p := range paths {
		alias := fmt.Sprintf("f%d", i)
		entryRaw, ok := ref.Target[alias]
		if !ok {
			return nil, fmt.Errorf("default branch files of %s/%s: the answer has no entry for %s", owner, name, p)
		}
		var entry *struct {
			Mode   int    `json:"mode"`
			Type   string `json:"type"`
			Object *struct {
				Text        *string `json:"text"`
				IsBinary    bool    `json:"isBinary"`
				IsTruncated bool    `json:"isTruncated"`
			} `json:"object"`
		}
		if err := json.Unmarshal(entryRaw, &entry); err != nil {
			return nil, fmt.Errorf("default branch files of %s/%s: decode %s: %w", owner, name, p, err)
		}
		if entry == nil {
			if !absent[alias] {
				return nil, fmt.Errorf("default branch files of %s/%s: %s came back empty without saying it is absent", owner, name, p)
			}
			continue
		}
		regular := entry.Type == "blob" && (entry.Mode == treeEntryModeRegular || entry.Mode == treeEntryModeExecutable)
		if !regular {
			out.Files[p] = forgetypes.RepoFile{}
			continue
		}
		if entry.Object == nil {
			return nil, fmt.Errorf("default branch files of %s/%s: %s has no blob", owner, name, p)
		}
		if entry.Object.IsTruncated {
			return nil, fmt.Errorf("default branch files of %s/%s: GitHub serves %s truncated", owner, name, p)
		}
		file := forgetypes.RepoFile{Regular: true}
		if entry.Object.Text != nil && !entry.Object.IsBinary {
			file.Content = []byte(*entry.Object.Text)
		}
		out.Files[p] = file
	}
	return out, nil
}

// missingFileAlias reports the alias of a DefaultBranchFiles query's file
// field that a GraphQL error says is absent: a NOT_FOUND at
// repository.defaultBranchRef.target.<alias>.
func missingFileAlias(errType string, path []interface{}) (string, bool) {
	if errType != "NOT_FOUND" || len(path) != 4 {
		return "", false
	}
	want := []string{"repository", "defaultBranchRef", "target"}
	for i, w := range want {
		if s, ok := path[i].(string); !ok || s != w {
			return "", false
		}
	}
	alias, ok := path[3].(string)
	if !ok || !strings.HasPrefix(alias, "f") {
		return "", false
	}
	return alias, true
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
