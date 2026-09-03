package okf_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
)

// The actor convention from the frontmatter contract. Restated here rather than
// exported from the package under test so a change to the production regex
// fails this test instead of silently redefining the contract.
var actorSpec = regexp.MustCompile(`^([a-z0-9._-]+/[A-Za-z0-9._-]+|human:\S+|process:\S+)$`)

func TestParseFrontmatter_FullContract(t *testing.T) {
	src := strings.Join([]string{
		"---",
		"type: decisions",
		"title: 'Decisions: #7'",
		"description: What we decided on #7",
		"tags: [kb, adr]",
		"related: ['#7']",
		"repos: [nightgauge]",
		"status: draft",
		"superseded_by: '#9'",
		"generated:",
		"  by: feature-dev/claude-sonnet-5",
		"  at: 2026-09-03T10:00:00Z",
		"verified:",
		"  - by: process:retro",
		"    at: 2026-09-04T10:00:00Z",
		"  - by: human:octocat",
		"    at: 2026-09-05T10:00:00Z",
		"sources:",
		"  - resource: https://github.com/nightgauge/nightgauge/issues/7",
		"    title: The issue",
		"  - resource: /architecture/go-ts-parity.md",
		"stale_after: 2027-01-01T00:00:00Z",
		"future_field_we_do_not_know: 42",
		"---",
		"",
		"# Decisions",
		"",
		"body",
		"",
	}, "\n")

	b, err := okf.ParseFrontmatter(src)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if b.Type != "decisions" {
		t.Errorf("type = %q", b.Type)
	}
	if b.Title == "" || b.Description == "" {
		t.Errorf("title = %q description = %q", b.Title, b.Description)
	}
	if len(b.Tags) != 2 || len(b.Related) != 1 || len(b.Repos) != 1 {
		t.Errorf("tags=%v related=%v repos=%v", b.Tags, b.Related, b.Repos)
	}
	if b.Status != okf.StatusDraft || b.SupersededBy != "#9" {
		t.Errorf("status=%q superseded_by=%q", b.Status, b.SupersededBy)
	}
	if b.Generated == nil || b.Generated.By != "feature-dev/claude-sonnet-5" || b.Generated.At != "2026-09-03T10:00:00Z" {
		t.Errorf("generated = %+v", b.Generated)
	}
	if len(b.Verified) != 2 || b.Verified[0].By != "process:retro" || b.Verified[1].By != "human:octocat" {
		t.Errorf("verified = %+v", b.Verified)
	}
	if len(b.Sources) != 2 || b.Sources[0].Title != "The issue" || b.Sources[1].Resource != "/architecture/go-ts-parity.md" {
		t.Errorf("sources = %+v", b.Sources)
	}
	if b.StaleAfter != "2027-01-01T00:00:00Z" {
		t.Errorf("stale_after = %q", b.StaleAfter)
	}
	// OKF consumer tolerance: an unknown key is carried, never fatal.
	if _, ok := b.Raw["future_field_we_do_not_know"]; !ok {
		t.Error("unknown key was dropped from Raw")
	}
}

func TestParseFrontmatter_ToleratesUnknownTypeAndMissingOptionals(t *testing.T) {
	b, err := okf.ParseFrontmatter("---\ntype: some-future-kind\n---\n# Doc\n")
	if err != nil {
		t.Fatalf("unknown type must not fail parsing: %v", err)
	}
	if b.Type != "some-future-kind" {
		t.Errorf("type = %q", b.Type)
	}
	if b.EffectiveStatus() != okf.StatusStable {
		t.Errorf("absent status must default to %q, got %q", okf.StatusStable, b.EffectiveStatus())
	}
}

func TestParseFrontmatter_RejectsSupersededStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.md")
	if err := os.WriteFile(path, []byte("---\nstatus: superseded\n---\n# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := okf.ParseFrontmatterFile(path)
	if err == nil {
		t.Fatal("expected an error for the deleted `superseded` status")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error must name the file, got %q", err)
	}
	if !strings.Contains(err.Error(), "deprecated") {
		t.Errorf("error must point at the replacement, got %q", err)
	}
}

func TestRenderFrontmatter_RoundTripsAndPreservesBody(t *testing.T) {
	src := "---\ntype: prd\nstatus: draft\ncustom: keepme\n---\n\n# Title\n\nbody text\n"
	b, err := okf.ParseFrontmatter(src)
	if err != nil {
		t.Fatal(err)
	}
	_, body := okf.SplitFrontmatter(src)
	if body != "# Title\n\nbody text\n" {
		t.Fatalf("body = %q", body)
	}
	out, err := okf.WithFrontmatter(b, body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "custom: keepme") {
		t.Errorf("unknown key was dropped on render:\n%s", out)
	}
	again, err := okf.ParseFrontmatter(out)
	if err != nil {
		t.Fatal(err)
	}
	if again.Type != b.Type || again.Status != b.Status {
		t.Errorf("round trip changed the contract fields: %+v vs %+v", again, b)
	}
	if _, rebody := okf.SplitFrontmatter(out); rebody != body {
		t.Errorf("body changed across a render: %q vs %q", rebody, body)
	}
}

func TestSplitFrontmatter_NoFrontmatterIsIdentity(t *testing.T) {
	src := "# Plain\n\nno block here\n"
	fm, body := okf.SplitFrontmatter(src)
	if fm != "" || body != src {
		t.Errorf("fm=%q body=%q", fm, body)
	}
}

func TestActorFormat(t *testing.T) {
	stage, err := okf.StageActor("feature-planning", "claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	proc, err := okf.ProcessActor("retro")
	if err != nil {
		t.Fatal(err)
	}
	human, err := okf.HumanActor("octocat")
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{stage, proc, human, okf.ScaffoldActor} {
		if !actorSpec.MatchString(actor) {
			t.Errorf("actor %q does not match the contract convention", actor)
		}
		if !okf.ValidActor(actor) {
			t.Errorf("ValidActor rejected its own output %q", actor)
		}
	}
	if stage != "feature-planning/claude-sonnet-5" {
		t.Errorf("stage actor = %q", stage)
	}

	// Model-authored prose can never become an actor string.
	for _, bad := range []string{
		"", "claude sonnet 5", "I decided this", "feature-dev",
		"human:", "process:", "Feature-Dev/claude", "a/b c",
	} {
		if okf.ValidActor(bad) {
			t.Errorf("ValidActor accepted %q", bad)
		}
	}
	if _, err := okf.StageActor("feature dev", "claude-sonnet-5"); err == nil {
		t.Error("StageActor accepted a stage name with a space")
	}
	if _, err := okf.HumanActor("oct cat"); err == nil {
		t.Error("HumanActor accepted a login with a space")
	}
}

func TestScaffoldFrontmatter_StampsTypeAndProvenance(t *testing.T) {
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	orig := okf.Now
	okf.Now = func() time.Time { return fixed }
	t.Cleanup(func() { okf.Now = orig })

	fm, err := okf.ScaffoldFrontmatter(okf.TypePRD, okf.WithTitle("PRD: #7"), okf.WithTags("kb"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := okf.ParseFrontmatter(fm + "\n# Body\n")
	if err != nil {
		t.Fatal(err)
	}
	if b.Type != okf.TypePRD {
		t.Errorf("type = %q", b.Type)
	}
	if b.Status != okf.StatusDraft {
		t.Errorf("a scaffolded template nobody reviewed must start as a draft, got %q", b.Status)
	}
	if b.Generated == nil || b.Generated.By != okf.ScaffoldActor {
		t.Fatalf("generated = %+v", b.Generated)
	}
	if b.Generated.At != "2026-09-03T12:00:00Z" {
		t.Errorf("generated.at = %q", b.Generated.At)
	}
}

// TestRenderFrontmatter_PreservesNestedUnknownKeys pins the parity the
// TypeScript ProvenanceSchema/SourceSchema already have via .passthrough():
// a key a foreign producer wrote inside `generated`, `verified[]` or
// `sources[]` survives a parse/render round trip instead of being silently
// deleted the first time anything stamps the entry.
func TestRenderFrontmatter_PreservesNestedUnknownKeys(t *testing.T) {
	src := strings.Join([]string{
		"---",
		"type: prd",
		"generated:",
		"  by: process:retro",
		"  at: \"2026-09-03T10:00:00Z\"",
		"  run_id: abc123",
		"verified:",
		"  - by: human:octocat",
		"    confidence: high",
		"sources:",
		"  - resource: https://example.com/1",
		"    checksum: deadbeef",
		"---",
		"",
		"# Body",
		"",
	}, "\n")

	b, err := okf.ParseFrontmatter(src)
	if err != nil {
		t.Fatal(err)
	}
	if b.Generated.Extra["run_id"] != "abc123" {
		t.Errorf("generated extras = %v", b.Generated.Extra)
	}

	_, body := okf.SplitFrontmatter(src)
	out, err := okf.WithFrontmatter(b, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run_id: abc123", "confidence: high", "checksum: deadbeef"} {
		if !strings.Contains(out, want) {
			t.Errorf("render dropped %q:\n%s", want, out)
		}
	}
	// The contract fields still come first inside each nested mapping.
	if !strings.Contains(out, "by: process:retro") || !strings.Contains(out, "resource: https://example.com/1") {
		t.Errorf("render lost a contract field:\n%s", out)
	}
}
