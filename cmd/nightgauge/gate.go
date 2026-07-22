package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/spf13/cobra"
)

// gateCmd is the top-level "gate" command for the stage-gate framework
// (Issue #3266). It is the seam the TypeScript HeadlessOrchestrator uses to
// delegate post-merge verification to a single Go-side implementation.
func gateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Stage post-condition verification gates",
		Long: `Run a stage post-condition gate (Issue #3266). Each pipeline stage has a
deterministic gate that verifies the stage actually achieved its claimed
post-state — catching the "skill reported success but did not do the work"
failure mode.`,
	}
	cmd.AddCommand(gateVerifyCmd())
	cmd.AddCommand(gateRecordMetricCmd())
	return cmd
}

// gateRecordMetricCmd runs `nightgauge gate record-metric` — the writer
// side of the quality-gate signal. The feature-validate adversarial-review
// phase (#4097) calls it to record the critic verdict (pass/catch) so a "catch"
// trips the deterministic FeatureValidateGate, keeping that gate pure (no LLM/
// network) while the non-deterministic judgment reaches it via gate-metrics.
//
// Exit codes: 0 on success; 1 on invalid input / IO error.
func gateRecordMetricCmd() *cobra.Command {
	var (
		issueNumber  int
		gateName     string
		result       string
		errorSummary string
		workdir      string
	)
	cmd := &cobra.Command{
		Use:   "record-metric",
		Short: "Append a quality-gate result (pass|catch) to gate-metrics.jsonl",
		Long: `Append one quality-gate record consumed by the deterministic
FeatureValidateGate. Used by the feature-validate adversarial-review phase to
record an LLM-critic verdict — a "catch" fails validation through the existing
gate without putting an LLM call inside the gate itself (see docs/STAGE_GATES.md).`,
		Example:      `  nightgauge gate record-metric --issue 4097 --gate adversarial-review --result catch --error-summary "correctness: nil-deref"`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			work := workdir
			if work == "" {
				if wd, err := os.Getwd(); err == nil {
					work = wd
				}
			}
			ts := time.Now().UTC().Format(time.RFC3339)
			if err := state.AppendGateMetric(work, issueNumber, gateName, result, errorSummary, ts); err != nil {
				fmt.Fprintf(os.Stderr, "gate record-metric: %v\n", err)
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&gateName, "gate", "", "Gate name, e.g. adversarial-review (required)")
	cmd.Flags().StringVar(&result, "result", "", "Result: pass | catch (required)")
	cmd.Flags().StringVar(&errorSummary, "error-summary", "", "Short reason when result=catch")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("gate")
	_ = cmd.MarkFlagRequired("result")
	return cmd
}

// gateVerifyCmd runs `nightgauge gate verify <stage> <issue-number>`.
//
// Exit codes:
//
//	0 — passed=true
//	2 — passed=false
//	1 — invalid arguments / IO error
func gateVerifyCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
		timeoutSec int
	)
	cmd := &cobra.Command{
		Use:   "verify <stage> <issue-number>",
		Short: "Run the post-condition gate for a stage",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageName := args[0]
			issueNumber, err := parseIssueNumberArg(args[1])
			if err != nil {
				return err
			}

			gate, ok := gates.LookupByStageName(stageName)
			if !ok {
				return fmt.Errorf("no gate registered for stage %q", stageName)
			}

			workspace := workdir
			if workspace == "" {
				wd, wdErr := os.Getwd()
				if wdErr != nil {
					return fmt.Errorf("resolve workspace: %w", wdErr)
				}
				workspace = wd
			}

			// Authenticate the gate's deterministic `gh` calls (gh pr view) as
			// the pipeline identity rather than the machine's ambient active gh
			// account. `gate verify` runs as a standalone process spawned by the
			// TS HeadlessOrchestrator — it inherits neither serve's exported
			// token nor any GH_TOKEN, so without this it uses whichever gh
			// account is active. On a multi-account machine that account may
			// lack target-org access, so `gh pr view` fails with "Could not
			// resolve to a Repository" and the gate false-negates a PR that was
			// in fact created (#3890). Resolve via the same config chain PR
			// creation uses (config → GITHUB_TOKEN → `gh auth token --user`).
			if cfg, cfgErr := config.Load(workspace); cfgErr == nil && cfg != nil {
				exportConfiguredGitHubToken(cfg, cfg.Owner)
			}

			ctx := cmd.Context()
			if timeoutSec > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
				defer cancel()
			}

			result := gate.Verify(ctx, issueNumber, workspace)

			if outputJSON {
				payload := gateVerifyJSON{
					Stage:      stageName,
					GateName:   result.GateName,
					Passed:     result.Passed,
					Reason:     result.Reason,
					Evidence:   result.Evidence,
					DurationMs: result.DurationMs,
					Timestamp:  result.Timestamp,
				}
				data, mErr := json.Marshal(payload)
				if mErr != nil {
					return fmt.Errorf("marshal gate result: %w", mErr)
				}
				fmt.Println(string(data))
			} else {
				renderGateHuman(stageName, result)
			}

			if !result.Passed {
				// Sentinel exit code 2 distinguishes gate-failure from CLI errors.
				os.Exit(2)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON instead of human output")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 60, "Gate timeout in seconds (0 = no timeout)")
	return cmd
}

// gateVerifyJSON is the exact shape parsed by the TypeScript shim. Keep
// stable — its consumers are external (HeadlessOrchestrator).
type gateVerifyJSON struct {
	Stage      string   `json:"stage"`
	GateName   string   `json:"gate_name"`
	Passed     bool     `json:"passed"`
	Reason     string   `json:"reason"`
	Evidence   []string `json:"evidence,omitempty"`
	DurationMs int64    `json:"duration_ms"`
	Timestamp  string   `json:"timestamp"`
}

func parseIssueNumberArg(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid issue number %q", s)
	}
	return n, nil
}

func renderGateHuman(stage string, r gates.GateResult) {
	verdict := "PASSED"
	if !r.Passed {
		verdict = "FAILED"
	}
	fmt.Printf("Stage gate: %s — %s\n", stage, verdict)
	if r.Reason != "" {
		fmt.Printf("Reason: %s\n", r.Reason)
	}
	for _, e := range r.Evidence {
		fmt.Printf("  - %s\n", e)
	}
	if r.DurationMs > 0 {
		fmt.Printf("(%dms)\n", r.DurationMs)
	}
}
