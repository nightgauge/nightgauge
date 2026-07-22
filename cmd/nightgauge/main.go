package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"path/filepath"

	forgecmd "github.com/nightgauge/nightgauge/cmd/nightgauge/forge"
	workspacecmd "github.com/nightgauge/nightgauge/cmd/nightgauge/workspace"
	apipkg "github.com/nightgauge/nightgauge/internal/audit"
	cipkg "github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/cmd/aggregatefindings"
	"github.com/nightgauge/nightgauge/internal/cmd/batchfailures"
	healthpkg "github.com/nightgauge/nightgauge/internal/cmd/health"
	runstatecmd "github.com/nightgauge/nightgauge/internal/cmd/runstate"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	docspkg "github.com/nightgauge/nightgauge/internal/docs"
	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/executor"
	"github.com/nightgauge/nightgauge/internal/focus"
	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/intelligence/acparse"
	"github.com/nightgauge/nightgauge/internal/intelligence/batch"
	"github.com/nightgauge/nightgauge/internal/intelligence/changeClassifier"
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/nightgauge/nightgauge/internal/intelligence/disciplineScore"
	"github.com/nightgauge/nightgauge/internal/intelligence/failure"
	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/intelligence/loopverdicts"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/intelligence/suggestions"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
	"github.com/nightgauge/nightgauge/internal/intelligence/teams"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/intelligence/typeinfer"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/notifications/inbound"
	"github.com/nightgauge/nightgauge/internal/notifications/inbound/auth"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/pipeline"
	"github.com/nightgauge/nightgauge/internal/platform"
	ibqueue "github.com/nightgauge/nightgauge/internal/queue"
	"github.com/nightgauge/nightgauge/internal/scan"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/validation"
	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

// version is set at build time via ldflags.
var version = "dev"

// effectiveVersion preserves the linker-injected release version and falls
// back to Go module build metadata for `go install ...@version` builds.
func effectiveVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return "dev"
}

// Action Center agent-registration bridge cadences (#341). The platform agent
// TTL is 90s; heartbeat well inside it (matching the VSCode extension's 30s
// AgentHeartbeatService). Registration retries every minute so an offline start
// eventually self-heals without hammering the platform.
const (
	attentionRegisterRetryInterval = 60 * time.Second
	attentionHeartbeatInterval     = 30 * time.Second
)

// runAttentionAgentRegistration registers this daemon as a platform agent, then
// wires the Action Center command SSE stream + heartbeat to the platform-assigned
// agent id (NOT the machine id, which is not a registered agent — #341). Until
// registration succeeds the attention sync stays mirror-only (agent_id omitted).
// Both the heartbeat 404 (TTL eviction) and the command stream 404 (agent gone)
// funnel into a single re-register path that swaps the id and restarts the
// stream against the new id. Returns when ctx is cancelled.
func runAttentionAgentRegistration(
	ctx context.Context,
	reg *platform.AgentRegistrationService,
	attnSync *platform.AttentionSyncService,
	platformClient *platform.Client,
	resolver platform.AttentionResolver,
) {
	agentID := registerAttentionAgentWithRetry(ctx, reg)
	if agentID == "" {
		return // ctx cancelled before registration succeeded
	}

	// agentGone funnels a command-stream 404 back to this loop so it shares the
	// heartbeat-404 re-register path. Buffered(1) + a non-blocking send so the
	// stream goroutine never blocks and stale signals coalesce to one.
	agentGone := make(chan struct{}, 1)
	signalGone := func() {
		select {
		case agentGone <- struct{}{}:
		default:
		}
	}

	var pollCancel context.CancelFunc
	startStream := func(id string) {
		var streamCtx context.Context
		streamCtx, pollCancel = context.WithCancel(ctx)
		startAttentionCommandStream(streamCtx, platformClient, resolver, id, signalGone)
	}

	// Late-bind the real agent id: subsequent sync pushes carry it, and the
	// watermark clear inside SetAgentID forces one full re-push so the platform
	// backfills agent_id on rows mirrored in mirror-only mode.
	attnSync.SetAgentID(agentID)
	startStream(agentID)
	log.Printf("[nightgauge] Action Center agent registered (agent=%s) — attention sync now carries agent_id; command stream + heartbeat started", agentID)

	// reRegister tears down the current stream, obtains a fresh agent id, rebinds
	// the sync + stream, and drains any stale gone-signal. Returns false only if
	// ctx was cancelled mid-registration (caller should return).
	reRegister := func(reason string) bool {
		log.Printf("[nightgauge] attention agent %s: %s — re-registering", agentID, reason)
		pollCancel()
		newID := registerAttentionAgentWithRetry(ctx, reg)
		if newID == "" {
			return false // ctx cancelled during re-registration
		}
		agentID = newID
		attnSync.SetAgentID(agentID)
		// Drop a stale gone-signal left by the torn-down stream before restarting.
		select {
		case <-agentGone:
		default:
		}
		startStream(agentID)
		log.Printf("[nightgauge] Action Center agent re-registered (agent=%s)", agentID)
		return true
	}

	ticker := time.NewTicker(attentionHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			pollCancel()
			return
		case <-agentGone:
			if !reRegister("command stream reported agent gone (404)") {
				return
			}
		case <-ticker.C:
			err := reg.Heartbeat(ctx, agentID)
			if err == nil {
				continue
			}
			if !errors.Is(err, platform.ErrAgentNotFound) {
				log.Printf("[nightgauge] attention agent heartbeat failed (will retry): %v", err)
				continue
			}
			if !reRegister("heartbeat 404 (TTL eviction)") {
				return
			}
		}
	}
}

// registerAttentionAgentWithRetry attempts registration immediately, then every
// attentionRegisterRetryInterval until it succeeds or ctx is cancelled. Returns
// the platform-assigned agent id, or "" if ctx was cancelled first.
func registerAttentionAgentWithRetry(ctx context.Context, reg *platform.AgentRegistrationService) string {
	for {
		info, err := reg.RegisterAgent(ctx)
		if err == nil && info.AgentID != "" {
			return info.AgentID
		}
		if err != nil {
			log.Printf("[nightgauge] attention agent registration failed (mirror-only; retrying in %s): %v", attentionRegisterRetryInterval, err)
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(attentionRegisterRetryInterval):
		}
	}
}

// startAttentionCommandStream builds a fresh command consumer bound to agentID
// (used for the ack path) and opens the agent-command SSE stream against it. A
// new consumer is built on every (re-)registration so the ack carries the
// current id. onAgentGone is invoked if the stream sees a 404 (agent evicted).
func startAttentionCommandStream(ctx context.Context, platformClient *platform.Client, resolver platform.AttentionResolver, agentID string, onAgentGone func()) {
	consumer := platform.NewAttentionCommandConsumer(
		resolver, // *ipc.Server implements platform.AttentionResolver
		platform.NewCommandService(platformClient).AcknowledgeAgentCommand,
		agentID,
	)
	platform.StartAttentionCommandStream(ctx, platformClient, consumer, agentID, onAgentGone)
}

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// getOwnerType reads the --owner-type persistent flag from the root command.
func getOwnerType(cmd *cobra.Command) gh.OwnerType {
	if f := cmd.Root().PersistentFlags().Lookup("owner-type"); f != nil {
		return gh.ParseOwnerType(f.Value.String())
	}
	return gh.OwnerTypeOrg
}

// globalToken holds the --token CLI flag value. Set by rootCmd PersistentPreRunE.
var globalToken string

// clientFromConfig creates a GitHub client using the full resolution chain:
//  1. --token CLI flag (globalToken)
//  2. Per-project or per-org token from config (github_auth.token / github_auth.tokens)
//  3. GITHUB_TOKEN env var
//  4. gh auth token --user <user> (from config github_user / github_auth.users) —
//     scoped to the configured identity, never the ambient active account (#3700)
//  5. gh auth token (default gh user) — only when no github_user is configured
func clientFromConfig() (*gh.Client, error) {
	workdir, err := os.Getwd()
	if err != nil {
		return gh.NewClientFromConfig(nil, "", globalToken)
	}
	cfg, err := config.Load(workdir)
	if err != nil || cfg == nil {
		return gh.NewClientFromConfig(nil, "", globalToken)
	}
	return gh.NewClientFromConfig(cfg, cfg.Owner, globalToken)
}

// exportConfiguredGitHubToken resolves the pipeline's GitHub token via the same
// chain the in-process client uses (config github_auth.token → GITHUB_TOKEN env
// → `gh auth token --user <github_user>`) and exports it as GH_TOKEN and
// GITHUB_TOKEN so deterministic `gh` SUBPROCESSES — post-condition gates,
// recovery/reconcile, board-status updates, and skills that shell out to `gh` —
// authenticate as the configured identity instead of the machine's ambient
// active gh account.
//
// Without this, on a multi-account machine whose active gh account lacks access
// to the target org, every bare `gh` call fails with "Could not resolve to a
// Repository" — producing false-negative post-condition gates and board-status
// failures — even though the in-process client (and PR creation) succeed,
// because they resolve the token via config (#3887).
//
// Returns the exported token, or "" when none resolved (env left untouched, so
// behaviour is unchanged). Single-org note: this exports ONE token (the
// workspace owner's), which is correct for single-org workspaces. Cross-org
// workspaces using per-owner github_auth.tokens still resolve correctly
// in-process via the per-repo client resolver; only their bare-`gh` subprocess
// calls would need a per-owner override, which is out of scope here.
func exportConfiguredGitHubToken(resolver gh.TokenResolver, owner string) string {
	tok, err := gh.ResolveTokenChain(resolver, owner)
	if err != nil || tok == "" {
		return ""
	}
	_ = os.Setenv("GH_TOKEN", tok)
	_ = os.Setenv("GITHUB_TOKEN", tok)
	return tok
}

// ownerGitHubUserResolver is the optional interface a resolver implements to
// expose the configured github_user for a SPECIFIC owner (config implements it
// via ResolveGitHubUserForOwner). When present and non-empty, the repo declares
// a deterministic per-repo identity that is authoritative over the ambient env
// token — so maybeExportGitHubToken re-resolves and OVERRIDES a shadowing
// ambient GH_TOKEN/GITHUB_TOKEN rather than deferring to it (#4068).
type ownerGitHubUserResolver interface {
	ResolveGitHubUserForOwner(owner string) string
}

// configuredGitHubUserFor returns the github_user the resolver configures for
// owner, or "" when the resolver doesn't expose one (single-identity repos).
func configuredGitHubUserFor(resolver gh.TokenResolver, owner string) string {
	if ur, ok := resolver.(ownerGitHubUserResolver); ok {
		return ur.ResolveGitHubUserForOwner(owner)
	}
	return ""
}

// maybeExportGitHubToken exports the configured per-repo token (see
// exportConfiguredGitHubToken) for the `gh` subprocesses every subcommand
// spawns. Called from the root PersistentPreRunE so the binary never depends on
// its caller having set GH_TOKEN: it works the same whether launched by the
// VSCode extension, a hook, a skill, or a bare terminal.
//
// Authority rule (#4068):
//   - When the repo configures a specific github_user, that identity is
//     AUTHORITATIVE over the ambient env. We re-resolve (the chain runs
//     `gh auth token --user` with ambient GH_TOKEN/GITHUB_TOKEN stripped) and
//     OVERRIDE a shadowing ambient token — otherwise a wrong-user ambient
//     token silently wins (the Acme-Community → octocat bug).
//   - When NO github_user is configured, the previous behavior holds: an
//     upstream GH_TOKEN (extension/terminal env, skillRunner, guard.sh) wins
//     and we skip the extra resolution on the hot path.
//
// Returns the token it exported, or "" (skipped, or none resolved).
func maybeExportGitHubToken(resolver gh.TokenResolver, owner string) string {
	configuredUser := configuredGitHubUserFor(resolver, owner)
	if configuredUser == "" {
		// Single-identity repo: keep the caller-set token if present.
		if os.Getenv("GH_TOKEN") != "" {
			return ""
		}
		return exportConfiguredGitHubToken(resolver, owner)
	}
	// Configured per-repo identity: override the ambient token. The resolver's
	// github_user-scoped path wins over ambient env (env-stripped resolution),
	// so re-resolving here guarantees `gh` subprocesses act as configuredUser.
	return exportConfiguredGitHubToken(resolver, owner)
}

// wireIdentityChecker loads the workspace config at cwd and, when present,
// attaches the per-repo identity preflight gate to the scheduler (#4068). A
// missing/invalid config or a config with no configured github_user yields a nil
// checker, leaving the gate disabled so single-identity workspaces are
// unaffected. Errors are intentionally swallowed — identity assertion is a
// safety gate, not a hard dependency for scheduler construction.
func wireIdentityChecker(sched *orchestrator.Scheduler, cwd string) {
	cfg, err := config.Load(cwd)
	if err != nil || cfg == nil {
		return
	}
	if ic := orchestrator.NewConfigIdentityChecker(cfg); ic != nil {
		sched.WithIdentityChecker(ic)
	}
}

// enrichError annotates a GitHub API error with retry guidance or auth instructions
// based on the failure classifier. Returns the original error unchanged when no
// enrichment applies (deterministic or unknown errors).
func enrichError(err error) error {
	if err == nil {
		return nil
	}
	clf := failure.NewClassifier()
	class := clf.Classify("cmd", 1, err.Error())
	switch class.Category {
	case failure.CatTransient, failure.CatInfra:
		return fmt.Errorf("%w (transient — wait and retry)", err)
	case failure.CatPermission:
		return fmt.Errorf("%w (auth error — run: gh auth login, or set GITHUB_TOKEN)", err)
	}
	return err
}

// subtractStrings returns a new slice containing the elements of `a` that are
// not present in `b`. Order is preserved; duplicates in `a` are preserved.
func subtractStrings(a, b []string) []string {
	if len(b) == 0 {
		out := make([]string, len(a))
		copy(out, a)
		return out
	}
	exclude := make(map[string]struct{}, len(b))
	for _, s := range b {
		exclude[s] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if _, skip := exclude[s]; !skip {
			out = append(out, s)
		}
	}
	return out
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "nightgauge",
		Short: "Nightgauge CLI — AI-powered SDLC pipeline",
		Long:  "Command-line interface for the Nightgauge pipeline. Provides GitHub project board operations, issue management, and IPC server for VSCode integration.",
	}

	// PersistentPreRunE runs before every subcommand and applies config.yaml defaults
	// for --owner, --repo, and --project flags when not explicitly set on the CLI.
	// Errors are silently ignored so missing/invalid config never blocks CLI usage.
	// Note: if a future subcommand adds its own PersistentPreRunE, it must chain
	// this call manually (cobra does not chain PersistentPreRunE automatically).
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		workdir, err := os.Getwd()
		if err != nil {
			return nil // not fatal — use hardcoded defaults
		}
		cfg, err := config.Load(workdir)
		if err != nil || cfg == nil {
			return nil // no config — use hardcoded defaults silently
		}
		if !cmd.Flags().Changed("owner") && cfg.Owner != "" {
			_ = cmd.Flags().Set("owner", cfg.Owner) // cobra flag.Set never errors for known flags
		}
		if !cmd.Flags().Changed("repo") && cfg.DefaultRepo != "" {
			_ = cmd.Flags().Set("repo", cfg.DefaultRepo) // cobra flag.Set never errors for known flags
		}
		if !cmd.Flags().Changed("project") && cfg.ProjectNumber != 0 {
			_ = cmd.Flags().Set("project", fmt.Sprintf("%d", cfg.ProjectNumber)) // cobra flag.Set never errors for known flags
		}
		if !cmd.Flags().Changed("owner-type") && cfg.OwnerType != "" {
			_ = cmd.Flags().Set("owner-type", cfg.OwnerType) // cobra flag.Set never errors for known flags
		}
		// Per-repo GitHub identity: export the configured (github_user-scoped)
		// token as GH_TOKEN/GITHUB_TOKEN so every `gh` subprocess this command
		// spawns authenticates as the repo's configured user, never the machine's
		// ambient active gh account. Guarded so a caller-set GH_TOKEN wins. This is
		// what makes a one-off subcommand (forge/project/hook) self-sufficient
		// without relying on a hook or skill to export the token first.
		maybeExportGitHubToken(cfg, cfg.Owner)
		return nil
	}

	// Global --owner-type flag inherited by all subcommands.
	// Defaults to "org"; set to "user" for user-owned GitHub project boards.
	root.PersistentFlags().String("owner-type", "org", "GitHub owner type: org or user")

	// Global --token flag for one-shot PAT override. Takes highest precedence
	// over all config-based tokens. Avoid using in scripts — prefer env:VAR_NAME
	// in config.yaml to avoid token exposure in shell history.
	root.PersistentFlags().StringVar(&globalToken, "token", "", "GitHub PAT for one-shot operations (overrides all config tokens)")

	root.AddCommand(
		adapterCmd(),
		backlogCmd(),
		boardCmd(),
		issueCmd(),
		epicCmd(),
		labelCmd(),
		projectCmd(),
		prCmd(),
		prePushCmd(),
		ciCmd(),
		gitCmd(),
		runCmd(),
		queueCmd(),
		statusCmd(),
		versionCmd(),
		serveCmd(),
		setupCmd(),
		healthCmd(),
		costCmd(),
		hookCmd(),
		skillsCmd(),
		carefulCmd(),
		learnCmd(),
		suggestCmd(),
		failureCmd(),
		teamsCmd(),
		validateCmd(),
		auditCmd(),
		depgraphCmd(),
		autonomousCmd(),
		focusCmd(),
		intelligenceCmd(),
		outcomeCmd(),
		authCmd(),
		repoCmd(),
		survivalCmd(),
		forgecmd.Cmd(),
		workspacecmd.Cmd(),
		doctorCmd(),
		sizeCmd(),
		sizeGateCmd(),
		baselineGateCmd(),
		depsGateCmd(),
		scopeDriftGateCmd(),
		versionDowngradeCmd(),
		knowledgeCmd(),
		spikeCmd(),
		cleanupCmd(),
		configCmd(),
		scanCmd(),
		preflightCmd(),
		pipelineCmd(),
		logsCmd(),
		exitRecordsCmd(),
		traceCmd(),
		attentionCmd(),
		budgetStatsCmd(),
		docsCmd(),
		buildCmd(),
		e2eCmd(),
		formatCmd(),
		gateCmd(),
		prStageCmd(),
		groundCmd(),
		approvalGateCmd(),
		disciplineScoreCmd(),
		integrationCmd(),
		releaseCmd(),
		testCmd(),
		modernizeCmd(),
	)

	return root
}

// --- board command ---

func boardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Project board operations",
	}
	cmd.AddCommand(boardListCmd())
	return cmd
}

func boardListCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
		status        string
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List project board items",
		Example: `  nightgauge board list --status Ready
  nightgauge board list --status "In Progress" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			svc := gh.NewBoardService(client, owner, projectNumber, getOwnerType(cmd))
			items, err := svc.ListItems(cmd.Context(), status)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(items)
			}

			if len(items) == 0 {
				fmt.Println("No items found.")
				return nil
			}

			for _, item := range items {
				icon := "·"
				if item.IsPR {
					icon = "PR"
				}
				fmt.Printf("[%s] #%-5d %-12s %-4s %s (%s)\n",
					icon, item.Number, item.Status, item.Priority, item.Title, item.Repo)
			}
			fmt.Printf("\nTotal: %d items\n", len(items))
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().StringVar(&status, "status", "", "Filter by status (e.g. Ready, 'In Progress')")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")

	return cmd
}

// --- issue command ---

func issueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Issue operations",
	}
	cmd.AddCommand(
		issueViewCmd(),
		issueListCmd(),
		issueCreateCmd(),
		issueCloseCmd(),
		issueEditCmd(),
		issueSyncLabelsCmd(),
		issueCreateSubCmd(),
		issueLinkSubCmd(),
		issueAddBlockedByCmd(),
		issueRemoveBlockedByCmd(),
		issueRouteCmd(),
		issueInferTypeCmd(),
		issueAcCheckCmd(),
		issueListUnrefinedCmd(),
		issueMarkRefinedCmd(),
		issueHasLabelCmd(),
		issueExtractTargetsCmd(),
	)
	return cmd
}

func issueExtractTargetsCmd() *cobra.Command {
	var (
		bodyFile   string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "extract-targets",
		Short: "Extract predicted change-target files from an issue body (stdin or --body-file)",
		Long: `Deterministically extract the CHANGE-TARGET files an issue body declares or
implies — the single shared implementation behind the epic wave planner and
the issue-create/issue-audit oversized-scope gates (#79).

Resolution order:
  1. An explicit file_ownership list in the nightgauge:dependency-metadata
     comment block wins outright ("declared") — citations cannot re-widen a
     declared scope.
  2. Otherwise targets are inferred from prose ("inferred") with markdown
     links stripped first: a linked file is a citation (evidence, a spike
     doc), not a declaration of intent to edit.`,
		Args: cobra.NoArgs,
		Example: `  printf '%s' "$ISSUE_BODY" | nightgauge issue extract-targets --json
  nightgauge issue extract-targets --body-file /tmp/body.md`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var (
				data []byte
				err  error
			)
			if bodyFile != "" && bodyFile != "-" {
				data, err = os.ReadFile(bodyFile)
			} else {
				data, err = io.ReadAll(cmd.InOrStdin())
			}
			if err != nil {
				return fmt.Errorf("read issue body: %w", err)
			}

			files, source := teams.ExtractTargetFilesDetailed(string(data))
			if files == nil {
				files = []string{}
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"targets": files,
					"count":   len(files),
					"source":  source,
				})
			}
			for _, f := range files {
				fmt.Println(f)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read the issue body from a file instead of stdin (- = stdin)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON {targets, count, source}")
	return cmd
}

func issueViewCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "view [number]",
		Short: "View an issue with sub-issues and blocking relationships",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			number := 0
			if _, err := fmt.Sscanf(args[0], "%d", &number); err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)
			issue, err := svc.GetIssue(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return fmt.Errorf("get issue #%d in %s/%s: %w", number, ownerPart, repoPart, enrichError(err))
			}

			if outputJSON {
				return printJSON(issue)
			}

			fmt.Printf("#%d %s [%s]\n", issue.Number, issue.Title, issue.State)
			if len(issue.Labels) > 0 {
				fmt.Printf("Labels: %s\n", strings.Join(issue.Labels, ", "))
			}
			if len(issue.Assignees) > 0 {
				fmt.Printf("Assignees: %s\n", strings.Join(issue.Assignees, ", "))
			}
			if issue.ParentIssueID != "" {
				fmt.Printf("Parent: %s\n", issue.ParentIssueID)
			}

			if len(issue.SubIssues) > 0 {
				fmt.Printf("\nSub-issues (%d):\n", len(issue.SubIssues))
				for _, si := range issue.SubIssues {
					state := "[ ]"
					if strings.EqualFold(si.State, "CLOSED") {
						state = "[x]"
					}
					fmt.Printf("  %s #%d %s (%s)\n", state, si.Number, si.Title, si.Repo)
				}
			}

			if len(issue.BlockedBy) > 0 {
				fmt.Printf("\nBlocked by:\n")
				for _, b := range issue.BlockedBy {
					fmt.Printf("  #%d %s [%s] (%s)\n", b.Number, b.Title, b.State, b.Repo)
				}
			}
			if len(issue.Blocking) > 0 {
				fmt.Printf("\nBlocking:\n")
				for _, b := range issue.Blocking {
					fmt.Printf("  #%d %s [%s] (%s)\n", b.Number, b.Title, b.State, b.Repo)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueListCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		epic       int
		search     string
		limit      int
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List issues, optionally filtered by epic or search query",
		Example: `  nightgauge issue list --epic 1503
  nightgauge issue list --repo acme-platform
  nightgauge issue list --search "migrate gh cli" --limit 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)

			if epic > 0 {
				progress, err := svc.GetEpicProgressByNumber(cmd.Context(), ownerPart, repoPart, epic)
				if err != nil {
					return err
				}
				if outputJSON {
					return printJSON(progress)
				}
				fmt.Printf("Epic #%d: %s (%.0f%% complete — %d/%d)\n",
					progress.Number, progress.Title, progress.PercentComplete, progress.Closed, progress.Total)
				for _, si := range progress.SubIssues {
					state := "[ ]"
					if strings.EqualFold(si.State, "CLOSED") {
						state = "[x]"
					}
					fmt.Printf("  %s #%d %s (%s)\n", state, si.Number, si.Title, si.Repo)
				}
				return nil
			}

			if search != "" {
				issues, err := svc.SearchIssues(cmd.Context(), ownerPart, repoPart, search, limit)
				if err != nil {
					return err
				}
				if outputJSON {
					return printJSON(issues)
				}
				for _, issue := range issues {
					labels := ""
					if len(issue.Labels) > 0 {
						labels = " [" + strings.Join(issue.Labels, ", ") + "]"
					}
					fmt.Printf("#%-5d %s%s\n", issue.Number, issue.Title, labels)
				}
				fmt.Printf("\nFound: %d issues\n", len(issues))
				return nil
			}

			issues, err := svc.ListIssues(cmd.Context(), ownerPart, repoPart, nil)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(issues)
			}

			for _, issue := range issues {
				fmt.Printf("#%-5d %s\n", issue.Number, issue.Title)
			}
			fmt.Printf("\nTotal: %d issues\n", len(issues))
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name")
	cmd.Flags().IntVar(&epic, "epic", 0, "Filter by epic issue number")
	cmd.Flags().StringVar(&search, "search", "", "Search issues by keyword")
	cmd.Flags().IntVar(&limit, "limit", 10, "Max results for search (default 10)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueCreateCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		title      string
		body       string
		labels     []string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new issue",
		Example: `  nightgauge issue create --title "Fix bug" --body "Description"
  nightgauge issue create --title "Feature" --labels type:feature,priority:high`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			repoID, err := client.GetRepositoryID(cmd.Context(), ownerPart, repoPart)
			if err != nil {
				return err
			}

			svc := gh.NewIssueService(client)
			issue, err := svc.CreateIssue(cmd.Context(), repoID, title, body, labels)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(issue)
			}

			fmt.Printf("Created #%d: %s\n", issue.Number, issue.Title)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().StringVar(&title, "title", "", "Issue title (required)")
	cmd.Flags().StringVar(&body, "body", "", "Issue body")
	cmd.Flags().StringSliceVar(&labels, "labels", nil, "Label node IDs to apply")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueCloseCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "close [number]",
		Short: "Close an issue",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge issue close 42
  nightgauge issue close 42 --repo nightgauge/other-repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)

			issue, err := svc.GetIssue(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			if err := svc.CloseIssue(cmd.Context(), issue.NodeID); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"issueNumber": number,
					"result":      "closed",
				})
			}

			fmt.Printf("Closed #%d\n", number)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueEditCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		body       string
		appendBody string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "edit [number]",
		Short: "Edit an issue (update body)",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge issue edit 42 --body "new full body"
  nightgauge issue edit 42 --append-body "\n\n> Knowledge: path/to/knowledge"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			if body == "" && appendBody == "" {
				return fmt.Errorf("--body or --append-body is required")
			}
			if body != "" && appendBody != "" {
				return fmt.Errorf("--body and --append-body are mutually exclusive")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)

			// Fetch the issue to get node ID (and existing body for append)
			issue, err := svc.GetIssue(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			newBody := body
			if appendBody != "" {
				newBody = issue.Body + appendBody
			}

			updated, err := svc.EditIssue(cmd.Context(), issue.NodeID, newBody)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(updated)
			}

			fmt.Printf("Updated #%d: %s\n", updated.Number, updated.Title)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().StringVar(&body, "body", "", "Replace issue body with this content")
	cmd.Flags().StringVar(&appendBody, "append-body", "", "Append to existing issue body")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueSyncLabelsCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "sync-labels [number] [status-label]",
		Short: "Atomic status label swap (removes old status:* labels, adds new one)",
		Args:  cobra.ExactArgs(2),
		Example: `  nightgauge issue sync-labels 103 in-progress
  nightgauge issue sync-labels 103 done`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			statusLabel := args[1]

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)
			if err := svc.SyncStatusLabel(cmd.Context(), ownerPart, repoPart, number, statusLabel); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"issueNumber": number,
					"status":      statusLabel,
					"result":      "synced",
				})
			}

			fmt.Printf("Synced #%d labels → status:%s\n", number, statusLabel)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueCreateSubCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		title         string
		body          string
		labels        []string
		projectNumber int
		blockedBy     string
		dependsOn     string
		wave          int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "create-sub [parent-number]",
		Short: "Create and link a sub-issue under a parent (adds to project board)",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge issue create-sub 1543 --title "New sub-issue" --body "Details"
  nightgauge issue create-sub 295 --title "Sub-task" --labels type:feature --project 3
  nightgauge issue create-sub 295 --title "Sub-task" --blocked-by 280,290
  nightgauge issue create-sub 295 --title "Sub-task" --wave 2 --depends-on 280,290`,
		RunE: func(cmd *cobra.Command, args []string) error {
			parentNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid parent issue number: %s", args[0])
			}
			if title == "" {
				return fmt.Errorf("--title is required")
			}

			// Embed wave annotation in body when --wave is provided
			if wave > 0 {
				waveAnnotation := fmt.Sprintf("(Wave %d)", wave)
				if body != "" {
					body = body + "\n\n" + waveAnnotation
				} else {
					body = waveAnnotation
				}
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)

			var projectSvc *gh.ProjectService
			if projectNumber > 0 {
				projectSvc = gh.NewProjectService(client, ownerPart, projectNumber)
			}

			issue, err := svc.CreateSubIssue(cmd.Context(), ownerPart, repoPart, parentNumber, title, body, labels, projectSvc)
			if err != nil {
				return err
			}

			// Merge --blocked-by and --depends-on into a single blocker list.
			// Both flags create addBlockedBy relationships; --depends-on expresses
			// ordering intent while --blocked-by is the existing mechanical flag.
			var allBlockers []string
			if blockedBy != "" {
				allBlockers = append(allBlockers, strings.Split(blockedBy, ",")...)
			}
			if dependsOn != "" {
				allBlockers = append(allBlockers, strings.Split(dependsOn, ",")...)
			}

			if len(allBlockers) > 0 {
				var blockerErrors []string
				for _, raw := range allBlockers {
					raw = strings.TrimSpace(raw)
					if raw == "" {
						continue
					}
					blockerNumber, parseErr := strconv.Atoi(raw)
					if parseErr != nil {
						blockerErrors = append(blockerErrors, fmt.Sprintf("%s (invalid number)", raw))
						continue
					}
					blocker, fetchErr := svc.GetIssue(cmd.Context(), ownerPart, repoPart, blockerNumber)
					if fetchErr != nil {
						blockerErrors = append(blockerErrors, fmt.Sprintf("#%d (fetch failed: %v)", blockerNumber, fetchErr))
						continue
					}
					if addErr := svc.AddBlockedBy(cmd.Context(), issue.NodeID, blocker.NodeID); addErr != nil {
						blockerErrors = append(blockerErrors, fmt.Sprintf("#%d (%v)", blockerNumber, addErr))
					}
				}
				if len(blockerErrors) > 0 {
					return fmt.Errorf("issue #%d created and linked, but failed to add some blockers: %s",
						issue.Number, strings.Join(blockerErrors, "; "))
				}
			}

			if outputJSON {
				return printJSON(issue)
			}

			fmt.Printf("Created #%d as sub-issue of #%d: %s\n", issue.Number, parentNumber, issue.Title)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().StringVar(&title, "title", "", "Sub-issue title (required)")
	cmd.Flags().StringVar(&body, "body", "", "Sub-issue body")
	cmd.Flags().StringSliceVar(&labels, "labels", nil, "Label node IDs to apply")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number (skips board sync when 0)")
	cmd.Flags().StringVar(&blockedBy, "blocked-by", "", "Comma-separated blocker issue numbers (e.g. 280,290). Body text 'Blocked by #N' is not parsed.")
	cmd.Flags().StringVar(&dependsOn, "depends-on", "", "Comma-separated blocker issue numbers (semantic alias for --blocked-by; creates addBlockedBy relationships)")
	cmd.Flags().IntVar(&wave, "wave", 0, "Wave number for this sub-issue — embeds '(Wave N)' annotation in issue body")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueLinkSubCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "link-sub [parent-number] [child-number]",
		Short: "Link an existing issue as a sub-issue of a parent",
		Args:  cobra.ExactArgs(2),
		Example: `  nightgauge issue link-sub 1543 1550
  nightgauge issue link-sub 295 300`,
		RunE: func(cmd *cobra.Command, args []string) error {
			parentNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid parent issue number: %s", args[0])
			}
			childNumber, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid child issue number: %s", args[1])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)
			if err := svc.LinkSubIssue(cmd.Context(), ownerPart, repoPart, parentNumber, childNumber); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"parent": parentNumber,
					"child":  childNumber,
					"result": "linked",
				})
			}

			fmt.Printf("Linked #%d as sub-issue of #%d\n", childNumber, parentNumber)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueAddBlockedByCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "add-blocked-by [blocked-number] [blocker-number]",
		Short: "Add a blocking relationship (blocker blocks blocked)",
		Long: `Set a native GitHub blocking relationship between two issues.
The first argument is the issue that becomes blocked; the second is the blocker.
Uses issue numbers — node ID resolution is handled internally.`,
		Args: cobra.ExactArgs(2),
		Example: `  nightgauge issue add-blocked-by 171 170   # 171 is blocked by 170
  nightgauge issue add-blocked-by 42 41 --repo nightgauge/acmeweb`,
		RunE: func(cmd *cobra.Command, args []string) error {
			blockedNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid blocked issue number: %s", args[0])
			}
			blockerNumber, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid blocker issue number: %s", args[1])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)
			if err := svc.AddBlockedByNumber(cmd.Context(), ownerPart, repoPart, blockedNumber, blockerNumber); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"blocked": blockedNumber,
					"blocker": blockerNumber,
					"result":  "blocked",
				})
			}

			fmt.Printf("Set #%d blocked by #%d\n", blockedNumber, blockerNumber)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueRemoveBlockedByCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "remove-blocked-by [blocked-number] [blocker-number]",
		Short: "Remove a blocking relationship",
		Args:  cobra.ExactArgs(2),
		Example: `  nightgauge issue remove-blocked-by 171 170
  nightgauge issue remove-blocked-by 42 41 --repo nightgauge/acmeweb`,
		RunE: func(cmd *cobra.Command, args []string) error {
			blockedNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid blocked issue number: %s", args[0])
			}
			blockerNumber, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid blocker issue number: %s", args[1])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)
			if err := svc.RemoveBlockedByNumber(cmd.Context(), ownerPart, repoPart, blockedNumber, blockerNumber); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"blocked": blockedNumber,
					"blocker": blockerNumber,
					"result":  "unblocked",
				})
			}

			fmt.Printf("Removed #%d blocked by #%d\n", blockedNumber, blockerNumber)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueListUnrefinedCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		limit      int
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "list-unrefined",
		Short: "List open issues without the pipeline:refined label",
		Example: `  nightgauge issue list-unrefined --json
  nightgauge issue list-unrefined --limit 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)
			excludeLabels := []string{gh.LabelRefined, gh.LabelEpic}
			issues, err := svc.ListIssuesExcludingLabels(cmd.Context(), ownerPart, repoPart, excludeLabels, limit)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"issues": issues,
					"count":  len(issues),
				})
			}

			for _, issue := range issues {
				fmt.Printf("#%-5d %s\n", issue.Number, issue.Title)
			}
			fmt.Printf("\nTotal: %d unrefined issues\n", len(issues))
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name")
	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum number of issues to return (0 = no limit)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueMarkRefinedCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "mark-refined [number]",
		Short: "Add the pipeline:refined label to an issue",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge issue mark-refined 42
  nightgauge issue mark-refined 42 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)
			if err := svc.MarkRefined(cmd.Context(), ownerPart, repoPart, number); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"number": number,
					"label":  gh.LabelRefined,
					"status": "labeled",
				})
			}

			fmt.Printf("Marked #%d as refined (%s)\n", number, gh.LabelRefined)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func issueHasLabelCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "has-label [number] [label]",
		Short: "Check if an issue has a specific label",
		Args:  cobra.ExactArgs(2),
		Example: `  nightgauge issue has-label 42 pipeline:refined
  nightgauge issue has-label 42 type:epic --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			label := args[1]

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)
			hasLabel, err := svc.HasLabel(cmd.Context(), ownerPart, repoPart, number, label)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"number":    number,
					"label":     label,
					"has_label": hasLabel,
				})
			}

			if hasLabel {
				fmt.Printf("Issue #%d has label: %s\n", number, label)
			} else {
				fmt.Printf("Issue #%d does not have label: %s\n", number, label)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// issueRouteCmd derives pipeline routing decisions for an issue (label/board
// fields → change_type, task_type, complexity_score, suggested_route,
// skip_stages, foundation_task, documentation_scope). Wraps the pure
// `routing.Derive` function with a thin online lookup. When `--size`,
// `--priority`, and `--type` are all provided and the issue number is 0,
// runs entirely offline — useful for plumbing tests and CI.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B4.
func issueRouteCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
		sizeFlag      string
		priorityFlag  string
		typeFlag      string
		labelsFlag    string
	)

	cmd := &cobra.Command{
		Use:   "route [number]",
		Short: "Derive pipeline routing decision (change_type, complexity, route, skip_stages)",
		Long: `Derives the pipeline routing Decision for an issue from its labels and
project-board fields. Mirrors the canonical algorithm previously duplicated
in shell across issue-pickup Step 3.2.5 and feature-planning Phase 2.

Online mode (default): fetches the issue via GitHub and the matching board
item for size/priority. Offline mode: pass --size, --priority, --type with
issue number 0 — useful for tests.`,
		Args: cobra.ExactArgs(1),
		Example: `  nightgauge issue route 3062 --json
  nightgauge issue route 0 --size M --priority P1 --type feature --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			input := routing.DeriveInput{}

			if number > 0 {
				client, err := clientFromConfig()
				if err != nil {
					return err
				}
				ownerPart, repoPart := splitRepo(owner, repo)

				issue, err := gh.NewIssueService(client).GetIssue(cmd.Context(), ownerPart, repoPart, number)
				if err != nil {
					return fmt.Errorf("get issue #%d: %w", number, enrichError(err))
				}
				input.Title = issue.Title
				input.Body = issue.Body
				input.Labels = issue.Labels

				// Look up board size/priority by listing items and matching number.
				// Skipped silently when --size/--priority overrides are present, or
				// when the project number cannot be resolved (offline orgs).
				if sizeFlag == "" || priorityFlag == "" {
					if items, ferr := gh.NewBoardService(client, ownerPart, projectNumber, getOwnerType(cmd)).ListItems(cmd.Context(), ""); ferr == nil {
						for _, item := range items {
							if item.Number == number {
								if input.BoardSize == "" {
									input.BoardSize = string(item.Size)
								}
								if input.BoardPriority == "" {
									input.BoardPriority = string(item.Priority)
								}
								break
							}
						}
					}
				}
			} else if sizeFlag == "" || priorityFlag == "" || typeFlag == "" {
				return fmt.Errorf("offline mode (issue number 0) requires --size, --priority, and --type")
			}

			// CLI overrides win over board fetches.
			if sizeFlag != "" {
				input.BoardSize = sizeFlag
			}
			if priorityFlag != "" {
				input.BoardPriority = priorityFlag
			}
			if typeFlag != "" {
				input.Labels = upsertTypeLabel(input.Labels, typeFlag)
			}
			// --labels augments the label set (e.g. component:security) so the
			// risk classifier (#4093) is exercisable offline and online.
			for _, l := range strings.Split(labelsFlag, ",") {
				if t := strings.TrimSpace(l); t != "" {
					input.Labels = append(input.Labels, t)
				}
			}

			// Layer routing config (force_full_pipeline + change_rules) so the CLI
			// honors the same fast-track customization the scheduler will (#4125).
			// Best-effort: a missing/unreadable config leaves the built-in
			// DefaultChangeRules() in force.
			if workdir, werr := os.Getwd(); werr == nil {
				if cfg, cerr := config.Load(workdir); cerr == nil && cfg != nil && cfg.Routing != nil {
					input.ForceFullPipeline = cfg.Routing.ForceFullPipeline
					input.ChangeRules = cfg.Routing.ChangeRules
				}
			}

			decision := routing.Derive(input)

			if outputJSON {
				return printJSON(decision)
			}

			fmt.Printf("→ %s path, complexity %d, task %s, change %s, skip: %s\n",
				decision.SuggestedRoute,
				decision.ComplexityScore,
				decision.TaskType,
				decision.ChangeType,
				skipStagesLabel(decision.SkipStages),
			)
			if decision.RiskHigh {
				fmt.Printf("  ⚠ high-risk (forced full pipeline + extensive): %s\n",
					strings.Join(decision.RiskReasons, ", "))
			}
			if decision.MatchedChangeRule != "" {
				fmt.Printf("  ↳ change_rule: %s (fast-tracked)\n", decision.MatchedChangeRule)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&sizeFlag, "size", "", "Override size (XS|S|M|L|XL)")
	cmd.Flags().StringVar(&priorityFlag, "priority", "", "Override priority (P0|P1|P2|P3)")
	cmd.Flags().StringVar(&typeFlag, "type", "", "Override type (feature|bug|docs|refactor|chore|verification|spike)")
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "Comma-separated extra labels for risk classification (e.g. component:security,migration)")
	return cmd
}

// issueInferTypeCmd derives a `type:*` label classification for an issue
// using the canonical keyword rules consolidated in `typeinfer.Infer`.
// Mirrors the routing/has-label online-by-default + offline-overrides
// pattern: when issue number > 0 the issue is fetched via GitHub; when 0
// the caller passes --title/--body/--labels for offline plumbing tests.
//
// `--apply` adds the inferred label to the issue. When source == "default"
// the apply step is skipped unless `--apply-default` is also set, mirroring
// the "Confirm before applying" guidance already in the consumer SKILL.md.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B12.
func issueInferTypeCmd() *cobra.Command {
	var (
		owner        string
		repo         string
		outputJSON   bool
		apply        bool
		applyDefault bool
		titleFlag    string
		bodyFlag     string
		labelsFlag   []string
	)

	cmd := &cobra.Command{
		Use:   "infer-type [number]",
		Short: "Infer type:* label from issue title/body/labels (consolidates shell keyword rules)",
		Long: `Classifies an issue as type:bug | type:feature | type:docs | type:refactor |
type:chore using the canonical keyword rules consolidated in
internal/intelligence/typeinfer/. Source priority: existing type:* label >
body keywords > title keywords > default (type:feature).

Online mode (default): fetches the issue via GitHub.
Offline mode (number 0): pass --title, --body, --labels for tests.

--apply adds the inferred label to the issue. When source == "default" the
apply step is skipped unless --apply-default is also passed.`,
		Args: cobra.ExactArgs(1),
		Example: `  nightgauge issue infer-type 3070 --json
  nightgauge issue infer-type 42 --apply --json
  nightgauge issue infer-type 0 --title "fix crash" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			input := typeinfer.InferInput{
				Title:  titleFlag,
				Body:   bodyFlag,
				Labels: labelsFlag,
			}

			var issueNodeID string
			ownerPart, repoPart := splitRepo(owner, repo)
			var svc *gh.IssueService

			if number > 0 {
				client, err := clientFromConfig()
				if err != nil {
					return err
				}
				svc = gh.NewIssueService(client)
				issue, err := svc.GetIssue(cmd.Context(), ownerPart, repoPart, number)
				if err != nil {
					return fmt.Errorf("get issue #%d: %w", number, enrichError(err))
				}
				if input.Title == "" {
					input.Title = issue.Title
				}
				if input.Body == "" {
					input.Body = issue.Body
				}
				if len(input.Labels) == 0 {
					input.Labels = issue.Labels
				}
				issueNodeID = issue.NodeID
			} else if input.Title == "" && input.Body == "" && len(input.Labels) == 0 {
				return fmt.Errorf("offline mode (issue number 0) requires at least one of --title, --body, --labels")
			}

			result := typeinfer.Infer(input)

			applied := false
			var applyErr error
			if apply {
				switch {
				case result.Source == typeinfer.SourceLabel:
					// Already labeled — nothing to do, idempotent success.
					applied = true
				case result.Source == typeinfer.SourceDefault && !applyDefault:
					// Skip silently — caller did not opt in to defaults.
					applied = false
				case number <= 0:
					applyErr = fmt.Errorf("--apply requires a real issue number (got %d)", number)
				default:
					if svc == nil {
						client, err := clientFromConfig()
						if err != nil {
							applyErr = err
							break
						}
						svc = gh.NewIssueService(client)
					}
					repoLabels, err := svc.GetRepoLabels(cmd.Context(), ownerPart, repoPart)
					if err != nil {
						applyErr = fmt.Errorf("fetch repo labels: %w", err)
						break
					}
					labelID, ok := repoLabels[result.Type]
					if !ok {
						applyErr = fmt.Errorf("label %q not found in repo %s/%s", result.Type, ownerPart, repoPart)
						break
					}
					if err := svc.AddLabels(cmd.Context(), issueNodeID, []string{labelID}); err != nil {
						applyErr = fmt.Errorf("add label %q: %w", result.Type, err)
						break
					}
					applied = true
				}
			}

			if outputJSON {
				out := map[string]interface{}{
					"number":  number,
					"type":    result.Type,
					"source":  result.Source,
					"applied": applied,
				}
				if applyErr != nil {
					out["apply_error"] = applyErr.Error()
				}
				if err := printJSON(out); err != nil {
					return err
				}
				return applyErr
			}

			if applyErr != nil {
				return applyErr
			}

			fmt.Printf("→ %s (source: %s, applied: %v)\n", result.Type, result.Source, applied)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&apply, "apply", false, "Add the inferred label to the issue")
	cmd.Flags().BoolVar(&applyDefault, "apply-default", false, "When --apply is set, also apply when source == default (otherwise skipped)")
	cmd.Flags().StringVar(&titleFlag, "title", "", "Override issue title (offline mode)")
	cmd.Flags().StringVar(&bodyFlag, "body", "", "Override issue body (offline mode)")
	cmd.Flags().StringSliceVar(&labelsFlag, "labels", nil, "Override issue labels (comma-separated, offline mode)")
	return cmd
}

// issueAcCheckCmd parses Markdown checkboxes from an issue body and emits a
// stable JSON verdict suitable for gating feature-validate Phase 0.6.
// Mirrors the issueInferTypeCmd shape: online-by-default + offline-via-flags
// using issue number 0 as the offline sentinel.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B14.
func issueAcCheckCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
		bodyFlag   string
	)

	cmd := &cobra.Command{
		Use:   "ac-check [number]",
		Short: "Parse Markdown acceptance-criteria checkboxes from an issue body",
		Long: `Counts top-level Markdown task-list items (- [ ] / - [x]) in an issue
body and returns a deterministic verdict suitable for gating
feature-validate Phase 0.6 type:docs completion.

Status enum: passed | failed | not_applicable. The verb itself never exits
non-zero on a successful parse — gating is the caller's job (parity with
issue infer-type).

Online mode (default): fetches the issue body via GitHub.
Offline mode (number 0): pass --body for tests and CI plumbing checks.

Lines inside fenced code blocks (` + "```" + ` or ~~~) are ignored, matching the
fence-toggle approach in internal/docs/checklinks.go.`,
		Args: cobra.ExactArgs(1),
		Example: `  nightgauge issue ac-check 3072 --json
  nightgauge issue ac-check 0 --body "- [x] done\n- [ ] todo" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			body := bodyFlag

			if number > 0 {
				client, err := clientFromConfig()
				if err != nil {
					return err
				}
				svc := gh.NewIssueService(client)
				ownerPart, repoPart := splitRepo(owner, repo)
				issue, err := svc.GetIssue(cmd.Context(), ownerPart, repoPart, number)
				if err != nil {
					return fmt.Errorf("get issue #%d: %w", number, enrichError(err))
				}
				if body == "" {
					body = issue.Body
				}
			} else if body == "" {
				return fmt.Errorf("offline mode (issue number 0) requires --body")
			}

			result := acparse.Parse(body)

			if outputJSON {
				return printJSON(map[string]interface{}{
					"v":               result.V,
					"number":          number,
					"status":          result.Status,
					"checked_count":   result.Checked,
					"unchecked_count": result.Unchecked,
					"total":           result.Total,
				})
			}

			fmt.Printf("status=%s checked=%d unchecked=%d total=%d\n",
				result.Status, result.Checked, result.Unchecked, result.Total)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&bodyFlag, "body", "", "Override issue body (offline mode, or skip GitHub fetch online)")
	return cmd
}

// upsertTypeLabel replaces (or appends) the type:* label so a CLI --type flag
// overrides any pre-existing type label fetched from GitHub.
func upsertTypeLabel(labels []string, typeValue string) []string {
	out := make([]string, 0, len(labels)+1)
	for _, l := range labels {
		if !strings.HasPrefix(strings.ToLower(l), "type:") {
			out = append(out, l)
		}
	}
	return append(out, "type:"+typeValue)
}

// skipStagesLabel renders the skip stages slice for the human one-liner.
func skipStagesLabel(stages []string) string {
	if len(stages) == 0 {
		return "none"
	}
	return strings.Join(stages, ",")
}

// --- epic command ---

// sweepRateLimitMin is the minimum GraphQL rate-limit headroom required to
// start an epic-sweep cycle. A sweep can make 10+ calls per open epic (list
// sub-issues, fetch labels, read board status), so skipping below this
// threshold leaves room for the rest of the session's inline pipeline calls.
const sweepRateLimitMin = 20

func epicCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "epic",
		Short: "Epic operations (completion, summary, sync)",
	}
	cmd.AddCommand(
		epicCheckCompletionCmd(),
		epicCheckLifecycleCmd(),
		epicCompleteCmd(),
		epicSyncClosedToDoneCmd(),
		epicTransitionStatusCmd(),
		epicSummaryCmd(),
		epicSummaryTierCmd(),
		epicAutoCloseCmd(),
		epicCreateBranchCmd(),
		epicValidateCmd(),
		epicAssessCmd(),
		epicPlanWavesCmd(),
	)
	return cmd
}

func epicValidateCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "validate [epic-number]",
		Short:        "Validate epic structure: circular blockers and stale blockers",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  nightgauge epic validate 3053
  nightgauge epic validate 3053 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			epicSvc := gh.NewEpicService(client)

			result, err := epicSvc.Validate(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			if result.Valid {
				fmt.Printf("Epic #%d: valid — no structural gaps found (%d sub-issues checked)\n",
					result.EpicNumber, result.TotalSubIssues)
			} else {
				fmt.Printf("Epic #%d: %d gap(s) found\n", result.EpicNumber, len(result.Gaps))
				for _, g := range result.Gaps {
					fmt.Printf("  [%s] #%d %s — %s\n", g.GapType, g.SubIssueNumber, g.SubIssueTitle, g.Detail)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicAssessCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "assess [epic-number]",
		Short:        "Assess epic sub-issues for batch vs sequential strategy",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  nightgauge epic assess 3053
  nightgauge epic assess 3053 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			epicNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			issueSvc := gh.NewIssueService(client)

			epic, err := issueSvc.GetIssue(cmd.Context(), ownerPart, repoPart, epicNumber)
			if err != nil {
				return fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
			}

			inputs := make([]batch.IssueInput, 0, len(epic.SubIssues))
			for _, ref := range epic.SubIssues {
				if ref.State != "OPEN" {
					continue
				}
				si, err := issueSvc.GetIssue(cmd.Context(), ownerPart, repoPart, ref.Number)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: skip #%d: %v\n", ref.Number, err)
					continue
				}
				blockedBy := make([]int, 0, len(si.BlockedBy))
				for _, b := range si.BlockedBy {
					blockedBy = append(blockedBy, b.Number)
				}
				inputs = append(inputs, batch.IssueInput{
					Number:    si.Number,
					Title:     si.Title,
					Body:      si.Body,
					Labels:    si.Labels,
					BlockedBy: blockedBy,
				})
			}

			result := batch.NewAssessor().Assess(inputs)

			if outputJSON {
				return printJSON(result)
			}

			fmt.Printf("Epic #%d Assessment\n", epicNumber)
			fmt.Printf("Strategy:  %s\n", result.Strategy)
			fmt.Printf("Reasoning: %s\n", result.Reasoning)
			fmt.Printf("Est. Cost: $%.2f\n", result.EstimatedCostUSD)
			fmt.Printf("Est. Time: %.0f min\n", result.EstimatedMinutes)
			fmt.Printf("Issues:    %d open\n", len(inputs))
			for _, ie := range result.IssueAssessments {
				dep := ""
				if ie.HasDependencies {
					dep = " [blocked]"
				}
				fmt.Printf("  #%d complexity=%d model=%s%s\n",
					ie.IssueNumber, ie.ComplexityScore, ie.RecommendedModel, dep)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicCheckCompletionCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		sweep      bool
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "check-completion [epic-number]",
		Short: "Check if all sub-issues of an epic are closed",
		Args:  cobra.MaximumNArgs(1),
		// Rate-limit errors and other runtime failures are not usage errors,
		// so suppress cobra's help dump on RunE returns. See issue #2839.
		SilenceUsage: true,
		Example: `  nightgauge epic check-completion 1543
  nightgauge epic check-completion --sweep`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			epicSvc := gh.NewEpicService(client)

			if sweep {
				// Precheck rate limit before a multi-call sweep. The rateLimit
				// GraphQL query is free (doesn't consume quota), so we can safely
				// call it even when the primary limit is exhausted. Skipping
				// here prints a clean info line and exits 0 — the sweep is a
				// safety-net; the per-issue completion check already runs inline.
				if info, rlErr := client.GetRateLimit(cmd.Context()); rlErr == nil {
					if info.Remaining < sweepRateLimitMin {
						resetAt := time.Unix(info.ResetAt, 0).UTC().Format(time.RFC3339)
						if outputJSON {
							return printJSON(map[string]interface{}{
								"skipped":       true,
								"reason":        "rate_limit_low",
								"remaining":     info.Remaining,
								"resetAt":       resetAt,
								"epics_checked": 0,
							})
						}
						fmt.Fprintf(os.Stderr,
							"epic sweep skipped: GitHub rate limit low (%d remaining, resets %s)\n",
							info.Remaining, resetAt,
						)
						return nil
					}
				}

				results, err := epicSvc.SweepEpics(cmd.Context(), ownerPart, repoPart)
				if err != nil {
					return err
				}

				if outputJSON {
					return printJSON(results)
				}

				for _, r := range results {
					status := "incomplete"
					if r.Complete {
						status = "COMPLETE"
					}
					fmt.Printf("#%-5d %-50s %d/%d [%s]\n", r.EpicNumber, r.Title, r.Closed, r.Total, status)
				}
				fmt.Printf("\nTotal: %d epics\n", len(results))
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("provide an epic number or use --sweep")
			}

			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}

			result, err := epicSvc.CheckCompletion(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			if result.Complete {
				fmt.Printf("Epic #%d is COMPLETE — all %d sub-issues closed.\n", result.EpicNumber, result.Total)
			} else {
				fmt.Printf("Epic #%d: %d/%d complete (%d remaining)\n", result.EpicNumber, result.Closed, result.Total, result.Open)
				for _, oi := range result.OpenIssues {
					fmt.Printf("  [ ] #%d %s\n", oi.Number, oi.Title)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&sweep, "sweep", false, "Check all open epics")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicPlanWavesCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		subIssues  string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "plan-waves",
		Short:        "Group sub-issues into parallel execution waves based on blockedBy relationships",
		SilenceUsage: true,
		Example: `  nightgauge epic plan-waves --sub-issues 3082,3083,3084 --json
  nightgauge epic plan-waves --sub-issues 3082,3083,3084`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if subIssues == "" {
				return fmt.Errorf("--sub-issues is required (comma-separated issue numbers)")
			}

			var issueNumbers []int
			for _, raw := range strings.Split(subIssues, ",") {
				raw = strings.TrimSpace(raw)
				if raw == "" {
					continue
				}
				n, err := strconv.Atoi(raw)
				if err != nil {
					return fmt.Errorf("invalid issue number %q in --sub-issues", raw)
				}
				issueNumbers = append(issueNumbers, n)
			}
			if len(issueNumbers) == 0 {
				return fmt.Errorf("--sub-issues must contain at least one issue number")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			result, err := gh.NewEpicService(client).PlanWaves(cmd.Context(), ownerPart, repoPart, issueNumbers)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			if len(result.Waves) == 0 {
				fmt.Printf("No waves computed for %d sub-issues.\n", result.SubIssueCount)
				return nil
			}
			for _, w := range result.Waves {
				parallel := "run in parallel"
				if len(w.Issues) == 1 {
					parallel = "no dependencies"
				}
				fmt.Printf("Wave %d (%d issue(s) — %s):\n", w.WaveIndex, len(w.Issues), parallel)
				for _, issue := range w.Issues {
					fmt.Printf("  #%d %s\n", issue.Number, issue.Title)
				}
			}
			// Surface deterministic file-overlap serializations so the
			// author applies the injected blockedBy edges (Phase 3.5). Each
			// error conflict pairs (earlier, later) by issue number; the
			// later issue was serialized after the earlier one.
			for _, c := range result.Conflicts {
				if c.Severity != "error" || len(c.Issues) < 2 {
					continue
				}
				earlier, later := c.Issues[0], c.Issues[1]
				fmt.Printf("Serialized #%d after #%d — shared target file %s\n", later, earlier, c.Path)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().StringVar(&subIssues, "sub-issues", "", "Comma-separated issue numbers to plan waves for (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicCheckLifecycleCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		sweep         bool
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:          "check-lifecycle [epic-number]",
		Short:        "Detect stale epics, board drift, orphaned issues, and stale blockers",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		Example: `  nightgauge epic check-lifecycle 1650
  nightgauge epic check-lifecycle 1650 --json
  nightgauge epic check-lifecycle --sweep --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewLifecycleAuditService(client, ownerPart, projectNumber)

			if !sweep && len(args) == 0 {
				return fmt.Errorf("provide an epic number or use --sweep")
			}

			result, err := svc.RunAudit(cmd.Context(), ownerPart, repoPart, false)
			if err != nil {
				return err
			}

			if !sweep {
				epicNumber, err := strconv.Atoi(args[0])
				if err != nil {
					return fmt.Errorf("invalid epic number: %s", args[0])
				}
				result = result.FilterByEpicNumber(epicNumber)
			}

			if outputJSON {
				return printJSON(result)
			}

			fmt.Printf("Lifecycle Check: %s — %d findings\n", result.Repo, result.Summary.Total)
			for _, f := range result.Findings {
				fmt.Printf("  [%s] #%d %s: %s\n", f.Category, f.IssueNumber, f.IssueTitle, f.Detail)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 5, "Project board number")
	cmd.Flags().BoolVar(&sweep, "sweep", false, "Check all open epics and issues")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicCompleteCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "complete <epic-number>",
		Short: "Complete an epic: check, close, create PR, merge, cleanup",
		Long: `Run the full epic completion flow:
1. Check if all sub-issues are closed
2. Close the epic issue
3. Create epic→main PR (MERGE strategy to preserve commit history)
4. Merge the PR
5. Cleanup epic branch (local + remote)

If sub-issues remain open, reports progress and exits without action.`,
		Args:    cobra.ExactArgs(1),
		Example: "  nightgauge epic complete 1650 --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			epicSvc := gh.NewEpicService(client)

			// Determine repo path for git operations
			repoPath, _ := os.Getwd()

			result, err := epicSvc.CompleteEpic(cmd.Context(), ownerPart, repoPart, number, repoPath)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			switch result.Action {
			case "not_complete":
				fmt.Printf("Epic #%d: %d/%d complete (%d remaining)\n", result.EpicNumber, result.Closed, result.Total, result.Open)
				for _, oi := range result.OpenIssues {
					fmt.Printf("  [ ] #%d %s\n", oi.Number, oi.Title)
				}
			case "closed_and_merged":
				fmt.Printf("Epic #%d completed — PR %s merged, branches cleaned up.\n", result.EpicNumber, result.PRURL)
			case "closed_pr_created":
				fmt.Printf("Epic #%d closed — PR %s created (manual merge required).\n", result.EpicNumber, result.PRURL)
				if result.Error != "" {
					fmt.Printf("  Warning: %s\n", result.Error)
				}
			case "already_merged":
				fmt.Printf("Epic #%d already merged — %s\n", result.EpicNumber, result.PRURL)
			case "no_epic_branch":
				fmt.Printf("Epic #%d closed but no epic branch found.\n", result.EpicNumber)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicSyncClosedToDoneCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:     "sync-closed-to-done [epic-number]",
		Short:   "Move closed sub-issues to Done on the project board",
		Args:    cobra.ExactArgs(1),
		Example: `  nightgauge epic sync-closed-to-done 1543 --project 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			epicSvc := gh.NewEpicService(client)
			synced, err := epicSvc.SyncClosedToDone(cmd.Context(), ownerPart, repoPart, number, projectNumber)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"epicNumber": number,
					"synced":     synced,
				})
			}

			fmt.Printf("Synced %d closed sub-issues of epic #%d to Done\n", synced, number)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicTransitionStatusCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "transition-status [epic-number] [new-status]",
		Short: "Move an epic and all sub-issues to a new status on the project board",
		Args:  cobra.ExactArgs(2),
		Example: `  nightgauge epic transition-status 1452 Ready --project 1
  nightgauge epic transition-status 1452 Backlog --project 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}
			newStatus := args[1]

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			epicSvc := gh.NewEpicService(client)
			result, err := epicSvc.TransitionStatus(cmd.Context(), ownerPart, repoPart, number, projectNumber, newStatus)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			fmt.Printf("Epic #%d → %s\n", result.EpicNumber, result.NewStatus)
			fmt.Printf("  Epic synced: %v\n", result.EpicSynced)
			fmt.Printf("  Sub-issues moved: %d/%d\n", result.SubIssueMoved, result.SubIssueTotal)
			if len(result.Failures) > 0 {
				fmt.Printf("  Failures:\n")
				for _, f := range result.Failures {
					fmt.Printf("    #%d: %s\n", f.Number, f.Error)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicSummaryCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "summary [epic-number]",
		Short: "Generate epic completion summary",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge epic summary 1543
  nightgauge epic summary 1543 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			epicSvc := gh.NewEpicService(client)
			summary, err := epicSvc.GenerateSummary(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(summary)
			}

			fmt.Println(summary.Summary)
			fmt.Printf("Tier: %s\n", summary.Tier)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicSummaryTierCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:     "summary-tier [epic-number]",
		Short:   "Classify summary verbosity tier for an epic",
		Args:    cobra.ExactArgs(1),
		Example: `  nightgauge epic summary-tier 1543`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			epicSvc := gh.NewEpicService(client)
			summary, err := epicSvc.GenerateSummary(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"epicNumber": number,
					"tier":       summary.Tier,
					"total":      summary.Total,
					"progress":   summary.Progress,
				})
			}

			fmt.Printf("Epic #%d tier: %s (total: %d, progress: %.0f%%)\n",
				number, summary.Tier, summary.Total, summary.Progress*100)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func epicAutoCloseCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "auto-close",
		Short: "Automatically close completed epics and move to Done",
		Long: `Check all open epics in a repo. For each epic:
- If ALL sub-issues are closed: close the epic with automated comment, move to Done
- If some sub-issues are open: skip
- If no sub-issues: skip

Returns a summary with checked, closed, and skipped counts.`,
		Example: `  nightgauge epic auto-close --project 5
  nightgauge epic auto-close --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			epicSvc := gh.NewEpicService(client)

			result, err := epicSvc.AutoClose(cmd.Context(), ownerPart, repoPart, projectNumber)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			fmt.Printf("Epic Auto-Close Results\n")
			fmt.Printf("Checked: %d | Closed: %d | Skipped: %d\n",
				result.Checked, result.Closed, result.Skipped)

			if len(result.Summary) > 0 {
				fmt.Println("\nSummary:")
				for _, item := range result.Summary {
					icon := "✓"
					if item.Status == "skipped" {
						icon = "⊘"
					} else if item.Status == "error" {
						icon = "✗"
					}

					fmt.Printf("%s #%-5d %-40s %s", icon, item.EpicNumber, item.Title, item.Status)
					if item.Reason != "" {
						fmt.Printf(" (%s)", item.Reason)
					}
					if item.Error != "" {
						fmt.Printf(" [%s]", item.Error)
					}
					fmt.Println()
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")

	return cmd
}

func epicCreateBranchCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "create-branch <epic-number>",
		Short: "Create epic branch from main if it does not already exist",
		Long: `Create the epic/<number>-<slug> branch on the remote if it does not yet exist.
The branch is created from the repository default branch (main/master).
This is idempotent: if the epic branch already exists, the command exits successfully.`,
		Example: `  nightgauge epic create-branch 2650
  nightgauge epic create-branch 2650 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			epicNumber, err := strconv.Atoi(args[0])
			if err != nil || epicNumber <= 0 {
				return fmt.Errorf("epic-number must be a positive integer, got %q", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			issueSvc := gh.NewIssueService(client)
			epicIssue, err := issueSvc.GetIssue(cmd.Context(), ownerPart, repoPart, epicNumber)
			if err != nil {
				return fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
			}

			gitSvc, err := openGitService()
			if err != nil {
				return fmt.Errorf("open git service: %w", err)
			}

			branchName, created, err := gitSvc.EnsureEpicBranch(epicNumber, epicIssue.Title)
			if err != nil {
				return fmt.Errorf("ensure epic branch: %w", err)
			}

			type result struct {
				Branch  string `json:"branch"`
				Created bool   `json:"created"`
			}
			res := result{Branch: branchName, Created: created}

			if outputJSON {
				return printJSON(res)
			}

			if created {
				fmt.Printf("Created epic branch: %s\n", branchName)
			} else {
				fmt.Printf("Epic branch already exists: %s\n", branchName)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// --- project command ---

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Project board operations (add, sync-status, sync-iteration, set-hours, set-estimate, set-field, drift-check, reconcile, view-list, view-create, ensure-fields)",
	}
	cmd.AddCommand(
		projectAddCmd(),
		projectSyncStatusCmd(),
		projectSyncIterationCmd(),
		projectSetHoursCmd(),
		projectSetEstimateCmd(),
		projectSetFieldCmd(),
		projectUpdateEstimatesCmd(),
		projectMoveStatusCmd(),
		projectDriftCheckCmd(),
		projectReconcileCmd(),
		projectViewListCmd(),
		projectViewCreateCmd(),
		projectResolveCmd(),
		projectEnsureFieldsCmd(),
	)
	return cmd
}

// validateStatusFlag normalizes lowercase status aliases to canonical board
// names and validates against the canonical Status enum. Returns the resolved
// canonical name, or an error listing valid values.
//
// The canonical enum mirrors the Status field options on the project board:
// Backlog, Ready, In progress, In review, Done. Lowercase aliases (ready,
// in-progress, in-review, done, backlog) are accepted and normalized.
//
// An empty input returns ("", nil) — callers treat that as "flag omitted".
func validateStatusFlag(value string) (string, error) {
	if value == "" {
		return "", nil
	}

	aliases := map[string]string{
		"backlog":     "Backlog",
		"ready":       "Ready",
		"in-progress": "In progress",
		"in-review":   "In review",
		"done":        "Done",
	}
	canonical := map[string]bool{
		"Backlog":     true,
		"Ready":       true,
		"In progress": true,
		"In review":   true,
		"Done":        true,
	}

	resolved := value
	if mapped, ok := aliases[value]; ok {
		resolved = mapped
	}
	if !canonical[resolved] {
		return "", fmt.Errorf("invalid --status %q (valid: Backlog, Ready, In progress, In review, Done)", value)
	}
	return resolved, nil
}

func projectAddCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		status        string
		outputJSON    bool
		bulk          bool
		milestone     string
		labels        []string
	)

	cmd := &cobra.Command{
		Use:   "add [issue-number]",
		Short: "Add issue to project board with field mappings",
		Args:  cobra.MaximumNArgs(1),
		Example: `  nightgauge project add 42
  nightgauge project add 42 --status Ready
  nightgauge project add 42 --repo acme/platform
  nightgauge project add --bulk --label type:feature --json
  nightgauge project add --bulk --milestone "Sprint 1" --label type:bug`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags before any network calls so tests can run offline.
			if bulk && len(args) > 0 {
				return fmt.Errorf("--bulk and a positional issue number are mutually exclusive")
			}
			if !bulk {
				if _, err := validateStatusFlag(status); err != nil {
					return err
				}
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}
			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)

			if bulk {
				issueSvc := gh.NewIssueService(client)
				issues, err := issueSvc.ListIssues(cmd.Context(), ownerPart, repoPart, labels)
				if err != nil {
					return fmt.Errorf("list issues: %w", err)
				}

				// Client-side milestone filter (ListIssues doesn't support milestone server-side)
				if milestone != "" {
					filtered := issues[:0]
					for _, iss := range issues {
						if iss.Milestone == milestone {
							filtered = append(filtered, iss)
						}
					}
					issues = filtered
				}

				result := svc.BulkAddIssues(cmd.Context(), ownerPart, repoPart, issues)

				if outputJSON {
					return printJSON(result)
				}

				fmt.Printf("Bulk add: %d total, %d added, %d skipped, %d failed\n",
					result.Total, result.Added, result.Skipped, result.Failed)
				for _, e := range result.Errors {
					fmt.Printf("  error: %s\n", e)
				}
				if result.Failed > 0 {
					return fmt.Errorf("%d issue(s) failed to add", result.Failed)
				}
				return nil
			}

			// Single-issue mode
			if len(args) == 0 {
				return fmt.Errorf("either provide an issue number or use --bulk")
			}
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			// Status already validated above (before client init).
			resolvedStatus, _ := validateStatusFlag(status)

			itemID, err := svc.AddIssueByNumber(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			if resolvedStatus != "" {
				if err := svc.SetFields(cmd.Context(), ownerPart, repoPart, number, map[string]string{"Status": resolvedStatus}); err != nil {
					return fmt.Errorf("set status %s on #%d: %w", resolvedStatus, number, err)
				}
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"itemId":      itemID,
					"issueNumber": number,
					"repo":        ownerPart + "/" + repoPart,
					"result":      "added",
					"status":      resolvedStatus,
				})
			}

			if resolvedStatus != "" {
				fmt.Printf("Added #%d to project board (item: %s, status: %s)\n", number, itemID, resolvedStatus)
			} else {
				fmt.Printf("Added #%d to project board (item: %s)\n", number, itemID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().StringVar(&status, "status", "", "Set Status field after add (Backlog, Ready, In progress, In review, Done). Atomic: non-zero exit if status assignment fails.")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&bulk, "bulk", false, "Add all open issues matching filters (mutually exclusive with positional issue-number)")
	cmd.Flags().StringVar(&milestone, "milestone", "", "Filter issues by milestone title (bulk mode only)")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "Filter issues by label (repeatable, bulk mode only)")
	return cmd
}

func projectSyncStatusCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "sync-status [issue-number] [status]",
		Short: "Update Status field via GraphQL",
		Long: `Update the Status field for an issue on the project board.

Valid statuses: ready, in-progress, in-review, done, blocked, needs-info`,
		Args: cobra.ExactArgs(2),
		Example: `  nightgauge project sync-status 103 in-progress
  nightgauge project sync-status 103 done --repo nightgauge/other-repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			status := args[1]

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)
			if err := svc.SyncStatus(cmd.Context(), ownerPart, repoPart, number, status); err != nil {
				return fmt.Errorf("sync-status #%d in %s/%s: %w", number, ownerPart, repoPart, enrichError(err))
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"issueNumber": number,
					"status":      status,
					"result":      "synced",
				})
			}

			fmt.Printf("Synced #%d status → %s\n", number, status)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// projectReconcileCmd is the board-wide backstop sweep that repairs post-merge
// state drift across the epic ↔ sub-issue ↔ board-Status triad (#3979/#3980/#3981).
func projectReconcileCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile post-merge state drift across the board (epic↔sub↔Status)",
		Long: `Sweep every item on the project board and repair post-merge state drift:

  - closed issues whose board Status is stale are synced to Done (#3981);
  - open epics whose sub-issues are all closed are auto-closed (#3980);
  - epics closed as completed that left sub-issues open have those orphaned
    subs closed so the autonomous picker does not re-spawn conflicting work (#3979).

Every action is idempotent — safe to run repeatedly and on a schedule. This is
the board-wide backstop for the post-merge hook, which already handles the
common case at merge time.`,
		Example: `  nightgauge project reconcile --project 6
  nightgauge project reconcile --project 6 --owner nightgauge --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			epicSvc := gh.NewEpicService(client)
			res, err := epicSvc.ReconcileBoard(cmd.Context(), owner, projectNumber, getOwnerType(cmd))
			if err != nil {
				return fmt.Errorf("reconcile board %s/%d: %w", owner, projectNumber, enrichError(err))
			}

			if outputJSON {
				return printJSON(res)
			}

			fmt.Printf("Reconciled %d board items: %d synced to Done, %d epic(s) auto-closed, %d orphan sub(s) closed.\n",
				res.Checked, res.IssuesSyncedToDone, res.EpicsClosed, res.OrphanSubsClosed)
			for _, a := range res.Actions {
				switch a.Kind {
				case "issue_done":
					fmt.Printf("  Status→Done   #%-5d %-28s (was %q)\n", a.Number, a.Repo, a.Detail)
				case "epic_closed":
					fmt.Printf("  epic closed   #%-5d %s\n", a.Number, a.Repo)
				case "orphan_sub_closed":
					fmt.Printf("  orphan closed #%-5d %-28s (epic #%d)\n", a.Number, a.Repo, a.Epic)
				case "orphan_sub_flagged":
					fmt.Printf("  FLAGGED epic  #%-5d %-28s %s\n", a.Number, a.Repo, a.Detail)
				}
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization (project owner)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("project") // cobra MarkFlagRequired never errors for known flags
	return cmd
}

func projectSyncIterationCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "sync-iteration [issue-number] [iteration]",
		Short: "Update Iteration field",
		Args:  cobra.ExactArgs(2),
		Example: `  nightgauge project sync-iteration 103 "Sprint 5"
  nightgauge project sync-iteration 103 "2026-Q1"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			iteration := args[1]

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)
			if err := svc.SyncIteration(cmd.Context(), ownerPart, repoPart, number, iteration); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"issueNumber": number,
					"iteration":   iteration,
					"result":      "synced",
				})
			}

			fmt.Printf("Synced #%d iteration → %s\n", number, iteration)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectSetHoursCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "set-hours [issue-number] [hours]",
		Short: "Set Hours estimate field",
		Args:  cobra.ExactArgs(2),
		Example: `  nightgauge project set-hours 295 24.5
  nightgauge project set-hours 295 12`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			hours, err := strconv.ParseFloat(args[1], 64)
			if err != nil {
				return fmt.Errorf("invalid hours value: %s", args[1])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)
			if err := svc.SetHours(cmd.Context(), ownerPart, repoPart, number, hours); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"issueNumber": number,
					"hours":       hours,
					"result":      "set",
				})
			}

			fmt.Printf("Set #%d hours → %.1f\n", number, hours)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectSetEstimateCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "set-estimate [issue-number]",
		Short: "Set Estimate field from Size label (skips if already set)",
		Long: `Derives the story-point Estimate from the issue's Size label and sets it on
the project board. The field is only written when it is currently empty — manual
estimates are never overwritten.

Default mapping: XS=1, S=2, M=3, L=5, XL=8.
Override via project.size_to_estimate in .nightgauge/config.yaml.`,
		Args: cobra.ExactArgs(1),
		Example: `  nightgauge project set-estimate 2659
  nightgauge project set-estimate 2659 --repo nightgauge/nightgauge`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			// Load mapping from config (falls back to default if not set)
			mapping := gh.DefaultSizeToEstimate()
			if workdir, wdErr := os.Getwd(); wdErr == nil {
				if cfg, cfgErr := config.Load(workdir); cfgErr == nil && len(cfg.SizeToEstimate) > 0 {
					mapping = cfg.SizeToEstimate
				}
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)

			// Fetch issue labels, then set estimate
			issueSvc := gh.NewIssueService(client)
			issue, err := issueSvc.GetIssue(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return fmt.Errorf("fetch issue #%d: %w", number, err)
			}

			if err := svc.SetEstimateFromLabels(cmd.Context(), ownerPart, repoPart, number, issue.Labels, mapping); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"issueNumber": number,
					"result":      "set",
				})
			}

			fmt.Printf("Set estimate on #%d from Size label\n", number)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectSetFieldCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		priority      string
		size          string
		status        string
		startDate     string
		targetDate    string
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "set-field [issue-number]",
		Short: "Set Priority, Size, Status, and/or date fields on a project board item",
		Long: `Set one or more fields on a project board item by issue number.
This is the fallback for when 'project add' does not set fields automatically
(e.g., when the binary is unavailable and raw GraphQL was used to add the item).`,
		Args: cobra.ExactArgs(1),
		Example: `  nightgauge project set-field 170 --priority P0 --size M --status Ready
  nightgauge project set-field 42 --status "In progress"
  nightgauge project set-field 300 --priority P1
  nightgauge project set-field 42 --start-date 2026-05-01 --target-date 2026-05-15`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			fields := make(map[string]string)
			if priority != "" {
				fields["Priority"] = priority
			}
			if size != "" {
				fields["Size"] = size
			}
			if status != "" {
				fields["Status"] = status
			}

			// Validate date flags before making any API calls
			if startDate != "" && !isValidDate(startDate) {
				return fmt.Errorf("--start-date %q is not a valid YYYY-MM-DD date", startDate)
			}
			if targetDate != "" && !isValidDate(targetDate) {
				return fmt.Errorf("--target-date %q is not a valid YYYY-MM-DD date", targetDate)
			}

			if len(fields) == 0 && startDate == "" && targetDate == "" {
				return fmt.Errorf("at least one of --priority, --size, --status, --start-date, or --target-date is required")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)

			if len(fields) > 0 {
				if err := svc.SetFields(cmd.Context(), ownerPart, repoPart, number, fields); err != nil {
					return err
				}
			}

			if startDate != "" {
				if err := svc.SetDateFieldByNumber(cmd.Context(), ownerPart, repoPart, number, "Start date", startDate); err != nil {
					return fmt.Errorf("set Start date=%s on #%d: %w", startDate, number, err)
				}
			}
			if targetDate != "" {
				if err := svc.SetDateFieldByNumber(cmd.Context(), ownerPart, repoPart, number, "Target date", targetDate); err != nil {
					return fmt.Errorf("set Target date=%s on #%d: %w", targetDate, number, err)
				}
			}

			if outputJSON {
				result := map[string]interface{}{
					"issueNumber": number,
					"fields":      fields,
					"result":      "set",
				}
				if startDate != "" {
					result["startDate"] = startDate
				}
				if targetDate != "" {
					result["targetDate"] = targetDate
				}
				return printJSON(result)
			}

			fmt.Printf("Set fields on #%d:", number)
			for k, v := range fields {
				fmt.Printf(" %s=%s", k, v)
			}
			if startDate != "" {
				fmt.Printf(" start-date=%s", startDate)
			}
			if targetDate != "" {
				fmt.Printf(" target-date=%s", targetDate)
			}
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().StringVar(&priority, "priority", "", "Priority value (P0, P1, P2, P3)")
	cmd.Flags().StringVar(&size, "size", "", "Size value (XS, S, M, L, XL)")
	cmd.Flags().StringVar(&status, "status", "", "Status value (Backlog, Ready, In progress, In review, Done)")
	cmd.Flags().StringVar(&startDate, "start-date", "", "Start date in YYYY-MM-DD format")
	cmd.Flags().StringVar(&targetDate, "target-date", "", "Target date in YYYY-MM-DD format")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectUpdateEstimatesCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "update-estimates [epic-number]",
		Short: "Roll up sub-issue estimates for an epic",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge project update-estimates 295
  nightgauge project update-estimates 1543`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid epic number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)
			totalHours, err := svc.UpdateEpicEstimates(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"epicNumber": number,
					"totalHours": totalHours,
					"result":     "updated",
				})
			}

			fmt.Printf("Updated epic #%d estimates → %.1f hours (%.1f days)\n", number, totalHours, totalHours/8)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectMoveStatusCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "move-status [issue-number] [status]",
		Short: "Move issue/epic through statuses",
		Long: `Transition an issue or epic status.

Valid statuses: ready, in-progress, in-review, done, blocked, needs-info`,
		Args: cobra.ExactArgs(2),
		Example: `  nightgauge project move-status 295 in-review
  nightgauge project move-status 295 done`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			status := args[1]

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewProjectService(client, ownerPart, projectNumber)
			if err := svc.MoveStatus(cmd.Context(), ownerPart, repoPart, number, status); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"issueNumber": number,
					"status":      status,
					"result":      "moved",
				})
			}

			fmt.Printf("Moved #%d → %s\n", number, status)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectDriftCheckCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
		fix           bool
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "drift-check",
		Short: "Audit field drift across project items",
		Long:  "Compare issue labels against project board field values and report mismatches.",
		Example: `  nightgauge project drift-check
  nightgauge project drift-check --fix
  nightgauge project drift-check --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			svc := gh.NewProjectService(client, owner, projectNumber)

			if fix {
				fixed, err := svc.DriftFix(cmd.Context())
				if err != nil {
					return err
				}

				if outputJSON {
					return printJSON(map[string]interface{}{
						"fixed": fixed,
						"count": len(fixed),
					})
				}

				if len(fixed) == 0 {
					fmt.Println("No drift detected — all fields match labels.")
					return nil
				}

				fmt.Printf("Fixed %d field drifts:\n", len(fixed))
				for _, d := range fixed {
					fmt.Printf("  #%-5d %-10s %s → %s (%s)\n", d.IssueNumber, d.FieldName, d.Actual, d.Expected, d.Repo)
				}
				return nil
			}

			drifts, err := svc.DriftCheck(cmd.Context())
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"drifts": drifts,
					"count":  len(drifts),
				})
			}

			if len(drifts) == 0 {
				fmt.Println("No drift detected — all fields match labels.")
				return nil
			}

			fmt.Printf("Field Drift Report (%d mismatches):\n", len(drifts))
			fmt.Println(strings.Repeat("=", 60))
			for _, d := range drifts {
				fmt.Printf("  #%-5d %-12s %-10s expected: %-4s actual: %-4s (%s)\n",
					d.IssueNumber, d.Title, d.FieldName, d.Expected, d.Actual, d.Repo)
			}
			fmt.Printf("\nRun with --fix to correct these drifts.\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&fix, "fix", false, "Fix detected drift (update board fields to match labels)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectEnsureFieldsCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "ensure-fields",
		Short: "Create or update required project board fields (Status, Priority, Size, Start date, Target date, Estimate)",
		Long: `Idempotently create the standard 6 project board fields for Nightgauge:
  - Status (SINGLE_SELECT): Backlog, Ready, In progress, In review, Done
  - Priority (SINGLE_SELECT): P0, P1, P2, P3
  - Size (SINGLE_SELECT): XS, S, M, L, XL
  - Start date (DATE)
  - Target date (DATE)
  - Estimate (NUMBER)

Existing fields with all required options are left untouched. Missing options are added
to existing SINGLE_SELECT fields (full option set is replaced). Field IDs are included
in JSON output so skills can consume them without a separate fields query.

Safe to run multiple times — idempotent.`,
		Example: `  nightgauge project ensure-fields --number 5
  nightgauge project ensure-fields --number 5 --owner nightgauge --json
  nightgauge project ensure-fields --number 5 --owner-type user --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectNumber == 0 {
				return fmt.Errorf("--number is required")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerType := getOwnerType(cmd)
			svc := gh.NewProjectService(client, owner, projectNumber, ownerType)
			result, err := svc.EnsureFields(cmd.Context(), gh.DefaultFieldSchema())
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			if len(result.Created) > 0 {
				fmt.Printf("Created: %s\n", strings.Join(result.Created, ", "))
			}
			if len(result.Updated) > 0 {
				fmt.Printf("Updated: %s\n", strings.Join(result.Updated, ", "))
			}
			if len(result.Already) > 0 {
				fmt.Printf("Already: %s\n", strings.Join(result.Already, ", "))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().IntVar(&projectNumber, "number", 0, "Project board number (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// --- run command ---

func runCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
		auto          bool
		pollSeconds   int
		maxPerRepo    int
		issueNumber   int
		adapterName   string
	)

	cmd := &cobra.Command{
		Use:   "run [issue number]",
		Short: "Run pipeline for next ready issue or continuously with --auto",
		Example: `  nightgauge run 1311                    # Run pipeline for specific issue
  nightgauge run 1311 --adapter codex    # Run with Codex adapter
  nightgauge run --project 5              # Pick next ready issue and run
  nightgauge run --auto --project 5       # Run continuously`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Accept issue number as positional arg or flag
			if len(args) > 0 && issueNumber == 0 {
				num, err := strconv.Atoi(args[0])
				if err != nil {
					return fmt.Errorf("invalid issue number: %s", args[0])
				}
				issueNumber = num
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			// Resolve adapter through the canonical chain (#54):
			// --adapter flag > NIGHTGAUGE_ADAPTER env > ui.core.adapter config
			// > claude-headless. Per-stage overrides
			// (pipeline.stage_adapters.<stage>) apply inside the scheduler's
			// stage loop unless the flag/env pinned the adapter explicitly.
			cwd, _ := os.Getwd()
			onFailureStatus := "ready"
			globalAdapterDefault := ""
			runCfg, cfgLoadErr := config.Load(cwd)
			if cfgLoadErr == nil && runCfg != nil {
				if runCfg.Autonomous != nil {
					onFailureStatus = runCfg.Autonomous.ResolvedOnFailureStatus()
				}
				if runCfg.UI != nil && runCfg.UI.Core != nil {
					globalAdapterDefault = runCfg.UI.Core.Adapter
				}
			}
			registry := adapters.NewRegistry()
			adapter, err := registry.Resolve(adapterName, globalAdapterDefault)
			if err != nil {
				return fmt.Errorf("adapter: %w", err)
			}
			fmt.Printf("Using adapter: %s\n", adapter.Name())

			adapterExplicit := adapterName
			if adapterExplicit == "" {
				adapterExplicit = os.Getenv("NIGHTGAUGE_ADAPTER")
			}
			excludeLabels := config.DefaultExcludeLabels
			if cfgLoadErr == nil && runCfg != nil && runCfg.Autonomous != nil {
				excludeLabels = runCfg.Autonomous.ResolvedExcludeLabels()
			}
			sched := orchestrator.NewScheduler(client, orchestrator.SchedulerConfig{
				Owner:           owner,
				OwnerType:       getOwnerType(cmd),
				ProjectNumber:   projectNumber,
				MaxPerRepo:      maxPerRepo,
				WorkspaceRoot:   cwd,
				Adapter:         adapter,
				AdapterExplicit: adapterExplicit,
				OnFailureStatus: onFailureStatus,
				ExcludeLabels:   excludeLabels,
			})

			// Identity preflight gate (#4068): assert the resolved per-repo
			// identity has push before dispatch. Skips when no github_user is
			// configured (nil checker = gate disabled).
			wireIdentityChecker(sched, cwd)

			// Log stage progress to stdout
			sched.OnStageStart(func(repo string, issue int, stage string, title string) {
				fmt.Printf("[#%d] stage %s started\n", issue, stage)
			})
			sched.OnStageComplete(func(repo string, issue int, stage string, err error, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, model string) {
				if err != nil {
					fmt.Printf("[#%d] stage %s FAILED: %v\n", issue, stage, err)
				} else {
					fmt.Printf("[#%d] stage %s complete — tokens: %d in (%d cached) / %d out, cost: $%.4f\n",
						issue, stage, inputTokens, cacheReadTokens, outputTokens, costUsd)
				}
			})

			if auto {
				interval := time.Duration(pollSeconds) * time.Second
				return sched.RunAuto(cmd.Context(), interval)
			}

			// Run specific issue if provided
			if issueNumber > 0 {
				repo := fmt.Sprintf("%s/nightgauge", owner)
				sched.QueueAdd(orchestrator.QueueEntry{
					Repo:        repo,
					IssueNumber: issueNumber,
				})
				return sched.RunQueue(cmd.Context())
			}

			// Single run: pick next ready issue and execute
			item, err := sched.PickNext(cmd.Context())
			if err != nil {
				return err
			}
			if item == nil {
				fmt.Println("No ready items to process.")
				return nil
			}

			fmt.Printf("Running #%d: %s (%s, %s)\n", item.Number, item.Title, item.Priority, item.Repo)
			sched.QueueAdd(orchestrator.QueueEntry{
				Repo:        item.Repo,
				IssueNumber: item.Number,
			})
			return sched.RunQueue(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&auto, "auto", false, "Run continuously, polling for ready items")
	cmd.Flags().IntVar(&pollSeconds, "poll", 30, "Poll interval in seconds (with --auto)")
	cmd.Flags().IntVar(&maxPerRepo, "max-per-repo", 1, "Max concurrent pipelines per repo")
	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Specific issue number to run")
	cmd.Flags().StringVar(&adapterName, "adapter", "", "AI adapter (claude-headless, claude-sdk, codex, gemini, gemini-sdk)")

	// `nightgauge run state {get,set,resume,discard,detect}` —
	// durable run-state.json (Issue #3238).
	cmd.AddCommand(runstatecmd.Cmd())

	return cmd
}

// --- queue command ---

func queueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Manage the issue execution queue",
	}
	cmd.AddCommand(queueAddCmd(), queueListCmd(), queueRunCmd(), queueClearCmd(), queueRemoveCmd())
	return cmd
}

// queueScheduler is a package-level scheduler for queue operations.
// Lazily initialized on first queue command that needs it.
var queueScheduler *orchestrator.Scheduler

func getQueueScheduler(owner string, projectNumber int) (*orchestrator.Scheduler, error) {
	if queueScheduler != nil {
		return queueScheduler, nil
	}
	client, err := clientFromConfig()
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()
	// Resolve owner type, failure status, and concurrency policy from config.
	var ot gh.OwnerType
	onFailureStatus := "ready"
	rc := config.ResolveConcurrency(nil) // defaults (workspace 3, per_repo 1)
	var repoOverrides map[string]int
	excludeLabels := config.DefaultExcludeLabels
	if cfg, err := config.Load(cwd); err == nil {
		ot = gh.ParseOwnerType(cfg.OwnerType)
		if cfg.Autonomous != nil {
			onFailureStatus = cfg.Autonomous.ResolvedOnFailureStatus()
			excludeLabels = cfg.Autonomous.ResolvedExcludeLabels()
		}
		rc = config.ResolveConcurrency(cfg)
		if cfg.Concurrency != nil {
			repoOverrides = cfg.Concurrency.RepositoryOverrides
		}
	}
	queueScheduler = orchestrator.NewScheduler(client, orchestrator.SchedulerConfig{
		Owner:                    owner,
		OwnerType:                ot,
		ProjectNumber:            projectNumber,
		MaxPerRepo:               rc.PerRepoMax,
		RepoConcurrencyOverrides: repoOverrides,
		WorkspaceRoot:            cwd,
		Adapter:                  adapters.NewClaudeAdapter(),
		OnFailureStatus:          onFailureStatus,
		ExcludeLabels:            excludeLabels,
	})
	// Identity preflight gate (#4068): skips when no github_user is configured.
	wireIdentityChecker(queueScheduler, cwd)
	return queueScheduler, nil
}

func queueAddCmd() *cobra.Command {
	var (
		repo          string
		owner         string
		projectNumber int
	)

	cmd := &cobra.Command{
		Use:     "add [issue numbers...]",
		Short:   "Add issues to the execution queue",
		Args:    cobra.MinimumNArgs(1),
		Example: `  nightgauge queue add 1311 1319 1320`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sched, err := getQueueScheduler(owner, projectNumber)
			if err != nil {
				return err
			}

			// Parse owner/repo from the --repo flag ("owner/repo" format).
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got: %s", repo)
			}
			repoOwner, repoName := parts[0], parts[1]

			client, err := clientFromConfig()
			if err != nil {
				return err
			}
			issueSvc := gh.NewIssueService(client)
			ctx := context.Background()

			for _, arg := range args {
				num, err := strconv.Atoi(arg)
				if err != nil {
					return fmt.Errorf("invalid issue number: %s", arg)
				}

				issue, err := issueSvc.GetIssue(ctx, repoOwner, repoName, num)
				if err != nil {
					return fmt.Errorf("fetch issue #%d: %w", num, err)
				}

				if ibqueue.IsEpic(issue.Labels) {
					fmt.Printf("Epic detected: #%d — %s. Expanding sub-issues...\n", num, issue.Title)
					if err := sched.EnqueueEpic(ctx, repoOwner, repoName, num, issue.Title, issue.Labels, nil); err != nil {
						return fmt.Errorf("enqueue epic #%d: %w", num, err)
					}
					queued := sched.QueueList()
					fmt.Printf("Enqueued sub-issues of epic #%d (%d items in queue)\n", num, len(queued))
				} else if label, excluded := orchestrator.ExcludedLabelMatch(issue.Labels, sched.ExcludeLabels()); excluded {
					// Human-only issue (#317) — needs a person, not the pipeline.
					// Refuse to queue it rather than burning tokens on a run that
					// can only fail at pr-create with nothing to commit.
					fmt.Printf("Skipping #%d — carries human-only label %q (autonomous.exclude_labels)\n", num, label)
				} else {
					sched.QueueAdd(orchestrator.QueueEntry{
						Repo:        repo,
						IssueNumber: num,
					})
					fmt.Printf("Queued #%d (%s)\n", num, repo)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "nightgauge/nightgauge", "Repository for queued issues")
	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	return cmd
}

func queueListCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List queued issues",
		RunE: func(cmd *cobra.Command, args []string) error {
			sched, err := getQueueScheduler(owner, projectNumber)
			if err != nil {
				return err
			}

			entries := sched.QueueList()
			if len(entries) == 0 {
				fmt.Println("Queue is empty.")
				return nil
			}

			for i, e := range entries {
				fmt.Printf("%d. #%d (%s)\n", i+1, e.IssueNumber, e.Repo)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	return cmd
}

func queueRunCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Process all queued issues sequentially",
		RunE: func(cmd *cobra.Command, args []string) error {
			sched, err := getQueueScheduler(owner, projectNumber)
			if err != nil {
				return err
			}

			entries := sched.QueueList()
			if len(entries) == 0 {
				fmt.Println("Queue is empty. Use 'queue add' first.")
				return nil
			}

			fmt.Printf("Processing %d queued issues...\n", len(entries))
			return sched.RunQueue(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	return cmd
}

func queueRemoveCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
	)

	cmd := &cobra.Command{
		Use:     "remove [issue number]",
		Short:   "Remove an issue from the queue",
		Args:    cobra.ExactArgs(1),
		Example: `  nightgauge queue remove 1311`,
		RunE: func(cmd *cobra.Command, args []string) error {
			num, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			sched, err := getQueueScheduler(owner, projectNumber)
			if err != nil {
				return err
			}

			sched.QueueRemove(num)
			fmt.Printf("Removed #%d from queue.\n", num)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	return cmd
}

func queueClearCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
	)

	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear the execution queue",
		RunE: func(cmd *cobra.Command, args []string) error {
			sched, err := getQueueScheduler(owner, projectNumber)
			if err != nil {
				return err
			}

			sched.QueueClear()
			fmt.Println("Queue cleared.")
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number")
	return cmd
}

// --- status command ---

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show pipeline status summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Nightgauge CLI")
			fmt.Printf("Version: %s\n", effectiveVersion())
			fmt.Println("Status: operational")

			// Check GitHub token
			if os.Getenv("GITHUB_TOKEN") != "" {
				fmt.Println("GitHub: authenticated")
			} else {
				fmt.Println("GitHub: not authenticated (set GITHUB_TOKEN)")
			}
			return nil
		},
	}
}

// --- version command ---

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("nightgauge %s\n", effectiveVersion())
		},
	}
}

// --- serve command (IPC server) ---

// setupServeLogging configures the Go log package to write to both stderr and
// a persistent log file at .nightgauge/logs/go-backend.log. The file is
// opened in append mode and rotated (truncated) when it exceeds 5 MB.
// Returns a closer that should be deferred.
func setupServeLogging(workspaceRoot string) func() {
	logDir := filepath.Join(workspaceRoot, ".nightgauge", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot create log dir %s: %v\n", logDir, err)
		return func() {}
	}

	logPath := filepath.Join(logDir, "go-backend.log")

	// Rotate: if the file exceeds 5 MB, truncate it (keep last 1 MB).
	if info, err := os.Stat(logPath); err == nil && info.Size() > 5*1024*1024 {
		if data, err := os.ReadFile(logPath); err == nil {
			keep := data
			if len(data) > 1024*1024 {
				keep = data[len(data)-1024*1024:]
			}
			_ = os.WriteFile(logPath, keep, 0644) // log rotation is best-effort; failure is non-fatal
		}
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot open log file %s: %v\n", logPath, err)
		return func() {}
	}

	// Tee: all log.Printf calls go to both stderr (captured by TypeScript) and the file
	multi := io.MultiWriter(os.Stderr, f)
	log.SetOutput(multi)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	log.Printf("=== Go backend started (pid=%d) ===", os.Getpid())

	return func() {
		log.Printf("=== Go backend shutting down (pid=%d) ===", os.Getpid())
		f.Close()
	}
}

func serveCmd() *cobra.Command {
	var (
		platformURL      string
		apiKey           string
		licenseKey       string
		workspaceDir     string
		githubGraphQLURL string
	)

	cmd := &cobra.Command{
		Use:    "serve",
		Short:  "Start JSON-over-stdio IPC server for VSCode",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var client *gh.Client
			if githubGraphQLURL != "" {
				token := os.Getenv("GITHUB_TOKEN")
				client = gh.NewClientWithURL(token, githubGraphQLURL)
			} else {
				var err error
				client, err = clientFromConfig()
				if err != nil {
					return err
				}
			}

			var opts []ipc.ServerOption

			// Workspace root: prefer explicit --workspace flag, fall back to CWD
			workspaceRoot := workspaceDir
			if workspaceRoot == "" {
				workspaceRoot, _ = os.Getwd() // os.Getwd failure falls back to empty string; Load() handles missing workdir
			}

			// Set up persistent file-based logging (tees to stderr + file)
			closeLog := setupServeLogging(workspaceRoot)
			defer closeLog()
			opts = append(opts, ipc.WithWorkspaceRoot(workspaceRoot))

			// Load config from .nightgauge/config.yaml
			cfg, cfgErr := config.Load(workspaceRoot)
			if cfgErr != nil {
				// Non-fatal: scheduler won't be available but other IPC methods still work
				fmt.Fprintf(os.Stderr, "warning: config load failed, scheduler disabled: %v\n", cfgErr)
			}
			if cfg != nil && cfg.SuppressGHWarning() {
				opts = append(opts, ipc.WithSuppressGHWarning(true))
			}

			// Export the configured GitHub token so deterministic `gh`
			// subprocesses (gates, recovery, board status, skill shell-outs)
			// authenticate as the pipeline identity rather than the machine's
			// active gh account (#3887). Skipped in mock mode, where an
			// injected client (githubGraphQLURL) drives GitHub access.
			if githubGraphQLURL == "" && cfg != nil {
				if tok := exportConfiguredGitHubToken(cfg, cfg.Owner); tok != "" {
					log.Printf("serve: exported configured GitHub token to GH_TOKEN/GITHUB_TOKEN for gh subprocesses (owner=%q)", cfg.Owner)
				} else {
					log.Printf("serve: no configured GitHub token resolved; gh subprocesses will use ambient auth (owner=%q)", cfg.Owner)
				}
			}

			// Resolve platform credentials with flag > env > config
			// precedence (#333). serveCmd registers --platform-url,
			// --api-key, and --license-key with os.Getenv(...) defaults, so
			// the flag variables above already encode "flag or env, flag
			// wins" by the time cobra hands control to RunE — the only
			// remaining fallback is the merged config file's platform
			// section, which an extension-spawned daemon (no flags, no env)
			// otherwise never consults. Without this, cfg.PlatformURL /
			// cfg.LicenseKey are silently ignored and both the remote-command
			// poller (below) and the #330 Action Center bridge stay dormant
			// in the product's primary deployment mode.
			resolvedPlatform := resolvePlatformConfig(platformURL, apiKey, licenseKey, cfg)
			platformURL, apiKey, licenseKey = resolvedPlatform.URL, resolvedPlatform.APIKey, resolvedPlatform.LicenseKey
			if resolvedPlatform.Configured() {
				apiURLForLog := platformURL
				if apiURLForLog == "" {
					apiURLForLog = "default"
				}
				licenseState := "absent"
				if licenseKey != "" {
					licenseState = "present"
				}
				log.Printf("serve: platform configured (api=%s, license=%s, source=%s)", apiURLForLog, licenseState, resolvedPlatform.Source)
			} else {
				log.Printf("serve: no platform configured — platform bridge disabled")
			}

			// Set up platform client if configured
			var platformClient *platform.Client
			if platformURL != "" || apiKey != "" || licenseKey != "" {
				pcfg := platform.DefaultConfig()
				if platformURL != "" {
					pcfg.BaseURL = platformURL
				}
				pcfg.APIKey = apiKey
				pcfg.LicenseKey = licenseKey
				// Stable per-machine id so the platform scopes this machine's
				// queue snapshot (delete-by-machine) and tags runs by origin.
				pcfg.AgentID = platform.ResolveMachineID()

				pc, err := platform.NewClient(pcfg)
				if err != nil {
					return fmt.Errorf("platform client: %w", err)
				}

				ctx := context.Background()
				pc.StartHealthPolling(ctx)
				platformClient = pc
				opts = append(opts, ipc.WithPlatformClient(pc))

				authSvc := platform.NewAuthService(pc)
				opts = append(opts, ipc.WithAuthService(authSvc))
			}

			// Start command polling if platform is configured and license key present
			var cmdExec *executor.CommandExecutor
			if platformClient != nil && cfg != nil && licenseKey != "" {
				rcCfg := platform.DefaultCommandPollerConfig()
				if cfg.RemoteCommands != nil {
					if cfg.RemoteCommands.PollInterval > 0 {
						rcCfg.PollInterval = cfg.RemoteCommands.PollInterval
					}
					if cfg.RemoteCommands.MaxBackoff > 0 {
						rcCfg.MaxBackoff = cfg.RemoteCommands.MaxBackoff
					}
				}
				if cfg.RemoteCommands.IsEnabled() {
					stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
					localStateSvc := state.NewLocalStateService(stateDir)
					cmdExec = executor.NewWithHandlers(executor.Deps{
						StateSvc: localStateSvc,
					})
					cmdSvc := platform.NewCommandService(platformClient)
					adapter := platform.NewExecutorAdapter(cmdExec, cmdSvc.AcknowledgeCommand)
					poller := platform.NewCommandPoller(platformClient, adapter, rcCfg)
					poller.Start(context.Background())
				} else {
					fmt.Fprintf(os.Stderr, "[debug] command polling disabled via config\n")
				}
			}

			// Wire the inbound webhook receiver's reloader option BEFORE
			// constructing the IPC server, so notifications.reloadTokens
			// has a target to invoke. The actual *Server is built after
			// NewServer returns (it needs server.Emit for dispatch).
			var inboundStore *inbound.TokenStore
			if cfg != nil && cfg.Notifications != nil &&
				cfg.Notifications.Inbound != nil && cfg.Notifications.Inbound.Enabled {
				// Apply the documented default port when the operator did
				// not set one. The receiver itself treats Port=0 as
				// "OS-pick" (used by tests), so the default is applied
				// here at the wiring layer.
				if cfg.Notifications.Inbound.Port == 0 {
					cfg.Notifications.Inbound.Port = config.DefaultInboundPort
				}
				inboundStore = inbound.NewTokenStore()
				if err := inboundStore.Reload(cfg); err != nil {
					log.Printf("inbound webhook: initial token load failed: %v", err)
				}
				opts = append(opts, ipc.WithNotificationReloader(inboundStore.Reload))
			}

			// Wire user-mapping authorization for inbound Mattermost commands (#3377).
			// Mirrors the inboundStore pattern: build the store + authorizer before
			// NewServer so the closure is available at handler registration time.
			// The store is always created so it can be hot-reloaded via config watch
			// even when cfg.Users is initially empty.
			{
				mappingStore := auth.NewUserMappingStore()
				if cfg != nil {
					mappingStore.Reload(cfg)
				}
				permCache := auth.NewPermissionCache()
				auditDir := workspaceRoot + "/.nightgauge/notifications"
				auditWriter := auth.NewAuditWriter(auditDir)
				ghChecker := auth.RepoPermissionCheckerFunc(func(ctx context.Context, login, owner, repo string) (bool, error) {
					return client.HasRepoWriteAccess(ctx, login, owner, repo)
				})
				authorizer := auth.NewAuthorizer(mappingStore, permCache, auditWriter, ghChecker)
				opts = append(opts, ipc.WithCommandAuthorizer(
					func(ctx context.Context, mattermostUserID, channelID, commandType, repoSlug string) (bool, string, string) {
						result := authorizer.Authorize(ctx, mattermostUserID, channelID, commandType, repoSlug)
						return result.Allowed, result.MappedIdentity, result.Reason
					},
				))
			}

			server := ipc.NewServer(client, opts...)

			// Register every workspace repo with the per-repo client resolver so
			// cross-repo operations resolve the target repo's configured identity
			// (github_user / token) instead of the default startup client. The
			// default client carries the primary repo's identity and cannot see
			// private sibling repos (#3700).
			registerWorkspaceReposInResolver(server, workspaceRoot, cfg)

			// Construct the inbound receiver now that server.Emit is
			// available. Start(ctx) is launched later, once the lifecycle
			// context exists (mirrors the pattern used for autonomous).
			var inboundRecv *inbound.Server
			if inboundStore != nil {
				disp := inbound.NewIPCDispatcher(server.Emit)
				inboundRecv = inbound.New(cfg.Notifications.Inbound, inboundStore, disp)
			}

			// Tracked so startup orphan recovery can run against it once the
			// IPC server context is available (below).
			var autoSched *orchestrator.AutonomousScheduler

			// Resolve the scheduler identity once. For a manifest-based multi-repo
			// root with no root config.yaml, config.Load returned DefaultConfig
			// (ProjectNumber==0), so the old inline gate (cfg.ProjectNumber>0)
			// never attached the scheduler and every scheduler-backed IPC call was
			// rejected "scheduler not configured". resolveSchedulerIdentity derives
			// the identity from the manifest in that case; single-repo roots return
			// their root config unchanged. See nightgauge#3860.
			schedIdent := resolveSchedulerIdentity(workspaceRoot, cfg)

			// Create scheduler and wire IPC stage runner (Go ↔ TypeScript bridge)
			if cfgErr == nil && schedIdent.Resolvable() {
				ipcOnFailure := "ready"
				ipcExcludeLabels := config.DefaultExcludeLabels
				if cfg.Autonomous != nil {
					ipcOnFailure = cfg.Autonomous.ResolvedOnFailureStatus()
					ipcExcludeLabels = cfg.Autonomous.ResolvedExcludeLabels()
				}
				fmt.Fprintf(os.Stderr, "[nightgauge] scheduler identity: %s/%d (source=%s)\n", schedIdent.Owner, schedIdent.ProjectNumber, schedIdent.Source)
				sched := orchestrator.NewScheduler(client, orchestrator.SchedulerConfig{
					Owner:           schedIdent.Owner,
					OwnerType:       gh.ParseOwnerType(schedIdent.OwnerType),
					ProjectNumber:   schedIdent.ProjectNumber,
					MaxPerRepo:      3,
					WorkspaceRoot:   workspaceRoot,
					Adapter:         nil, // IPC mode uses IpcStageRunner, not CLI adapter
					OnFailureStatus: ipcOnFailure,
					ExcludeLabels:   ipcExcludeLabels,
				})

				// Wire per-repo client resolution so cross-repo epics use the
				// target repo's configured identity (github_user / token) rather
				// than this server's primary-repo startup client (#3700).
				sched.WithClientResolver(server.ResolveGitHubClient)

				// Identity preflight gate (#4068): assert the resolved per-repo
				// identity has push before dispatch. Skips when no github_user is
				// configured (nil checker = gate disabled).
				if ic := orchestrator.NewConfigIdentityChecker(cfg); ic != nil {
					sched.WithIdentityChecker(ic)
				}

				// Wire platform skill resolution for paid tiers
				if platformClient != nil {
					sched.WithSkillService(platform.NewSkillService(platformClient))
				}

				// Wire platform telemetry (push completed run records)
				if platformClient != nil && cfg != nil {
					telemetrySvc := platform.NewTelemetryService(platformClient)
					sched.WithTelemetryService(telemetrySvc, cfg.Telemetry.IsEnabled())
					telemetrySvc.StartAutoFlush(context.Background())
				}

				// Wire Scheduler and IssueGetter into the remote command executor
				// (cmdExec was created before sched was available above).
				if cmdExec != nil {
					stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
					localStateSvc := state.NewLocalStateService(stateDir)
					issueSvc := gh.NewIssueService(client)
					cmdExec.UpdateDeps(executor.Deps{
						Scheduler:   &schedulerAdapter{sched: sched},
						StateSvc:    localStateSvc,
						IssueGetter: &issueGetterAdapter{svc: issueSvc},
					})
				}

				server.SetScheduler(sched)

				// Wire autonomous scheduler for IPC start/stop. Always created
				// so customers can use autonomous mode from any repo — the
				// `autonomous:` config section is optional (sensible defaults).
				{
					autoCfg := orchestrator.DefaultAutonomousConfig()
					// Unified slot ceiling: pipeline.max_concurrent is the
					// source of truth. autonomous.max_concurrent is honored
					// only as a legacy fallback so existing configs keep
					// working unchanged. See config.ResolvedMaxConcurrent.
					autoCfg.MaxConcurrent = config.ResolvedMaxConcurrent(cfg)
					if cfg.Autonomous != nil {
						autoCfg.ExcludeLabels = cfg.Autonomous.ResolvedExcludeLabels()
						if d := cfg.Autonomous.ScanInterval.Duration(); d > 0 {
							autoCfg.ScanInterval = d
						}
						if cfg.Autonomous.BudgetCeiling > 0 {
							autoCfg.BudgetCeiling = cfg.Autonomous.BudgetCeiling
						}
						if cfg.Autonomous.DryRun != nil {
							autoCfg.DryRun = *cfg.Autonomous.DryRun
						}
						if cfg.Autonomous.PickupBacklog != nil {
							autoCfg.PickupBacklog = *cfg.Autonomous.PickupBacklog
						}
						if cfg.Autonomous.SafetyRails != nil {
							src := cfg.Autonomous.SafetyRails
							autoCfg.SafetyRails = &orchestrator.SafetyConfig{
								BudgetCeiling:     src.BudgetCeiling,
								CircuitBreakerMax: src.CircuitBreakerMax,
								RateLimitPerHour:  src.RateLimitPerHour,
								EpicCheckpoint:    src.EpicCheckpoint,
								HealthGateMin:     src.HealthGateMin,
							}
						}
						rc := config.ResolveConcurrency(cfg)
						autoCfg.PerRepoMax = rc.PerRepoMax
						if cfg.Concurrency != nil && len(cfg.Concurrency.RepositoryOverrides) > 0 {
							autoCfg.RepositoryMaxConcurrent = cfg.Concurrency.RepositoryOverrides
						}
					}
					applyStuckEpicConfig(&autoCfg, cfg)

					// Resolve the autonomous repo set. The workspace manifest
					// (.vscode/nightgauge-workspace.yaml) is authoritative when
					// present: it lists exactly the repos this workspace owns, so the
					// scheduler is constructed with that set (and FilterRepos can scope
					// within it). This is required for the N:1 topology (many repos →
					// one project) and for layouts where member repos are CHILD dirs of
					// the workspace root rather than siblings. Without it the scheduler
					// was built from sibling detection + the folder base name, which
					// (a) missed child repos and (b) derived a bogus slug from the
					// directory name (e.g. dir "acmeapp" → "owner/acmeapp", the
					// wrong repo), leaving every issue rejected as repo-not-in-filter.
					// See nightgauge#3769. Falls back to the legacy
					// sibling-detection behavior when no manifest is present.
					// Use the resolved scheduler identity (not raw cfg) so the
					// autonomous repo set is consistent with the attached scheduler
					// — critical for manifest-only roots where cfg.ProjectNumber==0
					// but schedIdent carries the manifest-derived owner/project.
					repoConfigs := reposFromWorkspaceManifest(workspaceRoot, schedIdent.Owner, schedIdent.ProjectNumber)

					if len(repoConfigs) == 0 {
						// No workspace manifest — legacy behavior. Detect sibling repos
						// from workspaceRoot (not CWD — the IPC server's CWD is
						// unpredictable when started by VSCode).
						repoConfigs = detectSiblingRepos(workspaceRoot, schedIdent.Owner, schedIdent.ProjectNumber)

						// Always include the current workspace as a repo so standalone
						// repos (customers with a single project) can use autonomous
						// mode without needing sibling directories.
						currentRepoName := filepath.Base(workspaceRoot)
						alreadyIncluded := false
						for _, rc := range repoConfigs {
							if rc.Name == currentRepoName {
								alreadyIncluded = true
								break
							}
						}
						if !alreadyIncluded && schedIdent.Owner != "" && schedIdent.ProjectNumber > 0 {
							repoConfigs = append(repoConfigs, depgraph.RepoConfig{
								Owner:     schedIdent.Owner,
								OwnerType: gh.ParseOwnerType(schedIdent.OwnerType),
								Name:      currentRepoName,
								Project:   schedIdent.ProjectNumber,
							})
						}
					}

					if len(repoConfigs) > 0 {
						autoSched = orchestrator.NewAutonomousScheduler(
							sched, client, repoConfigs, nil, autoCfg, workspaceRoot,
						)
						// (#4151) Resolve the post-merge survival observation window
						// from pipeline.survival.window_days (safe on a nil Pipeline).
						autoSched.SetSurvivalWindowDays(cfg.Pipeline.ResolveSurvivalWindowDays())

						// Apply autonomous.enabled_repos filter from config.yaml
						// when set. This lets users scope autonomous scanning to
						// a subset of their workspace repos (e.g. ["platform"]
						// only), cutting GraphQL usage proportionally.
						if cfg.Autonomous != nil {
							if enabled := cfg.Autonomous.ResolvedEnabledRepos(cfg.Owner); len(enabled) > 0 {
								autoSched.FilterRepos(enabled)
								fmt.Fprintf(os.Stderr, "[nightgauge] autonomous.enabled_repos applied: %v\n", enabled)
							}
						}

						// Wire dispatcher based on pipeline.executor config.
						// Cloud dispatcher requires platformURL and apiKey.
						if cfg.PipelineExecutor != nil && cfg.PipelineExecutor.ExecutorType() == "cloud" {
							if cfg.PlatformURL != "" && cfg.APIKey != "" {
								cloudDisp := orchestrator.NewCloudDispatcher(cfg.PlatformURL, schedIdent.Owner, cfg.APIKey)
								autoSched.SetDispatcher(cloudDisp)
								fmt.Fprintf(os.Stderr, "[nightgauge] executor=cloud (platform=%s)\n", cfg.PlatformURL)
							} else {
								fmt.Fprintf(os.Stderr, "[nightgauge] WARNING: pipeline.executor=cloud requires platform_url and api_key; falling back to local\n")
							}
						}

						server.SetAutonomousScheduler(autoSched)
						fmt.Fprintf(os.Stderr, "[nightgauge] autonomous scheduler ready (%d repos)\n", len(repoConfigs))
					}
				}
			} else if cfgErr == nil {
				// Scheduler could not be attached. Name what was missing and how to
				// fix it so the operator is not left guessing why epic enqueue
				// returns "scheduler not configured" over IPC (#3860 AC #2).
				detail := schedIdent.Detail
				if detail == "" {
					detail = "no usable owner + project.number"
				}
				log.Printf("serve: pipeline scheduler NOT attached — %s. Add .nightgauge/config.yaml (owner + project.number) at the workspace root, or run `nightgauge workspace-init` to scaffold the manifest. Scheduler-backed actions (epic enqueue) will be rejected until configured.", detail)
			}

			// Set up signal handling for graceful shutdown
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Startup orphan recovery: reset any board items left stuck "In
			// progress" from a previous crashed session. Runs once at backend
			// startup so users are not required to click "Start Autonomous"
			// for recovery to occur. Idempotent and no-op when nothing stuck.
			// Runs in a goroutine so GitHub calls do not block IPC startup.
			if autoSched != nil {
				go autoSched.RecoverOrphanedRunning(ctx)
				// Hot-reload watcher: re-apply autonomous.enabled_repos when
				// ~/.nightgauge/config.yaml changes. Without this, direct
				// edits to the YAML silently no-op until the user restarts
				// autonomous or toggles a repo checkbox in the extension UI
				// (which fires autonomous.updateAllowlist over IPC).
				go orchestrator.WatchAutonomousConfig(ctx, autoSched, workspaceRoot)
			}

			// Action Center platform bridge (ADR 015 §C/§E, #330). The local
			// `.nightgauge/attention/` store is the single authoritative writer;
			// this is the additive client↔platform sync #330 identified as missing.
			// Gated on a configured platform + license key and offline-safe (each
			// service no-ops while the client is offline), so a fully local
			// pipeline sees zero behavior change:
			//   1. Uploader: mirror open + resolved DecisionRequests to
			//      PUT /v1/attention/sync (idempotent by id) — per-transition push
			//      plus a periodic reconciliation sweep.
			//   2. Consumer: consume dashboard-relayed `attention_resolve` commands
			//      off the agent-command bus, apply through the store's single
			//      writer (CAS: a local resolve wins; the late command is acked as
			//      already-resolved), execute the bound verb, and ack via
			//      POST /v1/agents/:agentId/commands/:commandId/ack. The resolved
			//      state syncs back via the uploader's transition subscription, and
			//      the store's existing attention.event push updates every surface
			//      live — no second event emitter.
			if autoSched != nil && platformClient != nil && licenseKey != "" {
				if store := autoSched.Attention(); store != nil {
					// Attach the sync uploader immediately in MIRROR-ONLY mode: the
					// body omits `agent_id` until the daemon registers as a platform
					// agent. The machine id is NOT a registered agent, and sending it
					// as `agent_id` violates the platform's decision_requests → agents
					// FK and 500s every sweep (#341). Mirroring (machine-id scoped)
					// works and never 500s in this mode.
					attnSync := platform.NewAttentionSyncService(platformClient)
					attnSync.Attach(ctx, store)
					log.Printf("[nightgauge] Action Center platform bridge active (attention sync attached; mirror-only until agent registration succeeds)")

					// Register in the background with retry/backoff, then late-bind
					// the platform-assigned agent id onto the sync + command poller +
					// heartbeat. Runs in a goroutine so an offline start self-heals
					// without blocking IPC startup.
					reg := platform.NewAgentRegistrationService(platformClient, version)
					go runAttentionAgentRegistration(ctx, reg, attnSync, platformClient, server)
				}
			}

			// Start the inbound webhook receiver alongside the IPC server.
			// Start(ctx) blocks on ctx.Done() and gracefully drains, so the
			// existing signal handler (cancel) terminates it without an
			// extra teardown branch.
			if inboundRecv != nil {
				go func() {
					if err := inboundRecv.Start(ctx); err != nil {
						log.Printf("inbound webhook server exited: %v", err)
					}
				}()
			}

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			go func() {
				sig := <-sigCh
				log.Printf("Received signal %v — initiating graceful shutdown", sig)
				cancel()
			}()

			err := server.Run(ctx)
			if err != nil {
				log.Printf("IPC server exited with error: %v", err)
			} else {
				log.Printf("IPC server exited cleanly")
			}
			return err
		},
	}

	cmd.Flags().StringVar(&workspaceDir, "workspace", "", "Workspace root directory (default: CWD)")
	cmd.Flags().StringVar(&platformURL, "platform-url", os.Getenv("NIGHTGAUGE_PLATFORM_URL"), "Platform API base URL")
	cmd.Flags().StringVar(&apiKey, "api-key", os.Getenv("NIGHTGAUGE_API_KEY"), "Platform API key")
	cmd.Flags().StringVar(&licenseKey, "license-key", os.Getenv("NIGHTGAUGE_LICENSE_KEY"), "License key")
	cmd.Flags().StringVar(&githubGraphQLURL, "github-graphql-url", "", "Override GitHub GraphQL URL (for tests only)")
	cmd.Flags().MarkHidden("github-graphql-url") //nolint:errcheck

	return cmd
}

// schedulerAdapter adapts orchestrator.Scheduler to executor.SchedulerIface.
// Required because executor.QueueEntry and orchestrator.QueueEntry are identical
// structs in different packages (to avoid import cycles).
type schedulerAdapter struct {
	sched *orchestrator.Scheduler
}

func (a *schedulerAdapter) QueueAdd(entries ...executor.QueueEntry) {
	oEntries := make([]orchestrator.QueueEntry, len(entries))
	for i, e := range entries {
		oEntries[i] = orchestrator.QueueEntry{
			Repo:        e.Repo,
			IssueNumber: e.IssueNumber,
			Priority:    e.Priority,
			RemoteRunID: e.RemoteRunID,
		}
	}
	a.sched.QueueAdd(oEntries...)
}

// issueGetterAdapter adapts github.IssueService to executor.IssueGetterIface.
type issueGetterAdapter struct {
	svc *gh.IssueService
}

func (a *issueGetterAdapter) GetIssue(ctx context.Context, owner, repo string, number int) (interface{}, error) {
	issue, err := a.svc.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, nil
	}
	return issue, nil
}

// --- health command ---

func healthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Pipeline health analysis",
	}
	cmd.AddCommand(healthTrendsCmd(), healthGateMetricsCmd())
	return cmd
}

func healthTrendsCmd() *cobra.Command {
	var limit int
	var outputJSON bool

	cmd := &cobra.Command{
		Use:     "trends",
		Short:   "Read the last N health trend entries",
		Example: "  nightgauge health trends --limit 10 --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			entries, err := healthpkg.ReadTrends(workdir, limit)
			if err != nil {
				return fmt.Errorf("read trends: %w", err)
			}
			if outputJSON {
				return printJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("No health trend data found.")
				return nil
			}
			for _, e := range entries {
				fmt.Printf("%s | Issue #%d | Score: %.0f | Findings: %d\n",
					e.Timestamp, e.IssueNumber, e.OverallScore, len(e.Findings))
			}
			fmt.Printf("\nTotal: %d entries\n", len(entries))
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Last N entries (0 = all)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func healthGateMetricsCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:     "gate-metrics",
		Short:   "Aggregate gate metrics with hit rates",
		Example: "  nightgauge health gate-metrics --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			entries, err := healthpkg.ReadGateMetrics(workdir)
			if err != nil {
				return fmt.Errorf("read gate metrics: %w", err)
			}
			aggs := healthpkg.AggregateGateMetrics(entries)
			if outputJSON {
				return printJSON(aggs)
			}
			if len(aggs) == 0 {
				fmt.Println("No gate metrics found.")
				return nil
			}
			fmt.Printf("%-20s %12s %10s %10s %14s\n", "Gate", "Invocations", "Catches", "Hit Rate", "Avg Duration")
			fmt.Println(strings.Repeat("-", 72))
			for _, a := range aggs {
				fmt.Printf("%-20s %12d %10d %9.1f%% %12.0fms\n",
					a.GateName, a.Invocations, a.Catches, a.HitRate*100, a.AverageDuration)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// --- cost command ---

func costCmd() *cobra.Command {
	var complexityScore int

	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Estimate pipeline cost for an issue",
		Example: `  nightgauge cost --complexity 5
  nightgauge cost --complexity 8`,
		RunE: func(cmd *cobra.Command, args []string) error {
			stages := []string{
				"issue-pickup", "feature-planning", "feature-dev",
				"feature-validate", "pr-create", "pr-merge",
			}

			// Also compute model routing for context
			estimator := complexity.NewEstimator()
			cplx := complexity.Score{Value: complexityScore, SizeLabel: "M"}
			_ = estimator // used for type reference

			cwd, _ := os.Getwd()
			router := routing.NewRouter(nil, cwd)
			rec := router.Route(cmd.Context(), "feature-dev", cplx)

			est := tokens.EstimateCost(stages, complexityScore)

			fmt.Printf("Cost Estimate (complexity: %d/10)\n", complexityScore)
			fmt.Printf("================================\n\n")
			fmt.Printf("Model recommendation: %s\n", rec.Model)
			fmt.Printf("Reasoning: %s\n\n", rec.Reasoning)

			fmt.Printf("%-20s %-25s %10s %8s\n", "Stage", "Model", "Cost", "Minutes")
			fmt.Println(strings.Repeat("-", 65))
			for _, s := range est.StageBreakdown {
				fmt.Printf("%-20s %-25s $%8.4f %7.1f\n", s.Stage, s.Model, s.CostUSD, s.Minutes)
			}
			fmt.Println(strings.Repeat("-", 65))
			fmt.Printf("%-45s $%8.4f %7d\n", "TOTAL", est.TotalCostUSD, est.TotalDuration)
			fmt.Printf("\nConfidence: %s\n", est.Confidence)

			return nil
		},
	}

	cmd.Flags().IntVar(&complexityScore, "complexity", 5, "Complexity score (1-10)")
	cmd.AddCommand(costByClassCmd())
	return cmd
}

// costByClassCmd implements `nightgauge cost by-class [--days N] [--json]`.
// It reads the recorded pipeline run history and reports cost (p50/p95/mean) and
// duration (p50/p95) grouped by the authoritative change_class (#4129) — the
// measurement loop that proves trivial changes (docs/config) cost less than
// source changes. Records without a recorded change_class (pre-#4129 runs)
// bucket under "unknown".
func costByClassCmd() *cobra.Command {
	var (
		outputJSON bool
		workdir    string
		days       int
		since      string
		until      string
	)

	cmd := &cobra.Command{
		Use:          "by-class",
		Short:        "Report recorded pipeline cost/duration grouped by change_class",
		SilenceUsage: true,
		Example: `  nightgauge cost by-class
  nightgauge cost by-class --days 90 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}
			records, err := pipeline.LoadHistory(workdir, since, until, days)
			if err != nil {
				return fmt.Errorf("load run history: %w", err)
			}
			res := pipeline.AggregateCostByClass(records)

			if outputJSON {
				return printJSON(res)
			}

			if res.RunsAnalyzed == 0 {
				fmt.Println("No pipeline run history found. Run a pipeline (or widen --days) and retry.")
				return nil
			}
			fmt.Printf("Cost by change_class — %d run(s) analyzed\n", res.RunsAnalyzed)
			fmt.Printf("%-12s %5s %12s %12s %12s %10s %10s\n",
				"class", "runs", "cost_p50", "cost_p95", "cost_mean", "dur_p50", "dur_p95")
			fmt.Println(strings.Repeat("-", 78))
			for _, c := range res.Classes {
				fmt.Printf("%-12s %5d %11.4f$ %11.4f$ %11.4f$ %9.1fm %9.1fm\n",
					c.ChangeClass, c.Runs, c.CostP50USD, c.CostP95USD, c.CostMeanUSD,
					float64(c.DurationP50Ms)/60000.0, float64(c.DurationP95Ms)/60000.0)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	cmd.Flags().IntVar(&days, "days", 30, "Look back this many days of run history")
	cmd.Flags().StringVar(&since, "since", "", "Start date (YYYY-MM-DD); overrides --days when set")
	cmd.Flags().StringVar(&until, "until", "", "End date (YYYY-MM-DD)")
	return cmd
}

// --- hook command ---

func hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Claude Code hook implementations",
	}
	cmd.AddCommand(
		hookWorkflowGateCmd(),
		hookStopVerifyCmd(),
		hookFormatCmd(),
		hookInjectContextCmd(),
		hookNotifyCmd(),
		hookCheckDepsCmd(),
		hookCheckVersionCmd(),
		hookSanitizePromptCmd(),
		hookPostMergeCmd(),
		hookSkillUsageCmd(),
		hookCarefulGateCmd(),
		hookStageGateCmd(),
	)
	return cmd
}

func hookWorkflowGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "workflow-gate",
		Short:        "Evaluate PreToolUse workflow gate (reads JSON from stdin)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := os.ReadFile("/dev/stdin")
			if err != nil {
				return printJSON(hooks.Allow())
			}
			// Load sanitization mode from config (default: warn)
			mode := config.SanitizationModeWarn
			workdir, _ := os.Getwd()
			if cfg, loadErr := config.Load(workdir); loadErr == nil && cfg != nil && cfg.Sanitization != nil {
				mode = cfg.Sanitization.ResolvedMode()
			}
			return printJSON(hooks.EvaluateGate(input, mode))
		},
	}
}

func hookSkillUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "skill-usage",
		Short:        "Log a Skill-tool invocation for catalog telemetry (PreToolUse:Skill, reads JSON from stdin)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := os.ReadFile("/dev/stdin")
			if err != nil {
				return printJSON(hooks.Allow())
			}
			return printJSON(hooks.LogSkillUsage(input))
		},
	}
}

func hookCarefulGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "careful-gate",
		Short:        "Block prod-destructive Bash commands while careful mode is on (PreToolUse:Bash, reads JSON from stdin)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := os.ReadFile("/dev/stdin")
			if err != nil {
				return printJSON(hooks.Allow())
			}
			return printJSON(hooks.EvaluateCarefulGate(input))
		},
	}
}

func hookStageGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "stage-gate",
		Short:        "Fence analysis pipeline stages from mutating git/forge state (PreToolUse:Bash, reads JSON from stdin)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := os.ReadFile("/dev/stdin")
			if err != nil {
				return printJSON(hooks.Allow())
			}
			return printJSON(hooks.EvaluateStageGate(input))
		},
	}
}

func hookStopVerifyCmd() *cobra.Command {
	var workdir string

	cmd := &cobra.Command{
		Use:   "stop-verify",
		Short: "Evaluate Stop hook (check PLAN.md completion)",
		Long: `Evaluate the Stop hook against PLAN.md completion status.

Output format conforms to Claude Code's canonical Stop hook contract:
  - All tasks complete  → silent (no stdout, exit 0) — Claude Code accepts the stop
  - Tasks incomplete    → {"decision":"block","reason":"..."} (exit 0) — Claude Code keeps the agent working

The legacy ` + "`" + `{"ok":...,"reason":...}` + "`" + ` shape was non-conformant and caused
Claude Code to fire a spurious "stop-hook-error" notification on every stage
exit (see #3605 retro). The sentinel file at
.nightgauge/pipeline/stop-hook-status-<N>.json is still written on the
not-OK path for the Go scheduler's uncommitted-work recovery — that path is
internal and unaffected by this output-format change.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				workdir, _ = os.Getwd() // os.Getwd failure falls back to empty string; Load() handles missing workdir
			}
			out, err := hooks.EvaluateStopHookOutput(workdir)
			if err != nil {
				return err
			}
			if len(out) > 0 {
				if _, werr := os.Stdout.Write(out); werr != nil {
					return werr
				}
				// Final newline so log capture is line-oriented.
				if _, werr := os.Stdout.Write([]byte("\n")); werr != nil {
					return werr
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

func hookFormatCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:          "format",
		Short:        "Run formatter on a saved file (PostToolUse for Write/Edit)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return printJSON(hooks.EvaluateFormat(filePath))
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "File path to format")
	_ = cmd.MarkFlagRequired("file") // cobra MarkFlagRequired never errors for known flags
	return cmd
}

func hookInjectContextCmd() *cobra.Command {
	var workdir string

	cmd := &cobra.Command{
		Use:          "inject-context",
		Short:        "Re-inject branch, issue, and plan context into session",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				workdir, _ = os.Getwd() // os.Getwd failure falls back to empty string; Load() handles missing workdir
			}
			return printJSON(hooks.EvaluateContext(workdir))
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

func hookNotifyCmd() *cobra.Command {
	var (
		event   string
		message string
	)

	cmd := &cobra.Command{
		Use:          "notify",
		Short:        "Send desktop notification",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return printJSON(hooks.EvaluateNotify(hooks.NotifyEvent(event), message))
		},
	}

	cmd.Flags().StringVar(&event, "event", "pipeline_complete", "Notification event type")
	cmd.Flags().StringVar(&message, "message", "", "Notification message")
	_ = cmd.MarkFlagRequired("message") // cobra MarkFlagRequired never errors for known flags
	return cmd
}

func hookCheckDepsCmd() *cobra.Command {
	var (
		checkOnly bool
		owner     string
		repo      string
	)

	cmd := &cobra.Command{
		Use:          "check-deps [issue-number]",
		Short:        "Check issue blockedBy dependencies on GitHub",
		Long:         "Queries GitHub's blockedBy relationships for the given issue. Without an issue number, falls back to checking local tool dependencies.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// No issue number provided — fall back to legacy tool-availability check
			if len(args) == 0 {
				return printJSON(hooks.EvaluateDeps())
			}

			issueNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}
			issueSvc := gh.NewIssueService(client)

			result, err := hooks.EvaluateIssueDeps(cmd.Context(), issueSvc, owner, repo, issueNumber)
			if err != nil {
				return err
			}

			if checkOnly && result.HasOpenDependencies {
				// Print the result, then exit non-zero so callers can filter
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
				os.Exit(1)
			}

			return printJSON(result)
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check-only", false, "Exit non-zero if issue has open blockers")
	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "Repository owner")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name")

	return cmd
}

func hookCheckVersionCmd() *cobra.Command {
	var (
		pluginVersion string
		skillVersion  string
	)

	cmd := &cobra.Command{
		Use:          "check-version",
		Short:        "Check version consistency between plugin.json and SKILL.md",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return printJSON(hooks.EvaluateVersionCheck(pluginVersion, skillVersion))
		},
	}

	cmd.Flags().StringVar(&pluginVersion, "plugin-version", "", "Version from plugin.json")
	cmd.Flags().StringVar(&skillVersion, "skill-version", "", "Version from SKILL.md")
	return cmd
}

func hookSanitizePromptCmd() *cobra.Command {
	var input string

	cmd := &cobra.Command{
		Use:          "sanitize-prompt",
		Short:        "Sanitize prompt input for injection attempts",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			match := hooks.MatchPatterns(input, hooks.CategoryPromptInjection)
			if match != nil {
				return printJSON(hooks.Block("Prompt injection detected: " + match.Pattern))
			}
			return printJSON(hooks.Allow())
		},
	}

	cmd.Flags().StringVar(&input, "input", "", "Text to sanitize")
	_ = cmd.MarkFlagRequired("input") // cobra MarkFlagRequired never errors for known flags
	return cmd
}

func hookPostMergeCmd() *cobra.Command {
	var (
		issueNumber   int
		owner         string
		repo          string
		projectNumber int
		prNumber      int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "post-merge",
		Short: "Run post-merge epic completion check",
		Long: `After a PR merges and its linked issue closes, check whether the parent
epic is now fully complete. If all sub-issues are closed, the epic is
auto-closed and moved to Done on the project board.

This check is non-blocking: failures are logged to stderr but do not
cause the command to exit non-zero.`,
		Example: `  nightgauge hook post-merge --issue 2358 --owner nightgauge --repo nightgauge
  nightgauge hook post-merge --issue 2358 --owner nightgauge --repo nightgauge --project 5 --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			issueSvc := gh.NewIssueService(client)
			epicSvc := gh.NewEpicService(client)
			prSvc := gh.NewPRService(client)

			// Board syncer is only wired when a project is configured; the hook
			// no-ops the board-Done sync (#3981) when it is nil.
			var boardSvc hooks.BoardSyncer
			if projectNumber > 0 {
				boardSvc = gh.NewProjectService(client, ownerPart, projectNumber)
			}

			result := hooks.EvaluatePostMerge(cmd.Context(), issueSvc, issueSvc, epicSvc, prSvc, boardSvc, hooks.PostMergeInput{
				IssueNumber:     issueNumber,
				RepositoryOwner: ownerPart,
				RepositoryName:  repoPart,
				ProjectNumber:   projectNumber,
				PRNumber:        prNumber,
			})

			// (#4151) Seed a pending survival record for an eligible single-issue
			// merge, mirroring the in-process scheduler path so the deterministic
			// plugin route also captures the breadcrumb. Best-effort/non-blocking,
			// rooted at the current working directory.
			if result.SurvivalEligible {
				if wd, wdErr := os.Getwd(); wdErr == nil {
					store := survival.NewStore(wd)
					rec := survival.NewPending(ownerPart+"/"+repoPart, issueNumber, prNumber, result.MergedCommitSha, result.MergedAt, "")
					if _, appErr := store.Append(rec); appErr != nil {
						fmt.Fprintf(os.Stderr, "Warning: post-merge survival capture failed: %v\n", appErr)
					}
				}
			}

			if outputJSON {
				return printJSON(result)
			}

			switch result.Reason {
			case "no_parent":
				fmt.Printf("Issue #%d has no parent epic — skipping auto-close check.\n", issueNumber)
			case "closed":
				fmt.Printf("Epic #%d auto-closed (all sub-issues complete).\n", result.EpicNumber)
			case "skipped":
				fmt.Printf("Epic #%d skipped: %s\n", result.EpicNumber, result.Reason)
			case "issue_fetch_error", "auto_close_error":
				fmt.Fprintf(os.Stderr, "Warning: post-merge check failed: %s\n", result.Error)
			default:
				fmt.Printf("Epic #%d: %s\n", result.EpicNumber, result.Reason)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number closed by the merge")
	cmd.Flags().StringVar(&owner, "owner", "", "Repository owner (or owner/repo)")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository name")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "GitHub Project number for board sync (optional)")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number to verify is MERGED before closing issue (optional; 0 skips verification)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	_ = cmd.MarkFlagRequired("issue") // cobra MarkFlagRequired never errors for known flags
	_ = cmd.MarkFlagRequired("owner") // cobra MarkFlagRequired never errors for known flags
	return cmd
}

// --- learn command ---

func learnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "learn",
		Short: "Self-improvement loop operations",
	}
	cmd.AddCommand(learnTuneCmd(), learnAuditCmd())
	return cmd
}

func learnTuneCmd() *cobra.Command {
	var workdir string

	cmd := &cobra.Command{
		Use:          "tune",
		Short:        "Run tuning optimizer on recorded outcomes",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				workdir, _ = os.Getwd() // os.Getwd failure falls back to empty string; Load() handles missing workdir
			}
			recorder := learning.NewRecorder(workdir)
			report, err := recorder.Calibrate()
			if err != nil {
				return err
			}

			tuner := learning.NewTuner(workdir, learning.DefaultTunerConfig())
			param := learning.TuningParam{
				Name: "size_accuracy", Current: report.SizeAccuracy,
				Target: 0.8, MinValue: 0.0, MaxValue: 1.0,
			}
			result := tuner.Tune(param, report.SizeAccuracy, nil)

			output := map[string]interface{}{
				"calibration": report,
				"tuning":      result,
			}
			return printJSON(output)
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	return cmd
}

func learnAuditCmd() *cobra.Command {
	var workdir string

	cmd := &cobra.Command{
		Use:          "audit",
		Short:        "Show tuning audit trail",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				workdir, _ = os.Getwd() // os.Getwd failure falls back to empty string; Load() handles missing workdir
			}
			tuner := learning.NewTuner(workdir, learning.DefaultTunerConfig())
			entries, err := tuner.LoadAudit()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Println("No tuning audit entries found.")
				return nil
			}
			return printJSON(entries)
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	return cmd
}

// --- suggest command ---

func suggestCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "suggest",
		Short:        "Get suggestions for current pipeline state",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Read findings from stdin as JSON array
			input, err := os.ReadFile("/dev/stdin")
			if err != nil || len(input) == 0 {
				fmt.Println("[]")
				return nil
			}
			var findings []suggestions.Finding
			if err := json.Unmarshal(input, &findings); err != nil {
				return fmt.Errorf("parse findings: %w", err)
			}

			engine := suggestions.NewEngine()
			result := engine.Generate(findings)
			return printJSON(result)
		},
	}
}

// --- failure command ---

func failureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "failure",
		Short: "Failure classification and analysis",
	}
	cmd.AddCommand(failureClassifyCmd())
	return cmd
}

func failureClassifyCmd() *cobra.Command {
	var (
		stage    string
		exitCode int
		stderr   string
	)

	cmd := &cobra.Command{
		Use:          "classify",
		Short:        "Classify a pipeline failure",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			classifier := failure.NewClassifier()
			result := classifier.Classify(stage, exitCode, stderr)
			return printJSON(result)
		},
	}

	cmd.Flags().StringVar(&stage, "stage", "", "Pipeline stage where failure occurred")
	cmd.Flags().IntVar(&exitCode, "exit-code", 1, "Process exit code")
	cmd.Flags().StringVar(&stderr, "stderr", "", "Error output text")
	return cmd
}

// --- teams command ---

func teamsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "Agent team infrastructure",
	}
	cmd.AddCommand(teamsWavesCmd(), teamsDepsCmd(), teamsBudgetCmd(), teamsConflictsCmd())
	return cmd
}

func teamsWavesCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "calculate-waves",
		Short:        "Compute execution waves from issues JSON (stdin)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := os.ReadFile("/dev/stdin")
			if err != nil || len(input) == 0 {
				return fmt.Errorf("read issues from stdin")
			}

			var data struct {
				Issues []teams.SubIssue `json:"issues"`
				Deps   map[int][]int    `json:"deps"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return fmt.Errorf("parse input: %w", err)
			}

			waves, err := teams.CalculateWaves(data.Issues, data.Deps)
			if err != nil {
				return err
			}
			return printJSON(waves)
		},
	}
}

func teamsDepsCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "detect-deps",
		Short:        "Detect inter-issue dependencies (stdin JSON)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := os.ReadFile("/dev/stdin")
			if err != nil || len(input) == 0 {
				return fmt.Errorf("read issues from stdin")
			}

			var data struct {
				Issues  []teams.SubIssue `json:"issues"`
				Sources []string         `json:"sources"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return fmt.Errorf("parse input: %w", err)
			}

			deps := teams.DetectDependencies(data.Issues, data.Sources, teams.DefaultDependencyConfig())
			return printJSON(deps)
		},
	}
}

func teamsBudgetCmd() *cobra.Command {
	var (
		totalBudget int
		strategy    string
	)

	cmd := &cobra.Command{
		Use:          "split-budget",
		Short:        "Allocate token budget across issues (stdin JSON)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := os.ReadFile("/dev/stdin")
			if err != nil || len(input) == 0 {
				return fmt.Errorf("read issues from stdin")
			}

			var issues []teams.SubIssue
			if err := json.Unmarshal(input, &issues); err != nil {
				return fmt.Errorf("parse issues: %w", err)
			}

			result := teams.SplitBudget(issues, totalBudget, teams.BudgetStrategy(strategy))
			return printJSON(result)
		},
	}

	cmd.Flags().IntVar(&totalBudget, "budget", 100000, "Total token budget")
	cmd.Flags().StringVar(&strategy, "strategy", "proportional", "Allocation strategy: proportional or equal")
	return cmd
}

func teamsConflictsCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "detect-conflicts",
		Short:        "Detect file conflicts between parallel agents (stdin JSON)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := os.ReadFile("/dev/stdin")
			if err != nil || len(input) == 0 {
				return fmt.Errorf("read issues from stdin")
			}

			var issues []teams.SubIssue
			if err := json.Unmarshal(input, &issues); err != nil {
				return fmt.Errorf("parse issues: %w", err)
			}

			conflicts := teams.DetectFileConflicts(issues)
			return printJSON(conflicts)
		},
	}
}

// --- validate command ---

func validateCmd() *cobra.Command {
	var (
		category   string
		reportJSON bool
	)

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Run parallel validation comparing shell scripts and Go binary",
		Example: `  nightgauge validate
  nightgauge validate --category hooks
  nightgauge validate --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			goBinary, _ := os.Executable()
			shellDir := cwd + "/claude-plugins/nightgauge/hooks"

			runner := validation.NewRunner(goBinary, shellDir)

			// Run all registered validations
			runner.RunAll(category)

			report := runner.Report()

			if reportJSON {
				return printJSON(report)
			}

			fmt.Print(validation.FormatReport(report))

			if report.Failed > 0 {
				return fmt.Errorf("%d validation(s) failed", report.Failed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&category, "category", "", "Filter by category (hooks, git, issue, project, pr, pipeline, intelligence)")
	cmd.Flags().BoolVar(&reportJSON, "json", false, "Output report as JSON")

	return cmd
}

// --- pr command ---

func prCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Pull request operations",
	}
	cmd.AddCommand(prCreateCmd(), prViewCmd(), prMergeCmd(), prCIWaitCmd(), prRulesetPrecheckCmd())
	return cmd
}

func prCreateCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		title      string
		body       string
		head       string
		base       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a pull request",
		Example: `  nightgauge pr create --title "Fix bug" --head fix/123 --base main
  nightgauge pr create --title "Feature" --body "Description" --head feat/456`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}
			if head == "" {
				return fmt.Errorf("--head is required")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			repoID, err := client.GetRepositoryID(cmd.Context(), ownerPart, repoPart)
			if err != nil {
				return err
			}

			svc := gh.NewPRService(client)
			pr, err := svc.CreatePR(cmd.Context(), repoID, title, body, head, base)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(pr)
			}

			fmt.Printf("Created PR #%d: %s\n", pr.Number, pr.Title)
			fmt.Printf("URL: %s\n", pr.URL)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().StringVar(&title, "title", "", "PR title (required)")
	cmd.Flags().StringVar(&body, "body", "", "PR body")
	cmd.Flags().StringVar(&head, "head", "", "Head branch (required)")
	cmd.Flags().StringVar(&base, "base", "main", "Base branch")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func prViewCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "view [number]",
		Short: "View a pull request with review and check status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PR number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewPRService(client)
			pr, err := svc.GetPR(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(pr)
			}

			fmt.Printf("PR #%d %s [%s]\n", pr.Number, pr.Title, pr.State)
			fmt.Printf("  %s → %s\n", pr.HeadRef, pr.BaseRef)
			fmt.Printf("  Review: %s  Checks: %s  Mergeable: %s\n",
				pr.ReviewStatus, pr.CheckStatus, pr.Mergeable)
			if pr.IsDraft {
				fmt.Println("  (Draft)")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// prMergeResult holds the JSON output for pr merge.
type prMergeResult struct {
	Merged        bool   `json:"merged"`
	SHA           string `json:"sha"`
	Strategy      string `json:"strategy"`
	BranchDeleted bool   `json:"branch_deleted"`
}

func buildBlockedMergeError(issueNumber int, blocker hooks.OpenDependency) string {
	url := fmt.Sprintf("https://github.com/%s/issues/%d", blocker.Repo, blocker.Number)
	return fmt.Sprintf(
		"Cannot merge: #%d is blocked by #%d (%s) — resolve %s first",
		issueNumber,
		blocker.Number,
		strings.ToUpper(blocker.State),
		url,
	)
}

func checkPRMergeBlockers(
	ctx context.Context,
	fetcher hooks.IssueFetcher,
	owner string,
	repo string,
	issueNumber int,
	force bool,
) error {
	if issueNumber <= 0 || force {
		return nil
	}

	result, err := hooks.EvaluateIssueDeps(ctx, fetcher, owner, repo, issueNumber)
	if err != nil {
		return fmt.Errorf("check blockedBy for issue #%d: %w", issueNumber, err)
	}
	if !result.HasOpenDependencies || len(result.OpenDependencies) == 0 {
		return nil
	}

	return fmt.Errorf("%s", buildBlockedMergeError(issueNumber, result.OpenDependencies[0]))
}

func prMergeCmd() *cobra.Command {
	var (
		owner        string
		repo         string
		strategy     string
		deleteBranch bool
		issueNumber  int
		force        bool
		outputJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "merge [pr-number]",
		Short: "Merge a pull request with configurable strategy",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge pr merge 42
  nightgauge pr merge 42 --strategy squash --delete-branch --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PR number: %s", args[0])
			}

			strategy = strings.ToUpper(strategy)
			switch strategy {
			case "SQUASH", "MERGE", "REBASE":
				// valid
			default:
				return fmt.Errorf("invalid strategy %q: must be squash, merge, or rebase", strings.ToLower(strategy))
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewPRService(client)
			issueSvc := gh.NewIssueService(client)

			pr, err := svc.GetPR(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return fmt.Errorf("get PR #%d in %s/%s: %w", number, ownerPart, repoPart, enrichError(err))
			}

			if pr.State == "MERGED" {
				if outputJSON {
					return printJSON(&prMergeResult{Merged: true, SHA: "", Strategy: strings.ToLower(strategy), BranchDeleted: false})
				}
				fmt.Printf("PR #%d is already merged\n", number)
				return nil
			}

			if pr.State != "OPEN" {
				return fmt.Errorf("PR #%d is %s and cannot be merged", number, strings.ToLower(pr.State))
			}

			if force && issueNumber > 0 && !outputJSON {
				fmt.Fprintf(os.Stderr, "WARNING: bypassing blockedBy merge guard for issue #%d via --force\n", issueNumber)
			}
			if err := checkPRMergeBlockers(cmd.Context(), issueSvc, ownerPart, repoPart, issueNumber, force); err != nil {
				return err
			}

			sha, err := svc.MergePRWithStrategy(cmd.Context(), pr.NodeID, strategy)
			if err != nil {
				return fmt.Errorf("merge PR #%d in %s/%s: %w", number, ownerPart, repoPart, enrichError(err))
			}

			branchDeleted := false
			if deleteBranch && pr.HeadRef != "" {
				if delErr := svc.DeleteBranch(cmd.Context(), ownerPart, repoPart, pr.HeadRef); delErr != nil {
					// Non-fatal: branch may have been auto-deleted
					_ = delErr
				} else {
					branchDeleted = true
				}
			}

			result := &prMergeResult{
				Merged:        true,
				SHA:           sha,
				Strategy:      strings.ToLower(strategy),
				BranchDeleted: branchDeleted,
			}

			if outputJSON {
				return printJSON(result)
			}

			fmt.Printf("Merged PR #%d using %s strategy\n", number, strings.ToLower(strategy))
			if branchDeleted {
				fmt.Printf("Deleted branch: %s\n", pr.HeadRef)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().StringVar(&strategy, "strategy", "squash", "Merge strategy: squash, merge, or rebase")
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", true, "Delete head branch after merge (pass --delete-branch=false to preserve)")
	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number for blockedBy pre-merge guard")
	cmd.Flags().BoolVar(&force, "force", false, "Bypass the blockedBy pre-merge guard")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func prCIWaitCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		timeoutSec int
		pollSecs   int
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "ci-wait [pr-number]",
		Short: "Poll CI checks for a PR until pass/fail/timeout",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge pr ci-wait 42
  nightgauge pr ci-wait 42 --timeout 600 --poll 15 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PR number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewCIService(client)

			cfg := gh.WaitConfig{
				Timeout:      time.Duration(timeoutSec) * time.Second,
				PollInterval: time.Duration(pollSecs) * time.Second,
			}

			if !outputJSON {
				cfg.OnProgress = func(status *gh.CheckStatus) {
					fmt.Printf("[%ds] CI checks: %s\n", status.ElapsedSecs, status.State)
				}
			}

			result, err := svc.WaitForChecks(cmd.Context(), ownerPart, repoPart, number, cfg)
			if err != nil && result == nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			if result != nil {
				switch result.State {
				case "SUCCESS":
					fmt.Printf("CI checks passed for PR #%d (%ds)\n", number, result.ElapsedSecs)
				case "FAILURE", "ERROR":
					fmt.Printf("CI checks failed for PR #%d: %s (%ds)\n", number, result.State, result.ElapsedSecs)
				case "TIMEOUT":
					fmt.Printf("CI checks timed out for PR #%d after %ds\n", number, result.ElapsedSecs)
				}
			}

			return err
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 600, "Timeout in seconds")
	cmd.Flags().IntVar(&pollSecs, "poll", 30, "Poll interval in seconds")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// --- pr ruleset-precheck command ---

func prRulesetPrecheckCmd() *cobra.Command {
	var (
		owner       string
		repo        string
		autoSatisfy bool
		outputJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "ruleset-precheck <pr-number>",
		Short: "Detect and auto-satisfy branch rulesets blocking merge",
		Long:  "Detects GitHub branch rulesets that would block a PR merge. Optionally auto-satisfies known blockers (e.g., requesting Copilot review).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PR number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewRulesetService(client)

			result, err := svc.CheckRulesets(cmd.Context(), ownerPart, repoPart, prNumber)
			if err != nil {
				return err
			}

			if autoSatisfy && len(result.Blockers) > 0 {
				resolved, err := svc.SatisfyRulesets(cmd.Context(), ownerPart, repoPart, prNumber, result.Blockers)
				if err != nil {
					return fmt.Errorf("auto-satisfy failed: %w", err)
				}
				result.ResolvedBlockers = resolved
				result.Blockers = subtractStrings(result.Blockers, resolved)
				result.AllowedToMerge = len(result.Blockers) == 0
				if result.AllowedToMerge {
					result.Message = "All detected ruleset blockers were auto-satisfied — safe to merge."
				} else {
					result.Message = fmt.Sprintf("Unresolved ruleset blockers on %q after auto-satisfy: %s",
						result.BaseRef, strings.Join(result.Blockers, ", "))
				}
			}

			// Config-mismatch probe (#184): a required check produced by a
			// continue-on-error job reports its real conclusion to branch
			// rules, so it can never turn green while the underlying command
			// fails — a non-retryable config blocker, not transient CI.
			ciSvc := gh.NewCIService(client)
			requiredChecks := result.RequiredChecks
			if union, unionErr := ciSvc.GetRequiredCheckNames(cmd.Context(), ownerPart, repoPart, result.BaseRef); unionErr == nil && len(union) > 0 {
				requiredChecks = union
			}
			var mismatches []cipkg.RequiredCheckConfigMismatch
			if workdir, wdErr := os.Getwd(); wdErr == nil {
				mismatches = cipkg.DetectRequiredCheckConfigMismatches(workdir, requiredChecks)
			}
			if len(mismatches) > 0 {
				prSvc := gh.NewPRService(client)
				if pr, prErr := prSvc.GetPR(cmd.Context(), ownerPart, repoPart, prNumber); prErr == nil {
					if checks, chErr := ciSvc.GetIndividualCheckRuns(cmd.Context(), ownerPart, repoPart, pr.HeadRef); chErr == nil {
						for i := range mismatches {
							mismatches[i].Failing = checkRunFailing(checks, mismatches[i].Check)
						}
					}
				}
				for _, m := range mismatches {
					if !m.Failing {
						continue
					}
					result.Blockers = append(result.Blockers, "required-check-config-mismatch:"+m.Check)
					result.AllowedToMerge = false
					result.Message = "CONFIG BLOCKER (non-retryable): " + m.Remediation +
						" Do not retry the merge — escalate to a human."
				}
			}

			if outputJSON {
				return printJSON(struct {
					*gh.RulesetCheckResult
					ConfigMismatches []cipkg.RequiredCheckConfigMismatch `json:"config_mismatches,omitempty"`
				}{result, mismatches})
			}

			fmt.Printf("Branch ruleset pre-check for PR #%d (base: %s)\n", prNumber, result.BaseRef)
			if len(requiredChecks) > 0 {
				fmt.Printf("Required status checks (classic + ruleset): %s\n", strings.Join(requiredChecks, ", "))
			}
			for _, m := range mismatches {
				if m.Failing {
					fmt.Printf("CONFIG BLOCKER (non-retryable): %s\n", m.Remediation)
				} else {
					fmt.Printf("Config hazard: %s\n", m.Remediation)
				}
			}
			if len(result.DetectedRules) == 0 {
				fmt.Println("No blocking rulesets detected — safe to merge.")
			} else {
				fmt.Printf("Detected rules: %s\n", strings.Join(result.DetectedRules, ", "))
				if autoSatisfy {
					if len(result.ResolvedBlockers) > 0 {
						fmt.Printf("Auto-satisfied: %s\n", strings.Join(result.ResolvedBlockers, ", "))
					}
					if len(result.Blockers) == 0 {
						fmt.Println("All blockers resolved — safe to merge.")
					} else {
						fmt.Printf("Unresolved blockers: %s\n", strings.Join(result.Blockers, ", "))
					}
				} else {
					fmt.Printf("Blocking rulesets: %s\n", strings.Join(result.Blockers, ", "))
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&autoSatisfy, "auto-satisfy", false, "Attempt to auto-satisfy detected blockers")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// checkRunFailing reports whether the named check run has a terminal
// non-passing conclusion. Names are compared case-insensitively — a ruleset
// context must equal the check-run name for GitHub to count it satisfied.
func checkRunFailing(checks []gh.CheckDetail, name string) bool {
	for _, c := range checks {
		if !strings.EqualFold(c.Name, name) {
			continue
		}
		switch c.Conclusion {
		case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED":
			return true
		}
	}
	return false
}

// --- ci command ---

func ciCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "CI check operations (wait, logs, parity, discover-commands)",
	}
	cmd.AddCommand(ciWaitCmd(), ciLogsCmd(), ciParityCheckCmd(), ciDiscoverCommandsCmd(), ciClassifyCmd(), ciClassifyUISurfaceCmd())
	return cmd
}

// ciClassifyUISurfaceCmd implements
// `nightgauge ci classify-ui-surface --base <ref> --head <ref> [--repo <name>] [--json]`.
// It decides whether a diff touches frontend code in a UI-bearing repo — the
// deterministic trigger for feature-validate's verify-ui gate (#4193). Built on
// the same shared changeClassifier.Classify primitive as ciClassifyCmd
// (CI fast-track) and gate relaxation, following the established one-primitive
// multiple-consumers pattern rather than overloading ClassifyForCI's CI-specific
// semantics.
func ciClassifyUISurfaceCmd() *cobra.Command {
	var (
		outputJSON bool
		base       string
		head       string
		repo       string
		workdir    string
	)

	cmd := &cobra.Command{
		Use:          "classify-ui-surface",
		Short:        "Decide whether a diff (base...head) touches UI-bearing frontend surface",
		SilenceUsage: true,
		Example: `  nightgauge ci classify-ui-surface --base origin/main --head HEAD --json
  nightgauge ci classify-ui-surface --base "$BASE_SHA" --repo acme-dashboard --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if base == "" {
				return fmt.Errorf("--base is required")
			}
			if workdir == "" {
				workdir, _ = os.Getwd()
			}
			repoName := repo
			if repoName == "" {
				repoName = filepath.Base(workdir)
			}
			files, err := cipkg.ChangedFilesFromGit(workdir, base, head)
			if err != nil {
				// Fail-closed here (opposite of CI's fail-open): an unclassifiable
				// diff must NOT silently skip the web gate — record it as touching
				// the surface so the gate runs (or explicitly skips with a reason
				// if no flow exists), never a silent pass.
				res := uiSurfaceResult{TouchesUISurface: true, Reason: fmt.Sprintf("git diff failed (treating as UI-relevant): %v", err), Repo: repoName}
				if outputJSON {
					return printJSON(res)
				}
				fmt.Println(res.Reason)
				return nil
			}
			touches, reason := changeClassifier.TouchesUISurface(files, repoName, changeClassifier.DefaultUIBearingRepos())
			res := uiSurfaceResult{TouchesUISurface: touches, Reason: reason, Repo: repoName}
			if outputJSON {
				return printJSON(res)
			}
			fmt.Printf("touches_ui_surface=%v repo=%s — %s\n", res.TouchesUISurface, res.Repo, res.Reason)
			return nil
		},
	}

	cmd.Flags().StringVar(&base, "base", "", "Base ref/SHA (required)")
	cmd.Flags().StringVar(&head, "head", "HEAD", "Head ref/SHA")
	cmd.Flags().StringVar(&repo, "repo", "", "Repo identifier (default: workdir basename)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// uiSurfaceResult is the JSON shape read by feature-validate's verify-ui gate
// (skills/nightgauge-feature-validate/_includes/verify-ui-gate.md).
type uiSurfaceResult struct {
	TouchesUISurface bool   `json:"touches_ui_surface"`
	Repo             string `json:"repo"`
	Reason           string `json:"reason"`
}

// ciClassifyCmd implements `nightgauge ci classify --base <ref> --head <ref> [--json]`.
// It classifies the diff between base and head into a CI fast-track decision so
// the always-running `changes` gate job can skip the heavy jobs' expensive steps
// on documentation-only changes without ever skipping a required job (which
// would deadlock branch protection). See docs §4127 and internal/ci/classify.go.
func ciClassifyCmd() *cobra.Command {
	var (
		outputJSON bool
		base       string
		head       string
		workdir    string
	)

	cmd := &cobra.Command{
		Use:          "classify",
		Short:        "Classify the diff (base...head) into CI fast-track job decisions",
		SilenceUsage: true,
		Example: `  nightgauge ci classify --base origin/main --head HEAD --json
  nightgauge ci classify --base "$BASE_SHA" --head "$HEAD_SHA" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if base == "" {
				return fmt.Errorf("--base is required")
			}
			if workdir == "" {
				workdir, _ = os.Getwd()
			}
			files, err := cipkg.ChangedFilesFromGit(workdir, base, head)
			if err != nil {
				// Fail-open: an unclassifiable diff runs the full suite so CI never
				// under-tests a change it could not classify.
				res := cipkg.FailOpenResult(fmt.Sprintf("git diff failed (running full CI): %v", err))
				if outputJSON {
					return printJSON(res)
				}
				fmt.Println(res.Reason)
				return nil
			}
			res := cipkg.ClassifyForCI(files)
			if outputJSON {
				return printJSON(res)
			}
			fmt.Printf("change_class=%s run_heavy=%v — %s\n", res.ChangeClass, res.RunHeavy, res.Reason)
			return nil
		},
	}

	cmd.Flags().StringVar(&base, "base", "", "Base ref/SHA (required)")
	cmd.Flags().StringVar(&head, "head", "HEAD", "Head ref/SHA")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// ciParityCheckCmd implements `nightgauge ci-parity check [--json] [--workdir <dir>]`.
func ciParityCheckCmd() *cobra.Command {
	var (
		outputJSON bool
		workdir    string
	)

	cmd := &cobra.Command{
		Use:          "parity-check",
		Short:        "Run CI parity checks matching the project workflow",
		SilenceUsage: true,
		Example: `  nightgauge ci parity-check
  nightgauge ci parity-check --json
  nightgauge ci parity-check --workdir /path/to/project --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			discoverResult, err := cipkg.DiscoverCommands(cmd.Context(), workdir, "")
			if err != nil {
				return fmt.Errorf("discover CI commands: %w", err)
			}

			result, err := cipkg.CheckParity(cmd.Context(), workdir, discoverResult.Commands)
			if err != nil {
				return fmt.Errorf("ci parity check: %w", err)
			}

			if outputJSON {
				return printJSON(result)
			}

			if result.Passed {
				fmt.Printf("CI parity: passed (%d commands)\n", len(result.CommandsRun))
			} else {
				fmt.Printf("CI parity: FAILED (%d failures)\n", len(result.Failures))
				for _, f := range result.Failures {
					fmt.Printf("  FAIL [%s]: %s (exit %d)\n", f.FailureType, f.Command, f.ExitCode)
				}
				return fmt.Errorf("CI parity check failed")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// ciDiscoverCommandsCmd implements `nightgauge ci discover-commands [--workflow <path>] [--json]`.
func ciDiscoverCommandsCmd() *cobra.Command {
	var (
		outputJSON   bool
		workdir      string
		workflowPath string
	)

	cmd := &cobra.Command{
		Use:          "discover-commands",
		Short:        "Parse CI workflow and return run-step commands as JSON",
		SilenceUsage: true,
		Example: `  nightgauge ci discover-commands
  nightgauge ci discover-commands --workflow .github/workflows/ci.yml --json
  nightgauge ci discover-commands --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			result, err := cipkg.DiscoverCommands(cmd.Context(), workdir, workflowPath)
			if err != nil {
				return fmt.Errorf("discover CI commands: %w", err)
			}

			if outputJSON {
				return printJSON(result)
			}

			if result.WorkflowPath != "" {
				fmt.Printf("Workflow: %s\n", result.WorkflowPath)
			}
			fmt.Printf("Framework: %s\n", result.Framework)
			fmt.Printf("Commands (%d):\n", len(result.Commands))
			for _, c := range result.Commands {
				fmt.Printf("  %s\n", c)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	cmd.Flags().StringVar(&workflowPath, "workflow", "", "Path to GitHub Actions workflow YAML (default: auto-discover)")
	return cmd
}

func ciWaitCmd() *cobra.Command {
	var (
		owner        string
		repo         string
		timeoutMins  int
		timeoutSecs  int
		pollSecs     int
		outputJSON   bool
		requiredOnly bool
	)

	cmd := &cobra.Command{
		Use:   "wait [pr-number]",
		Short: "Poll CI check status until pass/fail/timeout",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge ci wait 42
  nightgauge ci wait 42 --timeout 60 --poll 15
  nightgauge ci wait 42 --required-only
  nightgauge ci wait 42 --timeout-secs 90 --json   # bounded chunk for tool-budget-constrained callers (#187)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PR number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewCIService(client)

			cfg := gh.WaitConfig{
				Timeout:      time.Duration(timeoutMins) * time.Minute,
				PollInterval: time.Duration(pollSecs) * time.Second,
			}
			// --timeout-secs overrides --timeout: bounded-chunk mode for
			// callers running inside an agent tool budget (#187).
			if timeoutSecs > 0 {
				cfg.Timeout = time.Duration(timeoutSecs) * time.Second
				if cfg.PollInterval > cfg.Timeout {
					cfg.PollInterval = 10 * time.Second
				}
			}

			if requiredOnly {
				// Get the PR to determine base branch for required check lookup
				prSvc := gh.NewPRService(client)
				pr, err := prSvc.GetPR(cmd.Context(), ownerPart, repoPart, number)
				if err != nil {
					return fmt.Errorf("get PR for required-only mode: %w", err)
				}

				requiredNames, err := svc.GetRequiredCheckNames(cmd.Context(), ownerPart, repoPart, pr.BaseRef)
				if err != nil {
					return fmt.Errorf("get required check names: %w", err)
				}

				if len(requiredNames) > 0 {
					cfg.RequiredCheckNames = requiredNames
					if !outputJSON {
						fmt.Printf("[required-only] Required checks: %s\n", strings.Join(requiredNames, ", "))
						fmt.Println("Waiting for required checks to pass (non-required checks will be ignored)...")
					}
				} else {
					if !outputJSON {
						fmt.Println("No required checks configured — waiting for all checks")
					}
				}
			}

			if !outputJSON {
				cfg.OnProgress = func(status *gh.CheckStatus) {
					fmt.Printf("[%ds] CI checks: %s\n", status.ElapsedSecs, status.State)
				}
			}

			result, err := svc.WaitForChecks(cmd.Context(), ownerPart, repoPart, number, cfg)
			if err != nil && result == nil {
				return err
			}

			// Timeout is a distinct, meaningful outcome for chunked callers
			// (#187): exit 2 = "budget expired, checks still pending" so the
			// skill layer can distinguish it from pass (0) and failure (1)
			// and decide whether cumulative budget remains for another chunk.
			timedOut := result != nil && result.State == "TIMEOUT"

			if outputJSON {
				if jsonErr := printJSON(result); jsonErr != nil {
					return jsonErr
				}
				if timedOut {
					os.Exit(2)
				}
				return nil
			}

			if result != nil {
				switch result.State {
				case "SUCCESS":
					fmt.Printf("CI checks passed for PR #%d (%ds)\n", number, result.ElapsedSecs)
				case "FAILURE", "ERROR":
					fmt.Printf("CI checks failed for PR #%d: %s (%ds)\n", number, result.State, result.ElapsedSecs)
				case "TIMEOUT":
					fmt.Printf("CI checks timed out for PR #%d after %ds\n", number, result.ElapsedSecs)
				}
			}

			if timedOut {
				os.Exit(2)
			}
			if err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().IntVar(&timeoutMins, "timeout", 30, "Timeout in minutes")
	cmd.Flags().IntVar(&timeoutSecs, "timeout-secs", 0, "Timeout in seconds — overrides --timeout when > 0. Fits one bounded chunk inside a ~2-minute agent tool budget (#187)")
	cmd.Flags().IntVar(&pollSecs, "poll", 30, "Poll interval in seconds")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&requiredOnly, "required-only", false, "Wait only for branch-protection-required checks")
	return cmd
}

func ciLogsCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "logs [run-id]",
		Short: "Download CI failure logs for a workflow run",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge ci logs 12345678
  nightgauge ci logs 12345678 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid run ID: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewCIService(client)
			result, err := svc.GetRunLogs(cmd.Context(), ownerPart, repoPart, runID)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			fmt.Printf("Run %d [%s]\n", result.RunID, result.Status)
			fmt.Printf("URL: %s\n", result.URL)
			if result.Content != "" {
				fmt.Println("\n--- Logs ---")
				fmt.Println(result.Content)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// --- git command ---

func gitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Git operations via go-git (no system git required)",
	}
	cmd.AddCommand(
		gitCurrentBranchCmd(),
		gitBranchCreateCmd(),
		gitCheckoutCmd(),
		gitStatusGitCmd(),
		gitCommitCmd(),
		gitLogCmd(),
		gitDiffCmd(),
		gitFetchCmd(),
		gitPushCmd(),
		gitAbortPipelineCmd(),
		gitResetPipelineCmd(),
		gitBranchCleanupCmd(),
	)
	return cmd
}

func openGitService() (*gitpkg.Service, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get cwd: %w", err)
	}
	return gitpkg.NewService(cwd)
}

func gitCurrentBranchCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "current-branch",
		Short: "Output current branch name",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			branch, err := svc.CurrentBranch()
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(map[string]string{"branch": branch})
			}
			fmt.Println(branch)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitBranchCreateCmd() *cobra.Command {
	var (
		outputJSON bool
		owner      string
		repo       string
		issueFlag  int
	)

	cmd := &cobra.Command{
		Use:   "branch-create [name]",
		Short: "Create and checkout a new branch from HEAD",
		Args: func(cmd *cobra.Command, args []string) error {
			issueVal, _ := cmd.Flags().GetInt("issue")
			if issueVal > 0 {
				if len(args) > 0 {
					return fmt.Errorf("--issue cannot be combined with a positional branch name")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		Example: `  nightgauge git branch-create feat/123-my-feature
  nightgauge git branch-create --issue 123 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}

			var (
				branchName      string
				prefetchedIssue *types.Issue
				issueNumber     int
			)

			if issueFlag > 0 {
				if owner == "" || repo == "" {
					remoteSlug, slugErr := svc.RemoteRepoSlug()
					if slugErr != nil {
						return slugErr
					}
					owner, repo = splitRepo("", remoteSlug)
				}

				client, err := clientFromConfig()
				if err != nil {
					return err
				}
				issueSvc := gh.NewIssueService(client)
				fetched, err := issueSvc.GetIssue(cmd.Context(), owner, repo, issueFlag)
				if err != nil {
					return err
				}
				prefix := strings.TrimSuffix(gitpkg.BranchPrefixFromLabels(fetched.Labels), "/")
				branchName = gitpkg.GenerateBranchSlug(prefix, issueFlag, fetched.Title)
				prefetchedIssue = fetched
				issueNumber = issueFlag
			} else {
				branchName = args[0]
				if n, ok := gitpkg.ParseIssueNumberFromBranch(branchName); ok {
					issueNumber = n
				}
			}

			action := "created"
			baseBranch, err := svc.CurrentBranch()
			if err != nil {
				// Worktrees use detached HEAD by design; fall back to repo default.
				baseBranch, err = svc.DefaultBranch()
				if err != nil {
					return err
				}
			}

			parentIssue := 0
			epicBranch := ""

			if issueNumber != 0 {
				if owner == "" || repo == "" {
					remoteSlug, slugErr := svc.RemoteRepoSlug()
					if slugErr != nil {
						return slugErr
					}
					owner, repo = splitRepo("", remoteSlug)
				}

				var issueSvc *gh.IssueService
				issue := prefetchedIssue
				if issue == nil {
					client, err := clientFromConfig()
					if err != nil {
						return err
					}
					issueSvc = gh.NewIssueService(client)
					issue, err = issueSvc.GetIssue(cmd.Context(), owner, repo, issueNumber)
					if err != nil {
						return err
					}
				}

				if issue.ParentIssueNumber != 0 {
					parentIssue = issue.ParentIssueNumber

					if err := svc.Fetch(true); err != nil {
						return err
					}

					epicBranch, err = svc.FindEpicBranch(parentIssue)
					if err != nil {
						if issueSvc == nil {
							client, clientErr := clientFromConfig()
							if clientErr != nil {
								return clientErr
							}
							issueSvc = gh.NewIssueService(client)
						}
						epic, epicErr := issueSvc.GetIssue(cmd.Context(), owner, repo, parentIssue)
						if epicErr != nil {
							return epicErr
						}

						epicBranch = gitpkg.GenerateBranchSlug("epic", parentIssue, epic.Title)
						defaultBranch, defaultErr := svc.DefaultBranch()
						if defaultErr != nil {
							return defaultErr
						}

						localExists, localErr := svc.LocalBranchExists(epicBranch)
						if localErr != nil {
							return localErr
						}
						if !localExists {
							if err := svc.BranchCreateFrom(epicBranch, defaultBranch); err != nil {
								return err
							}
						} else if err := svc.Checkout(epicBranch); err != nil {
							return err
						}

						if err := svc.PushBranch(epicBranch); err != nil {
							return err
						}
					}

					baseBranch = epicBranch
				}
			}

			// Resolve the per-issue branch. The REMOTE is authoritative: when a
			// prior run already pushed feat/<N>-..., a re-run MUST continue from
			// that pushed tip. Otherwise a stale, diverged local branch (left by
			// the earlier run) gets checked out as-is, the next push is rejected
			// as non-fast-forward, the force-push safety hook blocks the
			// overwrite, and pr-create dead-ends with no PR (#3881). So we check
			// the remote FIRST and reset the local ref to origin/<branch> even
			// when a stale local ref exists — never blindly check it out.
			remoteExists, err := svc.RemoteBranchExists(branchName)
			if err != nil {
				return err
			}
			localExists, err := svc.LocalBranchExists(branchName)
			if err != nil {
				return err
			}

			switch {
			case remoteExists:
				if err := svc.Fetch(true); err != nil {
					return err
				}
				if err := svc.ResetLocalBranchToRemote(branchName); err != nil {
					return err
				}
				if err := svc.Checkout(branchName); err != nil {
					return err
				}
				action = "reused-remote"
			case localExists:
				if err := svc.Checkout(branchName); err != nil {
					return err
				}
				action = "already-exists"
			case parentIssue != 0:
				if err := svc.BranchCreateFrom(branchName, baseBranch); err != nil {
					return err
				}
			default:
				if err := svc.BranchCreate(branchName); err != nil {
					return err
				}
			}

			if outputJSON {
				payload := map[string]interface{}{
					"success":      true,
					"branch":       branchName,
					"base_branch":  baseBranch,
					"action":       action,
					"parent_issue": nil,
					"epic_branch":  nil,
				}
				if parentIssue != 0 {
					payload["parent_issue"] = parentIssue
					payload["epic_branch"] = epicBranch
				}
				return printJSON(payload)
			}
			fmt.Printf("Created and checked out branch: %s\n", branchName)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&owner, "owner", "", "GitHub organization (defaults to origin remote)")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository (owner/name or name; defaults to origin remote)")
	cmd.Flags().IntVar(&issueFlag, "issue", 0, "Derive prefix and slug from the issue's labels and title (mutually exclusive with positional name)")
	return cmd
}

func gitCheckoutCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "checkout [branch]",
		Short: "Switch branches",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			if err := svc.Checkout(args[0]); err != nil {
				return err
			}
			if outputJSON {
				return printJSON(map[string]string{"branch": args[0], "result": "checked out"})
			}
			fmt.Printf("Switched to branch: %s\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitStatusGitCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Working tree status (clean/dirty, staged files)",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			status, err := svc.Status()
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(status)
			}
			if status.IsClean {
				fmt.Println("Working tree clean.")
				return nil
			}
			if len(status.StagedFiles) > 0 {
				fmt.Println("Staged:")
				for _, f := range status.StagedFiles {
					fmt.Printf("  %s  %s\n", f.Status, f.Path)
				}
			}
			if len(status.UnstagedFiles) > 0 {
				fmt.Println("Unstaged:")
				for _, f := range status.UnstagedFiles {
					fmt.Printf("  %s  %s\n", f.Status, f.Path)
				}
			}
			if len(status.UntrackedFiles) > 0 {
				fmt.Println("Untracked:")
				for _, f := range status.UntrackedFiles {
					fmt.Printf("  %s\n", f)
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitCommitCmd() *cobra.Command {
	var (
		message    string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:     "commit",
		Short:   "Stage all changes and commit",
		Example: `  nightgauge git commit --message "feat: add feature"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if message == "" {
				return fmt.Errorf("--message is required")
			}
			svc, err := openGitService()
			if err != nil {
				return err
			}
			hash, err := svc.Commit(message)
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(map[string]string{"hash": hash, "result": "committed"})
			}
			fmt.Printf("Committed: %s\n", hash[:8])
			return nil
		},
	}

	cmd.Flags().StringVarP(&message, "message", "m", "", "Commit message (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitLogCmd() *cobra.Command {
	var (
		limit      int
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "log",
		Short: "Recent commit log",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			entries, err := svc.Log(limit)
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(entries)
			}
			for _, e := range entries {
				fmt.Printf("%s %s (%s, %s)\n", e.Hash, e.Message, e.Author, e.Date)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 10, "Number of commits to show")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitDiffCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show unstaged changes",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			diff, err := svc.Diff()
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(map[string]string{"diff": diff})
			}
			fmt.Println(diff)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitFetchCmd() *cobra.Command {
	var (
		prune      bool
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch from remote",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			if err := svc.Fetch(prune); err != nil {
				return err
			}
			if outputJSON {
				return printJSON(map[string]string{"result": "fetched"})
			}
			fmt.Println("Fetched from origin.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&prune, "prune", false, "Prune remote-tracking branches")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitPushCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push current branch to origin",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			if err := svc.Push(); err != nil {
				return err
			}
			if outputJSON {
				return printJSON(map[string]string{"result": "pushed"})
			}
			fmt.Println("Pushed to origin.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitAbortPipelineCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:     "abort-pipeline [branch]",
		Short:   "Clean up pipeline branch (checkout main, delete feature branch)",
		Args:    cobra.ExactArgs(1),
		Example: `  nightgauge git abort-pipeline feat/123-my-feature`,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			if err := svc.AbortPipeline(args[0]); err != nil {
				return err
			}
			if outputJSON {
				return printJSON(map[string]string{"branch": args[0], "result": "aborted"})
			}
			fmt.Printf("Aborted pipeline: deleted branch %s, switched to main\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitResetPipelineCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "reset-pipeline",
		Short: "Reset working tree to clean state (hard reset + clean)",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}
			if err := svc.ResetPipeline(); err != nil {
				return err
			}
			if outputJSON {
				return printJSON(map[string]string{"result": "reset"})
			}
			fmt.Println("Pipeline reset: working tree clean.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func gitBranchCleanupCmd() *cobra.Command {
	var outputJSON bool
	var dryRun bool
	var owner string
	var repo string

	cmd := &cobra.Command{
		Use:   "branch-cleanup",
		Short: "Delete stale local and remote branches for closed issues",
		Long: `Scans local branches matching feat/* and epic/* patterns, extracts
the issue number, checks if the issue is CLOSED on GitHub, and deletes
both local and remote branches for closed issues. Protected branches
(main, master) are never deleted. The current branch is never deleted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := openGitService()
			if err != nil {
				return err
			}

			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}
			issueSvc := gh.NewIssueService(client)

			o, r := splitRepo(owner, repo)

			branches, err := svc.ListLocalBranches()
			if err != nil {
				return fmt.Errorf("list local branches: %w", err)
			}

			// Also get remote branches to catch remote-only stragglers
			remoteBranches, _ := svc.ListRemoteBranches()
			// Merge remote-only branches into the set
			localSet := make(map[string]bool)
			for _, b := range branches {
				localSet[b] = true
			}
			for _, b := range remoteBranches {
				if !localSet[b] && (strings.HasPrefix(b, "feat/") || strings.HasPrefix(b, "fix/") || strings.HasPrefix(b, "epic/")) {
					branches = append(branches, b)
				}
			}

			// Get current branch to protect it
			currentBranch, _ := svc.CurrentBranch()

			// Extract issue number from branch name: feat/1234-..., epic/1234-..., fix/1234-...
			branchPattern := regexp.MustCompile(`^(?:feat|fix|epic)/(\d+)-`)

			type cleanupResult struct {
				Branch      string `json:"branch"`
				IssueNumber int    `json:"issueNumber"`
				IssueState  string `json:"issueState"`
				Action      string `json:"action"` // "deleted", "skipped", "protected", "error"
				Error       string `json:"error,omitempty"`
			}
			var results []cleanupResult
			deleted := 0

			ctx := context.Background()
			for _, branch := range branches {
				if branch == "main" || branch == "master" || branch == currentBranch {
					continue
				}

				m := branchPattern.FindStringSubmatch(branch)
				if m == nil {
					continue // not a pipeline branch
				}

				issueNum, _ := strconv.Atoi(m[1])
				if issueNum == 0 {
					continue
				}

				// Check issue state
				issue, err := issueSvc.GetIssue(ctx, o, r, issueNum)
				if err != nil {
					results = append(results, cleanupResult{
						Branch: branch, IssueNumber: issueNum,
						Action: "error", Error: err.Error(),
					})
					continue
				}

				if issue.State != "CLOSED" {
					results = append(results, cleanupResult{
						Branch: branch, IssueNumber: issueNum,
						IssueState: issue.State, Action: "skipped",
					})
					continue
				}

				if dryRun {
					results = append(results, cleanupResult{
						Branch: branch, IssueNumber: issueNum,
						IssueState: "CLOSED", Action: "would_delete",
					})
					if !outputJSON {
						fmt.Printf("  would delete: %s (issue #%d CLOSED)\n", branch, issueNum)
					}
					continue
				}

				// Delete both local and remote
				cleanupErr := svc.BranchCleanup(branch)
				action := "deleted"
				errMsg := ""
				if cleanupErr != nil {
					action = "error"
					errMsg = cleanupErr.Error()
				} else {
					deleted++
				}

				results = append(results, cleanupResult{
					Branch: branch, IssueNumber: issueNum,
					IssueState: "CLOSED", Action: action, Error: errMsg,
				})
				if !outputJSON {
					if cleanupErr != nil {
						fmt.Printf("  error: %s — %v\n", branch, cleanupErr)
					} else {
						fmt.Printf("  deleted: %s (issue #%d CLOSED)\n", branch, issueNum)
					}
				}
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"deleted":  deleted,
					"total":    len(results),
					"dryRun":   dryRun,
					"branches": results,
				})
			}

			if deleted == 0 && !dryRun {
				fmt.Println("No stale branches found.")
			} else if !dryRun {
				fmt.Printf("Cleaned up %d stale branch(es).\n", deleted)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be deleted without deleting")
	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	return cmd
}

// --- label command ---

func labelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Repository label operations (list, create, delete)",
	}
	cmd.AddCommand(
		labelListCmd(),
		labelCreateCmd(),
		labelDeleteCmd(),
	)
	return cmd
}

func labelListCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all labels for a repository",
		Example: `  nightgauge label list --owner nightgauge --repo nightgauge
  nightgauge label list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewLabelService(client, ownerPart, repoPart)
			labels, err := svc.List(cmd.Context())
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(labels)
			}

			if len(labels) == 0 {
				fmt.Println("No labels found.")
				return nil
			}
			for _, l := range labels {
				fmt.Printf("#%s  %-30s  %s\n", l.Color, l.Name, l.Description)
			}
			fmt.Printf("\nTotal: %d labels\n", len(labels))
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func labelCreateCmd() *cobra.Command {
	var (
		owner       string
		repo        string
		name        string
		description string
		color       string
		outputJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a label in a repository (idempotent)",
		Example: `  nightgauge label create --name "priority:critical" --color ff0000
  nightgauge label create --name "type:bug" --description "Bug report" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewLabelService(client, ownerPart, repoPart)
			label, err := svc.Create(cmd.Context(), name, description, color)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(label)
			}

			fmt.Printf("Label: %s (color: #%s)\n", label.Name, label.Color)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name (owner/name or name)")
	cmd.Flags().StringVar(&name, "name", "", "Label name (required)")
	cmd.Flags().StringVar(&description, "description", "", "Label description")
	cmd.Flags().StringVar(&color, "color", "", "Hex color without # (default: cccccc)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func labelDeleteCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		labelID    string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a label by node ID",
		Example: `  nightgauge label delete --label-id MDU6TGFiZWwxMjM=
  nightgauge label delete --label-id MDU6TGFiZWwxMjM= --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if labelID == "" {
				return fmt.Errorf("--label-id is required")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewLabelService(client, ownerPart, repoPart)
			if err := svc.Delete(cmd.Context(), labelID); err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{"deleted": labelID, "status": "ok"})
			}

			fmt.Printf("Deleted label: %s\n", labelID)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository name (owner/name or name)")
	cmd.Flags().StringVar(&labelID, "label-id", "", "Label node ID (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// --- project view commands ---

func projectViewListCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "view-list",
		Short: "List all views for a project board",
		Example: `  nightgauge project view-list --project 1
  nightgauge project view-list --project 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectNumber == 0 {
				return fmt.Errorf("--project is required")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			svc := gh.NewViewService(client, owner, projectNumber, getOwnerType(cmd))
			views, err := svc.List(cmd.Context())
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(views)
			}

			if len(views) == 0 {
				fmt.Println("No views found.")
				return nil
			}
			for _, v := range views {
				fmt.Printf("%-8s  %-30s  %s\n", v.Layout, v.Name, v.ID)
			}
			fmt.Printf("\nTotal: %d views\n", len(views))
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectViewCreateCmd() *cobra.Command {
	var (
		owner         string
		projectNumber int
		name          string
		layout        string
		filter        string
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "view-create",
		Short: "Create a project board view (idempotent)",
		Example: `  nightgauge project view-create --name "Ready Items" --layout board --filter "status:Ready" --project 1
  nightgauge project view-create --name "Backlog" --layout table --project 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if layout == "" {
				return fmt.Errorf("--layout is required (board, table, or roadmap)")
			}
			if projectNumber == 0 {
				return fmt.Errorf("--project is required")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			svc := gh.NewViewService(client, owner, projectNumber, getOwnerType(cmd))

			var filterPtr *string
			if filter != "" {
				filterPtr = &filter
			}

			view, err := svc.Create(cmd.Context(), name, layout, filterPtr)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(map[string]interface{}{
					"id":     view.ID,
					"name":   view.Name,
					"layout": view.Layout,
					"status": "created",
				})
			}

			fmt.Printf("View: %s (layout: %s, id: %s)\n", view.Name, view.Layout, view.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number (required)")
	cmd.Flags().StringVar(&name, "name", "", "View name (required)")
	cmd.Flags().StringVar(&layout, "layout", "", "View layout: board, table, or roadmap (required)")
	cmd.Flags().StringVar(&filter, "filter", "", "Server-side filter string (e.g., status:Ready)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func projectResolveCmd() *cobra.Command {
	var (
		owner      string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "resolve --number N",
		Short: "Resolve a project by number, trying org then user ownership",
		Long: `Resolve a GitHub project by number against both org and user ownership.
Org is tried first (preferred — personal projects cannot be linked to org repos).
Falls back to user ownership when the org lookup finds no matching project.

Outputs: number, owner, owner_type, id, title, url`,
		Example: `  nightgauge project resolve --number 5
  nightgauge project resolve --number 5 --owner myorg --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			numberFlag := cmd.Flags().Lookup("number")
			if numberFlag == nil || !numberFlag.Changed {
				return fmt.Errorf("--number is required")
			}
			projectNumber, err := strconv.Atoi(numberFlag.Value.String())
			if err != nil || projectNumber <= 0 {
				return fmt.Errorf("--number must be a positive integer")
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			result, err := gh.ResolveProject(cmd.Context(), client, owner, projectNumber)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			fmt.Printf("Project #%d: %s\n", result.Number, result.Title)
			fmt.Printf("  owner:      %s\n", result.Owner)
			fmt.Printf("  owner_type: %s\n", result.OwnerType)
			fmt.Printf("  id:         %s\n", result.ID)
			fmt.Printf("  url:        %s\n", result.URL)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user login")
	cmd.Flags().Int("number", 0, "Project board number (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// --- helpers ---

func splitRepo(owner, repo string) (string, string) {
	if strings.Contains(repo, "/") {
		parts := strings.SplitN(repo, "/", 2)
		return parts[0], parts[1]
	}
	return owner, repo
}

func printJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// isValidDate reports whether s is a valid ISO 8601 date (YYYY-MM-DD).
func isValidDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// --- adapter command ---

func adapterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adapter",
		Short: "Manage AI adapter integrations",
	}
	cmd.AddCommand(adapterListCmd(), adapterTestCmd())
	return cmd
}

func adapterListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available adapters and their status",
		Example: `  nightgauge adapter list
  nightgauge adapter list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := adapters.NewRegistry()
			infos := registry.List()

			if outputJSON {
				return printJSON(infos)
			}

			// Human-readable table
			fmt.Printf("%-18s %-18s %-8s %s\n", "NAME", "DISPLAY", "BINARY", "STATUS")
			fmt.Printf("%-18s %-18s %-8s %s\n", "----", "-------", "------", "------")
			for _, info := range infos {
				status := "not found"
				if info.Available {
					status = "available"
				}
				fmt.Printf("%-18s %-18s %-8s %s\n", info.Name, info.DisplayName, info.Binary, status)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func adapterTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test <name>",
		Short: "Verify an adapter binary exists and responds",
		Args:  cobra.ExactArgs(1),
		Example: `  nightgauge adapter test claude-headless
  nightgauge adapter test codex`,
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := adapters.NewRegistry()
			name := args[0]

			if err := registry.TestAdapter(name); err != nil {
				return fmt.Errorf("adapter %s: %w", name, err)
			}

			fmt.Printf("Adapter %s: OK\n", name)
			return nil
		},
	}

	return cmd
}

// --- audit command ---

func auditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Audit epic lifecycle and board status health",
	}
	cmd.AddCommand(auditLifecycleCmd())
	cmd.AddCommand(auditApiAlignmentCmd())
	cmd.AddCommand(auditSynthesizeCmd())
	cmd.AddCommand(auditTrendCmd())
	cmd.AddCommand(auditCreateIssuesCmd())
	return cmd
}

func auditSynthesizeCmd() *cobra.Command {
	var (
		inputDir   string
		outputDir  string
		configFile string
		outputJSON bool
		ciMode     bool
		threshold  float64
	)

	cmd := &cobra.Command{
		Use:     "synthesize",
		Short:   "Merge dimension JSON files into a unified product audit report",
		Example: "  nightgauge audit synthesize --input-dir .nightgauge/audit --output-dir .nightgauge/audit --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			// configWeights can be extended in the future via config.yaml parsing.
			// For now, dimension files carry their own weights.
			configWeights := map[string]float64{}
			_ = configFile // reserved for future config-driven weight overrides

			dimensions, warnings, err := apipkg.LoadDimensionFiles(inputDir)
			if err != nil {
				return fmt.Errorf("load dimension files: %w", err)
			}

			report, err := apipkg.SynthesizeReport(dimensions, configWeights)
			if err != nil {
				return fmt.Errorf("synthesize report: %w", err)
			}

			markdownReport := apipkg.GenerateMarkdownReport(report, warnings)

			jsonPath, mdPath, err := apipkg.WriteSynthesisOutputs(report, markdownReport, outputDir)
			if err != nil {
				return fmt.Errorf("write outputs: %w", err)
			}

			if ciMode {
				ciCfg := apipkg.CIConfig{Enabled: true, Threshold: threshold}
				ciResult := apipkg.BuildCIResult(report, ciCfg)
				fmt.Print(apipkg.FormatCIOutput(ciResult))
				if outputJSON {
					if err := printJSON(ciResult); err != nil {
						return err
					}
				}
				os.Exit(ciResult.ExitCode)
				return nil
			}

			if outputJSON {
				return printJSON(report)
			}

			fmt.Printf("Synthesis complete\n")
			fmt.Printf("  Overall score: %.1f\n", report.OverallScore)
			fmt.Printf("  Total findings: %d\n", report.TotalFindings)
			fmt.Printf("  JSON: %s\n", jsonPath)
			fmt.Printf("  Markdown: %s\n", mdPath)
			for _, w := range warnings {
				fmt.Printf("  WARNING: %s\n", w)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&inputDir, "input-dir", ".nightgauge/audit", "Directory containing dimension-*.json files")
	cmd.Flags().StringVar(&outputDir, "output-dir", ".nightgauge/audit", "Directory to write report outputs")
	cmd.Flags().StringVar(&configFile, "config", ".nightgauge/config.yaml", "Config file path")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&ciMode, "ci", false, "CI mode: print summary and exit with code based on threshold")
	cmd.Flags().Float64Var(&threshold, "threshold", 75.0, "Minimum acceptable score for CI pass (used with --ci)")
	return cmd
}

func auditTrendCmd() *cobra.Command {
	var (
		currentFile  string
		previousFile string
		outputDir    string
		outputJSON   bool
	)

	cmd := &cobra.Command{
		Use:     "trend",
		Short:   "Compare current audit against previous to detect trends",
		Example: "  nightgauge audit trend --current .nightgauge/audit/product-audit-2026-03-23.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load current report
			currentData, err := os.ReadFile(currentFile)
			if err != nil {
				return fmt.Errorf("read current report %q: %w", currentFile, err)
			}
			var current apipkg.SynthesisReport
			if err := json.Unmarshal(currentData, &current); err != nil {
				return fmt.Errorf("parse current report: %w", err)
			}

			// Load previous report (optional)
			var previous *apipkg.SynthesisReport
			if previousFile != "" {
				prevData, err := os.ReadFile(previousFile)
				if err != nil {
					fmt.Fprintf(os.Stderr, "WARNING: could not read previous report %q: %v\n", previousFile, err)
				} else {
					var prev apipkg.SynthesisReport
					if err := json.Unmarshal(prevData, &prev); err != nil {
						fmt.Fprintf(os.Stderr, "WARNING: could not parse previous report: %v\n", err)
					} else {
						previous = &prev
					}
				}
			} else {
				// Auto-detect: look for most recent file in history dir
				historyDir := filepath.Join(filepath.Dir(currentFile), "history")
				if entries, err := os.ReadDir(historyDir); err == nil {
					var histFiles []string
					for _, e := range entries {
						if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
							histFiles = append(histFiles, filepath.Join(historyDir, e.Name()))
						}
					}
					if len(histFiles) > 0 {
						// Sort: last entry is most recent
						prevData, err := os.ReadFile(histFiles[len(histFiles)-1])
						if err == nil {
							var prev apipkg.SynthesisReport
							if json.Unmarshal(prevData, &prev) == nil {
								previous = &prev
							}
						}
					}
				}
			}

			trend, err := apipkg.CompareTwoAudits(&current, previous, 2.0)
			if err != nil {
				return fmt.Errorf("compare audits: %w", err)
			}

			// Write trend output
			if outputDir != "" {
				trendData, err := json.MarshalIndent(trend, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal trend: %w", err)
				}
				// Derive date from current file name or timestamp
				date := strings.TrimPrefix(filepath.Base(currentFile), "product-audit-")
				date = strings.TrimSuffix(date, ".json")
				trendPath := filepath.Join(outputDir, fmt.Sprintf("product-audit-%s-trend.json", date))
				if err := os.WriteFile(trendPath, trendData, 0644); err != nil {
					return fmt.Errorf("write trend file: %w", err)
				}
				fmt.Printf("Trend report: %s\n", trendPath)
			}

			if outputJSON {
				return printJSON(trend)
			}

			fmt.Printf("Trend Analysis\n")
			fmt.Printf("  Overall score: %.1f → %.1f (delta: %+.1f)\n", trend.PreviousScore, trend.CurrentScore, trend.ScoreDelta)
			fmt.Printf("  Trend: %s\n", trend.Trend)
			fmt.Printf("  New findings: %d\n", trend.NewFindings)
			fmt.Printf("  Resolved findings: %d\n", trend.ResolvedFindings)
			fmt.Printf("  Persistent findings: %d\n", trend.PersistentFindings)
			return nil
		},
	}

	cmd.Flags().StringVar(&currentFile, "current", "", "Path to current synthesis report JSON (required)")
	cmd.Flags().StringVar(&previousFile, "previous", "", "Path to previous synthesis report JSON (auto-detected from history/ if omitted)")
	cmd.Flags().StringVar(&outputDir, "output-dir", ".nightgauge/audit", "Directory to write trend output")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("current") // cobra MarkFlagRequired never errors for known flags
	return cmd
}

func auditCreateIssuesCmd() *cobra.Command {
	var (
		inputFile     string
		configFile    string
		owner         string
		repo          string
		projectNumber int
		dryRun        bool
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:     "create-issues",
		Short:   "Create GitHub epics and sub-issues from a synthesis report",
		Example: "  nightgauge audit create-issues --input-file .nightgauge/audit/product-audit-2026-03-23.json --dry-run",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(inputFile)
			if err != nil {
				return fmt.Errorf("read report %q: %w", inputFile, err)
			}
			var report apipkg.SynthesisReport
			if err := json.Unmarshal(data, &report); err != nil {
				return fmt.Errorf("parse report: %w", err)
			}

			cfg := apipkg.IssueCreatorConfig{
				Owner:         owner,
				Repo:          repo,
				ProjectNumber: projectNumber,
				EpicLabel:     "type:epic",
				SeverityToPriority: map[string]string{
					"critical": "priority:critical",
					"high":     "priority:high",
					"medium":   "priority:medium",
					"low":      "priority:low",
				},
				DryRun: dryRun,
			}

			if dryRun {
				fmt.Println("DRY RUN — no GitHub changes will be made")
				result, err := apipkg.RunIssueCreation(cmd.Context(), &report, cfg, nil)
				if err != nil {
					return err
				}
				if outputJSON {
					return printJSON(result)
				}
				fmt.Printf("Would create %d epics and %d issues\n", result.EpicsCreated, result.IssuesCreated)
				return nil
			}

			ghClient, err := clientFromConfig()
			if err != nil {
				return err
			}

			creator := &githubIssueCreatorAdapter{
				issueService:   gh.NewIssueService(ghClient),
				projectService: gh.NewProjectService(ghClient, owner, projectNumber),
				client:         ghClient,
			}

			result, err := apipkg.RunIssueCreation(cmd.Context(), &report, cfg, creator)
			if err != nil {
				return err
			}

			if outputJSON {
				return printJSON(result)
			}

			fmt.Printf("Issue creation complete\n")
			fmt.Printf("  Epics created: %d\n", result.EpicsCreated)
			fmt.Printf("  Issues created: %d\n", result.IssuesCreated)
			fmt.Printf("  Issues skipped (duplicates): %d\n", result.IssuesSkipped)
			fmt.Printf("  BlockedBy relationships added: %d\n", result.BlockedByAdded)
			for _, e := range result.Errors {
				fmt.Fprintf(os.Stderr, "  ERROR: %s\n", e)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&inputFile, "input-file", "", "Path to synthesis report JSON (required)")
	cmd.Flags().StringVar(&configFile, "config", ".nightgauge/config.yaml", "Config file path")
	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Default repository for issue creation")
	cmd.Flags().IntVar(&projectNumber, "project", 5, "Project board number")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be created without making changes")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	_ = cmd.MarkFlagRequired("input-file") // cobra MarkFlagRequired never errors for known flags
	return cmd
}

// githubIssueCreatorAdapter adapts the GitHub services to the IssueCreator interface.
type githubIssueCreatorAdapter struct {
	issueService   *gh.IssueService
	projectService *gh.ProjectService
	client         *gh.Client
}

func (a *githubIssueCreatorAdapter) GetRepositoryID(ctx context.Context, owner, repo string) (string, error) {
	return a.client.GetRepositoryID(ctx, owner, repo)
}

func (a *githubIssueCreatorAdapter) CreateIssueWithID(ctx context.Context, owner, repo, title, body string, labels []string) (string, int, error) {
	repoID, err := a.client.GetRepositoryID(ctx, owner, repo)
	if err != nil {
		return "", 0, err
	}
	// Resolve label IDs
	var labelIDs []string
	for _, l := range labels {
		id, err := a.GetLabelID(ctx, owner, repo, l)
		if err == nil && id != "" {
			labelIDs = append(labelIDs, id)
		}
	}
	issue, err := a.issueService.CreateIssue(ctx, repoID, title, body, labelIDs)
	if err != nil {
		return "", 0, err
	}
	return issue.NodeID, issue.Number, nil
}

func (a *githubIssueCreatorAdapter) AddSubIssue(ctx context.Context, parentNodeID, childNodeID string) error {
	return a.issueService.AddSubIssue(ctx, parentNodeID, childNodeID)
}

func (a *githubIssueCreatorAdapter) AddBlockedBy(ctx context.Context, blockedNodeID, blockerNodeID string) error {
	return a.issueService.AddBlockedBy(ctx, blockedNodeID, blockerNodeID)
}

func (a *githubIssueCreatorAdapter) AddToProjectBoard(ctx context.Context, owner string, projectNumber int, issueNodeID string) error {
	_, err := a.projectService.AddItem(ctx, issueNodeID)
	return err
}

func (a *githubIssueCreatorAdapter) SetProjectItemStatus(ctx context.Context, owner string, projectNumber int, issueNodeID, status string) error {
	return a.projectService.SetSingleSelectField(ctx, issueNodeID, "Status", status)
}

func (a *githubIssueCreatorAdapter) SearchOpenIssueByTitle(ctx context.Context, owner, repo, title string) (int, string, bool, error) {
	issues, err := a.issueService.ListIssues(ctx, owner, repo, nil)
	if err != nil {
		return 0, "", false, err
	}
	for _, iss := range issues {
		if strings.EqualFold(iss.Title, title) {
			return iss.Number, iss.NodeID, true, nil
		}
	}
	return 0, "", false, nil
}

func (a *githubIssueCreatorAdapter) GetLabelID(ctx context.Context, owner, repo, labelName string) (string, error) {
	var q struct {
		Repository struct {
			Label struct {
				ID string `graphql:"id"`
			} `graphql:"label(name: $name)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	// TODO: direct label ID lookup not implemented; label step skipped.
	// The GraphQL struct above (q) is unused because we have no GetLabelID query wired.
	// Issue creation still succeeds, just without the label.
	_ = q // unused: direct label ID lookup not implemented (see TODO above)
	// Use ListIssues approach: fetch all labels via a query
	issues, err := a.issueService.ListIssues(ctx, owner, repo, []string{labelName})
	if err != nil {
		// Label lookup failed; return empty (issue will be created without label)
		return "", nil
	}
	_ = issues // unused: ListIssues result not usable for label ID resolution; return empty to skip labeling
	return "", nil
}

func auditLifecycleCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		fix           bool
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:     "lifecycle",
		Short:   "Detect stale epics, board drift, orphaned issues, and stale blockers",
		Example: "  nightgauge audit lifecycle --repo nightgauge/nightgauge --fix",
		RunE: func(cmd *cobra.Command, args []string) error {
			ownerPart, repoPart := splitRepo(owner, repo)
			client, err := clientFromConfig()
			if err != nil {
				return err
			}
			svc := gh.NewLifecycleAuditService(client, ownerPart, projectNumber)
			result, err := svc.RunAudit(cmd.Context(), ownerPart, repoPart, fix)
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(result)
			}
			fmt.Printf("Lifecycle Audit: %s — %d findings\n", result.Repo, result.Summary.Total)
			for _, f := range result.Findings {
				status := ""
				if f.Fixed {
					status = " [FIXED]"
				} else if f.FixError != "" {
					status = " [FIX FAILED: " + f.FixError + "]"
				}
				fmt.Printf("  [%s] #%d %s: %s%s\n", f.Category, f.IssueNumber, f.IssueTitle, f.Detail, status)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/repo or bare name)")
	cmd.Flags().IntVar(&projectNumber, "project", 5, "Project board number")
	cmd.Flags().BoolVar(&fix, "fix", false, "Auto-fix detected issues")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output findings as JSON")
	return cmd
}

func auditApiAlignmentCmd() *cobra.Command {
	var (
		angularRepo  string
		flutterRepo  string
		platformRepo string
		outputDir    string
		outputJSON   bool
	)

	cmd := &cobra.Command{
		Use:     "api-alignment",
		Short:   "Extract and align HTTP API endpoints across Angular, Flutter, and platform repos",
		Example: "  nightgauge audit api-alignment --json\n  nightgauge audit api-alignment --flutter-repo ../acme-mobile",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve repo paths relative to git root
			gitRoot, err := getGitRoot()
			if err != nil {
				gitRoot = "."
			}
			angular, flutter, platform := apipkg.ResolveRepoPaths(gitRoot, angularRepo, flutterRepo, platformRepo)

			svc := apipkg.NewApiAlignmentService(angular, flutter, platform)
			report, err := svc.Run()
			if err != nil {
				return err
			}

			if outputDir != "" {
				data, err := json.Marshal(report)
				if err != nil {
					return fmt.Errorf("marshal report: %w", err)
				}
				outPath := filepath.Join(outputDir, "api-alignment-report.json")
				if err := os.WriteFile(outPath, data, 0644); err != nil {
					return fmt.Errorf("write report: %w", err)
				}
				fmt.Printf("Report written to %s\n", outPath)
			}

			if outputJSON {
				return printJSON(report)
			}

			fmt.Print(apipkg.FormatReport(report))
			return nil
		},
	}

	cmd.Flags().StringVar(&angularRepo, "angular-repo", "../acme-dashboard", "Path to Angular repo")
	cmd.Flags().StringVar(&flutterRepo, "flutter-repo", "../acme-mobile", "Path to Flutter repo")
	cmd.Flags().StringVar(&platformRepo, "platform-repo", "../acme-platform", "Path to platform repo")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Save JSON report to directory")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// getGitRoot returns the current git repository root via git rev-parse.
func getGitRoot() (string, error) {
	gitSvc, err := gitpkg.NewService(".")
	if err != nil {
		return ".", err
	}
	return gitSvc.Root()
}

// --- depgraph command ---

func depgraphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Cross-repo dependency graph operations",
	}
	cmd.AddCommand(depgraphBuildCmd())
	return cmd
}

func depgraphBuildCmd() *cobra.Command {
	var (
		owner      string
		repos      []string
		project    int
		outputJSON bool
		format     string
	)

	cmd := &cobra.Command{
		Use:          "build",
		Short:        "Build and display the cross-repo dependency graph",
		Long:         "Fetches issues from project boards, analyzes blockedBy relationships and cross-repo references in issue bodies, and computes topological execution waves and critical path.",
		Example:      "  nightgauge graph build --owner nightgauge --project 1 --repos nightgauge,acme-platform,acme-mobile",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create github client: %w", err)
			}

			// Resolve owner and project from config if not set
			if !cmd.Flags().Changed("owner") || !cmd.Flags().Changed("project") {
				workdir, _ := os.Getwd()
				cfg, cfgErr := config.Load(workdir)
				if cfgErr == nil && cfg != nil {
					if !cmd.Flags().Changed("owner") && cfg.Owner != "" {
						owner = cfg.Owner
					}
					if !cmd.Flags().Changed("project") && cfg.ProjectNumber != 0 {
						project = cfg.ProjectNumber
					}
				}
			}

			if owner == "" {
				return fmt.Errorf("--owner is required (or set in config.yaml)")
			}
			if project == 0 {
				return fmt.Errorf("--project is required (or set in config.yaml)")
			}

			// Build repo configs
			ot := getOwnerType(cmd)
			var repoConfigs []depgraph.RepoConfig
			if len(repos) > 0 {
				for _, r := range repos {
					repoConfigs = append(repoConfigs, depgraph.RepoConfig{
						Owner:     owner,
						OwnerType: ot,
						Name:      r,
						Project:   project,
					})
				}
			} else {
				// Default: auto-detect from sibling directories
				repoConfigs = autoDetectRepos(owner, project)
			}

			if len(repoConfigs) == 0 {
				return fmt.Errorf("no repos found; use --repos to specify")
			}

			ctx := context.Background()
			graph, err := depgraph.BuildGraph(ctx, client, repoConfigs, nil)
			if err != nil {
				return fmt.Errorf("build graph: %w", err)
			}

			if outputJSON || format == "json" {
				return printJSON(graph)
			}

			// Table format
			printGraphTable(graph)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub org/owner")
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "Repo names (comma-separated)")
	cmd.Flags().IntVar(&project, "project", 0, "Project board number")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: json, table")
	return cmd
}

// autoDetectRepos looks for sibling directories with .nightgauge/ config.
func autoDetectRepos(owner string, project int) []depgraph.RepoConfig {
	gitRoot, err := getGitRoot()
	if err != nil {
		return nil
	}

	// Look at parent directory for sibling repos
	parent := filepath.Dir(gitRoot)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}

	var configs []depgraph.RepoConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		repoRoot := filepath.Join(parent, entry.Name())
		cfgPath := filepath.Join(repoRoot, ".nightgauge", "config.yaml")
		if _, err := os.Stat(cfgPath); err == nil {
			rc := depgraph.RepoConfig{
				Owner:   owner,
				Name:    entry.Name(),
				Project: project,
			}
			// Read per-repo config for owner type
			if repoCfg, loadErr := config.Load(repoRoot); loadErr == nil && repoCfg.OwnerType != "" {
				rc.OwnerType = gh.ParseOwnerType(repoCfg.OwnerType)
			}
			configs = append(configs, rc)
		}
	}

	return configs
}

// detectSiblingRepos finds repos with .nightgauge/config.yaml in the
// parent directory of the given root. Unlike autoDetectRepos, it does not
// depend on CWD — safe for use from the IPC server.
// registerWorkspaceReposInResolver registers the primary repo and every sibling
// repo (sharing the workspace's parent directory) with the IPC server's per-repo
// client resolver. Each registration maps (owner, repo) → filesystem path so the
// resolver can load that repo's .nightgauge/config.yaml and resolve its
// configured identity (github_user / token). Without this the resolver registry
// is empty and every Resolve call falls back to the default startup client,
// which carries the primary repo's identity and cannot authenticate against
// private sibling repos (#3700).
func registerWorkspaceReposInResolver(server *ipc.Server, workspaceRoot string, primaryCfg *config.Config) {
	if server == nil {
		return
	}

	// Primary repo (the current workspace root).
	if primaryCfg != nil && primaryCfg.Owner != "" && primaryCfg.DefaultRepo != "" {
		server.RegisterRepo(primaryCfg.Owner, primaryCfg.DefaultRepo, workspaceRoot)
	}

	// Sibling repos sharing the parent directory — mirrors detectSiblingRepos
	// discovery but registers owner/repo→path with the resolver.
	parent := filepath.Dir(workspaceRoot)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		repoRoot := filepath.Join(parent, entry.Name())
		if repoRoot == workspaceRoot {
			continue // primary repo already registered above
		}
		if _, statErr := os.Stat(filepath.Join(repoRoot, ".nightgauge", "config.yaml")); statErr != nil {
			continue
		}
		repoCfg, loadErr := config.Load(repoRoot)
		if loadErr != nil || repoCfg == nil || repoCfg.Owner == "" || repoCfg.DefaultRepo == "" {
			continue
		}
		server.RegisterRepo(repoCfg.Owner, repoCfg.DefaultRepo, repoRoot)
	}
}

func detectSiblingRepos(root string, defaultOwner string, defaultProject int) []depgraph.RepoConfig {
	parent := filepath.Dir(root)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}

	var configs []depgraph.RepoConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		repoRoot := filepath.Join(parent, entry.Name())
		cfgPath := filepath.Join(repoRoot, ".nightgauge", "config.yaml")
		if _, err := os.Stat(cfgPath); err != nil {
			continue
		}

		// Read each repo's own config to get its project number and owner type.
		// Each repo may have a different project board and owner type.
		repoOwner := defaultOwner
		repoProject := defaultProject
		var repoOwnerType gh.OwnerType
		if repoCfg, loadErr := config.Load(repoRoot); loadErr == nil {
			if repoCfg.Owner != "" {
				repoOwner = repoCfg.Owner
			}
			if repoCfg.ProjectNumber > 0 {
				repoProject = repoCfg.ProjectNumber
			}
			if repoCfg.OwnerType != "" {
				repoOwnerType = gh.ParseOwnerType(repoCfg.OwnerType)
			}
		}
		configs = append(configs, depgraph.RepoConfig{
			Owner:     repoOwner,
			OwnerType: repoOwnerType,
			Name:      entry.Name(),
			Project:   repoProject,
		})
	}
	return configs
}

// schedulerIdentity is the resolved (owner, ownerType, projectNumber) the serve
// daemon attaches its pipeline scheduler with. Source records where the identity
// came from for the startup log line: "root-config", "manifest:<member>", or
// "none". Detail carries a human-readable reason when Source == "none".
type schedulerIdentity struct {
	Owner         string
	OwnerType     string
	ProjectNumber int
	Source        string
	Detail        string
}

// Resolvable reports whether this identity can attach a scheduler.
func (id schedulerIdentity) Resolvable() bool {
	return id.Owner != "" && id.ProjectNumber > 0
}

// resolveSchedulerIdentity resolves the workspace's scheduler identity in a
// single deterministic place so the serve attach gate and the autonomous
// repo-set construction never drift. Resolution order:
//
//  1. Root config — when cfg supplies Owner+ProjectNumber>0, use it unchanged.
//     This preserves single-repo and configured multi-repo roots verbatim
//     (no behavior change; #3860 AC #3).
//  2. Manifest-derived — else read .vscode/nightgauge-workspace.yaml and
//     select a representative member, preferring routing.default_repository,
//     then the first role:primary member, then the first entry. That member's
//     own config.Load() is canonical for owner/ownerType/project, falling back
//     to the manifest entry's project_number. This is the missing wiring that
//     left manifest-only roots (no root config.yaml) with no scheduler — every
//     scheduler-backed IPC call was rejected "scheduler not configured" (#3860
//     AC #1). Mirrors the per-member resolution in reposFromWorkspaceManifest.
//  3. Unresolved — Source "none" with a Detail naming what was missing, so the
//     caller logs a precise warning and the IPC error names the fix (AC #2).
func resolveSchedulerIdentity(workspaceRoot string, cfg *config.Config) schedulerIdentity {
	// Branch 1: root config is authoritative when it carries a usable identity.
	if cfg != nil && cfg.Owner != "" && cfg.ProjectNumber > 0 {
		return schedulerIdentity{
			Owner:         cfg.Owner,
			OwnerType:     cfg.OwnerType,
			ProjectNumber: cfg.ProjectNumber,
			Source:        "root-config",
		}
	}

	// Branch 2: derive from the workspace manifest.
	manifestPath := filepath.Join(workspaceRoot, ".vscode", "nightgauge-workspace.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return schedulerIdentity{
			Source: "none",
			Detail: "no workspace-root .nightgauge/config.yaml (owner + project.number) and no .vscode/nightgauge-workspace.yaml manifest",
		}
	}

	var manifest struct {
		Repositories []struct {
			Name          string `yaml:"name"`
			Path          string `yaml:"path"`
			Role          string `yaml:"role"`
			ProjectNumber int    `yaml:"project_number"`
		} `yaml:"repositories"`
		Routing struct {
			DefaultRepository string `yaml:"default_repository"`
		} `yaml:"routing"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return schedulerIdentity{
			Source: "none",
			Detail: fmt.Sprintf("workspace manifest %s could not be parsed: %v", manifestPath, err),
		}
	}

	// Select the representative member: default_repository → first role:primary
	// → first entry. Skip entries without a path (cannot load a member config).
	type member struct {
		name          string
		path          string
		projectNumber int
	}
	var entries []member
	for _, r := range manifest.Repositories {
		if r.Path == "" {
			continue
		}
		entries = append(entries, member{name: r.Name, path: r.Path, projectNumber: r.ProjectNumber})
	}
	if len(entries) == 0 {
		return schedulerIdentity{
			Source: "none",
			Detail: fmt.Sprintf("workspace manifest %s lists no repositories with a path", manifestPath),
		}
	}

	pick := -1
	if def := manifest.Routing.DefaultRepository; def != "" {
		for i, e := range entries {
			if e.name == def {
				pick = i
				break
			}
		}
	}
	if pick < 0 {
		for _, r := range manifest.Repositories {
			if r.Path == "" || r.Role != "primary" {
				continue
			}
			// Map back to the filtered entries slice by name+path.
			for j, e := range entries {
				if e.name == r.Name && e.path == r.Path {
					pick = j
					break
				}
			}
			break
		}
	}
	if pick < 0 {
		pick = 0 // first entry
	}

	sel := entries[pick]
	memberRoot := filepath.Join(workspaceRoot, sel.path)
	owner := ""
	ownerType := ""
	project := sel.projectNumber
	if repoCfg, loadErr := config.Load(memberRoot); loadErr == nil && repoCfg != nil {
		if repoCfg.Owner != "" {
			owner = repoCfg.Owner
		}
		if repoCfg.OwnerType != "" {
			ownerType = repoCfg.OwnerType
		}
		if repoCfg.ProjectNumber > 0 {
			project = repoCfg.ProjectNumber
		}
	}

	if owner != "" && project > 0 {
		return schedulerIdentity{
			Owner:         owner,
			OwnerType:     ownerType,
			ProjectNumber: project,
			Source:        "manifest:" + sel.name,
		}
	}

	return schedulerIdentity{
		Source: "none",
		Detail: fmt.Sprintf("workspace manifest member %q (%s) did not yield an owner + project.number", sel.name, memberRoot),
	}
}

// reposFromWorkspaceManifest builds the autonomous repo set from the workspace
// manifest at <root>/.vscode/nightgauge-workspace.yaml. The manifest is the
// authoritative list of repos a workspace owns, so this is preferred over
// sibling/folder detection for autonomous scheduling. It supports the N:1
// topology (many repos sharing one project) and layouts where member repos are
// child directories of the workspace root.
//
// Each entry's path is resolved relative to root, and that repo's own
// .nightgauge/config.yaml is loaded for the canonical owner, repo slug,
// project number, and owner type (each repo may target a different project —
// e.g. nightgauge' 1:1 layout). The manifest's project_number and the
// passed defaults are used as fallbacks. Returns nil when no manifest exists or
// it yields no usable repos, so callers can fall back to legacy detection.
func reposFromWorkspaceManifest(root, defaultOwner string, defaultProject int) []depgraph.RepoConfig {
	manifestPath := filepath.Join(root, ".vscode", "nightgauge-workspace.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil // no manifest — caller falls back to legacy detection
	}

	var manifest struct {
		Repositories []struct {
			Name          string `yaml:"name"`
			Path          string `yaml:"path"`
			ProjectNumber int    `yaml:"project_number"`
		} `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		log.Printf("autonomous: failed to parse workspace manifest %s: %v — falling back to repo detection", manifestPath, err)
		return nil
	}

	var configs []depgraph.RepoConfig
	for _, entry := range manifest.Repositories {
		if entry.Path == "" {
			continue
		}
		repoRoot := filepath.Join(root, entry.Path)

		owner := defaultOwner
		project := entry.ProjectNumber
		if project == 0 {
			project = defaultProject
		}
		name := entry.Name
		var ownerType gh.OwnerType
		// Each member repo's own config is canonical for owner/slug/project/type.
		if repoCfg, loadErr := config.Load(repoRoot); loadErr == nil && repoCfg != nil {
			if repoCfg.Owner != "" {
				owner = repoCfg.Owner
			}
			if repoCfg.ProjectNumber > 0 {
				project = repoCfg.ProjectNumber
			}
			if repoCfg.DefaultRepo != "" {
				name = repoCfg.DefaultRepo
			}
			if repoCfg.OwnerType != "" {
				ownerType = gh.ParseOwnerType(repoCfg.OwnerType)
			}
		}
		if owner == "" || name == "" || project == 0 {
			continue // not enough to scan this repo
		}
		configs = append(configs, depgraph.RepoConfig{
			Owner:     owner,
			OwnerType: ownerType,
			Name:      name,
			Project:   project,
		})
	}
	if len(configs) == 0 {
		return nil
	}
	return configs
}

// printGraphTable prints the graph in a human-readable table format.
func printGraphTable(g *depgraph.Graph) {
	fmt.Printf("Cross-Repo Dependency Graph\n")
	fmt.Printf("===========================\n\n")

	fmt.Printf("Stats:\n")
	fmt.Printf("  Nodes:           %d\n", g.Stats.TotalNodes)
	fmt.Printf("  Edges:           %d\n", g.Stats.TotalEdges)
	fmt.Printf("  Repos:           %d\n", g.Stats.Repos)
	fmt.Printf("  Wave Depth:      %d\n", g.Stats.MaxDepth)
	fmt.Printf("  Critical Length:  %d\n\n", g.Stats.CriticalLength)

	if len(g.Cycles) > 0 {
		fmt.Printf("⚠ Cycles Detected: %d\n", len(g.Cycles))
		for i, cycle := range g.Cycles {
			fmt.Printf("  Cycle %d:", i+1)
			for _, id := range cycle {
				fmt.Printf(" %s#%d", id.Repo, id.Number)
			}
			fmt.Println()
		}
		fmt.Println()
	}

	if len(g.Waves) > 0 {
		fmt.Printf("Execution Waves:\n")
		for i, wave := range g.Waves {
			fmt.Printf("  Wave %d (%d issues):\n", i, len(wave))
			for _, id := range wave {
				key := g.NodeKey(id)
				if node, ok := g.Nodes[key]; ok {
					fmt.Printf("    - %s#%d [%s] %s\n", id.Repo, id.Number, node.Size, node.Title)
				} else {
					fmt.Printf("    - %s#%d\n", id.Repo, id.Number)
				}
			}
		}
		fmt.Println()
	}

	if len(g.CriticalPath) > 0 {
		fmt.Printf("Critical Path:\n")
		for i, id := range g.CriticalPath {
			key := g.NodeKey(id)
			arrow := "  →"
			if i == 0 {
				arrow = "   "
			}
			if node, ok := g.Nodes[key]; ok {
				fmt.Printf("%s %s#%d [%s, weight=%d] %s\n", arrow, id.Repo, id.Number, node.Size, node.Weight, node.Title)
			} else {
				fmt.Printf("%s %s#%d\n", arrow, id.Repo, id.Number)
			}
		}
		fmt.Println()
	}

	// Unresolvable edges
	var unresolvable []depgraph.Edge
	for _, e := range g.Edges {
		if !e.Resolvable {
			unresolvable = append(unresolvable, e)
		}
	}
	if len(unresolvable) > 0 {
		fmt.Printf("Unresolvable Dependencies (%d):\n", len(unresolvable))
		for _, e := range unresolvable {
			fmt.Printf("  %s#%d → %s#%d (repo not in workspace)\n", e.From.Repo, e.From.Number, e.To.Repo, e.To.Number)
		}
	}
}

// --- autonomous command ---

// applyStuckEpicConfig resolves the no-silent-stall watchdog settings (#4073)
// from config + environment into the orchestrator autonomous config. Enabled by
// default; the Discord webhook URL is read from the configured env var
// (NIGHTGAUGE_STUCK_EPIC_WEBHOOK by default) so the secret never lives in
// config.yaml.
func applyStuckEpicConfig(autoCfg *orchestrator.AutonomousConfig, cfg *config.Config) {
	if autoCfg == nil {
		return
	}
	autoCfg.StuckEpicDetectionEnabled = true
	if autoCfg.StuckEpicReAlertAfter <= 0 {
		autoCfg.StuckEpicReAlertAfter = 6 * time.Hour
	}
	webhookEnv := config.DefaultStuckEpicWebhookEnv
	if cfg != nil && cfg.Autonomous != nil && cfg.Autonomous.StuckEpicDetection != nil {
		sd := cfg.Autonomous.StuckEpicDetection
		if sd.Enabled != nil {
			autoCfg.StuckEpicDetectionEnabled = *sd.Enabled
		}
		if sd.DiscordWebhookEnv != "" {
			webhookEnv = sd.DiscordWebhookEnv
		}
		if d := sd.ReAlertAfter.Duration(); d > 0 {
			autoCfg.StuckEpicReAlertAfter = d
		}
	}
	if url := strings.TrimSpace(os.Getenv(webhookEnv)); url != "" {
		autoCfg.StuckEpicWebhookURL = url
	}
}

// autonomousStuckEpicsCmd surfaces epics the watchdog flagged as stalled on the
// most recent idle scan (#4073), read from the persisted autonomous state.
func autonomousStuckEpicsCmd() *cobra.Command {
	var outputJSON bool
	cmd := &cobra.Command{
		Use:          "stuck-epics",
		Short:        "Show epics detected as stalled (open but no eligible work, no run, no recovery)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, _ := os.Getwd()
			statePath := filepath.Join(workdir, ".nightgauge", "autonomous", "state.json")
			data, err := os.ReadFile(statePath)
			if os.IsNotExist(err) {
				fmt.Println("No autonomous scheduler state found. Run 'nightgauge autonomous run' first.")
				return nil
			}
			if err != nil {
				return fmt.Errorf("read state: %w", err)
			}
			var st orchestrator.AutonomousState
			if err := json.Unmarshal(data, &st); err != nil {
				return fmt.Errorf("parse state: %w", err)
			}
			if outputJSON {
				out, _ := json.MarshalIndent(st.StuckEpics, "", "  ")
				fmt.Println(string(out))
				return nil
			}
			if len(st.StuckEpics) == 0 {
				fmt.Println("No stalled epics detected on the most recent idle scan.")
				return nil
			}
			fmt.Printf("Stalled epics (%d):\n", len(st.StuckEpics))
			for _, e := range st.StuckEpics {
				fmt.Printf("\n  🛑 %s#%d  %s\n", e.Repo, e.Number, e.Title)
				for _, b := range e.Blockers {
					fmt.Printf("      #%-5d %s\n", b.Number, b.Reason)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "output as JSON")
	return cmd
}

func autonomousCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autonomous",
		Short: "Autonomous cross-repo pipeline scheduler",
	}
	cmd.AddCommand(autonomousRunCmd())
	cmd.AddCommand(autonomousStatusCmd())
	cmd.AddCommand(autonomousStopCmd())
	cmd.AddCommand(autonomousStuckEpicsCmd())
	return cmd
}

func autonomousRunCmd() *cobra.Command {
	var (
		owner      string
		repos      []string
		project    int
		interval   time.Duration
		budget     int64
		maxSlots   int
		dryRun     bool
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "run",
		Short:        "Start the autonomous scheduler loop",
		Long:         "Continuously scans all repo boards, builds the cross-repo dependency graph, fills pipeline slots with optimal next items, and cascades unblocks across repos.",
		Example:      "  nightgauge autonomous run --interval 30s --budget 500000\n  nightgauge autonomous run --dry-run",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create github client: %w", err)
			}

			// Resolve owner and project from config
			workdir, _ := os.Getwd()
			cfg, cfgErr := config.Load(workdir)
			if cfgErr == nil && cfg != nil {
				if !cmd.Flags().Changed("owner") && cfg.Owner != "" {
					owner = cfg.Owner
				}
				if !cmd.Flags().Changed("project") && cfg.ProjectNumber != 0 {
					project = cfg.ProjectNumber
				}
				// Apply autonomous config defaults from config.yaml.
				// Unified slot ceiling: pipeline.max_concurrent is the source
				// of truth, with autonomous.max_concurrent as a legacy fallback.
				// See config.ResolvedMaxConcurrent.
				if !cmd.Flags().Changed("max-concurrent") {
					if resolved := config.ResolvedMaxConcurrent(cfg); resolved > 0 {
						maxSlots = resolved
					}
				}
				if cfg.Autonomous != nil {
					if !cmd.Flags().Changed("interval") && cfg.Autonomous.ScanInterval.Duration() > 0 {
						interval = cfg.Autonomous.ScanInterval.Duration()
					}
					if !cmd.Flags().Changed("budget") && cfg.Autonomous.BudgetCeiling > 0 {
						budget = cfg.Autonomous.BudgetCeiling
					}
					if !cmd.Flags().Changed("dry-run") && cfg.Autonomous.DryRun != nil {
						dryRun = *cfg.Autonomous.DryRun
					}
				}
			}

			if owner == "" {
				return fmt.Errorf("--owner is required (or set in config.yaml)")
			}
			if project == 0 {
				return fmt.Errorf("--project is required (or set in config.yaml)")
			}

			// Build repo configs
			ot := getOwnerType(cmd)
			var repoConfigs []depgraph.RepoConfig
			if len(repos) > 0 {
				for _, r := range repos {
					repoConfigs = append(repoConfigs, depgraph.RepoConfig{
						Owner:     owner,
						OwnerType: ot,
						Name:      r,
						Project:   project,
					})
				}
			} else {
				repoConfigs = autoDetectRepos(owner, project)
			}
			if len(repoConfigs) == 0 {
				return fmt.Errorf("no repos found; use --repos to specify")
			}

			// Discipline gate (#4100): AI amplifies a weak engineering culture as
			// readily as a strong one, so refuse full autonomy on an under-prepared
			// repo (no real test suite / CI) where the gates over-trust themselves.
			// Skipped on --dry-run; configurable via autonomous.discipline_gate.
			if !dryRun {
				var autoCfg *config.AutonomousConfig
				if cfg != nil {
					autoCfg = cfg.Autonomous
				}
				disc := disciplineScore.Compute(disciplineScore.GatherSignals(workdir))
				if blocked, msg := disciplineGateBlocks(autoCfg, disc); blocked {
					return fmt.Errorf("autonomous run refused: %s", msg)
				} else if msg != "" {
					log.Printf("%s", msg)
				}
			}

			// Create the underlying scheduler
			autoOnFailure := "ready"
			autoExcludeLabels := config.DefaultExcludeLabels
			if cfg != nil && cfg.Autonomous != nil {
				autoOnFailure = cfg.Autonomous.ResolvedOnFailureStatus()
				autoExcludeLabels = cfg.Autonomous.ResolvedExcludeLabels()
			}
			sched := orchestrator.NewScheduler(client, orchestrator.SchedulerConfig{
				Owner:           owner,
				OwnerType:       getOwnerType(cmd),
				ProjectNumber:   project,
				MaxPerRepo:      maxSlots,
				WorkspaceRoot:   workdir,
				OnFailureStatus: autoOnFailure,
				ExcludeLabels:   autoExcludeLabels,
			})

			// Create autonomous scheduler
			pickupBacklog := false
			if cfg != nil && cfg.Autonomous != nil && cfg.Autonomous.PickupBacklog != nil {
				pickupBacklog = *cfg.Autonomous.PickupBacklog
			}

			// Bridge refinement config
			refinementEnabled := true
			refinementInterval := 60 * time.Second
			refinementMaxConcurrent := 1
			autoActionable := false

			if cfg != nil && cfg.Autonomous != nil {
				if cfg.Autonomous.RefinementEnabled != nil {
					refinementEnabled = *cfg.Autonomous.RefinementEnabled
				}
				if cfg.Autonomous.RefinementInterval.Duration() >= 30*time.Second {
					refinementInterval = cfg.Autonomous.RefinementInterval.Duration()
				}
				if cfg.Autonomous.RefinementMaxConcurrent > 0 {
					refinementMaxConcurrent = cfg.Autonomous.RefinementMaxConcurrent
				}
				if cfg.Autonomous.AutoActionable != nil {
					autoActionable = *cfg.Autonomous.AutoActionable
				}
			}

			autoCfg := orchestrator.AutonomousConfig{
				ScanInterval:            interval,
				MaxConcurrent:           maxSlots,
				BudgetCeiling:           budget,
				DebounceRepos:           true,
				DryRun:                  dryRun,
				PickupBacklog:           pickupBacklog,
				RefinementEnabled:       refinementEnabled,
				RefinementInterval:      refinementInterval,
				RefinementMaxConcurrent: refinementMaxConcurrent,
				RefinementCooldown:      5 * time.Minute,
				AutoActionable:          autoActionable,
				// Stuck-epic watchdog defaults; applyStuckEpicConfig refines from config.
				StuckEpicDetectionEnabled: true,
				StuckEpicReAlertAfter:     6 * time.Hour,
				ExcludeLabels:             autoExcludeLabels,
			}

			// Bridge safety rails config from config.yaml
			if cfg != nil && cfg.Autonomous != nil && cfg.Autonomous.SafetyRails != nil {
				src := cfg.Autonomous.SafetyRails
				autoCfg.SafetyRails = &orchestrator.SafetyConfig{
					BudgetCeiling:     src.BudgetCeiling,
					CircuitBreakerMax: src.CircuitBreakerMax,
					RateLimitPerHour:  src.RateLimitPerHour,
					EpicCheckpoint:    src.EpicCheckpoint,
					HealthGateMin:     src.HealthGateMin,
				}
			}

			// Per-repo concurrency from the unified concurrency: block.
			if cfg != nil {
				rc := config.ResolveConcurrency(cfg)
				autoCfg.PerRepoMax = rc.PerRepoMax
				if cfg.Concurrency != nil && len(cfg.Concurrency.RepositoryOverrides) > 0 {
					autoCfg.RepositoryMaxConcurrent = cfg.Concurrency.RepositoryOverrides
				}
			}
			if cfg != nil && cfg.Autonomous != nil && cfg.Autonomous.DisableEpicBlockedByCascade {
				autoCfg.DisableEpicBlockedByCascade = true
			}
			applyStuckEpicConfig(&autoCfg, cfg)

			autoSched := orchestrator.NewAutonomousScheduler(
				sched, client, repoConfigs, nil, autoCfg, workdir,
			)
			// (#4151) Resolve the post-merge survival observation window from
			// pipeline.survival.window_days (safe on a nil Pipeline; cfg guarded).
			if cfg != nil {
				autoSched.SetSurvivalWindowDays(cfg.Pipeline.ResolveSurvivalWindowDays())
			}

			// Apply autonomous.enabled_repos filter from config.yaml when set.
			// Lets users scope autonomous scanning to a subset of repos.
			if cfg != nil && cfg.Autonomous != nil {
				if enabled := cfg.Autonomous.ResolvedEnabledRepos(cfg.Owner); len(enabled) > 0 {
					autoSched.FilterRepos(enabled)
					fmt.Printf("autonomous.enabled_repos applied: %v\n", enabled)
				}
			}

			// Wire dispatcher based on pipeline.executor config.
			if cfg != nil && cfg.PipelineExecutor != nil && cfg.PipelineExecutor.ExecutorType() == "cloud" {
				if cfg.PlatformURL != "" && cfg.APIKey != "" {
					cloudDisp := orchestrator.NewCloudDispatcher(cfg.PlatformURL, cfg.Owner, cfg.APIKey)
					autoSched.SetDispatcher(cloudDisp)
					fmt.Printf("executor=cloud (platform=%s)\n", cfg.PlatformURL)
				} else {
					fmt.Println("WARNING: pipeline.executor=cloud requires platform_url and api_key; falling back to local")
				}
			}

			if dryRun {
				fmt.Println("Running in dry-run mode — no pipelines will be executed")
			}

			fmt.Printf("Autonomous scheduler starting (interval=%s, slots=%d, budget=%d, repos=%d)\n",
				interval, maxSlots, budget, len(repoConfigs))
			for _, rc := range repoConfigs {
				fmt.Printf("  - %s/%s (project %d)\n", rc.Owner, rc.Name, rc.Project)
			}

			ctx := context.Background()
			err = autoSched.Run(ctx)

			if outputJSON {
				status := autoSched.Status()
				return printJSON(status)
			}

			if err != nil && err != context.Canceled {
				return err
			}

			// Print final status
			status := autoSched.Status()
			fmt.Printf("\nAutonomous scheduler finished: status=%s\n", status.Status)
			fmt.Printf("  Cycles:    %d\n", status.CyclesRun)
			fmt.Printf("  Completed: %d\n", len(status.Completed))
			fmt.Printf("  Failed:    %d\n", len(status.Failed))
			fmt.Printf("  Tokens:    %d / %d\n", status.TokensSpent, status.TokensCeiling)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub org/owner")
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "Repo names (comma-separated)")
	cmd.Flags().IntVar(&project, "project", 0, "Project board number")
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "Scan interval")
	cmd.Flags().Int64Var(&budget, "budget", 0, "Token budget ceiling (0 = unlimited)")
	cmd.Flags().IntVar(&maxSlots, "max-concurrent", 3, "Maximum concurrent pipeline slots")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would run without executing")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output final status as JSON")
	return cmd
}

func autonomousStatusCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show autonomous scheduler status",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, _ := os.Getwd()
			statePath := filepath.Join(workdir, ".nightgauge", "autonomous", "state.json")
			data, err := os.ReadFile(statePath)
			if os.IsNotExist(err) {
				fmt.Println("No autonomous scheduler state found. Run 'nightgauge autonomous run' first.")
				return nil
			}
			if err != nil {
				return fmt.Errorf("read state: %w", err)
			}
			var state orchestrator.AutonomousState
			if err := json.Unmarshal(data, &state); err != nil {
				return fmt.Errorf("parse state: %w", err)
			}

			// JSON output mode for scripting
			if outputJSON {
				out, _ := json.MarshalIndent(state, "", "  ")
				fmt.Println(string(out))
				return nil
			}

			// Human-readable table format
			statusDisplay := state.Status
			if len(statusDisplay) > 0 {
				statusDisplay = strings.ToUpper(statusDisplay[:1]) + statusDisplay[1:]
			}
			fmt.Printf("Autonomous Mode: %s\n", statusDisplay)

			if state.StartedAt != "" {
				elapsed := formatElapsedSince(state.StartedAt)
				fmt.Printf("Started: %s (%s ago)\n", state.StartedAt, elapsed)
			}

			fmt.Printf("Cycles: %d\n", state.CyclesRun)

			// Budget display
			if state.TokensCeiling > 0 {
				pct := 0
				if state.TokensCeiling > 0 {
					pct = int(state.TokensSpent * 100 / state.TokensCeiling)
				}
				fmt.Printf("Budget: %d / %d tokens (%d%%)\n", state.TokensSpent, state.TokensCeiling, pct)
			} else if state.TokensSpent > 0 {
				fmt.Printf("Tokens spent: %d (no ceiling)\n", state.TokensSpent)
			}

			// Running items with elapsed time
			if len(state.Running) > 0 {
				fmt.Printf("\nRunning (%d):\n", len(state.Running))
				for _, r := range state.Running {
					elapsed := formatElapsedSince(r.StartedAt)
					// Extract repo short name from owner/repo format
					repoShort := r.Repo
					if idx := strings.LastIndex(r.Repo, "/"); idx >= 0 {
						repoShort = r.Repo[idx+1:]
					}
					fmt.Printf("  #%-4d %-12s %s elapsed\n", r.Number, repoShort, elapsed)
				}
			}

			// Completed items
			if len(state.Completed) > 0 {
				fmt.Printf("\nCompleted (%d):\n", len(state.Completed))
				for _, c := range state.Completed {
					repoShort := c.Repo
					if idx := strings.LastIndex(c.Repo, "/"); idx >= 0 {
						repoShort = c.Repo[idx+1:]
					}
					fmt.Printf("  #%-4d %-12s %s  %s\n", c.Number, repoShort, "\u2713", c.Title)
				}
			}

			// Failed items
			if len(state.Failed) > 0 {
				fmt.Printf("\nFailed (%d):\n", len(state.Failed))
				for _, f := range state.Failed {
					repoShort := f.Repo
					if idx := strings.LastIndex(f.Repo, "/"); idx >= 0 {
						repoShort = f.Repo[idx+1:]
					}
					reason := f.Reason
					if reason == "" {
						reason = "unknown"
					}
					fmt.Printf("  #%-4d %-12s %s  %s\n", f.Number, repoShort, "\u2717", reason)
				}
			}

			// Remaining
			if state.Remaining > 0 {
				fmt.Printf("\nRemaining: %d issues\n", state.Remaining)
			}

			// Config warnings (Issue #3640)
			if len(state.ConfigWarnings) > 0 {
				fmt.Printf("\nConfig Warnings (%d):\n", len(state.ConfigWarnings))
				for _, w := range state.ConfigWarnings {
					prefix := "⚠"
					if w.Severity == "info" {
						prefix = "ℹ"
					}
					fmt.Printf("  %s [%s] %s\n", prefix, w.Kind, w.Message)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output status as JSON for scripting")
	return cmd
}

// formatElapsedSince returns a human-readable elapsed time string from an ISO
// timestamp (e.g. "2h 15m", "45s", "3m").
func formatElapsedSince(isoTimestamp string) string {
	t, err := time.Parse(time.RFC3339, isoTimestamp)
	if err != nil {
		return "?"
	}
	d := time.Since(t)
	if d < 0 {
		return "0s"
	}

	totalSeconds := int(d.Seconds())
	if totalSeconds < 60 {
		return fmt.Sprintf("%ds", totalSeconds)
	}
	totalMinutes := totalSeconds / 60
	if totalMinutes < 60 {
		return fmt.Sprintf("%dm", totalMinutes)
	}
	hours := totalMinutes / 60
	minutes := totalMinutes % 60
	if minutes > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dh", hours)
}

func autonomousStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "stop",
		Short:        "Signal the autonomous scheduler to stop",
		Long:         "Writes a stop signal to the autonomous state file. The running scheduler will pick it up on the next cycle.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, _ := os.Getwd()
			statePath := filepath.Join(workdir, ".nightgauge", "autonomous", "state.json")
			data, err := os.ReadFile(statePath)
			if os.IsNotExist(err) {
				fmt.Println("No autonomous scheduler state found.")
				return nil
			}
			if err != nil {
				return fmt.Errorf("read state: %w", err)
			}
			var state orchestrator.AutonomousState
			if err := json.Unmarshal(data, &state); err != nil {
				return fmt.Errorf("parse state: %w", err)
			}
			if state.Status != "running" && state.Status != "paused" {
				fmt.Printf("Scheduler is not running (status=%s)\n", state.Status)
				return nil
			}
			state.Status = "stopped"
			out, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal state: %w", err)
			}
			tmp := statePath + ".tmp"
			if err := os.WriteFile(tmp, out, 0644); err != nil {
				return fmt.Errorf("write state: %w", err)
			}
			if err := os.Rename(tmp, statePath); err != nil {
				return fmt.Errorf("rename state: %w", err)
			}
			fmt.Println("Stop signal written. The scheduler will stop on the next cycle.")
			return nil
		},
	}
}

// --- focus command ---

func focusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "focus",
		Short: "Manage the self-improvement focus lens",
		Long:  "Configure a focus lens that steers autonomous improvement, release-watch scoring, and continuous-improvement proposals toward a specific quality dimension.",
	}

	cmd.AddCommand(focusSetCmd(), focusShowCmd(), focusClearCmd(), focusListCmd(), focusRankCmd())
	return cmd
}

func focusSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <lens>",
		Short: "Set the active focus lens",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			m := focus.NewManager(workdir)
			s, err := m.Set(args[0], "cli")
			if err != nil {
				return err
			}
			_, lens, _ := m.Show()
			fmt.Printf("Focus set to: %s\n", s.ActiveLens)
			if lens != nil {
				fmt.Printf("Description: %s\n", lens.Description)
			}
			return nil
		},
	}
}

func focusShowCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Display the current focus lens",
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			m := focus.NewManager(workdir)
			s, lens, err := m.Show()
			if err != nil {
				return err
			}
			if jsonOutput {
				data, _ := json.MarshalIndent(map[string]interface{}{
					"activeLens":  s.ActiveLens,
					"description": lens.Description,
					"setAt":       s.SetAt,
					"setBy":       s.SetBy,
					"boosts":      lens.ScoringBoosts,
					"keywords":    lens.Keywords,
				}, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			fmt.Printf("Active focus: %s\n", s.ActiveLens)
			fmt.Printf("Description: %s\n", lens.Description)
			if !s.SetAt.IsZero() {
				fmt.Printf("Set at: %s\n", s.SetAt.Format(time.RFC3339))
				fmt.Printf("Set by: %s\n", s.SetBy)
			}
			if len(lens.ScoringBoosts) > 0 {
				fmt.Println("Scoring boosts:")
				for dim, boost := range lens.ScoringBoosts {
					fmt.Printf("  %s: +%d\n", dim, boost)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func focusClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Reset focus to general (no bias)",
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			m := focus.NewManager(workdir)
			if _, err := m.Clear("cli"); err != nil {
				return err
			}
			fmt.Println("Focus cleared — reverted to general (no bias)")
			return nil
		},
	}
}

func focusListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all available focus lenses",
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			m := focus.NewManager(workdir)
			s, _ := m.Load()
			for _, l := range m.AllLenses() {
				marker := "  "
				if l.Name == s.ActiveLens {
					marker = "▸ "
				}
				src := ""
				if l.Builtin {
					src = " (built-in)"
				} else {
					src = " (custom)"
				}
				fmt.Printf("%s%-15s %s%s\n", marker, l.Name, l.Description, src)
			}
			return nil
		},
	}
}

func focusRankCmd() *cobra.Command {
	var proposalsFile string
	var lensName string
	cmd := &cobra.Command{
		Use:          "rank",
		Short:        "Rank improvement proposals by focus lens alignment",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(proposalsFile)
			if err != nil {
				return fmt.Errorf("read proposals file: %w", err)
			}
			var proposals []focus.Proposal
			if err := json.Unmarshal(data, &proposals); err != nil {
				return fmt.Errorf("parse proposals: %w", err)
			}
			workdir, _ := os.Getwd()
			m := focus.NewManager(workdir)
			s, _ := m.Load()
			if lensName == "" {
				lensName = s.ActiveLens
			}
			lens := m.ResolveLens(lensName, s)
			result := focus.Rank(proposals, lens)
			return printJSON(result)
		},
	}
	cmd.Flags().StringVar(&proposalsFile, "proposals", "", "Path to proposals JSON file (required)")
	cmd.Flags().StringVar(&lensName, "lens", "", "Lens name (default: active lens from focus.yaml)")
	_ = cmd.MarkFlagRequired("proposals")
	return cmd
}

// --- intelligence command ---

func intelligenceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "intelligence",
		Short: "Pipeline intelligence analysis operations",
	}
	cmd.AddCommand(intelligenceLoopVerdictsCmd())
	return cmd
}

func intelligenceLoopVerdictsCmd() *cobra.Command {
	var workdir string
	var period int
	cmd := &cobra.Command{
		Use:          "loop-verdicts",
		Short:        "Analyze self-improvement loop effectiveness",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				workdir, _ = os.Getwd()
			}
			if period <= 0 {
				period = 30
			}
			report, err := loopverdicts.Analyze(loopverdicts.AnalyzeInput{
				WorkspaceRoot: workdir,
				Period:        period,
			})
			if err != nil {
				return err
			}
			return printJSON(report)
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().IntVar(&period, "period", 30, "Analysis period in days")
	return cmd
}

// --- auth command ---

func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authentication operations",
	}
	cmd.AddCommand(authCheckCmd())
	return cmd
}

func authCheckCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate GitHub token scopes for pipeline operations",
		Long: `Validates the configured GitHub token has the required OAuth scopes
for pipeline operations (repo, project, read:org) and reports the authenticated
user and their organisation memberships.

Resolution methods checked in order:
  1. GITHUB_TOKEN environment variable
  2. gh CLI (gh auth token)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				if jsonOutput {
					out, _ := json.Marshal(map[string]interface{}{
						"valid": false,
						"error": err.Error(),
					})
					fmt.Println(string(out))
					return nil
				}
				return fmt.Errorf("resolve token: %w", err)
			}

			ctx := cmd.Context()
			info, err := client.CheckTokenScopes(ctx)
			if err != nil {
				if jsonOutput {
					out, _ := json.Marshal(map[string]interface{}{
						"valid": false,
						"error": err.Error(),
					})
					fmt.Println(string(out))
					return nil
				}
				return fmt.Errorf("check token scopes: %w", err)
			}

			if jsonOutput {
				out, err := json.Marshal(info)
				if err != nil {
					return fmt.Errorf("marshal result: %w", err)
				}
				fmt.Println(string(out))
				return nil
			}

			// Human-readable output
			fmt.Printf("Authenticated user : %s\n", info.Login)
			fmt.Printf("Token resolution   : %s\n", info.Resolution)
			fmt.Printf("Scopes             : %s\n", strings.Join(info.Scopes, ", "))
			if len(info.OrgMemberships) > 0 {
				fmt.Printf("Org memberships    : %s\n", strings.Join(info.OrgMemberships, ", "))
			}
			if info.Valid {
				fmt.Println("Status             : OK — all required scopes present")
			} else {
				fmt.Printf("Status             : INSUFFICIENT — missing scopes: %s\n", strings.Join(info.MissingScopes, ", "))
				fmt.Println("Tip: generate a new token at https://github.com/settings/tokens with the required scopes.")
				return fmt.Errorf("token is missing required scopes: %s", strings.Join(info.MissingScopes, ", "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (for IPC consumption)")
	return cmd
}

// --- doctor command ---

func doctorCmd() *cobra.Command {
	var jsonOutput bool
	var adaptersFlag string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Verify the full environment is healthy for pipeline operations",
		Long: `Checks all prerequisites for pipeline operations:
  - nightgauge binary self-check (PATH)
  - gh CLI availability
  - GitHub authentication and token validity
  - Required OAuth scopes (repo, project, read:org)
  - GitHub API rate limit
  - .nightgauge/config.yaml validity
  - Project number and owner configuration

Pass --adapters to also report per-adapter health (Issue #4031):
  - CLI binary presence + version (with the min-known-version floor)
  - SDK adapter API-key configuration / local-server model env
  - Codex MCP managed-block presence in $CODEX_HOME/config.toml
An unhealthy adapter is reported as a warning (degraded), never a hard failure.

  nightgauge doctor --adapters codex,claude --json

Exit codes:
  0  healthy — all required checks pass, no warnings
  1  degraded — all required checks pass but optional items have warnings
  2  broken — one or more required checks failed; skills will halt at Phase 0

Use --json for machine-readable output (skills parse this format).`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, _ := os.Getwd()
			cfg, _ := config.Load(workdir)

			client, clientErr := clientFromConfig()
			if clientErr != nil {
				client = nil
			}

			adapters := parseAdaptersFlag(adaptersFlag)
			result := doctor.RunDoctor(cmd.Context(), cfg, client, adapters)

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
				if result.ExitCode != 0 {
					os.Exit(result.ExitCode)
				}
				return nil
			}

			// Human-readable output
			fmt.Printf("nightgauge doctor — schema v%d\n\n", result.V)
			checkOrder := []string{"binary", "gh", "github_auth", "api_user", "scopes", "rate_limit", "config", "project"}
			for _, key := range checkOrder {
				item, ok := result.Checks[key]
				if !ok {
					continue
				}
				status := "✓"
				if !item.OK {
					status = "✗"
				}
				detail := item.Detail
				if item.Error != "" {
					detail = item.Error
				}
				if detail != "" {
					fmt.Printf("  %s  %-14s  %s\n", status, key, detail)
				} else {
					fmt.Printf("  %s  %s\n", status, key)
				}
			}

			if len(result.Adapters) > 0 {
				fmt.Println("\nAdapters:")
				for _, a := range result.Adapters {
					status := "✓"
					if !a.OK {
						status = "✗"
					}
					detail := renderAdapterDetail(a)
					fmt.Printf("  %s  %-14s  %s\n", status, a.Adapter, detail)
					if a.Mcp != nil {
						mcpState := "config.toml missing"
						if a.Mcp.ConfigPresent {
							mcpState = "config.toml present"
							if a.Mcp.ManagedBlock {
								mcpState += ", MCP managed block present"
							} else {
								mcpState += ", no MCP managed block"
							}
						}
						fmt.Printf("        mcp: %s (%s)\n", mcpState, a.Mcp.ConfigPath)
					}
					if !a.OK && a.Remediation != "" {
						fmt.Printf("        → %s\n", a.Remediation)
					}
				}
			}

			if len(result.Warnings) > 0 {
				fmt.Println("\nWarnings:")
				for _, w := range result.Warnings {
					fmt.Printf("  ⚠  %s\n", w)
				}
			}
			if len(result.Errors) > 0 {
				fmt.Println("\nErrors:")
				for _, e := range result.Errors {
					fmt.Printf("  ✗  %s\n", e)
				}
			}
			if result.InstallInstructions != "" {
				fmt.Printf("\n%s\n", result.InstallInstructions)
			}

			fmt.Println()
			if result.ExitCode == 0 {
				fmt.Println("Status: healthy — environment ready for pipeline operations")
			} else if result.ExitCode == 1 {
				fmt.Println("Status: degraded — pipeline will run but some features may be limited")
			} else {
				fmt.Println("Status: broken — fix the errors above before running pipeline skills")
			}

			if result.ExitCode != 0 {
				os.Exit(result.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&adaptersFlag, "adapters", "", "Comma-separated adapters to health-check (e.g. codex,claude); 'all' checks every adapter")
	return cmd
}

// parseAdaptersFlag splits the --adapters value into a deduped, trimmed list.
// The sentinel "all" expands to every adapter the doctor knows how to check.
func parseAdaptersFlag(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.EqualFold(raw, "all") {
		return doctor.AllAdapterNames()
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// renderAdapterDetail builds the one-line human summary for an adapter row.
func renderAdapterDetail(a doctor.AdapterHealth) string {
	switch a.Kind {
	case "cli":
		if !a.Installed {
			return fmt.Sprintf("%s CLI not found on PATH", a.Binary)
		}
		v := a.Version
		if v == "" {
			v = "unknown version"
		}
		if a.MinVersion != "" && !a.VersionOK {
			return fmt.Sprintf("%s %s (below min %s)", a.Binary, v, a.MinVersion)
		}
		return fmt.Sprintf("%s %s", a.Binary, v)
	case "sdk":
		if a.Installed {
			return "API key configured"
		}
		return "API key not set"
	case "http":
		if a.Installed {
			return "local model configured"
		}
		return "local model env not set"
	default:
		if a.OK {
			return "ready"
		}
		return "not ready"
	}
}

// --- knowledge helpers (migrated to internal/knowledge; kept here only while
//     referenced by legacy tests) ---

func knowledgePruneEmptyCmd() *cobra.Command {
	var (
		issueNumber int
		jsonOutput  bool
	)

	cmd := &cobra.Command{
		Use:   "prune-empty",
		Short: "Remove knowledge directories that contain only boilerplate content",
		Long: `Walks .nightgauge/knowledge and removes issue-scoped directories
whose .md files contain no substantive content (fewer than 30 non-boilerplate characters).

Boilerplate is defined as: HTML comments, empty table rows, headings-only,
status checkboxes, and whitespace. Content is substantive when ≥30 characters
remain after stripping these patterns.

Use --issue to target a single issue's directory. Omit for a global prune.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitRepoRoot()
			if err != nil {
				return fmt.Errorf("not in a git repository: %w", err)
			}

			knowledgeRoot := filepath.Join(repoRoot, ".nightgauge", "knowledge")
			pruned, err := pruneEmptyKnowledge(knowledgeRoot, issueNumber)
			if err != nil {
				return fmt.Errorf("prune-empty: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]interface{}{
					"pruned": pruned,
				})
			}

			if len(pruned) == 0 {
				fmt.Println("No empty knowledge directories found.")
			} else {
				fmt.Printf("Pruned %d director(ies):\n", len(pruned))
				for _, p := range pruned {
					fmt.Printf("  - %s\n", p)
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Prune only directories for this issue number (0 = prune all)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON")
	return cmd
}

// pruneEmptyKnowledge removes issue-scoped knowledge directories whose .md
// files are all boilerplate. When issueNumber > 0 only that issue's directory
// is considered. Returns relative paths of removed directories.
func pruneEmptyKnowledge(knowledgeRoot string, issueNumber int) ([]string, error) {
	pruned := []string{}

	categoryEntries, err := os.ReadDir(knowledgeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return pruned, nil
		}
		return nil, err
	}

	issuePrefix := ""
	if issueNumber > 0 {
		issuePrefix = fmt.Sprintf("%d-", issueNumber)
	}

	for _, categoryEntry := range categoryEntries {
		if !categoryEntry.IsDir() {
			continue
		}
		categoryPath := filepath.Join(knowledgeRoot, categoryEntry.Name())
		issueDirEntries, err := os.ReadDir(categoryPath)
		if err != nil {
			continue
		}

		for _, issueDirEntry := range issueDirEntries {
			if !issueDirEntry.IsDir() {
				continue
			}
			dirName := issueDirEntry.Name()
			// When filtering by issue, skip directories that don't match the prefix.
			if issuePrefix != "" && !strings.HasPrefix(dirName, issuePrefix) {
				continue
			}

			issueDirPath := filepath.Join(categoryPath, dirName)
			substantive, err := dirHasSubstantiveContent(issueDirPath)
			if err != nil {
				continue
			}
			if !substantive {
				rel, err := filepath.Rel(filepath.Dir(filepath.Dir(knowledgeRoot)), issueDirPath)
				if err != nil {
					rel = issueDirPath
				}
				if removeErr := os.RemoveAll(issueDirPath); removeErr == nil {
					pruned = append(pruned, rel)
				}
			}
		}
	}
	return pruned, nil
}

// dirHasSubstantiveContent returns true if any .md file in the directory has
// substantive content (≥30 characters after stripping boilerplate).
func dirHasSubstantiveContent(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if knowledgeContentIsSubstantive(string(data)) {
			return true, nil
		}
	}
	return false, nil
}

// knowledgeContentIsSubstantive mirrors KnowledgeService.contentIsSubstantive()
// from the TypeScript SDK. Content is substantive when ≥30 non-boilerplate
// characters remain after stripping HTML comments, headings, empty table rows,
// status checkboxes, and collapsing whitespace.
func knowledgeContentIsSubstantive(content string) bool {
	// Strip HTML comments
	htmlCommentRe := regexp.MustCompile(`(?s)<!--.*?-->`)
	stripped := htmlCommentRe.ReplaceAllString(content, "")

	// Strip headings
	headingRe := regexp.MustCompile(`(?m)^#+\s.*$`)
	stripped = headingRe.ReplaceAllString(stripped, "")

	// Strip empty table rows (lines containing only pipes, dashes, spaces)
	tableRowRe := regexp.MustCompile(`(?m)^\|[\s\|\-]*\|$`)
	stripped = tableRowRe.ReplaceAllString(stripped, "")

	// Strip status checkboxes
	checkboxRe := regexp.MustCompile(`(?m)^- \[.\].*$`)
	stripped = checkboxRe.ReplaceAllString(stripped, "")

	// Collapse whitespace
	wsRe := regexp.MustCompile(`\s+`)
	stripped = strings.TrimSpace(wsRe.ReplaceAllString(stripped, " "))

	return len(stripped) >= 30
}

// gitRepoRoot returns the root of the git repository containing the cwd.
func gitRepoRoot() (string, error) {
	// Walk up from cwd to find .git
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no .git directory found")
}

// --- scan command ---

func scanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Codebase scanners (dependencies, etc.)",
		Long:  `Deterministic codebase scanners. Replaces ad-hoc bash + jq audit chains in skills with a single binary call that emits a stable JSON schema.`,
	}
	cmd.AddCommand(scanDepsCmd())
	cmd.AddCommand(scanEcosystemCmd())
	cmd.AddCommand(scanSecretsCmd())
	cmd.AddCommand(scanDebtCmd())
	cmd.AddCommand(scanTestsCmd())
	cmd.AddCommand(scanToolingCmd())
	return cmd
}

func scanDepsCmd() *cobra.Command {
	var (
		jsonOutput   bool
		workdir      string
		ecosystems   []string
		includeVulns bool
	)
	cmd := &cobra.Command{
		Use:   "deps",
		Short: "Scan project dependencies for vulnerabilities and outdated versions",
		Long: `Run per-ecosystem audit and outdated checks across nodejs, python, go, and rust.

Auto-detects ecosystems present in --workdir (package.json, requirements.txt /
pyproject.toml, go.mod, Cargo.toml). Tools that are not on PATH are recorded as
unavailable rather than causing a failure — the verb is non-fatal by design so
skills can rely on a populated JSON shape.

Schema version 1 — field names (ecosystems, vulnerabilities, outdated, totals,
warnings) are stable. Skills parse output via jq paths; any breaking change
requires bumping v.

Exit codes:
  0  scan completed (vulnerabilities may be present — counts, not gates)
  1  every requested ecosystem reported unavailable tooling (warning)
  2  hard error (invalid --ecosystems value, internal failure)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := scan.RunDepScan(cmd.Context(), scan.Options{
				Workdir:      workdir,
				Ecosystems:   ecosystems,
				IncludeVulns: includeVulns,
			})
			if err != nil {
				return err
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printScanDepsHuman(result)
			}

			anyDetected := false
			anyAvailable := false
			for _, e := range result.Ecosystems {
				if e.Detected {
					anyDetected = true
				}
				if e.Available {
					anyAvailable = true
				}
			}
			if anyDetected && !anyAvailable {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to scan (default: current working directory)")
	cmd.Flags().StringSliceVar(&ecosystems, "ecosystems", nil, "Comma-separated subset (default: auto-detect all). Supported: nodejs,python,go,rust")
	cmd.Flags().BoolVar(&includeVulns, "include-vulns", true, "Run vulnerability audit step (set false to skip; outdated still runs)")
	return cmd
}

// printScanDepsHuman renders the scan result in a compact human-readable form.
func printScanDepsHuman(r *scan.DepScanResult) {
	fmt.Printf("nightgauge scan deps — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n\n", r.Workdir)
	for _, name := range []string{"nodejs", "python", "go", "rust"} {
		e := r.Ecosystems[name]
		status := "—"
		switch {
		case !e.Detected:
			status = "not detected"
		case !e.Available:
			status = "tool unavailable"
		default:
			status = "scanned"
		}
		fmt.Printf("  %-7s  %s\n", name, status)
		if e.Vulnerabilities != nil {
			fmt.Printf("           vulns: critical=%d high=%d moderate=%d low=%d\n",
				e.Vulnerabilities.Critical, e.Vulnerabilities.High,
				e.Vulnerabilities.Moderate, e.Vulnerabilities.Low)
		}
		if e.Detected {
			fmt.Printf("           outdated: %d\n", e.Outdated)
		}
		for _, errMsg := range e.Errors {
			fmt.Printf("           ! %s\n", errMsg)
		}
	}
	fmt.Printf("\ntotals: critical=%d high=%d moderate=%d low=%d outdated=%d\n",
		r.Totals.Critical, r.Totals.High, r.Totals.Moderate, r.Totals.Low, r.Totals.Outdated)
}

func scanEcosystemCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:   "ecosystem",
		Short: "Detect language ecosystems and monorepo structure in a workdir",
		Long: `Detect which language ecosystems (nodejs, python, go, rust, java) are present
in --workdir and whether the project is a monorepo (npm/yarn/pnpm workspaces,
Cargo workspace, go.work). Replaces the bash + jq Phase 0.2/0.3 file-existence
chain duplicated across health-check, security-audit, refactor-rewrite, and
dep-modernize (audit row B1).

Schema version 1 — field names (v, ecosystems, is_monorepo, monorepo_kind,
packages, lockfile, lockfiles, warnings) are stable. Skills parse output via
jq paths; any breaking change requires bumping v.

The verb is non-fatal by design — malformed manifests and unparseable
workspace declarations are recorded in warnings[] rather than causing the scan
to fail.

Exit codes:
  0  scan completed
  2  hard error (e.g. unresolvable workdir, internal failure)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := scan.RunEcosystemScan(cmd.Context(), scan.EcosystemOptions{
				Workdir: workdir,
			})
			if err != nil {
				return err
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printScanEcosystemHuman(result)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to scan (default: current working directory)")
	return cmd
}

// printScanEcosystemHuman renders the ecosystem-scan result in a compact
// human-readable form mirroring the scan deps layout.
func printScanEcosystemHuman(r *scan.EcosystemScanResult) {
	fmt.Printf("nightgauge scan ecosystem — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n\n", r.Workdir)
	if len(r.Ecosystems) == 0 {
		fmt.Println("  ecosystems: (none detected)")
	} else {
		fmt.Printf("  ecosystems: %s\n", strings.Join(r.Ecosystems, ", "))
	}
	if r.IsMonorepo {
		fmt.Printf("  monorepo:   %s\n", r.MonorepoKind)
		if len(r.Packages) > 0 {
			fmt.Printf("  packages:   %s\n", strings.Join(r.Packages, ", "))
		}
	} else {
		fmt.Println("  monorepo:   no")
	}
	hasLockfile := false
	for _, lf := range r.Lockfiles {
		if lf != "" {
			hasLockfile = true
			break
		}
	}
	if hasLockfile {
		fmt.Println("  lockfiles:")
		for _, name := range []string{"nodejs", "python", "go", "rust", "java"} {
			if lf := r.Lockfiles[name]; lf != "" {
				fmt.Printf("    %-7s %s\n", name, lf)
			}
		}
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

func scanSecretsCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Scan a workdir for hard-coded secret patterns",
		Long: `Scan --workdir for the six fixed secret patterns codified in the
security-audit skill (audit row B41): generic key/value secrets, PEM private
key headers, AWS access keys, JWT/bearer tokens, embedded connection strings,
and committed .env files. Replaces the inline grep+wc -l chains in
security-audit Phase 2.2 with a single binary call emitting a stable JSON
schema.

Schema version 1 — field names (v, workdir, patterns, total, warnings) and
the six pattern keys (generic_kv, pem_private_key, aws_access_key,
jwt_bearer, connection_string, dotenv_files) are stable. Skills parse output
via fixed jq paths; any breaking change requires bumping v.

Counts are line-based (each pattern increments at most once per matching
line, mirroring 'grep -rn ... | wc -l' behavior) so scoring rubrics
calibrated against the prior implementation remain valid.

The verb is non-fatal by design — unreadable files and oversize-skips are
recorded in warnings[] rather than causing the scan to fail. Detection is
pure regex over file content; no subprocess, no network.

Excluded directories (pruned at walk time): .git, node_modules, vendor,
dist, build, coverage. Files larger than 5 MiB are skipped with a warning.

Exit codes:
  0  scan completed (matches may be present — counts, not gates)
  2  hard error (e.g. unresolvable workdir, internal failure)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := scan.RunSecretsScan(cmd.Context(), scan.SecretsOptions{
				Workdir: workdir,
			})
			if err != nil {
				return err
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printScanSecretsHuman(result)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to scan (default: current working directory)")
	return cmd
}

// printScanSecretsHuman renders the secret-scan result in a compact
// human-readable form mirroring the scan ecosystem layout.
func printScanSecretsHuman(r *scan.SecretsScanResult) {
	fmt.Printf("nightgauge scan secrets — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n\n", r.Workdir)
	patternLabels := []struct{ key, label string }{
		{"generic_kv", "generic key/value"},
		{"pem_private_key", "PEM private key"},
		{"aws_access_key", "AWS access key"},
		{"jwt_bearer", "JWT / bearer token"},
		{"connection_string", "connection string"},
		{"dotenv_files", "committed .env files"},
	}
	for _, p := range patternLabels {
		fmt.Printf("  %-20s %d\n", p.label, r.Patterns[p.key])
	}
	fmt.Printf("\ntotal: %d\n", r.Total)
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

func scanDebtCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:   "debt",
		Short: "Count TODO/FIXME/HACK/XXX debt markers in source files",
		Long: `Walk --workdir counting TODO/FIXME/HACK/XXX comment markers in files
matching the source-extension allowlist (.ts, .tsx, .js, .jsx, .py, .go,
.rs, .java, .kt). Replaces the inline grep + awk chain in health-check
Phase 3.1 and the equivalent pass in refactor-rewrite Phase 2.2 (audit
row B5).

Counts are line-based — each marker increments at most once per matching
line, mirroring 'grep -cE TODO|FIXME|HACK|XXX file | awk' behavior. Word
boundaries are enforced (\bTODO\b) so 'TODOIST' does not match TODO; the
underlying rubric tolerances (e.g. <5 markers → 90-100, >100 → 0-29) are
wide enough that this slight tightening keeps scoring calibrated.

Schema version 1 — field names (v, workdir, markers, files, warnings) and
marker keys (todo, fixme, hack, xxx, total) are stable. Skills parse output
via fixed jq paths; any breaking change requires bumping v.

Excluded directories (pruned at walk time): .git, node_modules, vendor,
dist, build, coverage. Files larger than 5 MiB are skipped with a warning.

Exit codes:
  0  scan completed
  2  hard error (e.g. unresolvable workdir, internal failure)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := scan.RunDebtScan(cmd.Context(), scan.DebtOptions{
				Workdir: workdir,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printScanDebtHuman(result)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to scan (default: current working directory)")
	return cmd
}

func printScanDebtHuman(r *scan.DebtScanResult) {
	fmt.Printf("nightgauge scan debt — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n\n", r.Workdir)
	fmt.Printf("  todo:   %d\n", r.Markers.TODO)
	fmt.Printf("  fixme:  %d\n", r.Markers.FIXME)
	fmt.Printf("  hack:   %d\n", r.Markers.HACK)
	fmt.Printf("  xxx:    %d\n", r.Markers.XXX)
	fmt.Printf("\ntotal: %d (across %d source files)\n", r.Markers.Total, r.Files)
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

func scanTestsCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:   "tests",
		Short: "Count test files vs source files and report the test-to-source ratio",
		Long: `Walk --workdir counting test files (matching *.test.*, *.spec.*,
*_test.*, test_*) versus source files (same extension allowlist, minus
test files). Pure path classification — no file-content reads.

Replaces the parallel-Glob test/source counting in health-check Phase 2.2
and the equivalent inline pass in refactor-rewrite Phase 2.1 (audit
row B5).

Schema version 1 — field names (v, workdir, source_files, test_files,
test_to_source_ratio, warnings) are stable. Skills parse output via fixed
jq paths; any breaking change requires bumping v.

The ratio is float64 — 0 when source_files=0 (explicit zero-source guard,
not NaN). A test file is NEVER also counted as a source file.

Excluded directories (pruned at walk time): .git, node_modules, vendor,
dist, build, coverage.

Exit codes:
  0  scan completed
  2  hard error (e.g. unresolvable workdir, internal failure)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := scan.RunTestsScan(cmd.Context(), scan.TestsOptions{
				Workdir: workdir,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printScanTestsHuman(result)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to scan (default: current working directory)")
	return cmd
}

func printScanTestsHuman(r *scan.TestsScanResult) {
	fmt.Printf("nightgauge scan tests — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n\n", r.Workdir)
	fmt.Printf("  source files: %d\n", r.SourceFiles)
	fmt.Printf("  test files:   %d\n", r.TestFiles)
	fmt.Printf("  ratio:        %.3f\n", r.TestToSourceRatio)
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

func scanToolingCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:   "tooling",
		Short: "Probe for linter and formatter config files at the workdir root",
		Long: `Stat-probe --workdir for the canonical linter config files
(.eslintrc*, ruff.toml, .golangci.yml, clippy.toml, .flake8, .pylintrc,
checkstyle.xml) and formatter config files (.prettierrc*, .editorconfig).
Additionally, read pyproject.toml when present and detect the
[tool.ruff] / [tool.black] / [tool.ruff.format] sections — mirroring the
health-check Phase 3.2 and refactor-rewrite Phase 2.2 grep chains
(audit row B5).

Schema version 1 — field names (v, workdir, linters, formatters,
linter_present, formatter_present, warnings) and the linter/formatter
keys are stable. All map keys are pre-populated even when false so
consumer jq paths never resolve to null.

Linter keys: eslint, ruff, golangci, clippy, flake8, pylint, checkstyle.
Formatter keys: prettier, editorconfig, black, ruff_format.

Probes are O(linterCount + formatterCount) stat calls plus one bounded
pyproject.toml read (capped at 1 MiB). No directory walk.

Exit codes:
  0  scan completed
  2  hard error (e.g. unresolvable workdir, internal failure)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := scan.RunToolingScan(cmd.Context(), scan.ToolingOptions{
				Workdir: workdir,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printScanToolingHuman(result)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to probe (default: current working directory)")
	return cmd
}

func printScanToolingHuman(r *scan.ToolingScanResult) {
	fmt.Printf("nightgauge scan tooling — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n\n", r.Workdir)
	fmt.Println("  linters:")
	for _, k := range []string{"eslint", "ruff", "golangci", "clippy", "flake8", "pylint", "checkstyle"} {
		fmt.Printf("    %-12s %v\n", k, r.Linters[k])
	}
	fmt.Println("  formatters:")
	for _, k := range []string{"prettier", "editorconfig", "black", "ruff_format"} {
		fmt.Printf("    %-12s %v\n", k, r.Formatters[k])
	}
	fmt.Printf("\nlinter_present:    %v\n", r.LinterPresent)
	fmt.Printf("formatter_present: %v\n", r.FormatterPresent)
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// --- pipeline command ---

func pipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Pipeline history operations (aggregate)",
		Long: `Deterministic readers over .nightgauge/pipeline/history. Replaces the
inline-Python aggregators duplicated across the pipeline-audit, pipeline-health,
retro, and continuous-improvement skills with a single Go verb that emits a
stable JSON schema (audit row B2).`,
	}
	cmd.AddCommand(pipelineAggregateCmd())
	cmd.AddCommand(pipelineBatchFailuresCmd())
	cmd.AddCommand(pipelineBackfillCmd())
	return cmd
}

// pipelineBatchFailuresCmd extracts pipeline failure rows from
// batch-state.json AND history JSONL files, plus a context-files fallback.
// Replaces the inline-Python parsers in retro Phases 2.1, 2.2, and 2.4
// (audit row B29).
//
// Exit codes:
//
//	0 — extract completed (zero rows is not an error)
//	2 — hard error (invalid flag value, internal failure)
func pipelineBatchFailuresCmd() *cobra.Command {
	var (
		issue       int
		since       string
		allFailures bool
		workdir     string
		jsonOutput  bool
	)
	cmd := &cobra.Command{
		Use:   "batch-failures",
		Short: "Extract pipeline failure rows from batch-state and history JSONL",
		Long: `Reads .nightgauge/pipeline/batch-state.json AND
.nightgauge/pipeline/history/*.jsonl, emitting a stable JSON output that
unifies failure rows from both sources plus a context-files fallback. Replaces
~150 lines of inline Python in retro Phases 2.1, 2.2, and 2.4 (audit row B29).

Output schema is stable v1 — field names locked after first merge; additive
fields allowed. Missing input files are NOT errors (zero-row inputs).

Exit codes:
  0  extract completed
  2  hard error (invalid flag value, internal failure)`,
		Example: `  nightgauge pipeline batch-failures --json
  nightgauge pipeline batch-failures --since 2026-04-01 --json
  nightgauge pipeline batch-failures --issue 3087 --json
  nightgauge pipeline batch-failures --all-failures --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if since != "" && !isYYYYMMDD(since) {
				return fmt.Errorf("--since must be YYYY-MM-DD (got %q)", since)
			}
			result, err := batchfailures.Extract(batchfailures.Options{
				Workdir:     workdir,
				Issue:       issue,
				Since:       since,
				AllFailures: allFailures,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
				return nil
			}
			printPipelineBatchFailuresHuman(&result)
			return nil
		},
	}
	cmd.Flags().IntVar(&issue, "issue", 0, "Filter to a single issue number (0 = all)")
	cmd.Flags().StringVar(&since, "since", "", "Lower bound YYYY-MM-DD (history filename pre-filter)")
	cmd.Flags().BoolVar(&allFailures, "all-failures", false, "Disable --since filter for history (collect all failures regardless of date)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

func printPipelineBatchFailuresHuman(r *batchfailures.Result) {
	fmt.Printf("nightgauge pipeline batch-failures — schema v%d\n", r.V)
	if r.Batch != nil {
		fmt.Printf("batch: status=%s issues=%d\n", r.Batch.BatchStatus, r.Batch.TotalIssues)
	} else {
		fmt.Println("batch: (no batch-state.json)")
	}
	fmt.Printf("batch_failures=%d  history_failures=%d  context_failures=%d  skipped_records=%d\n",
		len(r.BatchFailures), len(r.HistoryFailures), len(r.ContextFailures), r.SkippedRecords)
	for _, f := range r.BatchFailures {
		fmt.Printf("  [batch]   #%d %s — %s (%d failed stages)\n",
			f.IssueNumber, f.Title, f.Status, len(f.FailedStages))
	}
	for _, f := range r.HistoryFailures {
		fmt.Printf("  [history] #%d %s — %s ($%.4f)\n",
			f.IssueNumber, f.Title, f.Outcome, f.EstimatedCostUSD)
	}
	for _, f := range r.ContextFailures {
		fmt.Printf("  [context] #%d has_dev=%v — %s\n",
			f.IssueNumber, f.HasDevContext, f.InferredFailure)
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

func pipelineAggregateCmd() *cobra.Command {
	var (
		runs       int
		since      string
		until      string
		issue      int
		include    string
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:   "aggregate",
		Short: "Aggregate per-stage and per-run metrics from JSONL history",
		Long: `Aggregate per-stage durations, token counts, model usage, and per-run cost
metrics from .nightgauge/pipeline/history/YYYY-MM-DD.jsonl. Replaces the
~300 lines of inline-Python aggregation in skills/nightgauge-pipeline-audit
(audit row B2). Output schema is stable v1 — field names locked after first
merge; additive fields allowed.

The --include flag accepts a comma-separated list of optional analysis blocks.
Currently only "analysis" is recognized (size-accuracy + weekly-trend block,
matching the audit skill's Issue #1591 logic). Unknown values produce a warning
in the output rather than an error — forward-compatible for future blocks like
"signals" or "gates".

Exit codes:
  0  aggregate completed (zero records is not an error)
  1  history directory missing AND --workdir was explicit
  2  hard error (invalid flag value, internal failure)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			includeAnalysis := false
			extraWarnings := []string{}
			if include != "" {
				for _, tok := range strings.Split(include, ",") {
					tok = strings.TrimSpace(tok)
					switch tok {
					case "":
						continue
					case "analysis":
						includeAnalysis = true
					default:
						extraWarnings = append(extraWarnings,
							fmt.Sprintf("unknown --include value %q (ignored)", tok))
					}
				}
			}

			records, err := pipeline.LoadHistory(workdir, since, until, 0)
			if err != nil {
				return err
			}

			result, warnings := pipeline.Aggregate(records, pipeline.Options{
				Runs:            runs,
				Since:           since,
				Until:           until,
				Issue:           issue,
				IncludeAnalysis: includeAnalysis,
			})
			if len(extraWarnings) > 0 {
				result.Warnings = append(result.Warnings, extraWarnings...)
			}
			if len(warnings) > 0 {
				result.Warnings = append(result.Warnings, warnings...)
			}

			// Layer the additive knowledge roll-up (Issue #3592, ADR-006).
			// A missing knowledge-events.jsonl leaves the zero-valued aggregate
			// in place; only hard errors are surfaced as warnings.
			kwRoot := workdir
			if kwRoot == "" {
				if cwd, err := os.Getwd(); err == nil {
					kwRoot = cwd
				}
			}
			if kAgg, kErr := pipeline.AggregateKnowledge(kwRoot); kErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("knowledge aggregate: %v", kErr))
			} else {
				result.Knowledge = kAgg
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printPipelineAggregateHuman(&result)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&runs, "runs", 0, "Limit to last N runs (0 = unbounded)")
	cmd.Flags().StringVar(&since, "since", "", "Lower bound YYYY-MM-DD (filename pre-filter)")
	cmd.Flags().StringVar(&until, "until", "", "Upper bound YYYY-MM-DD (forward-compat)")
	cmd.Flags().IntVar(&issue, "issue", 0, "Filter to a single issue number (0 = all)")
	cmd.Flags().StringVar(&include, "include", "", "Optional analysis blocks (comma-separated). Currently: analysis")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "History root (default: current working directory)")
	return cmd
}

func printPipelineAggregateHuman(r *pipeline.Result) {
	fmt.Printf("nightgauge pipeline aggregate — schema v%d\n", r.V)
	fmt.Printf("runs analyzed: %d", r.RunsAnalyzed)
	if r.DateFrom != "" {
		fmt.Printf(" (%s..%s)", r.DateFrom, r.DateTo)
	}
	fmt.Println()
	if r.Filters.Runs > 0 || r.Filters.Since != "" || r.Filters.Until != "" || r.Filters.Issue > 0 {
		fmt.Printf("filters: runs=%d since=%q until=%q issue=%d\n",
			r.Filters.Runs, r.Filters.Since, r.Filters.Until, r.Filters.Issue)
	}
	fmt.Println()
	fmt.Printf("  %-20s %6s %12s %12s %10s\n", "stage", "count", "median ms", "p90 ms", "failures")
	for _, stage := range pipeline.StageNames {
		agg := r.StageMetrics[stage]
		failures := agg.Status["failed"] + agg.Status["error"]
		fmt.Printf("  %-20s %6d %12.0f %12.0f %10d\n",
			stage, agg.DurationStats.Count, agg.DurationStats.Median,
			agg.DurationStats.P90, failures)
	}
	if r.Analysis != nil {
		fmt.Println()
		fmt.Printf("size baselines (with-size=%d, without-size=%d):\n",
			r.Analysis.RunsWithSize, r.Analysis.RunsWithoutSize)
		for _, size := range []string{"XS", "S", "M", "L", "XL"} {
			bl, ok := r.Analysis.SizeBaselines[size]
			if !ok {
				continue
			}
			ar := r.Analysis.SizeAccuracyRates[size]
			fmt.Printf("  %-3s n=%-3d median=$%.4f avg=$%.4f accuracy=%.1f%%\n",
				size, bl.Count, bl.MedianCost, bl.AvgCost, ar.AccuracyPct)
		}
		if len(r.Analysis.Oversized) > 0 || len(r.Analysis.Undersized) > 0 {
			fmt.Printf("oversized=%d undersized=%d\n",
				len(r.Analysis.Oversized), len(r.Analysis.Undersized))
		}
	}
	// Recovery summary (Issue #3239). Emit only when we observed any
	// recovery_events so quiet runs stay quiet.
	if r.Recovery.TotalEvents > 0 {
		fmt.Println()
		actionParts := make([]string, 0, len(r.Recovery.ByAction))
		for action, count := range r.Recovery.ByAction {
			actionParts = append(actionParts, fmt.Sprintf("%s=%d", action, count))
		}
		sort.Strings(actionParts)
		fmt.Printf("recovery: %.1f%% (runs_with_events=%d, total_events=%d; %s)\n",
			r.Recovery.RecoveryRate*100,
			r.Recovery.RunsWithEvents,
			r.Recovery.TotalEvents,
			strings.Join(actionParts, " "))
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// --- docs command ---

func docsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Documentation operations (markdown link validation, etc.)",
		Long: `Deterministic documentation operations. Replaces ad-hoc bash + grep markdown
chains in skills with a single binary call that emits a stable JSON schema
(audit row B6).`,
	}
	cmd.AddCommand(docsCheckLinksCmd())
	cmd.AddCommand(docsSnapshotDiffCmd())
	cmd.AddCommand(docsDetectPatternsCmd())
	cmd.AddCommand(docsVersionConsistencyCmd())
	cmd.AddCommand(docsCheckFreshnessCmd())
	return cmd
}

func docsSnapshotDiffCmd() *cobra.Command {
	var (
		snapshotFile string
		urlsFile     string
		jsonOutput   bool
	)
	cmd := &cobra.Command{
		Use:   "snapshot-diff",
		Short: "Detect new, changed, and removed pages by comparing URL hashes against a snapshot",
		Long: `Fetch each URL in --urls, compute its sha256 hash, and compare against the
hashes recorded in --snapshot. Emits a stable JSON object with three arrays:

  new[]     — URLs present in --urls but absent from the snapshot
  changed[] — URLs whose content hash differs from the snapshot
  removed[] — URLs in the snapshot but absent from --urls

This replaces the bash + curl + sha256sum chain in docs-watch Phase 4
(audit row B34). Fetch failures for individual URLs are recorded as warnings
and skipped — they do not cause a non-zero exit.

Schema version 1 — field names (v, new, changed, removed, warnings) are
stable. Skills parse output via fixed jq paths; any breaking change requires
bumping v.

Exit codes:
  0  completed, no errors
  2  hard error (e.g. snapshot file not found, malformed JSON)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := docspkg.SnapshotDiff(docspkg.SnapshotDiffOptions{
				SnapshotFile: snapshotFile,
				URLsFile:     urlsFile,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "docs snapshot-diff: %v\n", err)
				os.Exit(2)
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printDocsSnapshotDiffHuman(result)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&snapshotFile, "snapshot", "", "Path to existing snapshot JSON file (required)")
	cmd.Flags().StringVar(&urlsFile, "urls", "", "Path to file with current URLs, one per line (required)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	_ = cmd.MarkFlagRequired("snapshot")
	_ = cmd.MarkFlagRequired("urls")
	return cmd
}

// printDocsSnapshotDiffHuman renders the snapshot-diff result in a compact
// human-readable form.
func printDocsSnapshotDiffHuman(r *docspkg.SnapshotDiffResult) {
	fmt.Printf("nightgauge docs snapshot-diff — schema v%d\n", r.V)
	fmt.Printf("new: %d  changed: %d  removed: %d\n\n", len(r.New), len(r.Changed), len(r.Removed))
	for _, e := range r.New {
		fmt.Printf("  + %s  (%s)\n", e.URL, e.Hash)
	}
	for _, e := range r.Changed {
		fmt.Printf("  ~ %s  %s → %s\n", e.URL, e.OldHash, e.Hash)
	}
	for _, e := range r.Removed {
		fmt.Printf("  - %s\n", e.URL)
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

func docsCheckLinksCmd() *cobra.Command {
	var (
		jsonOutput       bool
		root             string
		target           string
		section          string
		excludeTemplates bool
	)
	cmd := &cobra.Command{
		Use:   "check-links",
		Short: "Validate relative markdown links resolve to real files",
		Long: `Walk --root for *.md files and verify that every relative link in each file
resolves to an existing path. External links (http://, https://, mailto:, etc.)
and in-page anchors (#section) are ignored by design — skills excluded the
same set from their grep patterns. Code-fence content (` + "```" + ` and ~~~) is
skipped so example links inside code blocks are not flagged.

Replaces the bash + dirname + grep link-validation chain duplicated across
docs-write Phase 7 and update-docs Phase 4.5 (audit row B6).

Schema version 1 — field names (v, root, files_scanned, links_total,
links_broken, findings, warnings) are stable. Skills parse output via fixed jq
paths; any breaking change requires bumping v. ` + "`reason`" + ` is a closed enum:
file_not_found, outside_root, unreadable.

The verb is non-fatal by design — missing files populate findings[]; unreadable
files become warnings[]. Hard input errors (unresolvable root, target outside
root) exit 2.

Exit codes:
  0  scan completed, no broken links
  1  scan completed, one or more broken links found
  2  hard error (e.g. unresolvable root, target outside root)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := docspkg.Run(cmd.Context(), docspkg.CheckLinksOptions{
				Root:             root,
				Target:           target,
				Section:          section,
				ExcludeTemplates: excludeTemplates,
			})
			if err != nil {
				// Hard error path — surface as exit code 2 to match scan verbs.
				fmt.Fprintf(os.Stderr, "docs check-links: %v\n", err)
				os.Exit(2)
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printDocsCheckLinksHuman(result)
			}

			if result.LinksBroken > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&root, "root", "", "Directory tree to scan (default: current working directory)")
	cmd.Flags().StringVar(&target, "target", "", "Restrict validation to a single markdown file (relative to --root, or absolute)")
	cmd.Flags().StringVar(&section, "section", "", "Restrict validation to links inside the named heading subtree (case-insensitive)")
	cmd.Flags().BoolVar(&excludeTemplates, "exclude-templates", false, "Skip skill (*/skills/*/SKILL.md) and command (*/claude-plugins/*/commands/*) files")
	return cmd
}

func docsDetectPatternsCmd() *cobra.Command {
	var (
		filesGlob  string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "detect-patterns",
		Short: "Detect codebase architectural patterns from a file glob",
		Long: `Expand --files glob and search each matched file for keywords belonging to
a closed set of 7 architectural pattern slugs:

  event-system      EventEmitter, .emit(), _onDid, vscode.EventEmitter
  auth-security     authenticate, authorize, middleware, guard, validateToken
  service-pattern   class.*Service / Manager / Provider
  repo-storage      class.*Repository / Store, db.query, prisma.
  config-system     config, settings, schema, zod, Config
  pipeline-workflow stage, orchestrat, pipeline, PipelineOrchestrator
  ipc-transport     stdio, ipc, socket, exec, spawn

Each slug is included in the output only when at least one file matches.
Unreadable files produce non-fatal warnings — the command exits 0.

Replaces the inline bash grep loop in docs-write Phase 1.5 Step 1.5.1
(audit row B35).

Schema version 1 — field names (v, patterns, warnings) are stable. Skills
parse output via fixed jq paths; any breaking change requires bumping v.

Exit codes:
  0  completed (including when no patterns matched or files were unreadable)
  2  hard error (e.g. malformed glob syntax, --files not provided)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := docspkg.DetectPatterns(docspkg.PatternDetectOptions{
				FilesGlob: filesGlob,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "docs detect-patterns: %v\n", err)
				os.Exit(2)
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				fmt.Print(docspkg.PrintDetectPatternsHuman(result))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&filesGlob, "files", "", "Glob pattern to match source files (required)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	_ = cmd.MarkFlagRequired("files")
	return cmd
}

func docsVersionConsistencyCmd() *cobra.Command {
	var (
		root       string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "version-consistency",
		Short: "Detect version number mismatches across project files",
		Long: `Detect project type from root directory markers (package.json, pyproject.toml,
Cargo.toml, go.mod, VERSION, skills/, *.csproj) and validate that version
references in markdown files match the authoritative source-of-truth version.

Replaces the bash project-type detection and version extraction prose in
update-docs Phase 4.6 (audit row B36).

Schema version 1 — field names (v, root, project_type, source_file,
source_version, mismatches, mismatches_count, warnings) are stable. Skills
parse output via fixed jq paths; any breaking change requires bumping v.

Exit codes:
  0  no mismatches found
  1  one or more mismatches found
  2  hard error (e.g. unresolvable root)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := docspkg.VersionConsistency(cmd.Context(), docspkg.VersionConsistencyOptions{
				Root: root,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "docs version-consistency: %v\n", err)
				os.Exit(2)
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printDocsVersionConsistencyHuman(result)
			}

			if result.MismatchesCount > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "Directory tree to scan (default: current working directory)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

func printDocsVersionConsistencyHuman(r *docspkg.VersionConsistencyResult) {
	fmt.Printf("nightgauge docs version-consistency — schema v%d\n", r.V)
	fmt.Printf("root: %s\n", r.Root)
	fmt.Printf("project_type: %s\n", r.ProjectType)
	if r.SourceFile != "" {
		fmt.Printf("source: %s @ %s\n", r.SourceFile, r.SourceVersion)
	} else {
		fmt.Println("source: (no authoritative version found)")
	}
	fmt.Printf("mismatches: %d\n\n", r.MismatchesCount)
	if len(r.Mismatches) == 0 {
		fmt.Println("  (no mismatches)")
	} else {
		for _, m := range r.Mismatches {
			fmt.Printf("  %s:%d  found=%s  expected=%s\n", m.File, m.Line, m.FoundVersion, m.ExpectedVersion)
		}
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

func docsCheckFreshnessCmd() *cobra.Command {
	var (
		root       string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "check-freshness",
		Short: "Detect markdown files with stale 'Updated: YYYY-MM-DD' metadata",
		Long: `Walk --root for *.md files, extract "Updated: YYYY-MM-DD" metadata lines, and
compare each documented date against the most recent git commit date for that
file. Files whose git date is newer than their documented date are flagged as
stale.

Replaces the bash + git log prose in update-docs Phase 4.8 (audit row B36).

Patterns matched (case-insensitive):
  Updated: 2026-01-22
  **Updated**: 2026-01-22
  | Updated | 2026-01-22 |

Content inside code fences (` + "```" + ` or ~~~) is skipped. Files not tracked by
git are skipped with a warning.

Schema version 1 — field names (v, root, files_scanned,
files_with_updated_metadata, stale_findings, stale_count, warnings) are
stable. Skills parse output via fixed jq paths; any breaking change requires
bumping v.

Exit codes:
  0  no stale dates found
  1  one or more stale dates found
  2  hard error (e.g. unresolvable root)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := docspkg.CheckFreshness(cmd.Context(), docspkg.CheckFreshnessOptions{
				Root: root,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "docs check-freshness: %v\n", err)
				os.Exit(2)
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printDocsCheckFreshnessHuman(result)
			}

			if result.StaleCount > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "Directory tree to scan (default: current working directory)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

func printDocsCheckFreshnessHuman(r *docspkg.FreshnessResult) {
	fmt.Printf("nightgauge docs check-freshness — schema v%d\n", r.V)
	fmt.Printf("root: %s\n", r.Root)
	fmt.Printf("files scanned: %d  with Updated metadata: %d\n", r.FilesScanned, r.FilesWithUpdatedMetadata)
	fmt.Printf("stale: %d\n\n", r.StaleCount)
	if len(r.StaleFindings) == 0 {
		fmt.Println("  (no stale files)")
	} else {
		for _, f := range r.StaleFindings {
			fmt.Printf("  %s:%d  documented=%s  git=%s  (%d days stale)\n",
				f.File, f.Line, f.DocumentedDate, f.GitDate, f.DaysStale)
		}
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// printDocsCheckLinksHuman renders the check-links result in a compact
// human-readable form mirroring the scan deps layout.
func printDocsCheckLinksHuman(r *docspkg.CheckLinksResult) {
	fmt.Printf("nightgauge docs check-links — schema v%d\n", r.V)
	fmt.Printf("root: %s\n", r.Root)
	fmt.Printf("files scanned: %d\n", r.FilesScanned)
	fmt.Printf("links: total=%d broken=%d\n\n", r.LinksTotal, r.LinksBroken)
	if len(r.Findings) == 0 {
		fmt.Println("  (no broken links)")
	} else {
		for _, f := range r.Findings {
			fmt.Printf("  %s:%d  %s  → %s  [%s]\n", f.File, f.Line, f.Link, f.Resolved, f.Reason)
		}
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// --- modernize command ---

func modernizeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "modernize",
		Short: "Modernization and assessment utilities",
		Long: `Deterministic utilities that consume .nightgauge/ assessment outputs
(health-check, security-audit, test-scaffold) and emit stable JSON schemas.
Replaces shell+jq extraction previously inlined in modernize-plan SKILL.md
Phase 2.1–2.4 (audit row B31).`,
	}
	cmd.AddCommand(modernizeAggregateFindingsCmd())
	return cmd
}

func modernizeAggregateFindingsCmd() *cobra.Command {
	var (
		workdir    string
		out        string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "aggregate-findings",
		Short: "Aggregate health, security, and test-scaffold findings into a single JSON output",
		Long: `Reads the three .nightgauge/ assessment reports (health-report.json,
security-audit.json, test-scaffold-report.json), applies severity normalization,
deduplicates overlapping findings, and outputs a single stable JSON structure.

At least one input file must be present; missing files are reported in
sources_missing and do not cause an error. All absent means exit 2.

Severity normalization (health-check only):
  critical → critical  |  poor → high  |  fair → medium
  good     → low       |  excellent → info  |  unknown → info

Deduplication key: source_dimension + "::" + lowercase(title). When two
findings share the key, the one with the longer recommendation wins.

Schema version 1 — field names locked; additive fields allowed.

Exit codes:
  0  success
  2  hard error (no inputs found, I/O failure, malformed JSON)`,
		Example: `  nightgauge modernize aggregate-findings --json
  nightgauge modernize aggregate-findings --workdir /path/to/project --json
  nightgauge modernize aggregate-findings --out /tmp/findings.json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "ERROR: cannot determine working directory: %v\n", err)
					os.Exit(2)
				}
			}

			result, err := aggregatefindings.Aggregate(workdir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				os.Exit(2)
			}

			b, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: marshal JSON: %v\n", err)
				os.Exit(2)
			}
			b = append(b, '\n')

			if out != "" {
				if err := os.WriteFile(out, b, 0o644); err != nil {
					fmt.Fprintf(os.Stderr, "ERROR: write output: %v\n", err)
					os.Exit(2)
				}
				fmt.Printf("aggregate-findings written to: %s\n", out)
				return nil
			}

			if jsonOutput {
				_, err = os.Stdout.Write(b)
				return err
			}

			// Human-readable summary
			printModernizeAggregateFindingsHuman(result)
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root containing .nightgauge/ (default: cwd)")
	cmd.Flags().StringVar(&out, "out", "", "Write JSON output to file instead of stdout")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

func printModernizeAggregateFindingsHuman(r *aggregatefindings.Result) {
	fmt.Printf("nightgauge modernize aggregate-findings — schema v%d\n", r.V)
	fmt.Printf("sources read: %s\n", strings.Join(r.SourcesRead, ", "))
	if len(r.SourcesMissing) > 0 {
		fmt.Printf("sources missing: %s\n", strings.Join(r.SourcesMissing, ", "))
	}
	fmt.Printf("findings: total=%d  after_dedup=%d  dedup_rate=%.1f%%\n",
		r.Summary.TotalFindings, r.Summary.AfterDedup, r.Summary.DeduplicationRate*100)
	fmt.Printf("by_severity: critical=%d  high=%d  medium=%d  low=%d  info=%d\n",
		r.Summary.BySeverity["critical"],
		r.Summary.BySeverity["high"],
		r.Summary.BySeverity["medium"],
		r.Summary.BySeverity["low"],
		r.Summary.BySeverity["info"],
	)
	if len(r.Findings) > 0 {
		fmt.Println()
		for _, f := range r.Findings {
			merged := ""
			if len(f.MergedFrom) > 0 {
				merged = fmt.Sprintf(" [merged %d]", len(f.MergedFrom))
			}
			fmt.Printf("  [%s] %s (%s::%s)%s\n", f.Severity, f.Title, f.Source, f.SourceDimension, merged)
		}
	}
}
