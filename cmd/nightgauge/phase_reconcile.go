package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// phaseCmd groups phase-reporting verbs.
func phaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "phase",
		Short: "Phase reporting for pipeline stages",
	}
	cmd.AddCommand(phaseReconcileCmd())
	return cmd
}

// phaseReconcileCmd runs the agentic-stage evidence reconciler (#1887) out of
// process. The extension's skillRunner reaches the agentic stages without the
// Go scheduler, so it calls this and replays the transitions — one Go
// implementation, no TypeScript copy (#1397's dual-path drift).
//
// stdout is a JSON array in the `pr-stage --json` phases wire shape.
func phaseReconcileCmd() *cobra.Command {
	var (
		stage      string
		issue      int
		since      string
		exitCode   int
		gatePassed bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:          "reconcile",
		Short:        "Derive agentic-stage phase outcomes from the stage's artifacts",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if issue <= 0 {
				return fmt.Errorf("--issue must be a positive issue number")
			}
			sinceAt, err := time.Parse(time.RFC3339, since)
			if err != nil {
				return fmt.Errorf("--since must be RFC3339: %w", err)
			}
			workspace, err := resolvePrStageWorkspace(workdir)
			if err != nil {
				return err
			}
			ev, err := orchestrator.CollectAgenticEvidence(workspace, issue, state.PipelineStage(stage), sinceAt, exitCode, gatePassed)
			if err != nil {
				return err
			}
			transitions := orchestrator.ReconcileAgenticPhases(ev)
			if transitions == nil {
				transitions = []orchestrator.PhaseTransition{}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(transitions)
		},
	}
	cmd.Flags().StringVar(&stage, "stage", "", "agentic stage (feature-planning, feature-dev, feature-validate)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number")
	cmd.Flags().StringVar(&since, "since", "", "stage start time, RFC3339; older artifacts do not count")
	cmd.Flags().IntVar(&exitCode, "exit-code", 0, "the stage's exit code")
	cmd.Flags().BoolVar(&gatePassed, "gate-passed", false, "the stage's post-condition gate passed")
	cmd.Flags().StringVar(&workdir, "workdir", "", "workspace (default: current directory)")
	cmd.Flags().Bool("json", true, "emit JSON (always on)")
	_ = cmd.MarkFlagRequired("stage")
	_ = cmd.MarkFlagRequired("since")
	return cmd
}
