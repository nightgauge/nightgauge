package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// evalSweepScript is the TypeScript runner `eval sweep` execs (#2118). The
// eval harness lives in the SDK; the verb validates flags and hands off.
const evalSweepScript = "scripts/eval-sweep.ts"

// evalRunScript executes argv in dir with the caller's stdio and returns the
// child's exit code. A variable so tests can capture argv without node.
var evalRunScript = func(dir string, argv []string) (int, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = dir
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// evalCmd is the `nightgauge eval` parent (#2118; #2119 adds `report`).
func evalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Model evaluation over the pinned control issue",
	}
	cmd.AddCommand(evalSweepCmd(), evalReportCmd())
	return cmd
}

type evalSweepFlags struct {
	model          string
	control        bool
	efforts        string
	maxUSD         float64
	estimateUSD    float64
	cellTimeoutMin int
	mode           string
	out            string
}

// evalSweepArgv validates flags and builds the script's argv. Pure.
func evalSweepArgv(f evalSweepFlags, changed func(string) bool) ([]string, error) {
	if !f.control {
		return nil, errors.New("--control is required: the sweep runs only the pinned control task")
	}
	if strings.TrimSpace(f.model) == "" {
		return nil, errors.New("--model is required")
	}
	if f.maxUSD < 0 {
		return nil, errors.New("--max-usd must be >= 0")
	}
	if f.estimateUSD < 0 {
		return nil, errors.New("--estimate-usd must be >= 0")
	}
	if f.cellTimeoutMin <= 0 {
		return nil, errors.New("--cell-timeout-min must be > 0")
	}
	if f.mode != "stub" && f.mode != "live" {
		return nil, fmt.Errorf("--mode must be stub or live, got %q", f.mode)
	}
	argv := []string{"npx", "tsx", evalSweepScript, "--model", f.model, "--control", "--mode", f.mode,
		"--cell-timeout-min", strconv.Itoa(f.cellTimeoutMin)}
	if f.efforts != "" {
		argv = append(argv, "--efforts", f.efforts)
	}
	if changed("max-usd") {
		argv = append(argv, "--max-usd", strconv.FormatFloat(f.maxUSD, 'f', -1, 64))
	}
	if changed("estimate-usd") {
		argv = append(argv, "--estimate-usd", strconv.FormatFloat(f.estimateUSD, 'f', -1, 64))
	}
	if f.out != "" {
		argv = append(argv, "--out", f.out)
	}
	return argv, nil
}

// findEvalRepoRoot walks up from dir to the nightgauge checkout holding the script.
func findEvalRepoRoot(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, evalSweepScript)); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("%s not found above %s: run from a nightgauge checkout", evalSweepScript, dir)
		}
	}
}

func evalSweepCmd() *cobra.Command {
	var f evalSweepFlags
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Run the control issue through the whole pipeline once per effort of one model",
		Long: `Runs the pinned control task once per supported effort of --model, one cell
at a time, and writes one JSONL record per cell (cost, wall time, attempts,
objective pass/fail, score) to the model-eval records store.

--max-usd stops the sweep before a cell whose projected spend would exceed the
cap and exits 1. --mode stub (default) exercises the loop without a model;
--mode live spends real money. See docs/MODEL_EVALUATION.md.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			argv, err := evalSweepArgv(f, cmd.Flags().Changed)
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
	cmd.Flags().StringVar(&f.model, "model", "", "Model id to sweep (registry id)")
	cmd.Flags().BoolVar(&f.control, "control", false, "Run the pinned control task (required)")
	cmd.Flags().StringVar(&f.efforts, "efforts", "", "Comma-separated subset of the model's efforts (default: all supported)")
	cmd.Flags().Float64Var(&f.maxUSD, "max-usd", 0, "Spend cap in USD; the sweep stops before a cell that would exceed it")
	cmd.Flags().Float64Var(&f.estimateUSD, "estimate-usd", 5, "Per-cell cost estimate used by the cap before any cell has run")
	cmd.Flags().IntVar(&f.cellTimeoutMin, "cell-timeout-min", 90, "Per-cell wall-clock timeout in minutes")
	cmd.Flags().StringVar(&f.mode, "mode", "stub", "stub (no model) or live (real spend)")
	cmd.Flags().StringVar(&f.out, "out", "", "JSONL output path (default: the model-eval records store)")
	return cmd
}
