package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/review"
	"github.com/nightgauge/nightgauge/internal/scaffold"
)

type fakeReviewer struct{ agentic bool }

func (f fakeReviewer) Name() string { return "fake" }
func (f fakeReviewer) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return "true", nil, nil
}
func (f fakeReviewer) UsesStdin() bool { return true }
func (f fakeReviewer) Agentic() bool   { return f.agentic }

func reviewRepo(t *testing.T) review.Session {
	t.Helper()
	root := t.TempDir()
	rules := filepath.Join(root, ".nightgauge", ".gitignore")
	if err := os.MkdirAll(filepath.Dir(rules), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rules, []byte(scaffold.GitignoreTemplate), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "init", "-q")
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base")
	gittest.Run(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"title":"T","base_branch":"main","requirements":{}}`
	if err := os.WriteFile(filepath.Join(dir, "issue-7.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, err := reviewSession(context.Background(), root, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	s.PipelineRun = "run"
	return s
}

func TestRunReviewSpawnsOnceAndReusesTheTree(t *testing.T) {
	s := reviewRepo(t)
	ctx := context.Background()
	p, err := prepareReview(ctx, s, true)
	if err != nil {
		t.Fatal(err)
	}
	spawns := 0
	var prompt string
	spawn := func(_ context.Context, _ adapters.SkillRunner, o adapters.RunOptions, _ string) error {
		spawns++
		prompt = o.Prompt
		if strings.Contains(strings.Join(o.AllowedTools, ","), "Bash") {
			t.Error("the reviewer must not get a shell")
		}
		if o.KnowledgeDir != "" {
			t.Errorf("the bundle is in the workspace and needs no grant: %s", o.KnowledgeDir)
		}
		return os.WriteFile(p.FindingsPath, []byte(`{"schema":1,"findings":[]}`), 0o600)
	}
	out, err := runReview(ctx, s, fakeReviewer{agentic: true}, p, spawn)
	if err != nil || out.Verdict != "pass" {
		t.Fatalf("%+v, %v", out, err)
	}
	if strings.Contains(prompt, "PLAN") || strings.Contains(prompt, "decisions.md") {
		t.Fatalf("the prompt carries authoring context: %s", prompt)
	}

	again, err := prepareReview(ctx, s, true)
	if err != nil || !again.Reusable || again.Result.Verdict != "pass" || again.Bundle != nil {
		t.Fatalf("second prepare: %+v, %v", again, err)
	}
	if spawns != 1 {
		t.Fatalf("spawns = %d", spawns)
	}
}

func TestRunReviewFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		runner adapters.SkillRunner
		spawn  reviewSpawn
	}{
		"spawn failed": {fakeReviewer{agentic: true}, func(context.Context, adapters.SkillRunner, adapters.RunOptions, string) error {
			return errors.New("blocked")
		}},
		"no findings": {fakeReviewer{agentic: true}, func(context.Context, adapters.SkillRunner, adapters.RunOptions, string) error {
			return nil
		}},
		"not agentic": {fakeReviewer{}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			s := reviewRepo(t)
			ctx := context.Background()
			p, err := prepareReview(ctx, s, true)
			if err != nil {
				t.Fatal(err)
			}
			out, err := runReview(ctx, s, tc.runner, p, tc.spawn)
			if err != nil || out.Verdict != "catch" {
				t.Fatalf("%+v, %v", out, err)
			}
			s.Now = time.Now()
			again, err := prepareReview(ctx, s, true)
			if err != nil || again.Reusable {
				t.Fatalf("an unfinished review must run again: %+v, %v", again, err)
			}
		})
	}
}

func TestDisabledReviewDoesNothing(t *testing.T) {
	s := reviewRepo(t)
	p, err := prepareReview(context.Background(), s, false)
	if err != nil || !p.Disabled || p.Bundle != nil {
		t.Fatalf("%+v, %v", p, err)
	}
}

// TestRunReviewPassesTheReviewerModel: the spawn carries review.model, and a
// config that sets max_rounds reaches the session (#3683).
func TestRunReviewPassesTheReviewerModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NIGHTGAUGE_STAGE", "")
	t.Setenv("NIGHTGAUGE_DISPATCH_MODEL", "")
	t.Setenv("NIGHTGAUGE_UI_CORE_DEFAULT_MODEL", "")
	s := reviewRepo(t)
	cfg := "owner: o\nproject:\n  number: 1\nreview:\n  max_rounds: 4\n  model: opus\n"
	if err := os.WriteFile(filepath.Join(s.Root, ".nightgauge", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, err := reviewSession(context.Background(), s.Root, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "opus" || s.MaxRounds != 4 {
		t.Fatalf("session = model %q, max rounds %d", s.Model, s.MaxRounds)
	}
	ctx := context.Background()
	p, err := prepareReview(ctx, s, true)
	if err != nil {
		t.Fatal(err)
	}
	var model string
	spawn := func(_ context.Context, _ adapters.SkillRunner, o adapters.RunOptions, _ string) error {
		model = o.Model
		return os.WriteFile(p.FindingsPath, []byte(`{"schema":1,"findings":[]}`), 0o600)
	}
	if _, err := runReview(ctx, s, fakeReviewer{agentic: true}, p, spawn); err != nil {
		t.Fatal(err)
	}
	if model != "opus" {
		t.Fatalf("spawned model = %q, want opus", model)
	}
}

// TestReviewSessionDefaultsAndRejectsAnInvalidCap: no review block leaves the
// default cap and the adapter's model; an out-of-range cap is an error, never
// the default (#3683).
func TestReviewSessionDefaultsAndRejectsAnInvalidCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NIGHTGAUGE_STAGE", "")
	t.Setenv("NIGHTGAUGE_DISPATCH_MODEL", "")
	t.Setenv("NIGHTGAUGE_UI_CORE_DEFAULT_MODEL", "")
	s := reviewRepo(t)
	if s.MaxRounds != config.DefaultReviewMaxRounds || s.Model != "" {
		t.Fatalf("defaults: max rounds %d, model %q", s.MaxRounds, s.Model)
	}
	cfg := "owner: o\nproject:\n  number: 1\nreview:\n  max_rounds: 9\n"
	if err := os.WriteFile(filepath.Join(s.Root, ".nightgauge", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reviewSession(context.Background(), s.Root, 7, ""); err == nil ||
		!strings.Contains(err.Error(), "review.max_rounds") {
		t.Fatalf("an out-of-range cap must fail: %v", err)
	}
}
