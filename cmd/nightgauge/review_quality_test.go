package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/qualityreview"
)

func qualityFixture(t *testing.T, changed ...string) (qualityRun, []byte) {
	t.Helper()
	s := reviewRepo(t)
	if len(changed) == 0 {
		changed = []string{"view.tsx"}
	}
	for _, f := range changed {
		p := filepath.Join(s.Root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
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

// TestQualityReviewRunsPathScopedUnitOnNonASCIIPath pins #2908: git quotes a
// non-ASCII path ("ui/caf\303\251/Button.tsx") unless the list is read with -z,
// and the quoted form matches no "**/*.tsx" glob, so accessibility was skipped
// on exactly the UI change it exists to review.
func TestQualityReviewRunsPathScopedUnitOnNonASCIIPath(t *testing.T) {
	q, _ := qualityFixture(t, "ui/café/Button.tsx")
	// Pinned so a git config with core.quotePath=false cannot hide the defect.
	gittest.Run(t, q.Root, "config", "core.quotePath", "true")
	var spawned atomic.Bool
	q.Runner, q.Stderr = fakeReviewer{agentic: true}, &bytes.Buffer{}
	q.Spawn = func(_ context.Context, _ adapters.SkillRunner, o adapters.RunOptions, _ string) error {
		m := findingsPath.FindStringSubmatch(o.Prompt)
		if m == nil {
			t.Errorf("prompt names no findings file: %s", o.Prompt)
			return nil
		}
		if m[2] == "accessibility" {
			spawned.Store(true)
		}
		return os.WriteFile(m[1], []byte(`{"unit":"`+m[2]+`","findings":[]}`), 0o600)
	}
	r := runQualityReview(context.Background(), q)
	if !spawned.Load() {
		t.Error("accessibility was not dispatched on a non-ASCII .tsx path")
	}
	if got := r.UnitStatus("accessibility"); got != qualityreview.ResultPassed {
		t.Fatalf("accessibility status = %q, want passed; report = %+v", got, r)
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

// phase5Block returns the first bash block under "## Phase 5: Quality Review"
// in a shipped feature-dev review include.
func phase5Block(t *testing.T, include string) string {
	t.Helper()
	data, err := os.ReadFile(include)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), "## Phase 5: Quality Review")
	if !ok {
		t.Fatalf("%s has no Phase 5 section", include)
	}
	_, rest, ok = strings.Cut(rest, "```bash\n")
	block, _, closed := strings.Cut(rest, "\n```")
	if !ok || !closed {
		t.Fatalf("%s Phase 5 has no bash block", include)
	}
	return block
}

// TestFeatureDevPhase5RunsOneReviewPerPath runs the Phase 5 block feature-dev
// ships, with a fake nightgauge that counts its review-quality calls. The
// Go-dispatched stage has no units phase, so it reviews here exactly once. On
// the SDK workflow path the orchestrator marks the implementation agent, the
// units phase is the one review (pipelineSelectExecutor.test.ts), and this
// block must not run another (#2908).
func TestFeatureDevPhase5RunsOneReviewPerPath(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, include := range []string{
		"../../skills/nightgauge-feature-dev/_includes/review-and-correction.md",
		"../../claude-plugins/nightgauge/skills/feature-dev/_includes/review-and-correction.md",
	} {
		block := phase5Block(t, include)
		for _, tc := range []struct {
			path   string
			marker string
			want   int
		}{
			{"go-dispatched", "", 1},
			{"sdk-workflow", "1", 0},
		} {
			bin := t.TempDir()
			calls := filepath.Join(bin, "calls")
			fake := "#!/bin/sh\n" +
				`[ "$1 $2" = "review quality" ] && echo x >>"` + calls + `"` + "\n" +
				`echo '{"result":"passed","units":[]}'` + "\n"
			if err := os.WriteFile(filepath.Join(bin, "nightgauge"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, "-c", block)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"NIGHTGAUGE_BIN="+filepath.Join(bin, "nightgauge"),
				"NIGHTGAUGE_ISSUE_NUMBER=7",
				"NIGHTGAUGE_REVIEW_UNITS_PHASE="+tc.marker,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s %s: %v\n%s", include, tc.path, err, out)
			}
			data, err := os.ReadFile(calls)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if got := strings.Count(string(data), "x"); got != tc.want {
				t.Errorf("%s %s: review quality ran %d times, want %d", include, tc.path, got, tc.want)
			}
		}
	}
}

func TestQualityReviewReportStaysValidJSONWithBackslashClaims(t *testing.T) {
	q, _ := qualityFixture(t)
	q.Runner = fakeReviewer{agentic: true}
	q.Stderr = &bytes.Buffer{}
	q.Spawn = func(_ context.Context, _ adapters.SkillRunner, o adapters.RunOptions, _ string) error {
		m := findingsPath.FindStringSubmatch(o.Prompt)
		if m == nil {
			t.Errorf("prompt names no findings file: %s", o.Prompt)
			return nil
		}
		body := `{"unit":"` + m[2] + `","result":"passed","findings":[]}`
		switch m[2] {
		case "test":
			body = `{"unit":"test","result":"passed","findings":[{"severity":"warning","file":"a","line":1,"claim":"path \\303"}]}`
		case "security":
			body = `{"unit":"security","result":"passed","findings":[{"severity":"warning","file":"a","line":1,"claim":"path \303"}]}`
		}
		return os.WriteFile(m[1], []byte(body), 0o600)
	}
	r := runQualityReview(context.Background(), q)
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil || !json.Valid(out) {
		t.Fatalf("report is not valid JSON: %v\n%s", err, out)
	}
	var back qualityreview.Report
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, u := range back.Units {
		seen[u.ID] = true
		switch u.ID {
		case "test":
			if len(u.Findings) != 1 || u.Findings[0].Claim != `path \303` {
				t.Errorf("escaped claim = %+v", u)
			}
		case "security":
			if u.Status != qualityreview.StatusUnfinished || !strings.Contains(u.Reason, `"security"`) {
				t.Errorf("invalid unit = %+v", u)
			}
		}
	}
	if !seen["test"] || !seen["security"] {
		t.Fatalf("units missing from report: %v", seen)
	}
}
