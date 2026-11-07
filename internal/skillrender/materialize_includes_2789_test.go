package skillrender

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// residualSkillPaths reports whether text still names a repo-relative skills/
// path that resolves against the worktree.
func residualSkillPaths(text string) bool {
	return strings.Contains(text, "skills/_shared/") || crossSkillRefRE.MatchString(text)
}

func materializeFixture(t *testing.T) (root, skillDir string) {
	t.Helper()
	root = t.TempDir()
	skillDir = filepath.Join(root, "nightgauge-feature-validate")
	write(t, filepath.Join(root, "nightgauge-verify-ui", "SKILL.md"), "---\nname: verify-ui\n---\n# UI\n")
	write(t, filepath.Join(root, "_shared", "X.md"), "# X\n")
	write(t, filepath.Join(skillDir, "_includes", "gate.md"),
		"# Gate\nSee `skills/_shared/X.md` and `skills/nightgauge-verify-ui/SKILL.md`.\n"+
			"Then `_includes/nested.md`.\n"+strings.Repeat("g", 300)+"\n")
	write(t, filepath.Join(skillDir, "_includes", "nested.md"), "# Nested\nUse `skills/_shared/X.md`.\n")
	write(t, filepath.Join(skillDir, "_includes", "lazy.md"), "# Lazy\nRead `skills/nightgauge-verify-ui/SKILL.md`.\n")
	write(t, filepath.Join(skillDir, "_includes", "clean.md"), "# Clean\nnothing here\n")
	write(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: nightgauge-feature-validate\n---\n# Validate\n\n"+
		"> **Read `_includes/gate.md` (same directory as this SKILL.md) now and follow it.**\n\n"+
		"Load on demand: `_includes/lazy.md`, `_includes/clean.md`.\n")
	return root, skillDir
}

func TestOverBudgetAndOnDemandIncludesAreMaterialized2789(t *testing.T) {
	root, skillDir := materializeFixture(t)
	cache := t.TempDir()
	opts := Options{Stage: "feature-validate", SkillsRoots: []string{root}, IncludeBudget: 100, IncludeCacheDir: cache}
	res, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SuppliedIncludes) != 0 {
		t.Fatalf("gate.md is over budget; supplied = %v", res.SuppliedIncludes)
	}
	if residualSkillPaths(res.Content) {
		t.Errorf("content keeps a repo-relative skills/ path:\n%s", res.Content)
	}
	if len(res.MaterializedIncludes) != 3 {
		t.Fatalf("materialized = %v, want gate.md, lazy.md, nested.md", res.MaterializedIncludes)
	}
	for _, p := range res.MaterializedIncludes {
		if !strings.HasPrefix(p, cache) {
			t.Errorf("copy %s is outside the cache dir", p)
		}
		if !strings.Contains(res.Content, "`"+p) && filepath.Base(p) != "nested.md" {
			t.Errorf("content does not point at %s", p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if residualSkillPaths(string(data)) {
			t.Errorf("copy %s keeps a repo-relative skills/ path:\n%s", p, data)
		}
		if strings.Contains(string(data), filepath.Join(skillDir, "_includes")) {
			t.Errorf("copy %s points at a raw sibling include:\n%s", p, data)
		}
	}
	gate, _ := os.ReadFile(res.MaterializedIncludes[0])
	for _, want := range []string{filepath.Join(root, "_shared", "X.md"), filepath.Join(root, "nightgauge-verify-ui", "SKILL.md"), filepath.Join(filepath.Dir(res.MaterializedIncludes[0]), "nested.md")} {
		if !strings.Contains(string(gate), want) {
			t.Errorf("gate copy lacks %s:\n%s", want, gate)
		}
	}
	// An include with nothing to rewrite keeps its original path.
	if !strings.Contains(res.Content, "`"+filepath.Join(skillDir, "_includes", "clean.md")) {
		t.Errorf("clean.md reference changed:\n%s", res.Content)
	}
	again, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	if again.Content != res.Content {
		t.Error("two renders differ")
	}
}

func TestSuppliedIncludesAreNotMaterialized2789(t *testing.T) {
	root, _ := materializeFixture(t)
	res, err := Render(Options{Stage: "feature-validate", SkillsRoots: []string{root}, IncludeBudget: EveryPhaseInclude, IncludeCacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.MaterializedIncludes {
		if filepath.Base(p) == "gate.md" {
			t.Errorf("supplied gate.md was materialized")
		}
	}
	if residualSkillPaths(res.Content) {
		t.Errorf("content keeps a repo-relative skills/ path")
	}
}

func TestMaterializeWriteFailureWarns2789(t *testing.T) {
	root, _ := materializeFixture(t)
	blocker := filepath.Join(t.TempDir(), "file")
	write(t, blocker, "x")
	var warned []string
	res, err := Render(Options{Stage: "feature-validate", SkillsRoots: []string{root}, IncludeBudget: 100,
		IncludeCacheDir: blocker, Warn: func(s string) { warned = append(warned, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MaterializedIncludes) != 0 || len(warned) == 0 {
		t.Errorf("materialized = %v, warnings = %v; want none and a warning", res.MaterializedIncludes, warned)
	}
}

// A planted or stale copy at the content-addressed path is replaced, not
// trusted, and a cache dir other users can write is refused.
func TestMaterializedCopyIsNotTrustedByExistence2789(t *testing.T) {
	root, _ := materializeFixture(t)
	cache := t.TempDir()
	opts := Options{Stage: "feature-validate", SkillsRoots: []string{root}, IncludeBudget: 100, IncludeCacheDir: cache}
	res, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	p := res.MaterializedIncludes[0]
	want, _ := os.ReadFile(p)
	write(t, p, "planted\n")
	if _, err := Render(opts); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(want) {
		t.Errorf("planted copy kept: %q", got)
	}
	if err := os.Chmod(filepath.Dir(p), 0o777); err != nil {
		t.Fatal(err)
	}
	var warned []string
	opts.Warn = func(s string) { warned = append(warned, s) }
	if again, _ := Render(opts); len(again.MaterializedIncludes) != 0 || len(warned) == 0 {
		t.Errorf("world-writable cache dir used: %v %v", again.MaterializedIncludes, warned)
	}
}

// TestCopyNamingASuppliedIncludeGetsARewrittenCopy2789: an on-demand include
// that sends the agent back to a supplied include must point at a rewritten
// copy of it, never at the raw file with its repo-relative skills/ paths.
func TestCopyNamingASuppliedIncludeGetsARewrittenCopy2789(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "nightgauge-feature-validate")
	write(t, filepath.Join(root, "_shared", "X.md"), "# X\n")
	write(t, filepath.Join(skillDir, "_includes", "a.md"), "# A\nSee `skills/_shared/X.md`.\n")
	write(t, filepath.Join(skillDir, "_includes", "b.md"), "# B\nRedo `_includes/a.md`.\n"+strings.Repeat("b", 300)+"\n")
	write(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: nightgauge-feature-validate\n---\n# V\n\n"+
		"> **Read `_includes/a.md` (same directory as this SKILL.md) now and follow it.**\n\n"+
		"> **Read `_includes/b.md` (same directory as this SKILL.md) now and follow it.**\n")
	cache := t.TempDir()
	res, err := Render(Options{Stage: "feature-validate", SkillsRoots: []string{root}, IncludeBudget: 100, IncludeCacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.SuppliedIncludes, "a.md") || slices.Contains(res.SuppliedIncludes, "b.md") {
		t.Fatalf("supplied = %v, want a.md only", res.SuppliedIncludes)
	}
	for _, p := range res.MaterializedIncludes {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if residualSkillPaths(string(data)) || strings.Contains(string(data), filepath.Join(skillDir, "_includes", "a.md")) {
			t.Errorf("copy %s reaches a raw include or a repo-relative path:\n%s", p, data)
		}
	}
	if len(res.MaterializedIncludes) != 2 {
		t.Errorf("materialized = %v, want copies of a.md and b.md", res.MaterializedIncludes)
	}
}
