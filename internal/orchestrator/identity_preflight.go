package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// ConfigIdentityChecker is the production IdentityChecker (#4068). It resolves
// the github_user configured for a target repo's owner, builds a GitHub client
// via the standard resolution chain (config → github_user-scoped token with
// ambient env stripped), and asserts the EFFECTIVE login matches the configured
// identity AND has push access on the target repo.
//
// It is skippable by construction: when the repo's owner has no configured
// github_user, CheckIdentity returns allowed=true so single-identity workspaces
// are unaffected. A nil *config.Config (no workspace config) likewise allows.
type ConfigIdentityChecker struct {
	cfg *config.Config
	// newClient builds a client for (cfg, owner). Overridable in tests to avoid
	// network I/O; defaults to gh.NewClientFromConfig.
	newClient func(cfg *config.Config, owner string) (*gh.Client, error)
}

// NewConfigIdentityChecker constructs a ConfigIdentityChecker from workspace
// config. Returns nil when cfg is nil (the scheduler treats a nil checker as
// "gate disabled"), so callers can wire it unconditionally.
func NewConfigIdentityChecker(cfg *config.Config) *ConfigIdentityChecker {
	if cfg == nil {
		return nil
	}
	return &ConfigIdentityChecker{
		cfg: cfg,
		newClient: func(c *config.Config, owner string) (*gh.Client, error) {
			return gh.NewClientFromConfig(c, owner, "")
		},
	}
}

// CheckIdentity implements IdentityChecker. Returns (allowed, reason).
//
// Fail-open vs fail-closed: the scheduler gate must distinguish a genuine
// misconfiguration (block) from an environmental hiccup (don't turn a network
// blip into a total silent stall). So:
//   - A CONFIRMED wrong identity (Whoami succeeded, login != expected) or a
//     CONFIRMED lack of push (permission read succeeded, not write/admin) →
//     fail-CLOSED (block) with a specific reason.
//   - An inability to even check — client build error, Whoami error, or a
//     permission-read error (API down, repo not found, token can't query it) →
//     fail-OPEN: log a warning and allow, so a transient/infra issue doesn't
//     masquerade as a permission failure. The explicit `forge auth assert` verb
//     stays fail-closed for these (the operator asked to assert).
func (c *ConfigIdentityChecker) CheckIdentity(ctx context.Context, owner, repo string, issueNumber int) (bool, string) {
	if c == nil || c.cfg == nil {
		return true, "" // No config = nothing to assert.
	}
	expected := c.cfg.ResolveGitHubUserForOwner(owner)
	if expected == "" {
		// No configured identity for this owner — single-identity path, skip.
		return true, ""
	}

	client, err := c.newClient(c.cfg, owner)
	if err != nil {
		log.Printf("#%d: identity preflight: could not resolve a client for github_user %q on %s/%s — allowing (infra, not a permission denial): %v",
			issueNumber, expected, owner, repo, err)
		return true, ""
	}

	actor, err := client.Whoami(ctx)
	if err != nil {
		log.Printf("#%d: identity preflight: could not verify identity for %q on %s/%s — allowing (infra, not a permission denial): %v",
			issueNumber, expected, owner, repo, err)
		return true, ""
	}
	if !strings.EqualFold(actor.Login, expected) {
		// CONFIRMED wrong identity → block.
		return false, fmt.Sprintf("resolved identity is %q but config expects %q for %s/%s — fix token resolution (forge auth assert --repo %s/%s)",
			actor.Login, expected, owner, repo, owner, repo)
	}

	hasWrite, err := client.HasRepoWriteAccess(ctx, expected, owner, repo)
	if err != nil {
		log.Printf("#%d: identity preflight: could not read collaborator permission for %q on %s/%s — allowing (infra/visibility, not a confirmed denial): %v",
			issueNumber, expected, owner, repo, err)
		return true, ""
	}
	if !hasWrite {
		// CONFIRMED no push → block.
		return false, fmt.Sprintf("identity %q lacks push access on %s/%s — grant write/admin or set github_user to a collaborator who has it",
			expected, owner, repo)
	}

	return true, ""
}
