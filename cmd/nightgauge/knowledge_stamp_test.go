package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
)

// stampFixture builds a workspace with one knowledge entry and returns the
// workspace root and the entry's path relative to the knowledge root.
func stampFixture(t *testing.T, body string) (root, relEntry string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, ".nightgauge", "knowledge", "features", "42-photo-upload")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PRD.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join("features", "42-photo-upload", "PRD.md")
}

func runStamp(t *testing.T, args ...string) error {
	t.Helper()
	cmd := knowledgeStampCmd()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs(args)
	return cmd.Execute()
}

func bodyHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, body := okf.SplitFrontmatter(string(data))
	sum := sha256.Sum256([]byte(body))
	return string(sum[:])
}

const stampBody = "---\ntype: prd\nstatus: draft\n---\n\n# PRD: #42\n\nThe body must survive byte for byte.\n"

func TestKnowledgeStamp_MergesProvenanceAndLeavesBodyIntact(t *testing.T) {
	root, rel := stampFixture(t, stampBody)
	abs := filepath.Join(root, ".nightgauge", "knowledge", rel)
	before := bodyHash(t, abs)

	if err := runStamp(t, rel, "--workdir", root,
		"--generated-by", "feature-dev/claude-sonnet-5",
		"--source", "https://github.com/nightgauge/nightgauge/issues/1",
	); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	block, err := okf.ParseFrontmatterFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if block.Generated == nil || block.Generated.By != "feature-dev/claude-sonnet-5" {
		t.Errorf("generated = %+v", block.Generated)
	}
	if block.Generated.At == "" {
		t.Error("generated.at was not stamped")
	}
	if len(block.Sources) != 1 || block.Sources[0].Resource != "https://github.com/nightgauge/nightgauge/issues/1" {
		t.Errorf("sources = %+v", block.Sources)
	}
	if block.Type != "prd" || block.Status != okf.StatusDraft {
		t.Errorf("stamp clobbered existing fields: %+v", block)
	}
	if after := bodyHash(t, abs); after != before {
		t.Error("the stamp rewrote the body")
	}
}

func TestKnowledgeStamp_DeduplicatesSourcesAndVerified(t *testing.T) {
	root, rel := stampFixture(t, stampBody)
	abs := filepath.Join(root, ".nightgauge", "knowledge", rel)

	for i := 0; i < 2; i++ {
		if err := runStamp(t, rel, "--workdir", root,
			"--source", "https://github.com/nightgauge/nightgauge/issues/1",
			"--verified-by", "process:retro",
		); err != nil {
			t.Fatalf("stamp %d: %v", i, err)
		}
	}

	block, err := okf.ParseFrontmatterFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Sources) != 1 {
		t.Errorf("sources = %d entries after two identical stamps, want 1", len(block.Sources))
	}
	// De-duplication is on the actor alone: `at` differs on every run, so a
	// by+at key would append forever and make retro non-idempotent.
	if len(block.Verified) != 1 {
		t.Errorf("verified = %d events after two identical stamps, want 1", len(block.Verified))
	}
}

func TestKnowledgeStamp_RejectsSourcesOutsideTheRepository(t *testing.T) {
	root, rel := stampFixture(t, stampBody)
	abs := filepath.Join(root, ".nightgauge", "knowledge", rel)

	// A real file above the workspace root, so the rejection cannot pass
	// vacuously through a "does not exist" branch.
	outside := filepath.Join(filepath.Dir(root), "outside-secret.md")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	cases := []struct {
		name   string
		source string
	}{
		{"parent traversal", "../outside-secret.md"},
		{"absolute path outside the repo", outside},
		{"deep traversal", "../../../../etc/passwd"},
		{"non-https scheme", "http://example.com/x"},
		{"file scheme", "file:///etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := os.ReadFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			if err := runStamp(t, rel, "--workdir", root, "--source", tc.source); err == nil {
				t.Fatalf("stamp accepted source %q", tc.source)
			}
			after, err := os.ReadFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Error("a rejected stamp still wrote to the entry")
			}
		})
	}
}

func TestKnowledgeStamp_RejectsASymlinkedSourceLeavingTheRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevation on Windows")
	}
	root, rel := stampFixture(t, stampBody)

	outside := filepath.Join(filepath.Dir(root), "outside-target.md")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	link := filepath.Join(root, "looks-local.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if err := runStamp(t, rel, "--workdir", root, "--source", "looks-local.md"); err == nil {
		t.Fatal("stamp accepted a source that is a symlink out of the repository")
	}
}

func TestKnowledgeStamp_RejectsATargetOutsideTheKnowledgeBase(t *testing.T) {
	root, _ := stampFixture(t, stampBody)

	// The stamp verb is the first knowledge command that takes a path from
	// the command line rather than deriving it from an issue number, so it is
	// the first that could be pointed at an arbitrary file.
	victim := filepath.Join(root, "shell-config")
	if err := os.WriteFile(victim, []byte("export PATH=/usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runStamp(t, "../../shell-config", "--workdir", root, "--status", "stable"); err == nil {
		t.Fatal("stamp accepted a target outside the knowledge base")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(data), "---") {
		t.Errorf("the stamp prepended a frontmatter block to a file outside the knowledge base:\n%s", data)
	}
}

func TestKnowledgeStamp_RejectsMalformedActorsAndStatuses(t *testing.T) {
	root, rel := stampFixture(t, stampBody)

	cases := [][]string{
		{"--generated-by", "I decided this"},
		{"--generated-by", "feature-dev"},
		{"--verified-by", "human:"},
		{"--status", "superseded"},
		{"--stale-after", "2027-01-01"},
	}
	for _, extra := range cases {
		args := append([]string{rel, "--workdir", root}, extra...)
		if err := runStamp(t, args...); err == nil {
			t.Errorf("stamp accepted %v", extra)
		}
	}
}

func TestKnowledgeStamp_RefusesAnEmptyStamp(t *testing.T) {
	root, rel := stampFixture(t, stampBody)
	if err := runStamp(t, rel, "--workdir", root); err == nil {
		t.Fatal("stamp with no fields should be an error, not a silent no-op write")
	}
}

func TestKnowledgeStamp_SeedsFrontmatterOnAnEntryWithout(t *testing.T) {
	root, rel := stampFixture(t, "# PRD: #42\n\nNo frontmatter here.\n")
	abs := filepath.Join(root, ".nightgauge", "knowledge", rel)

	if err := runStamp(t, rel, "--workdir", root, "--generated-by", "process:knowledge-migrate"); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "---\n") {
		t.Errorf("no block was written:\n%s", data)
	}
	if !strings.Contains(string(data), "No frontmatter here.") {
		t.Errorf("body lost:\n%s", data)
	}
}

// TestKnowledgeStamp_StageBuildsTheActorFromTheDispatchedModel pins the form
// skills use. The binary constructs the actor from the stage name and the
// dispatched model it exported itself, so no model-authored string can ever
// become a provenance claim.
func TestKnowledgeStamp_StageBuildsTheActorFromTheDispatchedModel(t *testing.T) {
	root, rel := stampFixture(t, stampBody)
	abs := filepath.Join(root, ".nightgauge", "knowledge", rel)

	t.Setenv("NIGHTGAUGE_DISPATCH_MODEL", "claude-sonnet-5")
	if err := runStamp(t, rel, "--workdir", root, "--stage", "feature-planning"); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	block, err := okf.ParseFrontmatterFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if block.Generated == nil || block.Generated.By != "feature-planning/claude-sonnet-5" {
		t.Fatalf("generated = %+v", block.Generated)
	}
	if block.TrustTier() != okf.TrustUnverified {
		t.Errorf("producing an entry is not verifying it: tier = %q", block.TrustTier())
	}
}

func TestKnowledgeStamp_StageWithoutADispatchedModelIsAnError(t *testing.T) {
	root, rel := stampFixture(t, stampBody)
	abs := filepath.Join(root, ".nightgauge", "knowledge", rel)
	before, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("NIGHTGAUGE_DISPATCH_MODEL", "")
	// Guessing a model, or falling back to a process: actor, would record a
	// provenance nobody can check. Refusing is the honest outcome.
	if err := runStamp(t, rel, "--workdir", root, "--stage", "feature-planning"); err == nil {
		t.Fatal("expected an error when the dispatched model is unknown")
	}
	after, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a rejected stamp still wrote to the entry")
	}
}

func TestKnowledgeStamp_StageAndGeneratedByAreAlternatives(t *testing.T) {
	root, rel := stampFixture(t, stampBody)
	t.Setenv("NIGHTGAUGE_DISPATCH_MODEL", "claude-sonnet-5")
	if err := runStamp(t, rel, "--workdir", root,
		"--stage", "feature-planning", "--generated-by", "process:retro"); err == nil {
		t.Fatal("expected --stage and --generated-by together to be rejected")
	}
}
