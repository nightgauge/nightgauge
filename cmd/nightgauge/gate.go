package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/ipc"
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
		recordRoot string
		daemonRoot string
		outputJSON bool
		timeoutSec int
		record     bool
		runID      string
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

			// Issue #210 / #377: persist the gate result onto the run record for
			// orchestration paths that call this CLI seam rather than the
			// in-process Go scheduler loop (HeadlessOrchestrator.ts).
			//
			// TWO ROUTES, AND THE POINT OF #377 IS THAT ONLY ONE OF THEM WRITES
			// THE FILE. This process is the one snapshot writer that lives
			// outside the IPC server, so the server's in-memory terminal latch
			// cannot cover it; ADR-017 Decision 5 narrowed that to a rename race
			// and named the residual R-1. When a `nightgauge serve` daemon is
			// reachable the result now goes through it, making the server the
			// SINGLE AUTHORITATIVE WRITER whenever it is alive (#316's
			// discipline). The direct write is reserved for the no-server path,
			// where there is exactly one writer by definition.
			//
			// Best-effort throughout: a persistence failure is logged and NEVER
			// changes the gate's pass/fail exit-code contract. The verdict is
			// the command's product; the record is bookkeeping.
			if record {
				recordGateResult(ctx, workspace, recordRoot, daemonRoot, issueNumber, stageName, runID, result.ToStageGateResult())
			}

			if outputJSON {
				payload := gateVerifyJSON{
					Stage:        stageName,
					GateName:     result.GateName,
					Passed:       result.Passed,
					Reason:       result.Reason,
					Evidence:     result.Evidence,
					DurationMs:   result.DurationMs,
					Timestamp:    result.Timestamp,
					Kind:         string(result.Kind),
					TerminalKind: result.TerminalKind,
					Files:        result.Files,
					FileCount:    result.FileCount,
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
	cmd.Flags().StringVar(&daemonRoot, "daemon-root", "",
		"Workspace root the `nightgauge serve` daemon was started with — where its socket lives (#1054). "+
			"In a multi-repo workspace this is NOT the run's repo root: the daemon serves one workspace while "+
			"runs execute in sibling repos. Defaults to --record-root, then --workdir.")
	cmd.Flags().StringVar(&recordRoot, "record-root", "",
		"Repo root that owns the run record — where --record files the result and where the daemon socket lives. "+
			"Defaults to --workdir. Set this when --workdir is a worktree (#1054): the gate reads its inputs from the "+
			"worktree, but the run snapshot and the daemon live at the repo root.")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON instead of human output")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 60, "Gate timeout in seconds (0 = no timeout)")
	cmd.Flags().BoolVar(&record, "record", false,
		"Persist the gate result onto the run record's stageGateResults map (Issue #210)")
	cmd.Flags().StringVar(&runID, "run-id", "",
		"Run identity to record against (default: $NIGHTGAUGE_RUN_ID, exported into every stage environment)")
	return cmd
}

// gateVerifyJSON is the exact shape parsed by the TypeScript shim. Keep
// stable — its consumers are external (HeadlessOrchestrator).
type gateVerifyJSON struct {
	Stage        string   `json:"stage"`
	GateName     string   `json:"gate_name"`
	Passed       bool     `json:"passed"`
	Reason       string   `json:"reason"`
	Evidence     []string `json:"evidence,omitempty"`
	DurationMs   int64    `json:"duration_ms"`
	Timestamp    string   `json:"timestamp"`
	Kind         string   `json:"kind,omitempty"`
	TerminalKind string   `json:"terminal_kind,omitempty"`
	// Files and FileCount are populated only on the dev_handoff_missing path
	// (#134) — the deliverable paths git found in the stage workspace, so
	// feature-validate can proceed against them when dev-{N}.json is missing.
	Files     []string `json:"files,omitempty"`
	FileCount int      `json:"file_count,omitempty"`
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

// recordGateResult persists one gate verdict onto the run's snapshot, through
// the IPC server when one is reachable and directly to disk when it is not
// (#377, ADR-017 R-1).
//
// WHY THE PREFERENCE ORDER IS THE WHOLE FIX. The runtime snapshot has five
// writers in three processes sharing Persist's whole-file last-write-wins
// contract, and this CLI is the one that lives outside the IPC server — so the
// server's in-memory terminal latch cannot cover it. ADR-017 Decision 5 closed
// the cross-process half as far as a foreign process can: load-or-skip, refuse
// a terminal snapshot, and write through PersistExisting so a read-modify-write
// cannot re-create a file that was removed between the load and the write. What
// it could not close is the rename race in that window — the residual it names
// R-1 — because a second process cannot participate in a latch it cannot see.
//
// Posting through the server does not narrow that window; it removes this
// process from the set of writers entirely whenever a server is alive, which is
// the only way the residual actually goes away.
//
// THE FALLBACK IS NOT A DEGRADED MODE. With no daemon there is no second
// writer, so the direct path is not racing anything; it keeps every one of
// Decision 5's three rules. What it lacks is exact addressing — it resolves by
// issue number and picks the newest non-terminal snapshot, which mis-attributes
// only under two truly concurrent dispatches of one issue, and only when no
// server is running to route around it.
//
// A run id is REQUIRED for the IPC route and the server refuses a call without
// one (run_id_required). NIGHTGAUGE_RUN_ID is exported into every stage
// environment by every adapter, and the gate CLI is spawned as a stage
// subprocess, so the id is normally present without anyone passing a flag. When
// it is genuinely absent — a hand-run gate, or a dispatch that predates the
// export — there is nothing to address the server with, and the direct path is
// the honest answer rather than an invented identity.
// recordRoot addresses the RUN, workspace addresses the GATE'S INPUTS, and on a
// worktree run they are different directories (#1054).
//
// The extension deliberately passes `--workdir <worktree>`, because that is
// where the gate reads `issue-N.json` / `dev-N.json` from; repointing it at the
// repo root would make every gate false-negate. But this function used
// `workspace` for BOTH the daemon socket path and the direct-write state dir.
// The daemon listens only at serve's own workspace root, so the dial at
// `<worktree>/.nightgauge/daemon.sock` always failed, and the fallback then
// wrote into `<worktree>/.nightgauge/pipeline`, which holds stage context files
// but never a `runtime-{issue}-{runID}.json` — so the append took its
// load-or-skip branch and returned without writing. The record was created
// nowhere, on every worktree run.
//
// That is why #1021's fix appeared to change nothing: it corrected the run id in
// the payload, not the address the payload was sent to.
//
// #1054 ROUND TWO — the first fix was insufficient and this comment records why,
// because the mistake is easy to repeat.
//
// Round one pointed BOTH the socket and the state dir at recordRoot (the run's
// repo root). Verified in production afterwards: the dial STILL failed and
// [gate-not-invoked] still fired. The daemon does not listen at the run's repo
// root — it listens at the workspace root `nightgauge serve` was started with,
// and in a multi-repo workspace those are different directories. Measured on a
// live run: the socket existed at <workspace>/.nightgauge/daemon.sock while the
// run executed in a sibling repo, which had no socket at all.
//
// The fallback file write is not a substitute, and that is the load-bearing
// part. The durable run record is built by the daemon from its IN-MEMORY
// RuntimeState — `claimTerminal` snapshots `entry.rs` from activeRuntimes and
// never reads the file (internal/ipc/run_registry.go). A gate result written to
// disk by this separate process is invisible to the record builder, so the
// write "succeeds" and the result still never appears. Reaching the daemon is
// the ONLY path that lands a gate result on the run.
//
// The tell that separates this from a plain plumbing bug: phase records reach
// the durable record while gate results do not, and phases travel by IPC while
// gate results travelled by file.
//
// An empty recordRoot falls back to workspace, and an empty daemonRoot falls
// back to recordRoot, preserving the single-repo behaviour the existing tests
// pin — in a single-repo workspace all three are the same directory, which is
// exactly why this survived every test.
func recordGateResult(
	ctx context.Context,
	workspace string,
	recordRoot string,
	daemonRoot string,
	issueNumber int,
	stageName string,
	runID string,
	result state.StageGateResult,
) {
	if recordRoot == "" {
		recordRoot = workspace
	}
	if daemonRoot == "" {
		daemonRoot = recordRoot
	}
	if runID == "" {
		runID = os.Getenv(adapters.RunIDEnvVar)
	}

	if runID != "" {
		if client, dialErr := ipc.DialClient(ctx, ipc.DaemonSocketPath(daemonRoot), daemonDialTimeout); dialErr == nil {
			defer client.Close()
			params := ipc.PipelineRecordStageGateResultParams{
				IssueNumber: issueNumber,
				Stage:       stageName,
				RunID:       runID,
				Result:      result,
			}
			if callErr := client.Call(ctx, "pipeline.recordStageGateResult", params, nil); callErr != nil {
				// The server REFUSED it — a closed run, a wrong owner, a
				// non-canonical id. Do NOT fall back to the direct write: a
				// refusal is the single authoritative writer saying this record
				// does not belong on that run, and writing it to the file
				// anyway would reintroduce exactly the second writer this
				// routing exists to remove. The verdict is already the caller's.
				fmt.Fprintf(os.Stderr,
					"gate verify --record: the daemon refused the gate record for #%d run %s (verdict still returned, record NOT written): %v\n",
					issueNumber, runID, callErr)
			}
			return
		}
	}

	// No daemon reachable (or no run identity to address one with): write
	// directly, under Decision 5's three rules.
	stateDir := filepath.Join(recordRoot, ".nightgauge", "pipeline")
	if recErr := state.AppendStageGateResultToDisk(
		stateDir, issueNumber, state.PipelineStage(stageName), result,
	); recErr != nil {
		fmt.Fprintf(os.Stderr, "gate verify --record: failed to persist gate result: %v\n", recErr)
	}
}
