package teams

import (
	"testing"
)

func TestDetectDepsSharedFiles(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Title: "First", Files: []string{"src/shared.go"}},
		{Number: 2, Title: "Second", Files: []string{"src/shared.go", "src/other.go"}},
	}

	deps := DetectDependencies(issues, nil, DefaultDependencyConfig())

	// Issue 1 (index 1) should depend on issue 0 (shared file)
	if d, ok := deps[1]; !ok || len(d) == 0 {
		t.Error("expected issue[1] to depend on issue[0] via shared files")
	}
}

func TestDetectDepsSequentialKeywords(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Title: "Setup database"},
		{Number: 2, Title: "Add API endpoints"},
	}
	sources := []string{
		"Initial setup for the database layer",
		"Add API endpoints. This depends on Setup database being complete.",
	}

	deps := DetectDependencies(issues, sources, DefaultDependencyConfig())

	if d, ok := deps[1]; !ok || len(d) == 0 {
		t.Error("expected issue[1] to depend on issue[0] via sequential keywords")
	}
}

func TestDetectDepsNoDeps(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Title: "Foo", Files: []string{"a.go"}},
		{Number: 2, Title: "Bar", Files: []string{"b.go"}},
	}

	deps := DetectDependencies(issues, nil, DefaultDependencyConfig())
	if len(deps) != 0 {
		t.Errorf("expected no dependencies, got %v", deps)
	}
}

func TestExtractTargetFilesDart(t *testing.T) {
	// The #143/#144 collision class: Flutter pages referenced both as a
	// directory-qualified path and as a bare filename in prose/lists.
	body := "Read-first view changes:\n" +
		"- `lib/pages/journal_entry_page.dart`\n" +
		"- journal_list_page.dart\n\n" +
		"Touches lib/widgets/entry_card.dart too."

	files := ExtractTargetFiles(body)
	want := map[string]bool{
		"lib/pages/journal_entry_page.dart": true,
		"journal_list_page.dart":            true,
		"lib/widgets/entry_card.dart":       true,
	}
	for w := range want {
		found := false
		for _, f := range files {
			if f == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in extracted files, got %v", w, files)
		}
	}
}

func TestExtractTargetFilesMixedLanguages(t *testing.T) {
	// Regression guard for the widened extension allowlist across forms.
	body := "Updates packages/sdk/src/Orchestrator.ts, internal/scheduler.go, " +
		"`app/models/user.rb`, config.yaml and android/MainActivity.kt."

	files := ExtractTargetFiles(body)
	for _, want := range []string{
		"packages/sdk/src/Orchestrator.ts",
		"internal/scheduler.go",
		"app/models/user.rb",
		"config.yaml",
		"android/MainActivity.kt",
	} {
		found := false
		for _, f := range files {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in extracted files, got %v", want, files)
		}
	}
}

func TestExtractTargetFilesIgnoresProse(t *testing.T) {
	// "e.g." / "i.e." and bare words must not be mistaken for files.
	body := "Refactor the dashboard, e.g. by extracting helpers (i.e. utilities)."
	files := ExtractTargetFiles(body)
	if len(files) != 0 {
		t.Errorf("expected no files extracted from prose, got %v", files)
	}
}

func TestDetectDepsImportChains(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Title: "Create utils", Files: []string{"src/utils.ts"}},
		{Number: 2, Title: "Use utils", Files: []string{"src/main.ts"}},
	}
	sources := []string{
		"Create the utils module at src/utils.ts",
		"Main module at src/main.ts that imports src/utils.ts",
	}

	deps := DetectDependencies(issues, sources, DefaultDependencyConfig())

	if d, ok := deps[1]; !ok || len(d) == 0 {
		t.Error("expected issue[1] to depend on issue[0] via import chain")
	}
}

// ── #79: citations are not change targets ────────────────────────────────

// TestExtractTargetFilesExcludesMarkdownLinks pins the #79 fix: a markdown
// link (or image) is a CITATION — evidence, prior art, a spike doc — and its
// destination must never count as a change target. Counting link destinations
// is what falsely serialized epic #71 behind one shared spike-doc reference.
func TestExtractTargetFilesExcludesMarkdownLinks(t *testing.T) {
	body := "Fix the cache in `internal/cache/lru.go`.\n" +
		"Evidence: [the spike](docs/spikes/fable-5-behavior-porting.md) and " +
		"[current schema](packages/nightgauge-sdk/src/eval/modelEvalSchemas.ts).\n" +
		"![diagram](docs/img/cache-flow.png)"

	files := ExtractTargetFiles(body)
	if len(files) != 1 || files[0] != "internal/cache/lru.go" {
		t.Errorf("expected only the inline-code target, got %v", files)
	}
}

// A bare path in prose still counts — regex extraction cannot read intent, so
// the documented author escape hatches are markdown links or an explicit
// file_ownership declaration.
func TestExtractTargetFilesBareProsePathStillCounts(t *testing.T) {
	body := "See docs/spikes/fable-5-behavior-porting.md for background."
	files := ExtractTargetFiles(body)
	if len(files) != 1 || files[0] != "docs/spikes/fable-5-behavior-porting.md" {
		t.Errorf("bare prose path should still extract, got %v", files)
	}
}

// TestExtractTargetFilesDeclarationWins pins the file_ownership override
// (#79): an explicit declaration in the dependency-metadata block is the
// author's statement of the change surface — prose citations cannot re-widen
// it, and the declared list is returned verbatim.
func TestExtractTargetFilesDeclarationWins(t *testing.T) {
	body := "Refactor cites a.ts b.ts c.ts d.ts e.ts f.ts and src/other/real.go in prose.\n" +
		"<!-- nightgauge:dependency-metadata\n" +
		"parallel_eligible: true\n" +
		"file_ownership:\n" +
		"  - src/real/target.ts\n" +
		"  - src/real/target.test.ts\n" +
		"-->"

	files, source := ExtractTargetFilesDetailed(body)
	if source != "declared" {
		t.Fatalf("source = %q, want declared", source)
	}
	want := []string{"src/real/target.ts", "src/real/target.test.ts"}
	if len(files) != len(want) || files[0] != want[0] || files[1] != want[1] {
		t.Errorf("declared targets = %v, want %v", files, want)
	}
}

// A malformed or empty declaration falls back to prose inference rather than
// silently zeroing the change surface.
func TestExtractTargetFilesEmptyDeclarationFallsBack(t *testing.T) {
	body := "Touches `src/main.ts`.\n" +
		"<!-- nightgauge:dependency-metadata\nparallel_eligible: true\nfile_ownership: []\n-->"
	files, source := ExtractTargetFilesDetailed(body)
	if source != "inferred" {
		t.Fatalf("source = %q, want inferred (empty declaration)", source)
	}
	if len(files) != 1 || files[0] != "src/main.ts" {
		t.Errorf("expected inferred fallback to find src/main.ts, got %v", files)
	}

	malformed := "Touches `src/main.ts`.\n<!-- nightgauge:dependency-metadata\n\t{{bad yaml\n-->"
	files2, source2 := ExtractTargetFilesDetailed(malformed)
	if source2 != "inferred" || len(files2) != 1 {
		t.Errorf("malformed declaration must fall back to inference, got %v (%s)", files2, source2)
	}
}
