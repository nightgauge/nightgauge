package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// --- opencode command ---

func opencodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "opencode",
		Short: "OpenCode adapter operations",
		Long: "Operations for the experimental opencode adapter. See " +
			"docs/decisions/022-opencode-multi-provider-adapter.md.",
	}
	cmd.AddCommand(opencodeConfigCmd())
	return cmd
}

// opencodeConfigCmd prints what an opencode spawn for a stage is given: the
// per-run config, the isolation environment and the run's directories. It is
// the one authority the SDK path reads (#1648), and it runs the same code as
// the Go adapter's PrepareRunRoot, so both paths spawn OpenCode under the same
// bytes. Every refusal the adapter makes before spawning is an error here.
func opencodeConfigCmd() *cobra.Command {
	var (
		stage     string
		worktree  string
		model     string
		runID     string
		maxTurns  int
		maxTokens int
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print the per-run OpenCode config and environment for a stage",
		Long: `Print what an opencode spawn for a stage is given, as JSON:

  schema_version  the output's version (` + adapters.OpenCodeConfigSchemaVersion + `)
  config_content  OPENCODE_CONFIG_CONTENT, the per-run OpenCode config
  env             every variable the spawn sets from the run: the isolation
                  variables and OPENCODE_CONFIG_CONTENT (no credential)
  plugin_dir      where OpenCode loads the run's plugins from
  run_dir         the run's private root
  non_loopback    true when the model server is not on this machine

The run's root is created, or reused when --run-id names a run that has one.
Without --run-id a new root is minted; the caller owns it, and a root no stage
uses for 7 days is swept.

The model defaults to opencode.model in the machine-tier config. The command
fails, and prints nothing on stdout, whenever the adapter would refuse the
dispatch before spawning: a model it cannot dispatch, an anthropic/ model while
ANTHROPIC_API_KEY is unset, a local model with no declared endpoint, an
endpoint whose limit.context or limit.output is 0 or missing, or a base_url
that is not http or https or that carries credentials.`,
		Example: `  nightgauge opencode config --stage feature-dev --worktree "$PWD" --json
  nightgauge opencode config --stage feature-dev --worktree "$PWD" --model lmstudio/qwen/qwen3.8-27b --max-turns 40 --json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !asJSON {
				return errors.New("pass --json: the command prints JSON only")
			}
			run, err := openCodeConfigForStage(openCodeConfigFlags{
				stage: stage, worktree: worktree, model: model, runID: runID,
				maxTurns: maxTurns, maxTokens: maxTokens,
			})
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			return enc.Encode(run)
		},
	}
	cmd.Flags().StringVar(&stage, "stage", "", "Pipeline stage the config is for (required)")
	cmd.Flags().StringVar(&worktree, "worktree", "", "Worktree the stage runs in (required)")
	cmd.Flags().StringVar(&model, "model", "", "Model as <provider>/<model> (default: opencode.model in the machine-tier config)")
	cmd.Flags().StringVar(&runID, "run-id", "", "Run identity whose root to use (default: a new root)")
	cmd.Flags().IntVar(&maxTurns, "max-turns", 0, "Stage turn cap, set as the steps cap of the build agent and each subagent (0: the adapter's default of 200)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "Stage token cap, which lowers the endpoint's output limit (0: none)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON (the only output format)")
	_ = cmd.MarkFlagRequired("stage")
	_ = cmd.MarkFlagRequired("worktree")
	return cmd
}

type openCodeConfigFlags struct {
	stage, worktree, model, runID string
	maxTurns, maxTokens           int
}

// openCodeConfigForStage resolves the verb's inputs the way the manager
// resolves a dispatch's, and prepares the run through
// adapters.PrepareOpenCodeRun, the function the adapter's PrepareRunRoot calls.
func openCodeConfigForStage(f openCodeConfigFlags) (*adapters.OpenCodeRun, error) {
	stage := strings.TrimSpace(f.stage)
	if stage == "" {
		return nil, errors.New("--stage is required")
	}
	if f.maxTurns < 0 || f.maxTokens < 0 {
		return nil, errors.New("--max-turns and --max-tokens must not be negative")
	}
	worktree, err := filepath.Abs(f.worktree)
	if err != nil {
		return nil, fmt.Errorf("--worktree: %w", err)
	}
	if fi, err := os.Stat(worktree); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("--worktree %q is not a directory", f.worktree)
	}

	settings, err := config.LoadOpenCodeConfig(worktree)
	if err != nil {
		return nil, err
	}
	model := f.model
	if model == "" {
		model = settings.Model
	}
	if model == "" {
		return nil, errors.New("no model: pass --model <provider>/<model>, or set opencode.model in the machine-tier config (~/.nightgauge/config.yaml)")
	}
	if err := adapters.NewOpenCodeAdapter().ValidateModel(model); err != nil {
		return nil, err
	}

	id := f.runID
	if id == "" {
		if id, err = runstate.NewRunID(); err != nil {
			return nil, fmt.Errorf("mint a run root id: %w", err)
		}
	} else if !runstate.IsIdentity(id) {
		return nil, fmt.Errorf("--run-id %q is not a run identity (a canonical lowercase UUIDv7)", id)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("the opencode per-run root needs the home directory: %w", err)
	}
	machineDir, err := config.MachineConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve the machine-tier config directory: %w", err)
	}
	return adapters.PrepareOpenCodeRun(adapters.OpenCodeRunRequest{
		Home:             home,
		ID:               id,
		MachineConfigDir: machineDir,
		Run: adapters.RunOptions{
			Stage:       stage,
			WorktreeDir: worktree,
			Model:       model,
			MaxTurns:    f.maxTurns,
			MaxTokens:   f.maxTokens,
		},
		Settings: settings,
		Lookup:   os.LookupEnv,
		GOOS:     runtime.GOOS,
	})
}
