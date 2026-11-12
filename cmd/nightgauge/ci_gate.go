package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/contractrollout"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gateledger"
	"github.com/nightgauge/nightgauge/internal/gaterun"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/spf13/cobra"
)

// gateStatus is the JSON shape of `nightgauge ci gate-status`.
type gateStatus struct {
	Reusable bool   `json:"reusable"`
	Reason   string `json:"reason"`
	Tree     string `json:"tree"`
	// GateCommand renders the repository's complete local gate for display,
	// empty when it has none. Never execute it: `ci gate-run` runs the gate.
	GateCommand string `json:"gate_command"`
	// GateError says why a declared gate cannot be used. `ci gate-run` fails with
	// it, so a broken declaration never falls back to a lesser set of commands.
	GateError string `json:"gate_error,omitempty"`
	// GateRecordsItself is true when GateCommand writes its own ledger events.
	GateRecordsItself bool   `json:"gate_records_itself"`
	PassedStage       string `json:"passed_stage,omitempty"`
	PassedAt          string `json:"passed_at,omitempty"`
	PassedSeconds     int    `json:"passed_seconds,omitempty"`
}

// resolveGateStatus answers `ci gate-status`. It never returns an error for
// something the caller can treat as "run the gate": that is the fail-closed
// answer, with the reason in the result.
func resolveGateStatus(ctx context.Context, root, kind string) gateStatus {
	var st gateStatus
	gate, err := contractrollout.ResolveGate(root)
	if err != nil {
		st.GateError = err.Error()
	} else if !gate.Missing() {
		st.GateCommand = gate.String()
		st.GateRecordsItself = !gate.Declared
	}
	tree, err := hooks.TreeFingerprint(ctx, &hooks.ExecCmdRunner{}, root)
	if err != nil {
		st.Reason = fmt.Sprintf("the tree could not be fingerprinted: %v", err)
		return st
	}
	st.Tree = tree
	answer, err := gateledger.Latest(root, tree, kind)
	st.Reason = answer.Reason
	if err != nil {
		st.Reason = fmt.Sprintf("%s: %v", answer.Reason, err)
		return st
	}
	if answer.Reusable {
		st.Reusable = true
		st.PassedStage = answer.Pass.Stage
		st.PassedAt = answer.Pass.TS
		st.PassedSeconds = answer.Pass.Seconds
	}
	return st
}

func validGateKind(kind string) error {
	if kind != gateledger.KindLocalGate && kind != gateledger.KindParity {
		return fmt.Errorf("--kind must be %s or %s", gateledger.KindLocalGate, gateledger.KindParity)
	}
	return nil
}

func gateWorkdir(workdir string) (string, error) {
	if workdir != "" {
		return workdir, nil
	}
	return os.Getwd()
}

// ciGateStatusCmd implements `nightgauge ci gate-status`.
func ciGateStatusCmd() *cobra.Command {
	var (
		kind       string
		outputJSON bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:          "gate-status",
		Short:        "Report whether this exact tree already passed a full gate (#2784)",
		SilenceUsage: true,
		Example: `  nightgauge ci gate-status --kind local-gate --json
  nightgauge ci gate-status --kind parity --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validGateKind(kind); err != nil {
				return err
			}
			root, err := gateWorkdir(workdir)
			if err != nil {
				return err
			}
			st := resolveGateStatus(cmd.Context(), root, kind)
			if outputJSON {
				return printJSON(st)
			}
			fmt.Printf("reusable=%t tree=%s: %s\n", st.Reusable, st.Tree, st.Reason)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", gateledger.KindLocalGate, "Gate kind: local-gate or parity")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// runRepositoryGate runs the gate of the repository at root under the
// worktree's guard (#3207), with each step an argv, and returns its exit code.
// A declaration that fails validation is an error, as is having no gate.
func runRepositoryGate(ctx context.Context, root string, stdout, stderr io.Writer) (int, error) {
	gate, err := contractrollout.ResolveGate(root)
	if err != nil {
		return 1, err
	}
	if gate.Missing() {
		return 1, fmt.Errorf("%s has no local gate", root)
	}
	tree, err := hooks.TreeFingerprint(ctx, &hooks.ExecCmdRunner{}, root)
	if err != nil {
		return 1, fmt.Errorf("the tree could not be fingerprinted: %w", err)
	}
	return gaterun.Run(ctx, gaterun.Options{
		Root: root, Tree: tree,
		// The longest a gate may run: a guard older than this is stale, and a
		// same-tree wait gives up after it.
		Timeout: time.Duration(gateTimeoutSeconds(root, stderr)) * time.Second,
		// A declared gate is recorded by gate-run as parity, the kind the
		// skill queries; the default gate records itself as local-gate.
		Kind:          gateKindFor(gate),
		RecordsItself: !gate.Declared,
		Run: func(ctx context.Context, runStep func(*exec.Cmd) error) error {
			return gate.RunWith(ctx, root, stdout, stderr, runStep)
		},
		Record: func(ev gateledger.Event) error {
			if err := completeGateEvent(ctx, root, &ev); err != nil {
				return err
			}
			return gateledger.Append(root, ev)
		},
		Stderr: stderr,
	})
}

// gateTimeoutSeconds is hooks.pre_push.timeout_seconds, or its default.
func gateTimeoutSeconds(root string, stderr io.Writer) int {
	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintf(stderr, "gate-run: config load failed, using the default gate timeout: %v\n", err)
		cfg = nil
	}
	return config.ResolvePrePushTimeoutSeconds(cfg)
}

func gateKindFor(g contractrollout.Gate) string {
	if g.Declared {
		return gateledger.KindParity
	}
	return gateledger.KindLocalGate
}

// ciGateRunCmd implements `nightgauge ci gate-run`.
func ciGateRunCmd() *cobra.Command {
	var workdir string
	cmd := &cobra.Command{
		Use:   "gate-run",
		Short: "Run the repository's local gate, at most one per worktree (#2784, #3207)",
		Long: `Run the repository's local gate, each step as an argv, at most one per worktree.

A second gate-run on the same tree waits for the running gate and exits with its
result. A gate-run on a different tree supersedes the running gate: its process
group is stopped and verified dead, and the ledger records it as superseded.

The wait is bounded by hooks.pre_push.timeout_seconds. A guard is stale, and
reclaimed, when its holder's pid is dead, its start time differs (a reused pid
or a reboot), or the guard is older than that timeout. SIGINT, SIGTERM and
SIGHUP are forwarded to the running step's process group, which is killed
after a grace period and verified dead before the guard is released.

Exit codes: the gate's own; 75 when another gate holds this worktree and could
not be stopped, or a same-tree wait timed out (nothing ran); 76 when this gate
was superseded by a newer tree (not a failure); 128+signo when interrupted.`,
		SilenceUsage: true,
		Example:      `  nightgauge ci gate-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := gateWorkdir(workdir)
			if err != nil {
				return err
			}
			code, err := runRepositoryGate(cmd.Context(), root, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if code != 0 {
				return verdictExit{code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// gateCount is the JSON shape of `nightgauge ci gate-count`.
type gateCount struct {
	Issue       int    `json:"issue"`
	PipelineRun string `json:"pipeline_run"`
	gateledger.RunTally
}

// ciGateCountCmd implements `nightgauge ci gate-count` (#3207).
func ciGateCountCmd() *cobra.Command {
	var (
		issue       int
		pipelineRun string
		outputJSON  bool
		workdir     string
	)
	cmd := &cobra.Command{
		Use:          "gate-count",
		Short:        "Count the full gates one issue's pipeline run ran (#3207)",
		SilenceUsage: true,
		Example: `  nightgauge ci gate-count --json
  nightgauge ci gate-count --issue 3207 --run "$NIGHTGAUGE_RUN_ID" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := gateWorkdir(workdir)
			if err != nil {
				return err
			}
			c, err := countGates(cmd.Context(), root, issue, pipelineRun, cmd.Flags().Changed("run"))
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(c)
			}
			fmt.Printf("issue %d run %s: %d full gate(s) (%d pass, %d fail, %d infra, %d superseded, %d unfinished), %d reused\n",
				c.Issue, c.PipelineRun, c.Full, c.Passed, c.Failed, c.Infra, c.Superseded, c.Unfinished, c.Reused)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&issue, "issue", 0, "Issue number (default: NIGHTGAUGE_ISSUE_NUMBER, else the branch's)")
	f.StringVar(&pipelineRun, "run", "", "Pipeline run id (default: NIGHTGAUGE_RUN_ID; empty counts every run)")
	f.BoolVar(&outputJSON, "json", false, "Output result as JSON")
	f.StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// countGates resolves the issue and pipeline run, then counts the ledger.
func countGates(ctx context.Context, root string, issue int, pipelineRun string, runSet bool) (gateCount, error) {
	if issue == 0 {
		issue, _ = strconv.Atoi(os.Getenv("NIGHTGAUGE_ISSUE_NUMBER"))
	}
	if issue == 0 {
		if out, err := (&hooks.ExecCmdRunner{}).Run(ctx, root, "git", "branch", "--show-current"); err == nil {
			issue = issueFromBranch(strings.TrimSpace(string(out)))
		}
	}
	if issue == 0 {
		return gateCount{}, fmt.Errorf("--issue is required outside an issue branch or stage")
	}
	if !runSet {
		pipelineRun = os.Getenv(adapters.RunIDEnvVar)
	}
	tally, err := gateledger.RunCount(root, issue, pipelineRun)
	if err != nil {
		return gateCount{}, err
	}
	return gateCount{Issue: issue, PipelineRun: pipelineRun, RunTally: tally}, nil
}

// ciGateRecordCmd implements `nightgauge ci gate-record`. Scripts and skills
// record through it so nothing writes under the git directory by path, and so
// a pass is never recorded for a tree that moved during the run.
func ciGateRecordCmd() *cobra.Command {
	var (
		ev      gateledger.Event
		workdir string
	)
	cmd := &cobra.Command{
		Use:          "gate-record",
		Short:        "Append a start, finish or reused event to the gate ledger (#2784)",
		SilenceUsage: true,
		Example: `  RUN=$(nightgauge ci gate-record --event start --kind parity --tree "$TREE" --full)
  nightgauge ci gate-record --event finish --run-id "$RUN" --kind parity --tree "$TREE" \
    --verdict pass --full --seconds 120`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validGateKind(ev.Kind); err != nil {
				return err
			}
			root, err := gateWorkdir(workdir)
			if err != nil {
				return err
			}
			if err := completeGateEvent(cmd.Context(), root, &ev); err != nil {
				return err
			}
			if err := gateledger.Append(root, ev); err != nil {
				return err
			}
			fmt.Println(ev.RunID)
			if ev.TreeMoved {
				fmt.Fprintln(os.Stderr, "gate-record: the tree moved during the run, so this pass is not reusable")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&ev.Event, "event", "", "start, finish or reused")
	f.StringVar(&ev.RunID, "run-id", "", "Run id printed by --event start (default: a new id)")
	f.StringVar(&ev.Kind, "kind", gateledger.KindLocalGate, "Gate kind: local-gate or parity")
	f.StringVar(&ev.Tree, "tree", "", "Fingerprint of the tree the gate started on")
	f.StringVar(&ev.Verdict, "verdict", "", "pass, fail, infra or superseded (finish only)")
	f.BoolVar(&ev.Full, "full", false, "Every step ran: no change scoping and no skipped step")
	f.IntVar(&ev.Seconds, "seconds", 0, "Wall-clock seconds of the run (finish only)")
	f.StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// completeGateEvent validates ev and fills what the environment and the tree
// supply. Issue and stage come from the stage's environment, the branch from git.
func completeGateEvent(ctx context.Context, root string, ev *gateledger.Event) error {
	// A gate started by `ci gate-run` shares its run id (#3207).
	if ev.RunID == "" && ev.Event == gateledger.EventStart {
		ev.RunID = os.Getenv(gaterun.EnvRunID)
	}
	switch ev.Event {
	case gateledger.EventStart, gateledger.EventReused:
	case gateledger.EventFinish:
		switch ev.Verdict {
		case gateledger.VerdictPass, gateledger.VerdictFail, gateledger.VerdictInfra, gateledger.VerdictSuperseded:
		default:
			return fmt.Errorf("--event finish needs --verdict pass, fail, infra or superseded")
		}
		if ev.RunID == "" {
			return fmt.Errorf("--event finish needs the --run-id --event start printed")
		}
		// The tree must be the one that started: a pass over a moving tree
		// proves nothing about either end of it.
		after, err := hooks.TreeFingerprint(ctx, &hooks.ExecCmdRunner{}, root)
		ev.TreeMoved = err != nil || ev.Tree == "" || after != ev.Tree
	default:
		return fmt.Errorf("--event must be start, finish or reused")
	}
	if ev.RunID == "" {
		id := make([]byte, 8)
		if _, err := rand.Read(id); err != nil {
			return err
		}
		ev.RunID = hex.EncodeToString(id)
	}
	ev.Issue, _ = strconv.Atoi(os.Getenv("NIGHTGAUGE_ISSUE_NUMBER"))
	ev.Stage = os.Getenv("NIGHTGAUGE_STAGE")
	if ev.PipelineRun == "" {
		ev.PipelineRun = os.Getenv(adapters.RunIDEnvVar)
	}
	out, err := (&hooks.ExecCmdRunner{}).Run(ctx, root, "git", "branch", "--show-current")
	if err == nil {
		ev.Branch = strings.TrimSpace(string(out))
	}
	if ev.Issue == 0 {
		ev.Issue = issueFromBranch(ev.Branch)
	}
	return nil
}

// issueFromBranch reads N from a `type/N-slug` branch, the pipeline's convention.
// A gate an operator runs by hand on an issue branch still counts toward it.
func issueFromBranch(branch string) int {
	_, rest, ok := strings.Cut(branch, "/")
	if !ok {
		return 0
	}
	digits, _, _ := strings.Cut(rest, "-")
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return n
}
