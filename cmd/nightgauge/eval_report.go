package main

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// evalReportScript is the TypeScript runner `eval report` execs (#2119).
const evalReportScript = "scripts/eval-report.ts"

type evalReportFlags struct {
	model       string
	controlHash string
	recordsDir  string
	tolerance   float64
	minSamples  int
	json        bool
	emitPatch   string
}

// evalReportArgv validates flags and builds the script's argv. Pure.
func evalReportArgv(f evalReportFlags, changed func(string) bool) ([]string, error) {
	if strings.TrimSpace(f.model) == "" {
		return nil, errors.New("--model is required")
	}
	if f.tolerance < 0 {
		return nil, errors.New("--tolerance must be >= 0")
	}
	if f.minSamples < 1 {
		return nil, errors.New("--min-samples must be >= 1")
	}
	argv := []string{"npx", "tsx", evalReportScript, "--model", f.model}
	if f.controlHash != "" {
		argv = append(argv, "--control-hash", f.controlHash)
	}
	if f.recordsDir != "" {
		argv = append(argv, "--records-dir", f.recordsDir)
	}
	if changed("tolerance") {
		argv = append(argv, "--tolerance", strconv.FormatFloat(f.tolerance, 'f', -1, 64))
	}
	if changed("min-samples") {
		argv = append(argv, "--min-samples", strconv.Itoa(f.minSamples))
	}
	if f.json {
		argv = append(argv, "--json")
	}
	if f.emitPatch != "" {
		argv = append(argv, "--emit-patch", f.emitPatch)
	}
	return argv, nil
}

func evalReportCmd() *cobra.Command {
	var f evalReportFlags
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Per-effort report and effort_default recommendation from control-sweep records",
		Long: `Reads the records eval sweep wrote for --model on one control hash and prints
a per-effort table (n, pass, objective, quality, variance, cost, wall time,
attempts), a comparison with other models swept on the same hash, and a
deterministic effort_default recommendation.

--emit-patch writes a unified diff of behavior.effort_default for both registry
copies; it never changes the registry. Apply it in a pull request. See
docs/MODEL_EVALUATION.md.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			argv, err := evalReportArgv(f, cmd.Flags().Changed)
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := findEvalRepoRoot(wd)
			if err != nil {
				return err
			}
			code, err := evalRunScript(root, argv)
			if err != nil {
				return err
			}
			if code != 0 {
				return verdictExit{code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&f.model, "model", "", "Model id to report on (registry id)")
	cmd.Flags().StringVar(&f.controlHash, "control-hash", "", "Control hash to report on (default: the model's newest record)")
	cmd.Flags().StringVar(&f.recordsDir, "records-dir", "", "Directory of sweep JSONL records (default: the model-eval records store)")
	cmd.Flags().Float64Var(&f.tolerance, "tolerance", 5, "Quality tolerance in composite points below the best cell")
	cmd.Flags().IntVar(&f.minSamples, "min-samples", 3, "Runs a cell needs before it can be recommended")
	cmd.Flags().BoolVar(&f.json, "json", false, "Print the report as JSON")
	cmd.Flags().StringVar(&f.emitPatch, "emit-patch", "", "Write a diff of behavior.effort_default for both registry copies to this path")
	return cmd
}
