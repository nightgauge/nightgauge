package qualityreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

const skill = `---
name: x
orchestration:
  mode: fanout
  units:
    - id: code-quality
      role: reviewer
    - id: security
      role: reviewer
  judge:
    mode: merge
    quorum: 1
---
body
`

func TestParseSpec(t *testing.T) {
	s, err := ParseSpec([]byte(skill))
	if err != nil || len(s.Units) != 2 || s.Units[1].ID != "security" || s.Judge.Quorum != 1 {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestParseSpecFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"no frontmatter": "body",
		"no block":       "---\nname: x\n---\n",
		"no units":       "---\norchestration:\n  units: []\n---\n",
		"repeated id":    "---\norchestration:\n  units:\n    - id: a\n    - id: a\n---\n",
		"vote judge":     "---\norchestration:\n  units:\n    - id: a\n  judge:\n    mode: vote\n---\n",
	} {
		if _, err := ParseSpec([]byte(body)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestShippedFeatureDevSkillDeclaresSixUnits(t *testing.T) {
	data, err := os.ReadFile("../../skills/nightgauge-feature-dev/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Units) != 6 {
		t.Fatalf("units = %d, want 6", len(s.Units))
	}
	for _, u := range s.Units {
		if _, ok := focus[u.ID]; !ok {
			t.Errorf("unit %q has no focus brief", u.ID)
		}
		if got := len(u.Paths) > 0; got != (u.ID == "accessibility") {
			t.Errorf("unit %q paths = %v; only accessibility is path-scoped", u.ID, u.Paths)
		}
	}
}

func TestUnitApplies(t *testing.T) {
	ui := Unit{ID: "accessibility", Paths: []string{"packages/nightgauge/src/webview/**", "**/*.tsx"}}
	for files, want := range map[string]bool{
		"cmd/x.go,internal/y.go":  false,
		"a.tsx":                   true,
		"packages/x/src/view.tsx": true,
		"packages/nightgauge/src/webview/main.ts":    true,
		"packages/nightgauge/src/webviewish/main.ts": false,
		"": false,
	} {
		var list []string
		if files != "" {
			list = strings.Split(files, ",")
		}
		if got := ui.Applies(list); got != want {
			t.Errorf("Applies(%q) = %v, want %v", files, got, want)
		}
	}
	if !(Unit{ID: "security"}).Applies([]string{"cmd/x.go"}) {
		t.Error("a unit without paths must always apply")
	}
}

// TestBuildBundleListsNonASCIIPathsUnquoted pins #2908: git quotes and
// octal-escapes a non-ASCII path in a newline listing, so a path-scoped unit
// never matched it and was skipped at zero tokens. core.quotePath is forced on
// so the operator's own git config cannot make the broken parse pass.
func TestBuildBundleListsNonASCIIPathsUnquoted(t *testing.T) {
	root := gittest.InitRepo(t, t.TempDir(), "-q")
	gittest.Run(t, root, "config", "core.quotePath", "true")
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", ".gitignore"), []byte("quality-review/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write("a")
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-qm", "base")
	gittest.Run(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	write("ui/café/Button.tsx") // committed: reaches the list through git diff
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-qm", "ui")
	write("ui/naïve/Card.tsx") // untracked: reaches it through git ls-files

	b, err := BuildBundle(context.Background(), root, 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ui/café/Button.tsx", "ui/naïve/Card.tsx"} {
		if !slices.Contains(b.Files, want) {
			t.Errorf("Files = %q, missing %q", b.Files, want)
		}
	}
	ui := Unit{ID: "accessibility", Paths: []string{"ui/café/**"}}
	if !ui.Applies(b.Files) {
		t.Errorf("unit scoped to ui/café/** does not apply to %q", b.Files)
	}
}

func TestMergeExcludesPathSkippedUnits(t *testing.T) {
	spec := Spec{Judge: Judge{Mode: "merge", Quorum: 2}}
	skipped := PathSkipped(Unit{ID: "accessibility", Paths: []string{"**/*.tsx"}})
	if skipped.Status != ResultSkipped || !strings.Contains(skipped.Reason, "**/*.tsx") {
		t.Fatalf("path-skipped = %+v", skipped)
	}
	one := UnitResult{ID: "security", Status: ResultPassed}
	r := Merge(spec, "a", []UnitResult{one, skipped})
	if r.Result != ResultPassed {
		t.Errorf("one applicable unit finished, quorum capped to it: %+v", r)
	}
	if r.UnitStatus("accessibility") != ResultSkipped {
		t.Error("path-skipped unit read as run")
	}
	unfinished := UnitResult{ID: "test", Status: StatusUnfinished}
	if r := Merge(spec, "a", []UnitResult{one, unfinished, skipped}); r.Result != ResultSkipped {
		t.Errorf("1 of 2 applicable finished under quorum 2 = %+v, want skipped", r)
	}
	all := Merge(spec, "a", []UnitResult{skipped})
	if all.Result != ResultSkipped || all.Reason != "no unit applies to the diff" {
		t.Errorf("all skipped = %+v", all)
	}
}

func TestParseSpecRejectsUnsafeIDAndEmptyPath(t *testing.T) {
	for name, body := range map[string]string{
		"traversal id": "---\norchestration:\n  units:\n    - id: ../x\n---\n",
		"empty glob":   "---\norchestration:\n  units:\n    - id: a\n      paths: [\"\"]\n---\n",
	} {
		if _, err := ParseSpec([]byte(body)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func write(t *testing.T, b Bundle, id, body string) {
	t.Helper()
	if err := os.MkdirAll(b.UnitDir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.FindingsPath(id), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadUnitAndMerge(t *testing.T) {
	b := Bundle{Dir: t.TempDir()}
	write(t, b, "ok", `{"unit":"ok","result":"failed","findings":[{"severity":"warning","file":"a","line":1,"claim":"c"}]}`)
	write(t, b, "bad", `{"unit":"bad","result":"passed","findings":[{"severity":"blocking","file":"a","line":1,"claim":"c"}]}`)
	write(t, b, "junk", `not json`)
	write(t, b, "sev", `{"unit":"sev","findings":[{"severity":"nit"}]}`)

	if r := ReadUnit(b, "ok"); r.Status != ResultPassed {
		t.Errorf("warning-only unit = %s, want passed (status follows findings)", r.Status)
	}
	if r := ReadUnit(b, "bad"); r.Status != ResultFailed {
		t.Errorf("blocking unit = %s, want failed", r.Status)
	}
	write(t, b, "old", `{"findings":[]}`)
	for _, id := range []string{"junk", "sev", "missing", "old"} {
		if r := ReadUnit(b, id); r.Status != StatusUnfinished || r.Reason == "" {
			t.Errorf("%s = %+v, want unfinished with reason", id, r)
		}
	}

	spec := Spec{Judge: Judge{Mode: "merge", Quorum: 1}}
	pass := Merge(spec, "a", []UnitResult{ReadUnit(b, "ok"), ReadUnit(b, "missing")})
	if pass.Result != ResultPassed || pass.Judge.Result != ResultPassed {
		t.Errorf("passed+unfinished = %s", pass.Result)
	}
	if pass.UnitStatus("missing") != ResultSkipped || pass.UnitStatus("ok") != ResultPassed {
		t.Errorf("UnitStatus wrong: %+v", pass)
	}
	if r := Merge(spec, "a", []UnitResult{ReadUnit(b, "ok"), ReadUnit(b, "bad")}); r.Result != ResultFailed {
		t.Errorf("with a blocking unit = %s, want failed", r.Result)
	}
	none := Merge(spec, "a", []UnitResult{ReadUnit(b, "junk"), ReadUnit(b, "missing")})
	if none.Result != ResultSkipped || !strings.Contains(none.Reason, "quorum") {
		t.Errorf("nothing finished = %+v, want skipped below quorum", none)
	}
	if none.UnitStatus("junk") != ResultSkipped {
		t.Error("an unfinished unit read as run")
	}
}

func TestPromptNamesUnitAndFindingsPath(t *testing.T) {
	b := Bundle{Dir: "/b"}
	p := Prompt("/root", b, Unit{ID: "security"})
	for _, want := range []string{"security reviewer", focus["security"],
		filepath.Join("/b", "security", "findings.json"), filepath.Join("/b", "security", "diff.patch"),
		`"unit":"security"`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, filepath.Join("/b", "test")) {
		t.Error("prompt names a sibling unit's directory")
	}
}

func TestUnitsAreIsolated(t *testing.T) {
	b := Bundle{Dir: t.TempDir(), Diff: []byte("d"), Files: []string{"a.go"}}
	for _, id := range []string{"a", "b"} {
		if err := b.PrepareUnit(id); err != nil {
			t.Fatal(err)
		}
	}
	if b.UnitDir("a") == b.UnitDir("b") || filepath.Dir(b.FindingsPath("a")) == filepath.Dir(b.FindingsPath("b")) {
		t.Fatal("units share a directory")
	}
	if pa := Prompt("/r", b, Unit{ID: "a"}); strings.Contains(pa, b.UnitDir("b")) {
		t.Error("unit a's prompt names unit b's directory")
	}
	// A file at b's path, or one in a's path claiming to be b's, never becomes a's result.
	write(t, b, "b", `{"unit":"b","findings":[{"severity":"blocking","file":"x","line":1,"claim":"c"}]}`)
	if r := ReadUnit(b, "a"); r.Status != StatusUnfinished {
		t.Errorf("a read b's file: %+v", r)
	}
	write(t, b, "a", `{"unit":"b","findings":[]}`)
	if r := ReadUnit(b, "a"); r.Status != StatusUnfinished || !strings.Contains(r.Reason, `"b"`) {
		t.Errorf("a accepted a file naming unit b: %+v", r)
	}
	if data, _ := os.ReadFile(b.FilesPath("a")); string(data) != "a.go\n" {
		t.Errorf("files.txt = %q", data)
	}
}

func TestReadUnitInvalidEscapeIsUnfinishedWithParseDetail(t *testing.T) {
	b := Bundle{Dir: t.TempDir()}
	write(t, b, "security", `{"unit":"security","result":"failed","findings":[{"severity":"warning","file":"a","line":1,"claim":"path \303 here"}]}`)
	r := ReadUnit(b, "security")
	if r.Status != StatusUnfinished {
		t.Fatalf("status = %s, want unfinished", r.Status)
	}
	for _, want := range []string{`"security"`, "invalid escape", "offset"} {
		if !strings.Contains(r.Reason, want) {
			t.Errorf("reason %q lacks %q", r.Reason, want)
		}
	}
}

func TestReadUnitSchemaFailuresNameTheCase(t *testing.T) {
	b := Bundle{Dir: t.TempDir()}
	write(t, b, "old", `{"unit":"old"}`)
	if r := ReadUnit(b, "old"); r.Status != StatusUnfinished || !strings.Contains(r.Reason, "no findings array") {
		t.Errorf("missing findings = %+v", r)
	}
	write(t, b, "typ", `{"unit":"typ","findings":[{"severity":"warning","line":"1"}]}`)
	r := ReadUnit(b, "typ")
	for _, want := range []string{`"typ"`, "wrong type", "offset"} {
		if r.Status != StatusUnfinished || !strings.Contains(r.Reason, want) {
			t.Errorf("type mismatch = %+v, want reason with %q", r, want)
		}
	}
}

func TestPromptTeachesBackslashEscape(t *testing.T) {
	if p := Prompt("/root", Bundle{Dir: "/b"}, Unit{ID: "test"}); !strings.Contains(p, `literal
backslash in a claim as \\ and`) {
		t.Errorf("prompt lacks escape rule:\n%s", p)
	}
}

func TestReadUnitEscapedBackslashPassesThrough(t *testing.T) {
	b := Bundle{Dir: t.TempDir()}
	write(t, b, "test", `{"unit":"test","result":"passed","findings":[{"severity":"warning","file":"a","line":1,"claim":"path \\303 here"}]}`)
	r := ReadUnit(b, "test")
	if r.Status != ResultPassed || len(r.Findings) != 1 || r.Findings[0].Claim != `path \303 here` {
		t.Fatalf("unit = %+v", r)
	}
	out, err := json.Marshal(Merge(Spec{Judge: Judge{Mode: "merge", Quorum: 1}}, "a", []UnitResult{r}))
	if err != nil || !json.Valid(out) {
		t.Fatalf("report invalid: %v %s", err, out)
	}
	var back Report
	if err := json.Unmarshal(out, &back); err != nil || back.Units[0].Findings[0].Claim != `path \303 here` {
		t.Fatalf("round trip = %+v %v", back, err)
	}
}
