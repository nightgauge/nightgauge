// Package runstatecmd wires the internal/runstate package up as the
// `nightgauge run state {get,set,resume,discard}` Cobra subcommand. It
// follows the same template as projectCmd and epicCmd in cmd/nightgauge/main.go.
package runstatecmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/spf13/cobra"
)

const defaultBaseDir = ".nightgauge/pipeline"

// Cmd returns the `run state` parent subcommand. The caller registers this
// under `runCmd()` in cmd/nightgauge/main.go.
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Inspect or mutate the durable pipeline run-state.json (Issue #3238)",
		Long: `Manage .nightgauge/pipeline/run-state.json — the single source of truth
for the pipeline lifecycle (running / paused / completed / discarded / aborted).

Mirrors the TypeScript-side RunStateManager. Both runtimes write the same
file format using the atomic+fsync write contract.`,
	}
	cmd.AddCommand(getCmd(), setCmd(), resumeCmd(), discardCmd(), detectCmd())
	return cmd
}

func resolveBaseDir(cmd *cobra.Command) string {
	if v, _ := cmd.Flags().GetString("dir"); v != "" {
		return v
	}
	return defaultBaseDir
}

func getCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Print the current run-state.json as JSON (or empty when absent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := resolveBaseDir(cmd)
			rs, err := runstate.Load(baseDir)
			if err != nil {
				return err
			}
			if rs == nil {
				fmt.Println("{}")
				return nil
			}
			out, err := json.MarshalIndent(rs, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}
	cmd.Flags().String("dir", "", "override base directory (default .nightgauge/pipeline)")
	return cmd
}

func setCmd() *cobra.Command {
	var (
		issue       int
		branch      string
		worktree    string
		state       string
		reason      string
		recoverable bool
		force       bool
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Privileged: write a run-state record (used by tests and recovery flows)",
		Example: `  # Bootstrap a fresh running record
  nightgauge run state set --issue 42 --branch feat/x --state running

  # Mark paused with a reason (useful from automation)
  nightgauge run state set --state paused --reason "user stop"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := resolveBaseDir(cmd)
			switch runstate.Lifecycle(state) {
			case runstate.StateRunning:
				if issue == 0 || branch == "" {
					return fmt.Errorf("--issue and --branch required for state=running")
				}
				_, err := runstate.MarkRunning(baseDir, runstate.MarkRunningOptions{
					IssueNumber:  issue,
					Branch:       branch,
					WorktreePath: worktree,
					Force:        force,
				})
				return err
			case runstate.StatePaused:
				_, err := runstate.MarkPaused(baseDir, reason, nil)
				return err
			case runstate.StateAborted:
				_, err := runstate.MarkAborted(baseDir, reason, recoverable)
				return err
			case runstate.StateCompleted:
				_, err := runstate.MarkCompleted(baseDir)
				return err
			case runstate.StateDiscarded:
				_, err := runstate.MarkDiscarded(baseDir, reason)
				return err
			default:
				return fmt.Errorf("invalid --state %q (running|paused|aborted|completed|discarded)", state)
			}
		},
	}
	cmd.Flags().String("dir", "", "override base directory")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required for state=running)")
	cmd.Flags().StringVar(&branch, "branch", "", "feature branch (required for state=running)")
	cmd.Flags().StringVar(&worktree, "worktree", "", "absolute worktree path (optional)")
	cmd.Flags().StringVar(&state, "state", "", "target state (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "reason string (paused/aborted/discarded)")
	cmd.Flags().BoolVar(&recoverable, "recoverable", false, "recoverable flag (aborted only)")
	cmd.Flags().BoolVar(&force, "force-concurrent", false, "bypass concurrent-run detection")
	_ = cmd.MarkFlagRequired("state")
	return cmd
}

func resumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Transition paused → running and print the resume_from_stage",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := resolveBaseDir(cmd)
			rs, err := runstate.Resume(baseDir)
			if err != nil {
				return err
			}
			stage := ""
			if rs.ResumeFromStage != nil {
				stage = string(*rs.ResumeFromStage)
			}
			fmt.Println(stage)
			return nil
		},
	}
	cmd.Flags().String("dir", "", "override base directory")
	return cmd
}

func discardCmd() *cobra.Command {
	var (
		reason   string
		repoRoot string
		archive  bool
	)
	cmd := &cobra.Command{
		Use:   "discard",
		Short: "Transition to discarded, archive context files, and tear down branch/worktree",
		Long: `Discard is the only destructive transition. It archives every live
context file for the issue under .nightgauge/pipeline/history/<runId>/
and removes the recorded worktree plus the feature branch (locally and
remote-if-pushed). Protected branches (main, master) are never deleted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := resolveBaseDir(cmd)
			rs, err := runstate.MarkDiscarded(baseDir, reason)
			if err != nil {
				return err
			}
			if archive {
				if _, err := runstate.ArchiveRun(baseDir, rs); err != nil {
					return fmt.Errorf("archive run: %w", err)
				}
			}
			if repoRoot != "" {
				if err := runstate.CleanupBranchAndWorktree(repoRoot, rs); err != nil {
					return fmt.Errorf("cleanup: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().String("dir", "", "override base directory")
	cmd.Flags().StringVar(&reason, "reason", "user discard", "reason recorded in run-state")
	cmd.Flags().StringVar(&repoRoot, "repo", "", "repo root for branch/worktree teardown (omit to skip teardown)")
	cmd.Flags().BoolVar(&archive, "archive", true, "archive context files into history/<runId>")
	return cmd
}

func detectCmd() *cobra.Command {
	var (
		issue           int
		branch          string
		hasContextFlag  bool
		autoDetectFiles bool
	)
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Inspect run-state and report what the orchestrator should do",
		Long: `Reports a JSON object describing the orchestrator's start-path:
{ kind: "fresh"|"paused"|"aborted"|"running"|"orphaned", choices: [...], state: {...} }

Used by the autonomous orchestrator to skip-on-paused and by the user-driven
runner to surface a recovery quick-pick. The #3237 fixture (branch present,
no context, no run-state) returns kind=orphaned with choices=[restart, manual-pickup].`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := resolveBaseDir(cmd)
			has := hasContextFlag
			if autoDetectFiles && issue > 0 {
				has = runstate.HasContextFiles(baseDir, issue)
			}
			det, err := runstate.DetectResume(baseDir, branch, has)
			if err != nil {
				return err
			}
			out, err := json.MarshalIndent(det, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}
	cmd.Flags().String("dir", "", "override base directory")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required when --auto-detect-files is set)")
	cmd.Flags().StringVar(&branch, "branch", "", "current git branch (omit when not on a feature branch)")
	cmd.Flags().BoolVar(&hasContextFlag, "has-context", false, "set true when context files exist for the issue")
	cmd.Flags().BoolVar(&autoDetectFiles, "auto-detect-files", true, "auto-detect context files via --issue")
	return cmd
}

// AbsoluteDir resolves a possibly-relative dir against the cwd. Exposed so
// downstream callers can pre-resolve and avoid races where tests cd into a
// temp dir between flag-parse and exec.
func AbsoluteDir(dir string) string {
	if dir == "" {
		dir = defaultBaseDir
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Join(cwd, dir)
	}
	return dir
}

// ParseIssueArg lifts a positional arg → int. Shared with tests.
func ParseIssueArg(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid issue number: %q", s)
	}
	return n, nil
}
