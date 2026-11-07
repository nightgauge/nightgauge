package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/review"
	"github.com/spf13/cobra"
)

// reviewTimeout bounds one headless review.
const reviewTimeout = 30 * time.Minute

// reviewTools is what a headless reviewer may use: no shell, no forge.
var reviewTools = []string{"Read", "Glob", "Grep", "Write"}

// reviewCmd implements `nightgauge review`, the deterministic half of
// feature-validate's independent code review (#2839).
func reviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run one independent code review per tree (#2839)",
		Long: `prepare consults the gate ledger and builds the bundle a fresh context reads,
run spawns that context as one headless process, and record validates its
findings and writes the verdict. See docs/PR_REVIEW_PASSES.md.`,
	}
	cmd.AddCommand(reviewPrepareCmd(), reviewRunCmd(), reviewRecordCmd())
	return cmd
}

func reviewSession(ctx context.Context, workdir string, issue int, tree string) (review.Session, error) {
	root, err := gateWorkdir(workdir)
	if err != nil {
		return review.Session{}, err
	}
	s := review.Session{
		Root: root, Issue: issue, Tree: tree, Now: time.Now(),
		PipelineRun: os.Getenv(adapters.RunIDEnvVar),
	}
	if s.Tree == "" {
		if s.Tree, err = hooks.TreeFingerprint(ctx, &hooks.ExecCmdRunner{}, root); err != nil {
			return s, fmt.Errorf("the tree could not be fingerprinted: %w", err)
		}
	}
	return s, nil
}

func prepareReview(ctx context.Context, s review.Session, enabled bool) (review.Prepared, error) {
	if !enabled {
		return review.Prepared{Disabled: true, Reason: "pipeline.adversarial_review.enabled is false"}, nil
	}
	p, err := review.Consult(s)
	if err != nil || p.Reusable || p.CapReached {
		return p, err
	}
	p.StartedAt = s.Now.UTC().Format(time.RFC3339Nano)
	p.Bundle, err = review.BuildBundle(ctx, s)
	return p, err
}

func reviewPrepareCmd() *cobra.Command {
	var (
		issue   int
		workdir string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:          "prepare",
		Short:        "Reuse this tree's verdict, or build the bundle for a new review",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := reviewSession(cmd.Context(), workdir, issue, "")
			if err != nil {
				return err
			}
			enabled := true
			if cfg, err := config.Load(s.Root); err == nil && cfg != nil {
				enabled = cfg.Pipeline.ResolveAdversarialReviewEnabled()
			}
			p, err := prepareReview(cmd.Context(), s, enabled)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(p)
			}
			fmt.Println(p.Reason)
			return nil
		},
	}
	cmd.Flags().IntVar(&issue, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output result as JSON")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

// reviewSpawn runs one reviewer process, its output in logPath.
type reviewSpawn func(ctx context.Context, runner adapters.SkillRunner, opts adapters.RunOptions, logPath string) error

func spawnReview(ctx context.Context, runner adapters.SkillRunner, opts adapters.RunOptions, logPath string) error {
	name, args, env := runner.BuildCommand(opts)
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = opts.WorktreeDir
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if runner.UsesStdin() {
		cmd.Stdin = strings.NewReader(opts.Prompt)
	}
	return cmd.Run()
}

// runReview spawns the reviewer and records its result. Every way the review
// can fail to finish ends as a recorded catch, not as an error.
func runReview(ctx context.Context, s review.Session, runner adapters.SkillRunner, p review.Prepared, spawn reviewSpawn) (review.Outcome, error) {
	started, _ := time.Parse(time.RFC3339Nano, p.StartedAt)
	prompt, err := os.ReadFile(p.PromptPath)
	if err != nil {
		return review.Unfinished(s, started, "the bundle has no prompt")
	}
	if !runner.Agentic() {
		return review.Unfinished(s, started, "adapter "+runner.Name()+" has no tool loop")
	}
	err = spawn(ctx, runner, adapters.RunOptions{
		WorktreeDir:  s.Root,
		IssueNumber:  s.Issue,
		Stage:        review.Stage,
		AllowedTools: reviewTools,
		Prompt:       string(prompt),
		MaxTurns:     review.MaxTurns,
	}, p.LogPath)
	s.Now = time.Now()
	if err != nil {
		return review.Unfinished(s, started, "spawn failed, see "+p.LogPath+": "+err.Error())
	}
	return review.Record(s, recordTree(ctx, s), p.FindingsPath, started)
}

// recordTree is the tree now. An empty answer can never equal the reviewed tree.
func recordTree(ctx context.Context, s review.Session) string {
	tree, err := hooks.TreeFingerprint(ctx, &hooks.ExecCmdRunner{}, s.Root)
	if err != nil {
		return ""
	}
	return tree
}

func reviewRunCmd() *cobra.Command {
	var (
		issue   int
		workdir string
	)
	cmd := &cobra.Command{
		Use:          "run",
		Short:        "Review this tree in one headless process, through the stage's adapter",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), reviewTimeout)
			defer cancel()
			s, err := reviewSession(ctx, workdir, issue, "")
			if err != nil {
				return err
			}
			cfg, err := config.Load(s.Root)
			if err != nil {
				return err
			}
			p, err := prepareReview(ctx, s, cfg.Pipeline.ResolveAdversarialReviewEnabled())
			if err != nil {
				return err
			}
			if p.Disabled || p.Reusable || p.CapReached {
				return printJSON(p)
			}
			adapter := config.ResolveStageAdapter(cfg, review.Stage, os.Getenv).Adapter
			runner, err := adapters.NewRegistry().Resolve("", adapter)
			if err != nil {
				out, recErr := review.Unfinished(s, time.Time{}, err.Error())
				if recErr != nil {
					return recErr
				}
				return printJSON(out)
			}
			out, err := runReview(ctx, s, runner, p, spawnReview)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "review log:", p.LogPath)
			return printJSON(out)
		},
	}
	cmd.Flags().IntVar(&issue, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func reviewRecordCmd() *cobra.Command {
	var (
		findings, tree, startedAt, workdir string
		issueNumber                        int
	)
	cmd := &cobra.Command{
		Use:          "record",
		Short:        "Validate a findings file and write the verdict for the reviewed tree",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := reviewSession(cmd.Context(), workdir, issueNumber, tree)
			if err != nil {
				return err
			}
			started, _ := time.Parse(time.RFC3339Nano, startedAt)
			out, err := review.Record(s, recordTree(cmd.Context(), s), findings, started)
			if err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&findings, "findings", "", "Findings file from prepare (required)")
	cmd.Flags().StringVar(&tree, "tree", "", "Tree the review read, from prepare (required)")
	cmd.Flags().StringVar(&startedAt, "started-at", "", "started_at from prepare")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	for _, name := range []string{"issue", "findings", "tree"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
