package qualityreview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	}
}

func write(t *testing.T, b Bundle, id, body string) {
	t.Helper()
	if err := os.WriteFile(b.FindingsPath(id), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadUnitAndMerge(t *testing.T) {
	b := Bundle{Dir: t.TempDir()}
	write(t, b, "ok", `{"result":"failed","findings":[{"severity":"warning","file":"a","line":1,"claim":"c"}]}`)
	write(t, b, "bad", `{"result":"passed","findings":[{"severity":"blocking","file":"a","line":1,"claim":"c"}]}`)
	write(t, b, "junk", `not json`)
	write(t, b, "sev", `{"findings":[{"severity":"nit"}]}`)

	if r := ReadUnit(b, "ok"); r.Status != ResultPassed {
		t.Errorf("warning-only unit = %s, want passed (status follows findings)", r.Status)
	}
	if r := ReadUnit(b, "bad"); r.Status != ResultFailed {
		t.Errorf("blocking unit = %s, want failed", r.Status)
	}
	for _, id := range []string{"junk", "sev", "missing"} {
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
	b := Bundle{Dir: "/b", DiffPath: "/b/diff.patch", FilesPath: "/b/files.txt"}
	p := Prompt("/root", b, Unit{ID: "security"})
	for _, want := range []string{"security reviewer", focus["security"], filepath.Join("/b", "findings-security.json"), "/b/diff.patch"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
