package config

import (
	"errors"
	"sort"
	"strings"
)

// ErrTrustedAuthorAssociationsRemoved is the error a config that still sets
// autonomous.trusted_author_associations fails to load with (#2130).
var ErrTrustedAuthorAssociationsRemoved = errors.New(
	"autonomous.trusted_author_associations was removed (#2130): the pipeline now works an issue only " +
		"when a user with write access to the repository authored it or applied the `pipeline:approved` " +
		"label, and author_association is no longer read. Delete the key; to trust a bot account by " +
		"authorship, list it in autonomous.trusted_bots. See docs/CONFIGURATION.md#trusted_bots and " +
		"docs/AUTONOMOUS_ORCHESTRATOR.md")

// ValidateNoRemovedTrustKeys refuses a config that still sets a trust key
// #2130 removed, rather than silently ignoring a setting its author believes
// is in force.
func ValidateNoRemovedTrustKeys(cfg *Config) error {
	if cfg != nil && cfg.Autonomous != nil && cfg.Autonomous.RemovedTrustedAuthorAssociations != nil {
		return ErrTrustedAuthorAssociationsRemoved
	}
	return nil
}

// AppOwners are the owners where the pipeline acts as a GitHub App
// installation: github_auth.app names an App and lists an installation for
// the owner. The eligibility gate (#2130) accepts label approval only there;
// elsewhere the pipeline's stages act as a personal token, whose labels cannot
// be told apart from the maintainer's own.
func (c *Config) AppOwners() []string {
	if c == nil || c.GitHubAuth == nil || c.GitHubAuth.App == nil || strings.TrimSpace(c.GitHubAuth.App.ID) == "" {
		return nil
	}
	owners := make([]string, 0, len(c.GitHubAuth.App.Installations))
	for owner, id := range c.GitHubAuth.App.Installations {
		if id > 0 {
			owners = append(owners, owner)
		}
	}
	sort.Strings(owners)
	return owners
}

// ConfiguredGitHubLogins are the logins named in github_auth.users and
// github_user. The eligibility gate treats each as an identity the stages
// can act as (#2130).
func (c *Config) ConfiguredGitHubLogins() []string {
	if c == nil {
		return nil
	}
	var out []string
	if c.GitHubUser != "" {
		out = append(out, c.GitHubUser)
	}
	if c.GitHubAuth != nil {
		for _, u := range c.GitHubAuth.Users {
			if u != "" {
				out = append(out, u)
			}
		}
	}
	sort.Strings(out)
	return out
}
