package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/qualityreview"
)

func qualityFixture(t *testing.T, changed ...string) (qualityRun, []byte) {
	t.Helper()
	s := reviewRepo(t)
	if len(changed) == 0 {
		changed = []string{"view.tsx"}
	}
	for _, f := range changed {
		if err := os.WriteFile(filepath.Join(s.Root, f), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile("../../skills/nightgauge-feature-dev/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return qualityRun{Root: s.Root, Issue: 7, Base: "main", Adapter: "fake", Skill: data}, data
}

var findingsPath = regexp.MustCompile(`Write ONLY (\S+/([a-z-]+)/findings\.json)`)

func TestQualityReviewFansOutSixUnitsOnAgenticAdapter(t *testing.T) {
	q, _ := qualityFixture(t)
	var spawns atomic.Int32
	var stages stageSet
	q.Runner = fakeReviewer{agentic: true}
	q.Stderr = &bytes.Buffer{}
	q.Spawn = func(_ context.Context, _ adapters.SkillRunner, o adapters.RunOptions, _ string) error {
		spawns.Add(1)
		stages.add(o.Stage)
		m := findingsPath.FindStringSubmatch(o.Prompt)
		if m == nil {
			t.Errorf("prompt names no findings file: %s", o.Prompt)
			return nil
		}
		body := `{"unit":"` + m[2] + `","result":"passed","findings":[]}`
		if m[2] == "security" {
			body = `{"unit":"security","result":"failed","findings":[{"severity":"blocking","file":"a","line":1,"claim":"secret"}]}`
		}
		return os.WriteFile(m[1], []byte(body), 0o600)
	}
	r := runQualityReview(context.Background(), q)
	if got := spawns.Load(); got != 6 {
		t.Fatalf("spawns = %d, want 6", got)
	}
	if len(r.Units) != 6 || r.Judge == nil || r.Judge.Mode != "merge" {
		t.Fatalf("report = %+v", r)
	}
	if r.Result != qualityreview.ResultFailed || r.UnitStatus("security") != qualityreview.ResultFailed ||
		r.UnitStatus("code-quality") != qualityreview.ResultPassed {
		t.Fatalf("merge = %+v", r)
	}
	if stages.only() != qualityreview.Stage {
		t.Fatalf("reviewer stage = %q", stages.only())
	}
}

func TestQualityReviewSkipsWithoutToolLoop(t *testing.T) {
	q, _ := qualityFixture(t)
	var stderr bytes.Buffer
	q.Runner, q.Stderr = fakeReviewer{agentic: false}, &stderr
	q.Spawn = func(context.Context, adapters.SkillRunner, adapters.RunOptions, string) error {
		t.Error("spawned on a non-agentic adapter")
		return nil
	}
	r := runQualityReview(context.Background(), q)
	if r.Result != qualityreview.ResultSkipped || !strings.Contains(r.Reason, "adapter fake has no tool loop") {
		t.Fatalf("report = %+v", r)
	}
	if !strings.Contains(stderr.String(), "[quality-review] skipped: adapter fake has no tool loop") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if r.UnitStatus("security") != qualityreview.ResultSkipped {
		t.Fatal("security not skipped")
	}
}

func TestQualityReviewFailsClosed(t *testing.T) {
	never := func(context.Context, adapters.SkillRunner, adapters.RunOptions, string) error {
		t.Error("spawned")
		return nil
	}
	for name, mut := range map[string]func(*qualityRun){
		"resolve error": func(q *qualityRun) { q.Resolve = errors.New("unknown adapter") },
		"no block":      func(q *qualityRun) { q.Skill = []byte("---\nname: x\n---\n") },
		"skill missing": func(q *qualityRun) { q.SkillErr = os.ErrNotExist },
	} {
		q, _ := qualityFixture(t)
		q.Runner, q.Stderr, q.Spawn = fakeReviewer{agentic: true}, &bytes.Buffer{}, never
		mut(&q)
		if r := runQualityReview(context.Background(), q); r.Result != qualityreview.ResultSkipped || r.Reason == "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestQualityReviewUnitThatWritesNothingIsUnfinished(t *testing.T) {
	q, _ := qualityFixture(t)
	q.Runner, q.Stderr = fakeReviewer{agentic: true}, &bytes.Buffer{}
	q.Spawn = func(context.Context, adapters.SkillRunner, adapters.RunOptions, string) error {
		return errors.New("exit 1")
	}
	r := runQualityReview(context.Background(), q)
	if r.Result != qualityreview.ResultSkipped || r.UnitStatus("security") != qualityreview.ResultSkipped {
		t.Fatalf("report = %+v", r)
	}
	if !strings.Contains(r.Units[0].Reason, "spawn failed") {
		t.Fatalf("reason = %q", r.Units[0].Reason)
	}
	if _, err := os.Stat(filepath.Join(q.Root, qualityreview.BundleParent, "7", "security", "diff.patch")); err != nil {
		t.Fatal(err)
	}
}

// stageSet collects the distinct stages the spawns ran under.
type stageSet struct {
	mu  sync.Mutex
	set map[string]bool
}

func (s *stageSet) add(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set == nil {
		s.set = map[string]bool{}
	}
	s.set[v] = true
}

func (s *stageSet) only() string {
	if len(s.set) != 1 {
		return ""
	}
	for k := range s.set {
		return k
	}
	return ""
}

func TestQualityReviewSkipsPathScopedUnitOnGoOnlyDiff(t *testing.T) {
	q, _ := qualityFixture(t, "x.go")
	var spawns atomic.Int32
	q.Runner, q.Stderr = fakeReviewer{agentic: true}, &bytes.Buffer{}
	q.Spawn = func(_ context.Context, _ adapters.SkillRunner, o adapters.RunOptions, _ string) error {
		spawns.Add(1)
		m := findingsPath.FindStringSubmatch(o.Prompt)
		if m == nil {
			t.Errorf("prompt names no findings file: %s", o.Prompt)
			return nil
		}
		if m[2] == "accessibility" {
			t.Error("spawned accessibility on a Go-only diff")
		}
		return os.WriteFile(m[1], []byte(`{"unit":"`+m[2]+`","findings":[]}`), 0o600)
	}
	r := runQualityReview(context.Background(), q)
	if got := spawns.Load(); got != 5 {
		t.Fatalf("spawns = %d, want 5", got)
	}
	if r.Result != qualityreview.ResultPassed || r.UnitStatus("accessibility") != qualityreview.ResultSkipped {
		t.Fatalf("report = %+v", r)
	}
	for _, u := range r.Units {
		if u.ID == "accessibility" && !strings.Contains(u.Reason, "no changed file matches paths") {
			t.Fatalf("accessibility reason = %q", u.Reason)
		}
	}
}

func TestTrustedQualitySkillRefusesWorktreeCopy(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "skills", "nightgauge-feature-dev")
	outside := t.TempDir()
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(outside, "link")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIGHTGAUGE_SKILL_DIR", "")
	if _, err := trustedQualitySkill(root, ""); err == nil {
		t.Error("no env and no flag resolved a skill (worktree fallback)")
	}
	for name, setup := range map[string]func() string{
		"env inside":          func() string { t.Setenv("NIGHTGAUGE_SKILL_DIR", inside); return "" },
		"flag inside":         func() string { return filepath.Join(inside, "SKILL.md") },
		"symlink into inside": func() string { return filepath.Join(link, "SKILL.md") },
	} {
		if _, err := trustedQualitySkill(root, setup()); err == nil || !strings.Contains(err.Error(), "no trusted skill copy") {
			t.Errorf("%s: err = %v", name, err)
		}
		t.Setenv("NIGHTGAUGE_SKILL_DIR", "")
	}
	t.Setenv("NIGHTGAUGE_SKILL_DIR", outside)
	if p, err := trustedQualitySkill(root, ""); err != nil || filepath.Base(p) != "SKILL.md" {
		t.Errorf("outside copy refused: %q %v", p, err)
	}
}
