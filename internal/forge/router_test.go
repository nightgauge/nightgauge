package forge

import (
	"errors"
	"testing"
)

// fakeClient is the test stand-in for a real ForgeClient. The router
// only cares about constructing it, so the inner methods can be nil.
type fakeClient struct{ id string }

func (f *fakeClient) Issues() IssueService     { return nil }
func (f *fakeClient) PRs() PRService           { return nil }
func (f *fakeClient) Project() ProjectService  { return nil }
func (f *fakeClient) Board() BoardService      { return nil }
func (f *fakeClient) CI() CIService            { return nil }
func (f *fakeClient) Labels() LabelService     { return nil }
func (f *fakeClient) Rulesets() RulesetService { return nil }
func (f *fakeClient) Auth() AuthService        { return nil }
func (f *fakeClient) Repo() RepoService        { return nil }

// withFakeAdapter registers a fake adapter under a custom Kind for the
// duration of the test. The dispatch table uses a stringly-typed Kind so
// arbitrary names work.
func withFakeAdapter(t *testing.T, id string) {
	t.Helper()
	prev, hadPrev := adapters[Kind(id)]
	RegisterAdapter(Kind(id), func(cfg Config) (ForgeClient, error) {
		return &fakeClient{id: id}, nil
	})
	t.Cleanup(func() {
		if hadPrev {
			adapters[Kind(id)] = prev
		} else {
			delete(adapters, Kind(id))
		}
	})
}

func TestRouter_ForgeFlagWins(t *testing.T) {
	withFakeAdapter(t, "fake-a")
	withFakeAdapter(t, "fake-b")
	r := NewRouter()
	r.Register("fake-a", Config{Kind: "fake-a"})
	r.Register("fake-b", Config{Kind: "fake-b"})
	r.SetDefault("fake-a")

	t.Setenv("IB_FORGE", "fake-a")
	c, err := r.For("fake-b", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if c.(*fakeClient).id != "fake-b" {
		t.Errorf("expected fake-b, got %q", c.(*fakeClient).id)
	}
}

func TestRouter_EnvVar(t *testing.T) {
	withFakeAdapter(t, "fake-env")
	withFakeAdapter(t, "fake-default")
	r := NewRouter()
	r.Register("fake-env", Config{Kind: "fake-env"})
	r.Register("fake-default", Config{Kind: "fake-default"})
	r.SetDefault("fake-default")

	t.Setenv("IB_FORGE", "fake-env")
	c, err := r.For("", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if c.(*fakeClient).id != "fake-env" {
		t.Errorf("expected fake-env, got %q", c.(*fakeClient).id)
	}
}

func TestRouter_RepoInference(t *testing.T) {
	withFakeAdapter(t, "ghub")
	withFakeAdapter(t, "glab")
	r := NewRouter()
	r.Register("ghub", Config{Kind: "ghub"})
	r.Register("glab", Config{Kind: "glab"})
	r.MapRepo("nightgauge/nightgauge", "ghub")
	r.MapRepo("acme/platform", "glab")
	r.SetDefault("ghub")
	t.Setenv("IB_FORGE", "")

	c, err := r.For("", "acme/platform")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if c.(*fakeClient).id != "glab" {
		t.Errorf("expected glab, got %q", c.(*fakeClient).id)
	}

	// Case-insensitive match.
	c, err = r.For("", "nightgauge/NIGHTGAUGE")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if c.(*fakeClient).id != "ghub" {
		t.Errorf("expected ghub, got %q", c.(*fakeClient).id)
	}
}

func TestRouter_SoleForgeFallback(t *testing.T) {
	withFakeAdapter(t, "only-one")
	r := NewRouter()
	r.Register("only-one", Config{Kind: "only-one"})
	t.Setenv("IB_FORGE", "")

	c, err := r.For("", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if c.(*fakeClient).id != "only-one" {
		t.Errorf("expected only-one, got %q", c.(*fakeClient).id)
	}
}

func TestRouter_AmbiguousNoDefault(t *testing.T) {
	withFakeAdapter(t, "a")
	withFakeAdapter(t, "b")
	r := NewRouter()
	r.Register("a", Config{Kind: "a"})
	r.Register("b", Config{Kind: "b"})
	t.Setenv("IB_FORGE", "")

	_, err := r.For("", "")
	if err == nil {
		t.Fatal("expected ambiguous-error when 2 forges registered without default or hint")
	}
}

func TestRouter_UnknownForgeFlag(t *testing.T) {
	r := NewRouter()
	t.Setenv("IB_FORGE", "")

	_, err := r.For("nope", "")
	if err == nil {
		t.Fatal("expected error for unknown forge id")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("expected wrapped ErrUnsupported, got %v", err)
	}
}

func TestRouter_NoForgesRegistered(t *testing.T) {
	r := NewRouter()
	t.Setenv("IB_FORGE", "")

	_, err := r.For("", "")
	if err == nil {
		t.Fatal("expected error when no forges are registered")
	}
}

func TestRouter_ByID(t *testing.T) {
	withFakeAdapter(t, "byid")
	r := NewRouter()
	r.Register("byid", Config{Kind: "byid"})

	c, err := r.ByID("byid")
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if c.(*fakeClient).id != "byid" {
		t.Errorf("got %q, want byid", c.(*fakeClient).id)
	}

	_, err = r.ByID("missing")
	if err == nil || !errors.Is(err, ErrUnsupported) {
		t.Errorf("expected wrapped ErrUnsupported for missing id, got %v", err)
	}
}

func TestRouter_IDs_Sorted(t *testing.T) {
	r := NewRouter()
	r.Register("zeta", Config{})
	r.Register("alpha", Config{})
	r.Register("mu", Config{})
	got := r.IDs()
	want := []string{"alpha", "mu", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("IDs len mismatch: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("IDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- Validate tests ---

func TestRouter_Validate_Clean(t *testing.T) {
	withFakeAdapter(t, "github")
	withFakeAdapter(t, "gitlab")
	r := NewRouter()
	r.Register("github", Config{Kind: "github"})
	r.Register("gitlab", Config{Kind: "gitlab"})
	r.MapRepo("nightgauge/nightgauge", "github")
	r.MapRepo("acme/platform", "gitlab")
	r.SetDefault("github")

	errs := r.Validate()
	if len(errs) != 0 {
		t.Errorf("expected no validation errors, got: %v", errs)
	}
}

func TestRouter_Validate_DanglingRef(t *testing.T) {
	withFakeAdapter(t, "github")
	r := NewRouter()
	r.Register("github", Config{Kind: "github"})
	r.MapRepo("nightgauge/nightgauge", "nonexistent-forge")
	r.SetDefault("github")

	errs := r.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for dangling forge ref")
	}
	found := false
	for _, ve := range errs {
		if ve.Fatal {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected at least one fatal validation error for dangling ref")
	}
}

func TestRouter_Validate_OrphanForge(t *testing.T) {
	withFakeAdapter(t, "github")
	withFakeAdapter(t, "gitlab")
	r := NewRouter()
	r.Register("github", Config{Kind: "github"})
	r.Register("gitlab", Config{Kind: "gitlab"}) // orphan — no repos mapped
	r.SetDefault("github")
	r.MapRepo("nightgauge/nightgauge", "github")

	errs := r.Validate()
	if len(errs) == 0 {
		t.Fatal("expected orphan warning for gitlab forge")
	}
	for _, ve := range errs {
		if ve.Fatal {
			t.Errorf("orphan forge should produce warning (Fatal=false), got Fatal=true: %v", ve)
		}
	}
}

// --- ResolveLink tests ---

func TestRouter_ResolveLink_IntraForge(t *testing.T) {
	t.Setenv("IB_FORGE", "")
	withFakeAdapter(t, "github")
	r := NewRouter()
	r.Register("github", Config{Kind: KindGitHub})
	r.MapRepo("nightgauge/nightgauge", "github")
	r.MapRepo("acme/platform", "github")
	r.SetDefault("github")

	got := r.ResolveLink("acme/core", "acme/platform", "acme/platform#42")
	// Same forge → compact slug
	if got != "acme/platform#42" {
		t.Errorf("intra-forge: got %q, want %q", got, "acme/platform#42")
	}
}

func TestRouter_ResolveLink_CrossForge_GitHub(t *testing.T) {
	t.Setenv("IB_FORGE", "")
	withFakeAdapter(t, "gitlab")
	withFakeAdapter(t, "github")
	r := NewRouter()
	r.Register("github", Config{Kind: KindGitHub})
	r.Register("gitlab", Config{Kind: KindGitLab})
	r.MapRepo("acme/platform", "gitlab")
	r.MapRepo("nightgauge/nightgauge", "github")
	r.SetDefault("gitlab")

	got := r.ResolveLink("acme/platform", "nightgauge/nightgauge", "nightgauge/nightgauge#99")
	want := "https://github.com/nightgauge/nightgauge/issues/99"
	if got != want {
		t.Errorf("cross-forge to GitHub: got %q, want %q", got, want)
	}
}

func TestRouter_ResolveLink_CrossForge_GitLab(t *testing.T) {
	t.Setenv("IB_FORGE", "")
	withFakeAdapter(t, "github")
	withFakeAdapter(t, "gitlab")
	r := NewRouter()
	r.Register("github", Config{Kind: KindGitHub})
	r.Register("gitlab", Config{Kind: KindGitLab, Host: "gitlab.mycompany.com"})
	r.MapRepo("nightgauge/nightgauge", "github")
	r.MapRepo("acme/platform", "gitlab")
	r.SetDefault("github")

	got := r.ResolveLink("nightgauge/nightgauge", "acme/platform", "acme/platform#7")
	want := "https://gitlab.mycompany.com/acme/platform/-/issues/7"
	if got != want {
		t.Errorf("cross-forge to GitLab: got %q, want %q", got, want)
	}
}

func TestRouter_ResolveLink_CrossForge_GitLab_DefaultHost(t *testing.T) {
	t.Setenv("IB_FORGE", "")
	withFakeAdapter(t, "github")
	withFakeAdapter(t, "gitlab")
	r := NewRouter()
	r.Register("github", Config{Kind: KindGitHub})
	r.Register("gitlab", Config{Kind: KindGitLab}) // no Host — should default to gitlab.com
	r.MapRepo("nightgauge/nightgauge", "github")
	r.MapRepo("acme/platform", "gitlab")
	r.SetDefault("github")

	got := r.ResolveLink("nightgauge/nightgauge", "acme/platform", "acme/platform#3")
	want := "https://gitlab.com/acme/platform/-/issues/3"
	if got != want {
		t.Errorf("cross-forge GitLab default host: got %q, want %q", got, want)
	}
}

// --- ForgeIDFor tests ---

func TestRouter_ForgeIDFor_MappedRepo(t *testing.T) {
	withFakeAdapter(t, "ghub")
	withFakeAdapter(t, "glab")
	r := NewRouter()
	r.Register("ghub", Config{Kind: "ghub"})
	r.Register("glab", Config{Kind: "glab"})
	r.MapRepo("nightgauge/nightgauge", "ghub")
	r.MapRepo("acme/platform", "glab")
	r.SetDefault("ghub")
	t.Setenv("IB_FORGE", "")

	id, err := r.ForgeIDFor("acme/platform")
	if err != nil {
		t.Fatalf("ForgeIDFor: %v", err)
	}
	if id != "glab" {
		t.Errorf("got %q, want %q", id, "glab")
	}
}

func TestRouter_ForgeIDFor_DefaultFallback(t *testing.T) {
	withFakeAdapter(t, "github")
	r := NewRouter()
	r.Register("github", Config{Kind: "github"})
	r.SetDefault("github")
	t.Setenv("IB_FORGE", "")

	id, err := r.ForgeIDFor("unknown/repo")
	if err != nil {
		t.Fatalf("ForgeIDFor: %v", err)
	}
	if id != "github" {
		t.Errorf("expected default 'github', got %q", id)
	}
}

// --- Mixed-forge integration test ---

func TestMixedForgeWorkspace(t *testing.T) {
	withFakeAdapter(t, "fake-github")
	withFakeAdapter(t, "fake-gitlab")
	r := NewRouter()
	r.Register("fake-github", Config{Kind: "fake-github"})
	r.Register("fake-gitlab", Config{Kind: "fake-gitlab"})
	r.MapRepo("nightgauge/nightgauge", "fake-github")
	r.MapRepo("acme/platform", "fake-gitlab")
	r.SetDefault("fake-github")
	t.Setenv("IB_FORGE", "")

	// Validate — no errors expected
	if errs := r.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors for valid mixed-forge config, got: %v", errs)
	}

	// For("", "repo-a") → fake-github
	cA, err := r.For("", "nightgauge/nightgauge")
	if err != nil {
		t.Fatalf("For repo-a: %v", err)
	}
	if cA.(*fakeClient).id != "fake-github" {
		t.Errorf("expected fake-github, got %q", cA.(*fakeClient).id)
	}

	// For("", "repo-b") → fake-gitlab
	cB, err := r.For("", "acme/platform")
	if err != nil {
		t.Fatalf("For repo-b: %v", err)
	}
	if cB.(*fakeClient).id != "fake-gitlab" {
		t.Errorf("expected fake-gitlab, got %q", cB.(*fakeClient).id)
	}
}
