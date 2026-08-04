package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMemberConfig writes a member repo's own .nightgauge/config.yaml — the
// declaration ResolveRepoProject reads when a repo names its own board.
func writeMemberConfig(t *testing.T, repoRoot, owner, repo string, project int) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := "owner: " + owner + "\nowner_type: org\nrepo: " + repo + "\nproject:\n  number: " + itoa(project) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write member config: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestResolveRepoProject_Precedence pins the full precedence ladder and the
// reason code each rung produces. The reason code is the whole point of #313:
// two callers with different policies (file vs poll) must be able to act on one
// lookup, which they can only do if the answer says how it was reached.
func TestResolveRepoProject_Precedence(t *testing.T) {
	memberRoot := t.TempDir()
	writeMemberConfig(t, memberRoot, "acme", "platform", 4)

	base := func() *Config {
		return &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3}
	}
	withMapping := func(n int) *Config {
		c := base()
		c.Autonomous = &AutonomousConfig{
			Repositories: map[string]*RepositoryConfig{"acme/platform": {ProjectNumber: n}},
		}
		return c
	}

	tests := []struct {
		name       string
		cfg        *Config
		query      RepoProjectQuery
		wantNumber int
		wantSource RepoProjectSource
	}{
		{
			name:       "local repo answers for itself",
			cfg:        base(),
			query:      RepoProjectQuery{Owner: "acme", Repo: "web"},
			wantNumber: 3,
			wantSource: RepoProjectLocalConfig,
		},
		{
			name:       "SharedBoard overrides the local board so --project still works",
			cfg:        base(),
			query:      RepoProjectQuery{Owner: "acme", Repo: "web", SharedBoard: 9},
			wantNumber: 9,
			wantSource: RepoProjectLocalConfig,
		},
		{
			name:       "explicit mapping outranks the member's own config",
			cfg:        withMapping(12),
			query:      RepoProjectQuery{Owner: "acme", Repo: "platform", MemberRoot: memberRoot},
			wantNumber: 12,
			wantSource: RepoProjectExplicitMapping,
		},
		{
			name:       "member config is a declaration, not a guess",
			cfg:        base(),
			query:      RepoProjectQuery{Owner: "acme", Repo: "platform", MemberRoot: memberRoot},
			wantNumber: 4,
			wantSource: RepoProjectMemberConfig,
		},
		{
			name:       "member config wins over the shared board",
			cfg:        base(),
			query:      RepoProjectQuery{Owner: "acme", Repo: "platform", MemberRoot: memberRoot, SharedBoard: 3},
			wantNumber: 4,
			wantSource: RepoProjectMemberConfig,
		},
		{
			name:       "nothing declared falls back to the shared board",
			cfg:        base(),
			query:      RepoProjectQuery{Owner: "acme", Repo: "platform"},
			wantNumber: 3,
			wantSource: RepoProjectSharedBoardDefault,
		},
		{
			name:       "nothing declared and no shared board is unmapped",
			cfg:        &Config{Owner: "acme", DefaultRepo: "web"},
			query:      RepoProjectQuery{Owner: "acme", Repo: "platform"},
			wantNumber: 0,
			wantSource: RepoProjectUnmapped,
		},
		{
			name:       "a zero board is never an answer, whatever produced it",
			cfg:        &Config{Owner: "acme", DefaultRepo: "web"},
			query:      RepoProjectQuery{Owner: "acme", Repo: "web"},
			wantNumber: 0,
			wantSource: RepoProjectUnmapped,
		},
		{
			// #3860: a manifest-only workspace root has no config of its own.
			// Short-circuiting on nil there would hand back zero repos, which
			// is how the scheduler loses an entire workspace.
			name:       "nil config still reads the member's declaration",
			cfg:        nil,
			query:      RepoProjectQuery{Owner: "acme", Repo: "platform", MemberRoot: memberRoot},
			wantNumber: 4,
			wantSource: RepoProjectMemberConfig,
		},
		{
			name:       "owner-qualified Repo is split, not treated as a slug",
			cfg:        withMapping(12),
			query:      RepoProjectQuery{Owner: "ignored", Repo: "acme/platform"},
			wantNumber: 12,
			wantSource: RepoProjectExplicitMapping,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveRepoProject(tt.cfg, tt.query)
			if got.Number != tt.wantNumber || got.Source != tt.wantSource {
				t.Errorf("got {%d %s}, want {%d %s}", got.Number, got.Source, tt.wantNumber, tt.wantSource)
			}
		})
	}
}

// TestResolveRepoProject_DefaultRepoUnsetIsTriedLast pins the ordering fix that
// keeps #262's permissive fallback from eating a real declaration. A config
// with no repo of its own cannot tell the target apart from itself, but that
// ambiguity must not outrank the member repo's explicit statement — resolving
// it first is what made a manifest-only workspace give every member the same
// board.
func TestResolveRepoProject_DefaultRepoUnsetIsTriedLast(t *testing.T) {
	memberRoot := t.TempDir()
	writeMemberConfig(t, memberRoot, "acme", "platform", 4)
	cfg := &Config{Owner: "acme", ProjectNumber: 3} // no DefaultRepo

	got := ResolveRepoProject(cfg, RepoProjectQuery{Owner: "acme", Repo: "platform", MemberRoot: memberRoot})
	if got.Number != 4 || got.Source != RepoProjectMemberConfig {
		t.Errorf("got {%d %s}, want {4 %s}", got.Number, got.Source, RepoProjectMemberConfig)
	}

	// With nothing declared, the ambiguous fallback still answers (#262).
	got = ResolveRepoProject(cfg, RepoProjectQuery{Owner: "acme", Repo: "unknown"})
	if got.Number != 3 || got.Source != RepoProjectLocalConfig {
		t.Errorf("got {%d %s}, want {3 %s}", got.Number, got.Source, RepoProjectLocalConfig)
	}
}

// TestResolveRepoProject_MemberRootFoundViaManifest covers the CLI path, which
// has no member root in hand and must find it the same way the scheduler does —
// otherwise `nightgauge project resolve --repo X` and the scheduler are back to
// two answers.
func TestResolveRepoProject_MemberRootFoundViaManifest(t *testing.T) {
	root := t.TempDir()
	writeMemberConfig(t, root, "acme", "web", 3)
	writeMemberConfig(t, filepath.Join(root, "platform"), "acme", "platform", 4)
	writeManifest(t, root, `repositories:
  - name: web
    path: .
    project_number: 3
  - name: platform
    path: platform
    project_number: 4
`)

	cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3}
	got := ResolveRepoProject(cfg, RepoProjectQuery{Owner: "acme", Repo: "platform", StartDir: root})
	if got.Number != 4 || got.Source != RepoProjectMemberConfig {
		t.Errorf("got {%d %s}, want {4 %s}", got.Number, got.Source, RepoProjectMemberConfig)
	}
}

// TestResolveRepoProjectNumber_FilingPolicy pins the half of #313 that must NOT
// change: a caller that writes to a board still refuses anything nothing
// declared. #3232 was a cross-repo issue silently filed onto the primary board
// because the lookup defaulted — a shared-board default is exactly that
// default, so it must remain an error even though it is now a real answer for
// the scheduler.
func TestResolveRepoProjectNumber_FilingPolicy(t *testing.T) {
	memberRoot := t.TempDir()
	writeMemberConfig(t, memberRoot, "acme", "platform", 4)

	t.Run("declared boards are answers", func(t *testing.T) {
		cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3}
		got, err := ResolveRepoProjectNumber(cfg, RepoProjectQuery{
			Owner: "acme", Repo: "platform", MemberRoot: memberRoot,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 4 {
			t.Errorf("got %d, want 4", got)
		}
	})

	t.Run("a shared-board default is refused, and says so", func(t *testing.T) {
		cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3}
		_, err := ResolveRepoProjectNumber(cfg, RepoProjectQuery{Owner: "acme", Repo: "platform"})
		if err == nil {
			t.Fatal("filing against a board nothing declared must fail — this is #3232")
		}
		msg := err.Error()
		if !strings.Contains(msg, "acme/platform") {
			t.Errorf("error must name the repo, got: %s", msg)
		}
		if !strings.Contains(msg, "3") {
			t.Errorf("error must name the board it refused to guess, got: %s", msg)
		}
		if !strings.Contains(msg, "project_number") {
			t.Errorf("error must name the config key to set, got: %s", msg)
		}
	})

	t.Run("a local repo with no board points at its own config", func(t *testing.T) {
		cfg := &Config{Owner: "acme", DefaultRepo: "web"}
		_, err := ResolveRepoProjectNumber(cfg, RepoProjectQuery{Owner: "acme", Repo: "web"})
		if err == nil {
			t.Fatal("expected an error when the local repo has no board")
		}
		// Telling the operator to set autonomous.repositories.web.project_number
		// for their OWN repo is the wrong fix; project.number is the right one.
		if strings.Contains(err.Error(), "autonomous.repositories") {
			t.Errorf("error names the wrong config key for the local repo: %s", err)
		}
		if !strings.Contains(err.Error(), "project.number") {
			t.Errorf("error must name project.number, got: %s", err)
		}
	})
}

// TestFindWorkspaceProjectMappingMismatches_SharedBoardDefaultIsNotAgreement is
// the #313 half of #280: a manifest entry that resolves only to the workspace
// default was never compared against anything, so it is not agreement — and the
// message must state what BOTH kinds of caller do, without either one asserting
// something about the other. The #280 message said "the scheduler polls no
// board for this repo", which was false.
func TestFindWorkspaceProjectMappingMismatches_SharedBoardDefaultIsNotAgreement(t *testing.T) {
	root := t.TempDir()
	// platform declares a board in the manifest, but its checkout is absent, so
	// no config declares one. This is the live shape #280 was filed from.
	writeManifest(t, root, `repositories:
  - name: platform
    path: platform
    project_number: 4
`)

	cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3}
	report, err := FindWorkspaceProjectMappingMismatches(cfg, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.OK() {
		t.Fatal("a repo nothing declares a board for is not a clean bill of health")
	}
	if len(report.Unresolvable) != 1 {
		t.Fatalf("want 1 unresolvable repo, got %+v", report.Unresolvable)
	}
	u := report.Unresolvable[0]
	if u.Fallback != 3 {
		t.Errorf("Fallback = %d, want 3 (the board the scheduler actually polls)", u.Fallback)
	}
	msg := u.String()
	if !strings.Contains(msg, "falls back to the workspace default board 3") {
		t.Errorf("message must name the board the scheduler polls, got: %s", msg)
	}
	if strings.Contains(msg, "polls no board") {
		t.Errorf("message repeats the #280 falsehood: %s", msg)
	}
}

// TestFindWorkspaceProjectMappingMismatches_MemberConfigIsSourceB proves the
// cross-check compares two genuinely independent files. Feeding the manifest's
// own project_number back into the resolver would make it compare a value to
// itself and never fail.
func TestFindWorkspaceProjectMappingMismatches_MemberConfigIsSourceB(t *testing.T) {
	root := t.TempDir()
	writeMemberConfig(t, filepath.Join(root, "platform"), "acme", "platform", 9)
	writeManifest(t, root, `repositories:
  - name: platform
    path: platform
    project_number: 4
`)

	cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3}
	report, err := FindWorkspaceProjectMappingMismatches(cfg, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Mismatches) != 1 {
		t.Fatalf("want 1 mismatch (manifest 4 vs member config 9), got %+v", report)
	}
	m := report.Mismatches[0]
	if m.ManifestProject != 4 || m.ResolvedProject != 9 {
		t.Errorf("got manifest %d vs resolved %d, want 4 vs 9", m.ManifestProject, m.ResolvedProject)
	}
	if m.ResolvedSource != RepoProjectMemberConfig {
		t.Errorf("ResolvedSource = %q, want %q — the message must name which config disagrees",
			m.ResolvedSource, RepoProjectMemberConfig)
	}
	if !strings.Contains(m.String(), string(RepoProjectMemberConfig)) {
		t.Errorf("message must carry the source, got: %s", m.String())
	}
}

func writeManifest(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".vscode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .vscode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nightgauge-workspace.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
