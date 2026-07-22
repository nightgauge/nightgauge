package docs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates a file at filepath.Join(dir, rel) with the given body,
// creating any parent directories as needed.
func writeFile(t *testing.T, dir, rel, body string) string {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

func TestRun_HappyPath_AllLinksResolve(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "docs/A.md", "See [B](./B.md) and [C](../C.md).\n")
	writeFile(t, dir, "docs/B.md", "# B\n")
	writeFile(t, dir, "C.md", "# C\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	if res.LinksBroken != 0 {
		t.Errorf("LinksBroken = %d, want 0; findings=%+v", res.LinksBroken, res.Findings)
	}
	if res.LinksTotal != 2 {
		t.Errorf("LinksTotal = %d, want 2", res.LinksTotal)
	}
	if res.FilesScanned != 3 {
		t.Errorf("FilesScanned = %d, want 3", res.FilesScanned)
	}
}

func TestRun_BrokenLinkDetected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "docs/A.md", "Broken: [missing](./missing.md)\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinksBroken != 1 {
		t.Fatalf("LinksBroken = %d, want 1", res.LinksBroken)
	}
	f := res.Findings[0]
	if f.Reason != ReasonFileNotFound {
		t.Errorf("Reason = %q, want %q", f.Reason, ReasonFileNotFound)
	}
	if f.File != "docs/A.md" {
		t.Errorf("File = %q, want docs/A.md", f.File)
	}
	if f.Line != 1 {
		t.Errorf("Line = %d, want 1", f.Line)
	}
	if f.Link != "./missing.md" {
		t.Errorf("Link = %q, want ./missing.md", f.Link)
	}
}

func TestRun_CodeFenceSkipsLinks(t *testing.T) {
	dir := t.TempDir()
	body := "Real link: [exists](./exists.md)\n" +
		"```bash\n" +
		"echo [fake](./does-not-exist.md)\n" +
		"```\n" +
		"More text [also-bad](./also-missing.md)\n"
	writeFile(t, dir, "A.md", body)
	writeFile(t, dir, "exists.md", "# exists\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// `also-bad` IS outside the fence and missing → 1 broken finding.
	// `fake` inside the fence must NOT be flagged.
	if res.LinksBroken != 1 {
		t.Fatalf("LinksBroken = %d, want 1; findings=%+v", res.LinksBroken, res.Findings)
	}
	if !strings.Contains(res.Findings[0].Link, "also-missing") {
		t.Errorf("expected fence-outside link reported, got %+v", res.Findings[0])
	}
	for _, f := range res.Findings {
		if strings.Contains(f.Link, "does-not-exist") {
			t.Errorf("link inside fence was flagged: %+v", f)
		}
	}
}

func TestRun_TildeFenceAlsoSkipped(t *testing.T) {
	dir := t.TempDir()
	body := "~~~\n[fake](./missing.md)\n~~~\n"
	writeFile(t, dir, "A.md", body)

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinksBroken != 0 {
		t.Errorf("LinksBroken = %d, want 0; tilde fence should hide link", res.LinksBroken)
	}
	if res.LinksTotal != 0 {
		t.Errorf("LinksTotal = %d, want 0", res.LinksTotal)
	}
}

func TestRun_HTTPLinksIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "A.md", "[a](http://x) [b](https://y) [c](mailto:z@w) [d](#anchor) [e](tel:+1)\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinksTotal != 0 || res.LinksBroken != 0 {
		t.Errorf("external/in-page links must be ignored: total=%d broken=%d", res.LinksTotal, res.LinksBroken)
	}
}

func TestRun_AnchorRecordedButNotValidated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "A.md", "[heading](./B.md#some-section)\n")
	writeFile(t, dir, "B.md", "# B\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinksBroken != 0 {
		t.Errorf("anchor must not affect validation; got broken=%d", res.LinksBroken)
	}
}

func TestRun_AnchorOnMissingFileFlaggedWithAnchorRecorded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "A.md", "[heading](./missing.md#section)\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinksBroken != 1 {
		t.Fatalf("LinksBroken = %d, want 1", res.LinksBroken)
	}
	if got, want := res.Findings[0].Anchor, "section"; got != want {
		t.Errorf("Anchor = %q, want %q", got, want)
	}
}

func TestRun_TargetFilterRestrictsToOneFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "A.md", "[broken](./nope-A.md)\n")
	writeFile(t, dir, "B.md", "[broken](./nope-B.md)\n")

	res, err := Run(context.Background(), CheckLinksOptions{
		Root:   dir,
		Target: "A.md",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", res.FilesScanned)
	}
	if res.LinksBroken != 1 {
		t.Fatalf("LinksBroken = %d, want 1", res.LinksBroken)
	}
	if res.Findings[0].File != "A.md" {
		t.Errorf("Findings[0].File = %q, want A.md", res.Findings[0].File)
	}
}

func TestRun_TargetOutsideRoot_Errors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "A.md", "# A\n")

	_, err := Run(context.Background(), CheckLinksOptions{
		Root:   dir,
		Target: "../escape.md",
	})
	if err == nil {
		t.Fatal("expected error for target outside root")
	}
}

func TestRun_NonexistentRoot_Errors(t *testing.T) {
	_, err := Run(context.Background(), CheckLinksOptions{
		Root: filepath.Join(t.TempDir(), "does", "not", "exist"),
	})
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestRun_OutsideRootFlagged(t *testing.T) {
	// A link of `../../../escape.md` resolves above Root → outside_root.
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, parent, "outside.md", "# outside\n")
	writeFile(t, root, "A.md", "[escape](../outside.md)\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinksBroken != 1 {
		t.Fatalf("LinksBroken = %d, want 1", res.LinksBroken)
	}
	if res.Findings[0].Reason != ReasonOutsideRoot {
		t.Errorf("Reason = %q, want %q", res.Findings[0].Reason, ReasonOutsideRoot)
	}
}

func TestRun_SectionFilterScopesValidation(t *testing.T) {
	dir := t.TempDir()
	body := "" +
		"# Top\n" +
		"\n" +
		"[outside](./missing-outside.md)\n" +
		"\n" +
		"## Validate Links\n" +
		"\n" +
		"[inside](./missing-inside.md)\n" +
		"\n" +
		"## Other\n" +
		"\n" +
		"[other](./missing-other.md)\n"
	writeFile(t, dir, "A.md", body)

	res, err := Run(context.Background(), CheckLinksOptions{
		Root:    dir,
		Section: "Validate Links",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinksBroken != 1 {
		t.Fatalf("LinksBroken = %d, want 1; got %+v", res.LinksBroken, res.Findings)
	}
	if !strings.Contains(res.Findings[0].Link, "missing-inside") {
		t.Errorf("section filter selected wrong link: %+v", res.Findings[0])
	}
}

func TestRun_SectionFilterCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	body := "" +
		"## Phase 7: VALIDATE LINKS\n" +
		"\n" +
		"[inside](./missing.md)\n"
	writeFile(t, dir, "A.md", body)

	res, err := Run(context.Background(), CheckLinksOptions{
		Root:    dir,
		Section: "phase 7: validate links",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinksBroken != 1 {
		t.Errorf("LinksBroken = %d, want 1", res.LinksBroken)
	}
}

func TestRun_ExcludeTemplatesSkipsSkillFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "skills/example/SKILL.md", "[broken](./nope.md)\n")
	writeFile(t, dir, "claude-plugins/foo/commands/bar.md", "[broken](./nope.md)\n")
	writeFile(t, dir, "docs/REAL.md", "[broken](./nope.md)\n")

	res, err := Run(context.Background(), CheckLinksOptions{
		Root:             dir,
		ExcludeTemplates: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Only docs/REAL.md should have been scanned.
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", res.FilesScanned)
	}
	if res.LinksBroken != 1 {
		t.Errorf("LinksBroken = %d, want 1", res.LinksBroken)
	}
	if res.Findings[0].File != "docs/REAL.md" {
		t.Errorf("Findings[0].File = %q, want docs/REAL.md", res.Findings[0].File)
	}
}

func TestRun_SkipsBuildDirectories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node_modules/pkg/README.md", "[broken](./nope.md)\n")
	writeFile(t, dir, ".git/HEADish.md", "[broken](./nope.md)\n")
	writeFile(t, dir, "dist/x.md", "[broken](./nope.md)\n")
	writeFile(t, dir, "docs/REAL.md", "# real\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FilesScanned != 1 || res.LinksBroken != 0 {
		t.Errorf("expected only docs/REAL.md scanned; got files=%d broken=%d findings=%+v",
			res.FilesScanned, res.LinksBroken, res.Findings)
	}
}

func TestRun_LinksTotalCountsResolvedAndBroken(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "A.md", "[ok](./B.md) [bad](./missing.md) [http](https://x)\n")
	writeFile(t, dir, "B.md", "# B\n")

	res, err := Run(context.Background(), CheckLinksOptions{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// http link is filtered before counting.
	if res.LinksTotal != 2 {
		t.Errorf("LinksTotal = %d, want 2", res.LinksTotal)
	}
	if res.LinksBroken != 1 {
		t.Errorf("LinksBroken = %d, want 1", res.LinksBroken)
	}
}

func TestRun_TargetCanBeAbsolute(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "A.md", "[bad](./missing.md)\n")
	writeFile(t, dir, "B.md", "# B\n")

	abs := filepath.Join(dir, "A.md")
	res, err := Run(context.Background(), CheckLinksOptions{
		Root:   dir,
		Target: abs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FilesScanned != 1 || res.LinksBroken != 1 {
		t.Errorf("FilesScanned=%d LinksBroken=%d", res.FilesScanned, res.LinksBroken)
	}
}

func TestIsTemplatePath(t *testing.T) {
	cases := map[string]bool{
		"/repo/skills/foo/SKILL.md":                  true,
		"/repo/claude-plugins/inc/commands/x.md":     true,
		"/repo/claude-plugins/inc/commands/sub/y.md": true,
		"/repo/docs/ARCHITECTURE.md":                 false,
		"/repo/skills/foo/README.md":                 false,
		"/repo/skills-archive/foo/SKILL.md":          false,
		"/repo/skills/foo/SKILL.md.bak":              false,
	}
	for path, want := range cases {
		if got := isTemplatePath(path); got != want {
			t.Errorf("isTemplatePath(%q) = %v, want %v", path, got, want)
		}
	}
}
