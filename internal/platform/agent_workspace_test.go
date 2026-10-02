package platform

import (
	"strings"
	"testing"
)

// The slug must be the extension's (WorkspaceRegistrationPayloadBuilder.toSlug)
// for every name, or the daemon and the extension register one workspace under
// two slugs. The cases are the extension's own test cases plus the edges where
// the two languages could part: the cut after the trim, and U+0130.
func TestWorkspaceSlug_MatchesTheExtension(t *testing.T) {
	cases := map[string]string{
		"MyWorkspace":                  "myworkspace",
		"my workspace":                 "my-workspace",
		"my  workspace":                "my-workspace",
		"my---workspace":               "my-workspace",
		"my / workspace":               "my-workspace",
		"-my-workspace-":               "my-workspace",
		"  spaces  ":                   "spaces",
		"---":                          "",
		"!@#$":                         "",
		"workspace-v2":                 "workspace-v2",
		"café workspace":               "caf-workspace",
		"Acme Platform Dev":            "acme-platform-dev",
		"İstanbul":                     "i-stanbul",                   // JS: "İ".toLowerCase() is "i" + U+0307
		"Kelvin":                       "kelvin",                      // Kelvin sign lowers to ASCII k in both
		strings.Repeat("a", 49) + " b": strings.Repeat("a", 49) + "-", // cut after trim
	}
	for name, want := range cases {
		if got := WorkspaceSlug(name); got != want {
			t.Errorf("WorkspaceSlug(%q) = %q, want %q", name, got, want)
		}
	}
	if got := WorkspaceSlug(strings.Repeat("x", 80)); len(got) != 50 {
		t.Errorf("an 80-character name gave a %d-character slug, want 50", len(got))
	}
}

func TestWorkspaceBlock(t *testing.T) {
	if b := WorkspaceBlock(""); b != nil {
		t.Errorf("an unnamed workspace sent a block: %+v", b)
	}
	if b := WorkspaceBlock("!@#$%"); b != nil {
		t.Errorf("a name with an empty slug sent a block: %+v", b)
	}
	b := WorkspaceBlock("Acme Platform")
	if b == nil || b.Slug != "acme-platform" || b.DisplayName != "Acme Platform" {
		t.Errorf("WorkspaceBlock(Acme Platform) = %+v", b)
	}
}

// The bounds are the hosted service's RegisterAgentSchema: anything outside
// them would be refused whole with a 422.
func TestWorkspaceDeclarationBounds(t *testing.T) {
	if err := validateRepos([]AgentRepo{{Owner: "o", Repo: "r"}}); err != nil {
		t.Errorf("a valid repo was refused: %v", err)
	}
	many := make([]AgentRepo, 101)
	for i := range many {
		many[i] = AgentRepo{Owner: "o", Repo: "r"}
	}
	for name, repos := range map[string][]AgentRepo{
		"101 repos":      many,
		"empty owner":    {{Owner: "", Repo: "r"}},
		"101-char repo":  {{Owner: "o", Repo: strings.Repeat("r", 101)}},
		"101-char owner": {{Owner: strings.Repeat("o", 101), Repo: "r"}},
	} {
		if validateRepos(repos) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := validateWorkspace(&AgentWorkspace{Slug: "ok-1", DisplayName: strings.Repeat("n", 200)}); err != nil {
		t.Errorf("a 200-character name was refused: %v", err)
	}
	for name, w := range map[string]*AgentWorkspace{
		"201-char name": {Slug: "ok", DisplayName: strings.Repeat("n", 201)},
		"bad slug":      {Slug: "Not_OK", DisplayName: "n"},
		"empty slug":    {Slug: "", DisplayName: "n"},
	} {
		if validateWorkspace(w) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
