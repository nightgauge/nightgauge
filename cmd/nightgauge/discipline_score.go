package main

import (
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/intelligence/disciplineScore"
	"github.com/spf13/cobra"
)

// disciplineScoreCmd runs `nightgauge discipline-score` — the per-repo
// verification-readiness score (#4100). Informational by default; with --gate it
// enforces the autonomous discipline gate (exit 1 when the repo is too
// under-prepared for full autonomy).
//
// Exit codes:
//
//	0 — score reported (or gated in warn mode, or gate disabled)
//	1 — --gate set and the repo is below the discipline floor in block mode
func disciplineScoreCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
		gate       bool
	)
	cmd := &cobra.Command{
		Use:   "discipline-score",
		Short: "Per-repo verification-readiness score; gates autonomy on under-prepared repos (#4100)",
		Long: `Scores a repository's verification discipline (0–100) from deterministic
signals — a real test suite, a runnable test command, CI workflows, process
docs, issue templates. AI amplifies a weak culture as readily as a strong one,
so --gate refuses full autonomy on a repo below the discipline floor
(autonomous.discipline_gate), steering it toward human-in-the-loop.`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			work := workdir
			if work == "" {
				if wd, e := os.Getwd(); e == nil {
					work = wd
				}
			}
			res := disciplineScore.Compute(disciplineScore.GatherSignals(work))

			if outputJSON {
				if err := printJSON(res); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON: %v\n", err)
				}
			} else {
				printDisciplineHuman(res)
			}

			if gate {
				var autoCfg *config.AutonomousConfig
				if cfg, e := config.Load(work); e == nil && cfg != nil {
					autoCfg = cfg.Autonomous
				}
				if blocked, msg := disciplineGateBlocks(autoCfg, res); blocked {
					fmt.Fprintln(os.Stderr, msg)
					os.Exit(1)
				} else if msg != "" {
					fmt.Fprintln(os.Stderr, msg)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Repository root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&gate, "gate", false, "Enforce the autonomous discipline gate (exit 1 when below the floor in block mode)")
	return cmd
}

func printDisciplineHuman(r disciplineScore.DisciplineResult) {
	fmt.Printf("Discipline score: %d/100 (%s)\n", r.Score, r.Readiness)
	for _, b := range r.Breakdown {
		fmt.Printf("  %s\n", b)
	}
	for _, g := range r.Gaps {
		fmt.Printf("  ✗ %s\n", g)
	}
}

// disciplineGateBlocks applies the autonomous.discipline_gate policy to a score.
// Returns (blocked, message). blocked=true means autonomy must be refused;
// a non-empty message with blocked=false is a warn-mode advisory.
func disciplineGateBlocks(autoCfg *config.AutonomousConfig, res disciplineScore.DisciplineResult) (bool, string) {
	enabled, minScore, mode := autoCfg.ResolveDisciplineGate()
	if !enabled || res.Score >= minScore {
		return false, ""
	}
	base := fmt.Sprintf("discipline score %d/100 is below the autonomy floor of %d (%s repo): %v",
		res.Score, minScore, res.Readiness, res.Gaps)
	if mode == "warn" {
		return false, "WARNING: " + base + " — proceeding (discipline_gate mode=warn). Add a test suite + CI, or run with human-in-the-loop."
	}
	return true, "BLOCKED: " + base +
		"\n  Full autonomy is refused on an under-prepared repo (#4100). Add a real test suite + CI, lower autonomous.discipline_gate.min_score, set mode: warn, or run pipelines with human-in-the-loop."
}
