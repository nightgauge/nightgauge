package platform

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

// What a daemon's registration declares about the workspace it serves
// (nightgauge#2335). The hosted service counts an agent toward a workspace's
// presence, and its command router places a repo-scoped command, by the
// owner/repo pairs the agent declared when it registered. A registration that
// declares none covers no workspace, so a daemon without this read as offline
// everywhere while it was online and heartbeating.
//
// The VS Code extension already declares the same thing for its own agent
// (`repos` plus, when the workspace config names the workspace, the
// `workspace` block). The daemon declares the same set for the same
// workspace, so the two never disagree about which repos a workspace holds.

// AgentRepo is one owner/repo pair an agent declares it serves. It is the
// hosted service's RepoSchema: both fields required, at most 100 characters.
type AgentRepo struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// AgentWorkspace is the registration's optional `workspace` block. The
// service creates or finds the named workspace for the account's team and
// links the agent's declared repos to it. The block carries no repos of its
// own: the core's clients declare them as the agent's `repos`.
type AgentWorkspace struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// WorkspaceDeclaration is everything a registration declares about the
// workspace this agent serves. Workspace is nil when the workspace config
// does not name the workspace.
type WorkspaceDeclaration struct {
	Repos     []AgentRepo
	Workspace *AgentWorkspace
}

// WorkspaceFunc resolves the declaration on every registration, so a
// re-registration after an eviction declares the workspace as it is then.
// An error declares nothing, and the registration proceeds without it.
type WorkspaceFunc func() (WorkspaceDeclaration, error)

// The hosted service's bounds on what a registration may declare
// (RegisterAgentSchema). A body outside them is refused whole with a 422, so
// they are checked here, before anything is sent, and an out-of-bounds part
// is dropped rather than costing the daemon its registration.
const (
	agentReposMax           = 100
	agentRepoFieldMax       = 100
	agentWorkspaceSlugMax   = 100
	agentWorkspaceNameMax   = 200
	workspaceSlugDerivedMax = 50
)

var agentWorkspaceSlugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// WorkspaceSlug derives a workspace's slug from its configured name exactly
// as the extension's WorkspaceRegistrationPayloadBuilder.toSlug does, so the
// daemon and the extension register one workspace under one slug: lowercase,
// every run of characters outside [a-z0-9] collapsed to one "-", leading and
// trailing "-" trimmed, then cut to 50 characters. The cut comes after the
// trim, as it does there, so a slug can end in "-".
//
// JavaScript lowercases with the full Unicode mapping and Go with the simple
// one. The only character whose full mapping differs and reaches ASCII is
// U+0130 (capital I with dot above), which JavaScript lowers to "i" plus a
// combining dot, so it is expanded first to keep the two derivations equal.
func WorkspaceSlug(name string) string {
	lower := strings.ToLower(strings.ReplaceAll(name, "İ", "i̇"))
	var b strings.Builder
	inRun := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			inRun = false
			continue
		}
		if !inRun {
			b.WriteByte('-')
			inRun = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > workspaceSlugDerivedMax {
		slug = slug[:workspaceSlugDerivedMax] // ASCII only by now: bytes are characters
	}
	return slug
}

// WorkspaceBlock builds the `workspace` block for a workspace config's name,
// or nil when the name is empty or derives an empty slug, the two cases in
// which the extension sends no block either.
func WorkspaceBlock(name string) *AgentWorkspace {
	if name == "" {
		return nil
	}
	slug := WorkspaceSlug(name)
	if slug == "" {
		return nil
	}
	return &AgentWorkspace{Slug: slug, DisplayName: name}
}

// jsLength is a string's length as the hosted service's validator counts it:
// UTF-16 code units.
func jsLength(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// validateRepos checks repos against the hosted service's bounds.
func validateRepos(repos []AgentRepo) error {
	if len(repos) > agentReposMax {
		return fmt.Errorf("%d repos, the service accepts at most %d", len(repos), agentReposMax)
	}
	for _, r := range repos {
		if n := jsLength(r.Owner); n == 0 || n > agentRepoFieldMax {
			return fmt.Errorf("repo owner %q must be 1-%d characters", r.Owner, agentRepoFieldMax)
		}
		if n := jsLength(r.Repo); n == 0 || n > agentRepoFieldMax {
			return fmt.Errorf("repo name %q must be 1-%d characters", r.Repo, agentRepoFieldMax)
		}
	}
	return nil
}

// validateWorkspace checks the workspace block against the hosted service's
// bounds.
func validateWorkspace(w *AgentWorkspace) error {
	if w == nil {
		return nil
	}
	if n := jsLength(w.Slug); n == 0 || n > agentWorkspaceSlugMax || !agentWorkspaceSlugPattern.MatchString(w.Slug) {
		return fmt.Errorf("workspace slug %q must be 1-%d characters of [a-z0-9-]", w.Slug, agentWorkspaceSlugMax)
	}
	if n := jsLength(w.DisplayName); n == 0 || n > agentWorkspaceNameMax {
		return fmt.Errorf("workspace name must be 1-%d characters, got %d", agentWorkspaceNameMax, n)
	}
	return nil
}
