package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/skillrender"
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
	cmd.AddCommand(opencodeCleanupCmd())
	return cmd
}

// opencodeConfigCmd prints what an opencode spawn for a stage is given: the
// per-run config, the isolation environment, the inherited variables the
// spawn must not get, and the run's directories. It is the one authority the
// SDK path reads (#1648): it runs the Go adapter's own pre-dispatch hook,
// model check and PrepareRunRoot preparation, in the manager's order, so both
// paths spawn OpenCode under the same bytes, and every refusal the adapter
// makes before spawning is an error here.
func opencodeConfigCmd() *cobra.Command {
	var (
		stage      string
		worktree   string
		repo       string
		model      string
		runID      string
		maxTurns   int
		maxTokens  int
		skillsRoot string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print the per-run OpenCode config and environment for a stage",
		Long: `Print what an opencode spawn for a stage is given, as JSON:

  schema_version  the output's version (` + adapters.OpenCodeConfigSchemaVersion + `)
  config_content  OPENCODE_CONFIG_CONTENT, the per-run OpenCode config
  env             every variable the spawn sets from the run: the isolation
                  variables and OPENCODE_CONFIG_CONTENT (no credential)
  env_withhold    the inherited variables the spawn must not get: remove every
                  one whose name starts with one of prefixes or is one of
                  names before adding env, as the Go adapter does
  plugin_dir      where OpenCode loads the run's plugins from
  run_dir         the run's private root
  binary          the absolute path of the opencode binary the version
                  policy checked: spawn this one
  run_id          the run identity naming run_dir: --run-id, or the one
                  minted; ` + "`nightgauge opencode cleanup --run-id`" + ` deletes it
  plugin_version  the plugin_version the plugin's handshake sentinel (at
                  env's NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL) must carry
  non_loopback    false only for a declared model server on this machine;
                  true for one elsewhere and for every hosted provider

The MCP servers are read from the head of --repo's default branch as GitHub
serves it, never from the worktree or its git refs or config. Without --repo,
or when GitHub cannot be read within 15 seconds, the stage is given no MCP
server and stderr says why.

The run's root is created, or reused when --run-id names a run that has one.
Without --run-id a new root is minted; the caller owns it, and a root no stage
uses for 7 days is swept.

--stage's AllowedTools and SkillPath, what the permission map's every read/
edit/bash key and its NIGHTGAUGE_SKILL_DIR allow-list entry are actually
derived from (#1638), are resolved the same way the Go pipeline resolves them
for a dispatch: skillrender.Render against --skills-root (default: the current
directory, plus the bundle next to this binary — skillrender.DefaultRoots).
When --stage's SKILL.md cannot be found there, the config is still printed,
with no tool granted (every permission key deny) and a stderr notice saying
so, rather than failing the command — the shape every caller before #1638's
fix round always got.

The model defaults to opencode.model in the machine-tier config. The command
runs the adapter's own checks, so it fails, and prints nothing on stdout,
wherever the adapter refuses a dispatch before spawning: without
` + adapters.ExperimentalOpenCodeEnvVar + `=1, an opencode binary below the compat
manifest's floor or whose version cannot be read, one newer than max-tested
that fails its self-test or would run a model server you run, an
opencode.binary that is not the absolute path of an executable, a model it
cannot dispatch, an anthropic/ model while ANTHROPIC_API_KEY is unset, an
anthropic/ model OpenCode's bundled catalog does not list or one of its
fast-mode entries, a
model on a forge or cloud platform provider, a provider key that is neither a
declared endpoint nor one OpenCode knows, an endpoint whose limit.context or
limit.output is 0 or missing, a base_url that is not http or https or that
carries credentials, an opencode: block in the worktree's committed config,
and, unless opencode.inherit_user_config is on, a ~/.opencode holding config
or managed OpenCode config on this machine. The adapter's warnings and
notices go to stderr.`,
		Example: `  nightgauge opencode config --stage feature-dev --worktree "$PWD" --repo nightgauge/nightgauge --json
  nightgauge opencode config --stage feature-dev --worktree "$PWD" --model lmstudio/qwen/qwen3.8-27b --max-turns 40 --json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !asJSON {
				return errors.New("pass --json: the command prints JSON only")
			}
			run, err := openCodeConfigForStage(cmd.Context(), openCodeConfigFlags{
				stage: stage, worktree: worktree, repo: repo, model: model, runID: runID,
				maxTurns: maxTurns, maxTokens: maxTokens, skillsRoot: skillsRoot,
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
	cmd.Flags().StringVar(&repo, "repo", "", "Repository the stage works on, as owner/name: its MCP servers are read from the head of its default branch on GitHub (without it, the stage is given no MCP server)")
	cmd.Flags().StringVar(&model, "model", "", "Model as <provider>/<model> (default: opencode.model in the machine-tier config)")
	cmd.Flags().StringVar(&runID, "run-id", "", "Run identity whose root to use (default: a new root)")
	cmd.Flags().IntVar(&maxTurns, "max-turns", 0, "Stage turn cap, set as the steps cap of the build agent and each subagent (0: the adapter's default of 200)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "Stage token cap, which lowers the endpoint's output limit (0: none)")
	cmd.Flags().StringVar(&skillsRoot, "skills-root", "", "Root to search for --stage's SKILL.md, the same convention skillrender.DefaultRoots resolves (default: the current directory, plus the installed bundle next to this binary)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON (the only output format)")
	_ = cmd.MarkFlagRequired("stage")
	_ = cmd.MarkFlagRequired("worktree")
	return cmd
}

type openCodeConfigFlags struct {
	stage, worktree, repo, model, runID, skillsRoot string
	maxTurns, maxTokens                             int
}

// openCodeConfigForStage resolves the verb's inputs the way the manager
// resolves a dispatch's, runs the adapter's PreDispatch and ValidateModel as
// the manager does, and prepares the run through adapters.PrepareOpenCodeRun,
// the function the adapter's PrepareRunRoot calls, with the one machine-tier
// block it read.
func openCodeConfigForStage(ctx context.Context, f openCodeConfigFlags) (*adapters.OpenCodeRun, error) {
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
	allowedTools, skillPath, err := openCodeVerbStageTools(stage, f.skillsRoot)
	if err != nil {
		return nil, err
	}
	run := adapters.RunOptions{
		Stage:        stage,
		WorktreeDir:  worktree,
		TargetRepo:   strings.TrimSpace(f.repo),
		Model:        model,
		MaxTurns:     f.maxTurns,
		MaxTokens:    f.maxTokens,
		AllowedTools: allowedTools,
		SkillPath:    skillPath,
	}
	adapter := adapters.NewOpenCodeAdapter()
	if err := adapter.PreDispatch(ctx, run); err != nil {
		return nil, err
	}
	if err := adapter.ValidateModel(model); err != nil {
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
	// The GitHub identity of the MCP servers' forge read is the one every
	// nightgauge command resolves from the directory it runs in
	// (clientFromConfig). It decides who asks, not what is read: the files
	// are what GitHub serves for --repo.
	cwd, _ := os.Getwd()
	prepared, err := adapters.PrepareOpenCodeRun(adapters.OpenCodeRunRequest{
		Home:             home,
		ID:               id,
		MachineConfigDir: machineDir,
		Run:              run,
		Settings:         settings,
		Lookup:           os.LookupEnv,
		GOOS:             runtime.GOOS,
		McpForge:         adapters.OpenCodeMcpForge(cwd),
		// The adapter's own PrepareRunRoot sets the identical BinDir
		// (adapters.OpenCodeBinDir()), so the permission map's
		// external_directory allow-list this verb prints matches the
		// adapter's exactly (#1638, TestOpenCodeConfigVerbMatchesTheAdapter).
		BinDir: adapters.OpenCodeBinDir(),
	})
	if err != nil {
		return nil, err
	}
	// The Go adapter's PrepareRunRoot installs the Nightgauge OpenCode plugin
	// (#1635) immediately after this same call; mirrored here so the SDK path
	// prints the identical `plugin` reference (TestOpenCodeConfigVerbMatchesTheAdapter).
	// id, not run.RunID, is what names the handshake: run.RunID (RunOptions'
	// own field) is never set by this command, while id is always a run
	// identity (validated or minted above) — the same thing req.ID is for the
	// adapter's own PrepareRunRoot call (opencode.go, #1635 fix round finding
	// 1/5). This verb never spawns opencode itself: the SDK caller that
	// spawns from its output verifies the minted handshake (#1648, #1804,
	// opencodeHandshake.ts), against the sentinel path and nonce in env and
	// the plugin_version printed beside them.
	if err := adapters.InstallNightgaugePlugin(ctx, prepared, run.OutputFile, id); err != nil {
		return nil, err
	}
	// The binary PreDispatch's version policy just vetted, resolved by the
	// same function from the same pin, so an SDK caller spawns exactly it.
	bin, err := adapters.ResolveOpenCodeBinary(settings.Binary, exec.LookPath)
	if err != nil {
		return nil, err
	}
	prepared.Binary = bin.Path
	prepared.RunID = id
	return prepared, nil
}

// openCodeVerbStageTools resolves stage's AllowedTools and SkillPath the way
// the Go pipeline resolves them for a real dispatch (internal/orchestrator/
// scheduler.go: skillrender.Render against skillrender.DefaultRoots(workspaceRoot),
// AllowedTools filtered through skillrender.FilterHeadlessTools) — the
// permission map's every read/edit/bash key and its NIGHTGAUGE_SKILL_DIR
// allow-list entry come from exactly these two fields (openCodePermissionMap,
// #1638), so leaving them unset here, as this verb always did before this fix
// round, silently printed a deny-everything map for every stage
// (TestOpenCodeConfigVerbResolvesStageTools; the issue's own #1648/#1625
// consumers read this verb as the one authority for what a spawn gets).
//
// skillsRoot is --skills-root when the caller passed one, else the current
// directory: this verb has no workspaceRoot of its own the way the Go
// pipeline's Scheduler does (WorkspaceRoot names the checkout that IS
// running the orchestrator, not any of --worktree, which can be a sibling
// repository's own worktree). skillrender.DefaultRoots' own bundle-relative
// fallback (a "skills" directory next to this binary's own directory,
// bundleSkillsRoot) still finds an installed layout's skills regardless of
// either.
//
// Without --skills-root, a resolution failure (no SKILL.md for stage under
// any searched root) is not fatal to the command: it prints a stderr notice
// and returns no tools, so the caller still gets a config — one with every
// permission key denied, the shape every caller of this verb got before this
// fix round, and still useful for every other field (env, isolation,
// run_dir). With --skills-root the caller named where the stage's skill is,
// so a failure is an error and nothing is printed on stdout: a caller that
// spawns from the output (the SDK, #1648) must never run a stage under an
// all-deny map it did not ask for. The Go dispatch path does not use this
// function.
func openCodeVerbStageTools(stage, skillsRoot string) (allowedTools []string, skillPath string, err error) {
	root := skillsRoot
	if root == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	skillData, err := skillrender.Render(skillrender.Options{
		Stage:       stage,
		Adapter:     "opencode",
		SkillsRoots: skillrender.DefaultRoots(root),
	})
	if err != nil {
		if skillsRoot != "" {
			return nil, "", fmt.Errorf("--skills-root %q: could not resolve %s's SKILL.md, so the stage's permission map cannot be built: %w", skillsRoot, stage, err)
		}
		fmt.Fprintf(os.Stderr, "[opencode] could not resolve %s's SKILL.md under %q (pass --skills-root, or run from the nightgauge checkout, to fix this): %v; printing the config with every permission key denied\n", stage, root, err)
		return nil, "", nil
	}
	return skillrender.FilterHeadlessTools(skillData.AllowedTools), skillData.SkillPath, nil
}

// opencodeCleanupCmd deletes one run's OpenCode per-run root, the SDK path's
// twin of the Go scheduler's CleanupOpenCodeRunRoot at every terminal outcome
// (ADR-022 § 22, #1648). The root's location stays Go's alone
// (adapters.RemoveOpenCodeRunRoot): the caller names only the run identity
// `nightgauge opencode config` printed as run_id.
func opencodeCleanupCmd() *cobra.Command {
	var runID string
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Delete a run's OpenCode per-run root",
		Long: `Delete the per-run root ` + "`nightgauge opencode config`" + ` created for --run-id,
with the session database and transcripts in it. A root that does not exist is
not an error. An id that is not a run identity (a canonical lowercase UUIDv7),
a root that is a symbolic link, is not a directory or is not directly under the
runs directory is refused, and nothing inside the root is followed.`,
		Example:      `  nightgauge opencode cleanup --run-id 01890a5d-ac96-774b-bcce-b30209a81625`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !runstate.IsIdentity(runID) {
				return fmt.Errorf("--run-id %q is not a run identity (a canonical lowercase UUIDv7)", runID)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("the opencode per-run root needs the home directory: %w", err)
			}
			return adapters.RemoveOpenCodeRunRoot(home, runID)
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "Run identity whose root to delete (required)")
	_ = cmd.MarkFlagRequired("run-id")
	return cmd
}
