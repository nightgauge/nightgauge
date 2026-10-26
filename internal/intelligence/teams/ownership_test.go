package teams

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func ownershipBody(acs []string, files ...string) string {
	var b strings.Builder
	b.WriteString("## Acceptance Criteria\n\n")
	for _, ac := range acs {
		b.WriteString("- [ ] " + ac + "\n")
	}
	b.WriteString("\n<!-- nightgauge:dependency-metadata\nfile_ownership:\n")
	for _, f := range files {
		b.WriteString("  - " + f + "\n")
	}
	b.WriteString("-->\n")
	return b.String()
}

func existsIn(paths ...string) func(string) bool {
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	return func(p string) bool { return set[p] }
}

func rules(fs []OwnershipFinding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule+":"+f.Path)
	}
	return out
}

func TestFileOwnership_TestMissing(t *testing.T) {
	body := ownershipBody([]string{"A regression test covers the new branch"}, "internal/x/x.go")
	got := CheckFileOwnership(body, existsIn("internal/x/x.go"))
	if len(got) != 1 || got[0].Rule != OwnershipRuleTestMissing || got[0].Type != OwnershipFindingType {
		t.Fatalf("findings = %v, want one test-missing", rules(got))
	}
}

func TestFileOwnership_TestSatisfiedOnlyByDeclaredTest(t *testing.T) {
	acs := []string{"Add unit tests for the parser"}
	onDisk := existsIn("internal/x/x.go", "internal/x/x_test.go")
	// #536 shape: the sibling test exists but is outside the declared set.
	sibling := CheckFileOwnership(ownershipBody(acs, "internal/x/x.go"), onDisk)
	declared := CheckFileOwnership(ownershipBody(acs, "internal/x/x.go", "internal/x/x_test.go"), onDisk)
	if len(sibling) != 1 || sibling[0].Rule != OwnershipRuleTestMissing {
		t.Errorf("sibling-only = %v, want test-missing", rules(sibling))
	}
	if len(declared) != 0 {
		t.Errorf("declared test = %v, want none", rules(declared))
	}
}

func TestFileOwnership_GeneratedMirrorNamesCanonical(t *testing.T) {
	body := ownershipBody([]string{"Document the flag"},
		"claude-plugins/nightgauge/skills/issue-audit/SKILL.md",
		"claude-plugins/nightgauge/skills/_shared/GOTCHAS.md",
		"internal/models/model-registry.json")
	got := CheckFileOwnership(body, existsIn("skills/nightgauge-issue-audit"))
	want := map[string]string{
		"claude-plugins/nightgauge/skills/issue-audit/SKILL.md": "skills/nightgauge-issue-audit/SKILL.md",
		"claude-plugins/nightgauge/skills/_shared/GOTCHAS.md":   "skills/_shared/GOTCHAS.md",
		"internal/models/model-registry.json":                   "packages/nightgauge-sdk/src/eval/model-registry.json",
	}
	if len(got) != len(want) {
		t.Fatalf("findings = %v, want %d generated-mirror", rules(got), len(want))
	}
	for _, f := range got {
		if f.Rule != OwnershipRuleGeneratedMirror || f.Canonical != want[f.Path] {
			t.Errorf("finding %+v, want canonical %q", f, want[f.Path])
		}
	}
}

func TestFileOwnership_PathMissing(t *testing.T) {
	body := ownershipBody([]string{"The flag is documented"}, "docs/GONE.md")
	got := CheckFileOwnership(body, existsIn())
	if len(got) != 1 || got[0].Rule != OwnershipRulePathMissing || got[0].Path != "docs/GONE.md" {
		t.Fatalf("findings = %v, want path-missing docs/GONE.md", rules(got))
	}
}

func TestFileOwnership_CreateACSuppressesPathMissing(t *testing.T) {
	body := ownershipBody([]string{"Create `docs/NEW.md` describing the flag"}, "docs/NEW.md")
	if got := CheckFileOwnership(body, existsIn()); len(got) != 0 {
		t.Fatalf("findings = %v, want none", rules(got))
	}
}

func TestFileOwnership_ConsistentSetAndNoBlock(t *testing.T) {
	consistent := ownershipBody([]string{"Update the docs"}, "docs/A.md")
	if got := CheckFileOwnership(consistent, existsIn("docs/A.md")); len(got) != 0 {
		t.Fatalf("consistent findings = %v, want none", rules(got))
	}
	if got := CheckFileOwnership("- [ ] add a test\n\nEdit `x.go`.", existsIn()); got != nil {
		t.Fatalf("no-block findings = %v, want nil", rules(got))
	}
}

func TestFileOwnership_SortedDeterministic(t *testing.T) {
	body := ownershipBody([]string{"Add a test"}, "z/missing.go", "a/missing.go")
	got := rules(CheckFileOwnership(body, existsIn()))
	want := []string{"test-missing:", "path-missing:a/missing.go", "path-missing:z/missing.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestFileOwnership_MirrorTableParity keeps GeneratedMirrors in step with the
// scripts that actually generate the mirrors (ADR-004).
func TestFileOwnership_MirrorTableParity(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(data)
	}
	has := func(generated, canonical string) bool {
		for _, m := range GeneratedMirrors {
			if m.Generated == generated && m.Canonical == canonical {
				return true
			}
		}
		return false
	}

	install := read("scripts/install-agent-skills.sh")
	m := regexp.MustCompile(`(?m)^MIRROR_REL="([^"]+)"`).FindStringSubmatch(install)
	if m == nil {
		t.Fatal("MIRROR_REL not found in install-agent-skills.sh")
	}
	if !has(m[1]+"/", "skills/") {
		t.Errorf("GeneratedMirrors lacks %s/ -> skills/", m[1])
	}

	sync := read("scripts/sync-model-registry.sh")
	src := regexp.MustCompile(`(?m)^SRC="\$ROOT/([^"]+)"`).FindStringSubmatch(sync)
	dst := regexp.MustCompile(`(?m)^DST="\$ROOT/([^"]+)"`).FindStringSubmatch(sync)
	if src == nil || dst == nil {
		t.Fatal("SRC/DST not found in sync-model-registry.sh")
	}
	if !has(dst[1], src[1]) {
		t.Errorf("GeneratedMirrors lacks %s -> %s", dst[1], src[1])
	}
}
