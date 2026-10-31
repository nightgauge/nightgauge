package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/contractrollout"
	"github.com/nightgauge/nightgauge/internal/gateledger"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/spf13/cobra"
)

// gateStatus is the JSON shape of `nightgauge ci gate-status`.
type gateStatus struct {
	Reusable bool   `json:"reusable"`
	Reason   string `json:"reason"`
	Tree     string `json:"tree"`
	// GateCommand is the repository's complete local gate, empty when it has none.
	GateCommand string `json:"gate_command"`
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
	if err == nil && !gate.Missing() {
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
	f.StringVar(&ev.Verdict, "verdict", "", "pass, fail or infra (finish only)")
	f.BoolVar(&ev.Full, "full", false, "Every step ran: no change scoping and no skipped step")
	f.IntVar(&ev.Seconds, "seconds", 0, "Wall-clock seconds of the run (finish only)")
	f.StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// completeGateEvent validates ev and fills what the environment and the tree
// supply. Issue and stage come from the stage's environment, the branch from git.
func completeGateEvent(ctx context.Context, root string, ev *gateledger.Event) error {
	switch ev.Event {
	case gateledger.EventStart, gateledger.EventReused:
	case gateledger.EventFinish:
		switch ev.Verdict {
		case gateledger.VerdictPass, gateledger.VerdictFail, gateledger.VerdictInfra:
		default:
			return fmt.Errorf("--event finish needs --verdict pass, fail or infra")
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
