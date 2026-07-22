package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
)

// CommandTier classifies a command's permission requirements.
type CommandTier int

const (
	// TierRead — status, health, queue.list, help, unknown: any mapped user.
	TierRead CommandTier = iota
	// TierWrite — run, pause, resume, stop, queue.add, queue.remove: requires repo write access.
	TierWrite
)

// commandTiers maps command type strings to their required permission tier.
var commandTiers = map[string]CommandTier{
	"status":       TierRead,
	"health":       TierRead,
	"queue.list":   TierRead,
	"help":         TierRead,
	"unknown":      TierRead,
	"run":          TierWrite,
	"pause":        TierWrite,
	"resume":       TierWrite,
	"stop":         TierWrite,
	"queue.add":    TierWrite,
	"queue.remove": TierWrite,
}

// CommandTierFor returns the permission tier for a given command type string.
// Unrecognised commands default to TierRead (safe — unknown commands produce
// a "command not found" response regardless of authorization).
func CommandTierFor(commandType string) CommandTier {
	if t, ok := commandTiers[commandType]; ok {
		return t
	}
	return TierRead
}

// AuthResult is the outcome of an Authorizer.Authorize call.
type AuthResult struct {
	Allowed        bool
	MappedIdentity string // empty if unmapped
	Reason         string // human-readable for audit + response
}

// RepoPermissionChecker verifies whether a user has write access to a repo.
// Implementations: GitHubPermissionChecker (real); stubPermissionChecker (GitLab, fail-closed).
type RepoPermissionChecker interface {
	HasWriteAccess(ctx context.Context, login, owner, repo string) (bool, error)
}

// StubPermissionChecker is a fail-closed stub used for GitLab until a real
// implementation is added in a follow-up issue.
type StubPermissionChecker struct{}

// HasWriteAccess always returns false for the stub (fail closed).
func (StubPermissionChecker) HasWriteAccess(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

// RepoPermissionCheckerFunc is a function type that implements RepoPermissionChecker.
// Callers in cmd/ can use this to wrap a concrete GitHub client method without
// importing the auth package's dependencies.
type RepoPermissionCheckerFunc func(ctx context.Context, login, owner, repo string) (bool, error)

// HasWriteAccess calls the underlying function.
func (f RepoPermissionCheckerFunc) HasWriteAccess(ctx context.Context, login, owner, repo string) (bool, error) {
	return f(ctx, login, owner, repo)
}

// Authorizer orchestrates user-mapping lookup → command tier check →
// (write tier only) permission cache + API check → audit log write.
type Authorizer struct {
	store   *UserMappingStore
	cache   *PermissionCache
	audit   *AuditWriter
	ghCheck RepoPermissionChecker
}

// NewAuthorizer returns an Authorizer wired with the provided dependencies.
func NewAuthorizer(store *UserMappingStore, cache *PermissionCache, audit *AuditWriter, ghCheck RepoPermissionChecker) *Authorizer {
	return &Authorizer{
		store:   store,
		cache:   cache,
		audit:   audit,
		ghCheck: ghCheck,
	}
}

// Authorize runs the full authorization flow for a Mattermost command.
//
// Flow:
//  1. Look up mattermostUserID in UserMappingStore.
//  2. Unmapped → deny + audit.
//  3. TierRead → allow (no API call needed).
//  4. TierWrite, github_login set → check PermissionCache; on miss, call ghCheck.HasWriteAccess.
//  5. TierWrite, no github_login (gitlab-only) → deny fail-closed (stub).
//  6. Write audit entry in all cases.
func (a *Authorizer) Authorize(ctx context.Context, mattermostUserID, channelID, commandType, repoSlug string) AuthResult {
	entry, mapped := a.store.Get(mattermostUserID)

	if !mapped {
		result := AuthResult{
			Allowed:        false,
			MappedIdentity: "",
			Reason:         "unmapped",
		}
		a.writeAudit(mattermostUserID, "", channelID, commandType, "", "denied")
		return result
	}

	tier := CommandTierFor(commandType)
	identity := resolveIdentity(entry)

	if tier == TierRead {
		result := AuthResult{
			Allowed:        true,
			MappedIdentity: identity,
			Reason:         "read-tier: any mapped user allowed",
		}
		a.writeAudit(mattermostUserID, identity, channelID, commandType, "", "allowed")
		return result
	}

	// Write tier — requires repo write access.
	if entry.GitHubLogin == "" {
		// No GitHub identity; stub GitLab check fails closed.
		result := AuthResult{
			Allowed:        false,
			MappedIdentity: identity,
			Reason:         "no github_login configured; gitlab permission check not yet implemented",
		}
		a.writeAudit(mattermostUserID, identity, channelID, commandType, "", "denied")
		return result
	}

	owner, repo := splitRepoSlug(repoSlug)
	if owner == "" || repo == "" {
		result := AuthResult{
			Allowed:        false,
			MappedIdentity: identity,
			Reason:         "no repo resolved for permission check",
		}
		a.writeAudit(mattermostUserID, identity, channelID, commandType, "", "denied")
		return result
	}

	cacheKey := CacheKey("github", entry.GitHubLogin, repoSlug)
	if allowed, hit := a.cache.Get(cacheKey); hit {
		auditResult := "denied"
		reason := fmt.Sprintf("cached: no write access to %s", repoSlug)
		if allowed {
			auditResult = "allowed"
			reason = fmt.Sprintf("cached: write access to %s confirmed", repoSlug)
		}
		a.writeAudit(mattermostUserID, identity, channelID, commandType, "", auditResult)
		return AuthResult{Allowed: allowed, MappedIdentity: identity, Reason: reason}
	}

	// Cache miss — call the GitHub API.
	allowed, err := a.ghCheck.HasWriteAccess(ctx, entry.GitHubLogin, owner, repo)
	if err != nil {
		// Fail closed on API error.
		a.cache.Set(cacheKey, false)
		a.writeAudit(mattermostUserID, identity, channelID, commandType, "", "error")
		return AuthResult{
			Allowed:        false,
			MappedIdentity: identity,
			Reason:         fmt.Sprintf("permission check failed: %v", err),
		}
	}

	a.cache.Set(cacheKey, allowed)

	auditResult := "denied"
	reason := fmt.Sprintf("no write access to %s", repoSlug)
	if allowed {
		auditResult = "allowed"
		reason = fmt.Sprintf("write access to %s confirmed", repoSlug)
	}
	a.writeAudit(mattermostUserID, identity, channelID, commandType, "", auditResult)
	return AuthResult{Allowed: allowed, MappedIdentity: identity, Reason: reason}
}

// writeAudit appends an audit entry, logging errors to stderr (non-fatal).
func (a *Authorizer) writeAudit(mattermostUserID, mappedIdentity, channelID, command, args, result string) {
	if a.audit == nil {
		return
	}
	_ = a.audit.Append(AuditEntry{
		MattermostUserID: mattermostUserID,
		MappedIdentity:   mappedIdentity,
		ChannelID:        channelID,
		Command:          command,
		Args:             args,
		Result:           result,
	})
}

// resolveIdentity returns a "github:login" or "gitlab:username" string for audit logging.
func resolveIdentity(e config.UserMappingEntry) string {
	if e.GitHubLogin != "" {
		return "github:" + e.GitHubLogin
	}
	if e.GitLabUsername != "" {
		return "gitlab:" + e.GitLabUsername
	}
	return "mapped:no-identity"
}

// splitRepoSlug splits "owner/repo" into (owner, repo). Returns ("", "") on invalid input.
func splitRepoSlug(repoSlug string) (owner, repo string) {
	parts := strings.SplitN(repoSlug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}
