package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/orchestrator/recovery"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/spf13/cobra"
)

// Outcomes of `pr-stage recover`, the JSON contract the VS Code path reacts to.
const (
	prRecoverRecovered = "recovered"
	prRecoverRewind    = "rewind"
	prRecoverEscalated = "escalated"
	prRecoverUnmatched = "unmatched"
)

const conflictResolutionSignal = "CONFLICT_RESOLUTION_NEEDED"

// prRecoverResultJSON is the wire shape of `pr-stage recover`.
type prRecoverResultJSON struct {
	Outcome          string          `json:"outcome"`
	Action           string          `json:"action,omitempty"`
	Reason           string          `json:"reason"`
	ConflictingFiles []string        `json:"conflicting_files"`
	BacktrackSignal  json.RawMessage `json:"backtrack_signal,omitempty"`
	MaxDevRedispatch int             `json:"max_dev_redispatch"`
}

// prRecoverDefaultTimeoutSec covers one CI wait on the new head plus one merge.
func prRecoverDefaultTimeoutSec(workspace string) int {
	ciWait := time.Duration(recovery.DefaultRebaseCIPolls) * recovery.DefaultRebaseCIPollInterval
	return prMergeDefaultTimeoutSec(workspace) + int(ciWait.Seconds())
}

// runPrStageRecover runs the shared registry for a refused pr-merge. try is the
// registry call, a seam for tests.
func runPrStageRecover(
	ctx context.Context,
	workspace string,
	issue, pr int,
	reason string,
	try func(context.Context, recovery.StageFailure) (recovery.RecoveryResult, bool),
) (prRecoverResultJSON, error) {
	out := prRecoverResultJSON{
		ConflictingFiles: []string{},
		MaxDevRedispatch: recovery.GetConflictMaxDevRedispatch(workspace),
	}
	if !recovery.GetConflictRecoveryEnabled(workspace) {
		out.Outcome = prRecoverUnmatched
		out.Reason = "pipeline.recovery.conflict_recovery.enabled is false"
		return out, nil
	}

	pipelineDir, err := layout.PipelineStateDir(workspace)
	if err != nil {
		return out, fmt.Errorf("resolve pipeline state dir: %w", err)
	}
	// This refusal is a new conflict. A context left by an earlier one would
	// send feature-dev at stale blobs.
	contextPath := filepath.Join(pipelineDir, fmt.Sprintf("conflict-context-%d.json", issue))
	if err := os.Remove(contextPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, fmt.Errorf("clear stale conflict context: %w", err)
	}

	res, matched := try(ctx, recovery.StageFailure{
		Stage:       state.StagePRMerge,
		GateKind:    gates.KindNoOp,
		Reason:      reason,
		StageError:  reason,
		PRNumber:    pr,
		IssueNumber: issue,
		Workspace:   workspace,
	})
	if !matched {
		out.Outcome = prRecoverUnmatched
		out.Reason = "no recovery action matched"
		return out, nil
	}
	out.Action = res.Action
	out.Reason = res.Reason
	out.ConflictingFiles = conflictingFiles(res.Evidence)

	switch {
	case res.Recovered:
		out.Outcome = prRecoverRecovered
	case res.FollowUp == recovery.FollowUpStageCanResume:
		signal, err := readConflictSignal(filepath.Join(pipelineDir, fmt.Sprintf("feedback-%d.json", issue)))
		if err != nil {
			out.Outcome = prRecoverEscalated
			out.Reason = fmt.Sprintf("%s; no usable rewind signal: %v", res.Reason, err)
			break
		}
		out.Outcome = prRecoverRewind
		out.BacktrackSignal = signal
	default:
		out.Outcome = prRecoverEscalated
	}
	return out, nil
}

// conflictingFiles extracts the de-duplicated conflicting_file= evidence.
func conflictingFiles(evidence []string) []string {
	files := []string{}
	seen := map[string]bool{}
	for _, e := range evidence {
		if f, ok := strings.CutPrefix(e, "conflicting_file="); ok && !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	return files
}

// readConflictSignal returns the newest CONFLICT_RESOLUTION_NEEDED signal.
func readConflictSignal(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Signals []json.RawMessage `json:"signals"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	for i := len(file.Signals) - 1; i >= 0; i-- {
		var head struct {
			SignalType string `json:"signal_type"`
		}
		if json.Unmarshal(file.Signals[i], &head) == nil && head.SignalType == conflictResolutionSignal {
			return file.Signals[i], nil
		}
	}
	return nil, fmt.Errorf("%s has no %s signal", filepath.Base(path), conflictResolutionSignal)
}

// prStageRecoverCmd runs `nightgauge pr-stage recover`.
func prStageRecoverCmd() *cobra.Command {
	var (
		reason     string
		workdir    string
		outputJSON bool
		timeoutSec int
	)
	cmd := &cobra.Command{
		Use:   "recover <issue-number>",
		Short: "Run conflict recovery for a refused pr-merge — the TS-path recovery seam (#2841)",
		Long: `Run the recovery registry the Go scheduler uses against a merge-queue conflict
refusal, so the VS Code path shares its actions instead of porting them.
Outcome is recovered, rewind (with backtrack_signal), escalated or unmatched.
Exit 0 means a result was produced; the caller reads outcome, not the exit code.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			issue, err := parseIssueNumberArg(args[0])
			if err != nil {
				return err
			}
			workspace, err := resolvePrStageWorkspace(workdir)
			if err != nil {
				return err
			}
			cfg := authenticatePrStageGh(workspace)
			if !cmd.Flags().Changed("timeout") {
				timeoutSec = prRecoverDefaultTimeoutSec(workspace)
			}
			ctx, cancel := prStageContext(cmd, timeoutSec)
			defer cancel()

			registry := recovery.Default(workspace, newPrStageMergeRunner(cfg), nil)
			try := func(ctx context.Context, f recovery.StageFailure) (recovery.RecoveryResult, bool) {
				return registry.TryRecover(ctx, f, 0)
			}
			out, err := runPrStageRecover(ctx, workspace, issue, readPrNumber(workspace, issue), reason, try)
			if err != nil {
				return err
			}
			if !outputJSON {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", out.Outcome, out.Reason)
				return nil
			}
			data, err := json.Marshal(out)
			if err != nil {
				return fmt.Errorf("marshal pr-stage recover: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "The refusal reason pr-merge reported")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace/worktree root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON instead of human output")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 0, "Overall timeout in seconds (default: derived from the pr-merge budget)")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

// readPrNumber reads pr-{N}.json; 0 when it cannot be read.
func readPrNumber(workspace string, issue int) int {
	dir, err := layout.PipelineStateDir(workspace)
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("pr-%d.json", issue)))
	if err != nil {
		return 0
	}
	var pr struct {
		PRNumber int `json:"pr_number"`
	}
	if json.Unmarshal(data, &pr) != nil {
		return 0
	}
	return pr.PRNumber
}
