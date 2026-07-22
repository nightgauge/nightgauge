/**
 * Manifest — single source of truth for all VSCode contribution points.
 *
 * This file is a build-time artifact. It is NOT imported at runtime.
 * The generator script reads this manifest, validates it, and writes
 * the "contributes" section of package.json.
 *
 * To update contribution points, edit this file and run:
 *   npm run generate:contributions
 */

import type { ManifestContributes } from "./types.js";

export const MANIFEST_CONTRIBUTES: ManifestContributes = {
  viewsContainers: {
    activitybar: [
      {
        id: "nightgauge-pipeline",
        title: "Nightgauge",
        icon: "resources/nightgauge-activity-bar.svg",
      },
    ],
  },

  views: {
    "nightgauge-pipeline": [
      {
        id: "nightgauge.pipelineView",
        name: "Pipeline",
        icon: "$(rocket)",
        contextualTitle: "Nightgauge Pipeline",
      },
      {
        // Live workflow node tree (run → phase → agent → judge) off the SDK
        // EventBus stream re-served over SSE (#3919).
        id: "nightgauge.workflowView",
        name: "Workflow",
        icon: "$(circuit-board)",
        contextualTitle: "Live Workflow Tree",
        visibility: "collapsed",
      },
      {
        id: "nightgauge.repositoriesView",
        name: "Repositories",
        icon: "$(repo)",
        contextualTitle: "Workspace Repositories",
      },
      {
        id: "nightgauge.queryResults",
        name: "Query Results",
        icon: "$(search)",
        contextualTitle: "Query Results",
        when: "nightgauge.hasQueryResults",
      },
      {
        id: "nightgauge.knowledgeView",
        name: "Knowledge",
        icon: "$(book)",
        contextualTitle: "Nightgauge Knowledge Base",
        visibility: "collapsed",
      },
      {
        id: "nightgauge.activeIssueKnowledgeView",
        name: "Active Issue Knowledge",
        icon: "$(book)",
        contextualTitle: "Active Issue Knowledge",
        visibility: "collapsed",
      },
      {
        // Action Center — severity-ordered DecisionRequest cards (ADR 015 / #325).
        id: "nightgauge.attentionView",
        name: "Attention",
        icon: "$(bell-dot)",
        contextualTitle: "Nightgauge Action Center",
      },
    ],
  },

  viewsWelcome: [
    {
      view: "nightgauge.pipelineView",
      contents:
        "Welcome to Nightgauge.\n\nThis repository isn't set up for Nightgauge yet. Run the quick setup steps below to get started — nothing is written to your repo until you opt in.\n\n[$(rocket) Initialize Repository](command:nightgauge.quickstartRepoInit)\n[$(book) Make Repo AI-Ready](command:nightgauge.quickstartSmartSetup)\n[$(info) Learn More](command:nightgauge.quickstartLearnMore)\n\nInitialize creates .nightgauge/config.yaml, standard labels, and links your GitHub Project board. AI-Ready adds AGENTS.md, CLAUDE.md, and focused docs for agent use.",
      when: "!nightgauge.repoInitialized",
    },
    {
      view: "nightgauge.pipelineView",
      contents: "No active pipeline.\nDrag an issue or epic from Ready Items to start.",
      when: "nightgauge.repoInitialized && !nightgauge.pipelineRunning",
    },
    {
      view: "nightgauge.repositoriesView",
      contents:
        "This repository isn't set up for Nightgauge yet.\n\n[$(rocket) Initialize Repository](command:nightgauge.quickstartRepoInit)\n[$(book) Make Repo AI-Ready](command:nightgauge.quickstartSmartSetup)",
      when: "!nightgauge.repoInitialized",
    },
    {
      view: "nightgauge.queryResults",
      contents:
        "Run a query to see results.\n[Query Project Items](command:nightgauge.queryProjectItems)",
      when: "!nightgauge.hasQueryResults",
    },
    {
      view: "nightgauge.knowledgeView",
      contents: "No knowledge entries yet.\nRun the pipeline or create entries manually.",
    },
    {
      // Empty-state copy from the ADR 015 mockup (§I) — "the goal, most of the day".
      view: "nightgauge.attentionView",
      contents: "$(check) All clear\nNo decisions pending. The fleet is steering itself.",
      when: "!nightgauge.attentionHasRequests",
    },
  ],

  commands: [
    {
      command: "nightgauge.quickstartRepoInit",
      title: "Nightgauge: Initialize Repository (Quickstart)",
      icon: "$(rocket)",
    },
    {
      command: "nightgauge.quickstartSmartSetup",
      title: "Nightgauge: Make Repo AI-Ready (Quickstart)",
      icon: "$(book)",
    },
    {
      command: "nightgauge.quickstartLearnMore",
      title: "Nightgauge: Quickstart Docs",
    },
    {
      command: "nightgauge.showGettingStarted",
      title: "Nightgauge: Show Getting Started",
      icon: "$(rocket)",
    },
    {
      command: "nightgauge.refreshRepoInitializedContext",
      title: "Nightgauge: Refresh Initialization Status",
    },
    {
      command: "nightgauge.runStage",
      title: "Nightgauge: Run Stage...",
      icon: "$(play)",
    },
    {
      command: "nightgauge.runInteractiveStage",
      title: "Nightgauge: Run Interactive Stage...",
      icon: "$(terminal)",
    },
    {
      command: "nightgauge.showDashboard",
      title: "Nightgauge: Show Dashboard",
      icon: "$(graph)",
    },
    {
      command: "nightgauge.rescrubDashboardHistory",
      title: "Nightgauge: Rescrub Dashboard History",
      icon: "$(history)",
    },
    {
      command: "nightgauge.exportTelemetry",
      title: "Nightgauge: Export Telemetry Analytics",
      icon: "$(export)",
    },
    {
      command: "nightgauge.stopPipeline",
      title: "Nightgauge: Stop Pipeline",
      icon: "$(debug-stop)",
    },
    {
      command: "nightgauge.stopBatchAfterCurrent",
      title: "Nightgauge: Stop After Current Issue",
      icon: "$(debug-step-out)",
    },
    {
      command: "nightgauge.pausePipeline",
      title: "Nightgauge: Pause Pipeline",
      icon: "$(debug-pause)",
    },
    {
      command: "nightgauge.resumePipeline",
      title: "Nightgauge: Resume Pipeline",
      icon: "$(debug-continue)",
    },
    {
      command: "nightgauge.refreshPipeline",
      title: "Nightgauge: Refresh Pipeline",
      icon: "$(refresh)",
    },
    {
      command: "nightgauge.viewContext",
      title: "View Context File",
      icon: "$(json)",
    },
    {
      command: "nightgauge.retryStage",
      title: "Retry Stage",
      icon: "$(debug-restart)",
    },
    {
      command: "nightgauge.retryFromPhase",
      title: "Retry from This Phase",
      icon: "$(debug-restart)",
    },
    {
      command: "nightgauge.refreshProjectBoard",
      title: "Nightgauge: Refresh Project Board",
      icon: "$(refresh)",
    },
    {
      command: "nightgauge.pickupIssue",
      title: "Nightgauge: Pick Up Issue",
      icon: "$(git-pull-request-create)",
    },
    {
      command: "nightgauge.selectPerformanceMode",
      title: "Nightgauge: Select Performance Mode",
      icon: "$(zap)",
    },
    {
      command: "nightgauge.viewIssueOnGitHub",
      title: "View on GitHub",
      icon: "$(github)",
    },
    {
      command: "nightgauge.showOutputWindow",
      title: "Nightgauge: Show Output Window",
      icon: "$(terminal)",
    },
    {
      command: "nightgauge-pipeline.showSlotOutput",
      title: "Show Output",
      icon: "$(terminal)",
    },
    {
      command: "nightgauge.clearOutputWindow",
      title: "Nightgauge: Clear Output Window",
      icon: "$(clear-all)",
    },
    {
      command: "nightgauge.copyOutputToClipboard",
      title: "Nightgauge: Copy Output to Clipboard",
      icon: "$(clippy)",
    },
    {
      command: "nightgauge.cleanupSessionLogs",
      title: "Nightgauge: Clean Up Session Logs",
      icon: "$(trash)",
    },
    {
      command: "nightgauge.setupPlugins",
      title: "Nightgauge: Setup Claude Code Plugins",
      icon: "$(extensions)",
    },
    {
      command: "nightgauge.setupCodex",
      title: "Nightgauge: Setup Codex Commands",
      icon: "$(terminal)",
    },
    {
      command: "nightgauge.resetPipeline",
      title: "Nightgauge: Reset Pipeline",
      icon: "$(clear-all)",
    },
    {
      command: "nightgauge.abortPipeline",
      title: "Nightgauge: Abort Pipeline",
      icon: "$(error)",
    },
    {
      command: "nightgauge.resetSession",
      title: "Nightgauge: Reset Session Metrics",
      icon: "$(history)",
    },
    {
      command: "nightgauge.selectTargetBranch",
      title: "Nightgauge: Select Target Branch",
      icon: "$(git-branch)",
    },
    {
      command: "nightgauge.showSettings",
      title: "Nightgauge: Open Settings",
      icon: "$(settings-gear)",
    },
    {
      command: "nightgauge.editTeamConfig",
      title: "Nightgauge: Edit Team Config",
      icon: "$(edit)",
    },
    {
      command: "nightgauge.switchAdapter",
      title: "Nightgauge: Switch Execution Adapter",
      icon: "$(plug)",
    },
    {
      command: "nightgauge.disableAutoAccept",
      title: "Nightgauge: Disable Auto-Accept",
      icon: "$(shield)",
    },
    {
      command: "nightgauge.sortProjectBoard",
      title: "Nightgauge: Sort Project Board",
      icon: "$(sort-precedence)",
    },
    {
      command: "nightgauge.filterProjectBoard",
      title: "Nightgauge: Filter Project Board",
      icon: "$(filter)",
    },
    {
      command: "nightgauge.searchProjectBoard",
      title: "Nightgauge: Search Project Board",
      icon: "$(search)",
    },
    {
      command: "nightgauge.clearSearchProjectBoard",
      title: "Nightgauge: Clear Search",
      icon: "$(close)",
    },
    {
      command: "nightgauge.sortRepositoriesView",
      title: "Sort Status Group",
      icon: "$(sort-precedence)",
    },
    {
      command: "nightgauge.filterRepositoriesView",
      title: "Filter Status Group",
      icon: "$(filter)",
    },
    {
      command: "nightgauge.repo.toggleSequential",
      title: "Toggle Sequential Mode",
      icon: "$(settings-gear)",
    },
    {
      // Per-repo concurrency cap (Issue #2987). Inline + context menu action
      // on each row of the Repositories tree. Opens a quick-pick to set
      // `autonomous.repositories.<repo>.max_concurrent` (or `sequential` for
      // the cap-of-1 case).
      command: "nightgauge.repo.setMaxConcurrent",
      title: "Set Max Concurrent Pipelines…",
      icon: "$(symbol-number)",
    },
    {
      command: "nightgauge.searchRepositoriesView",
      title: "Search Status Group",
      icon: "$(search)",
    },
    {
      command: "nightgauge.projectBoard.expandAll",
      title: "Nightgauge: Expand All Epic Groups",
      icon: "$(expand-all)",
    },
    {
      command: "nightgauge.projectBoard.collapseAll",
      title: "Nightgauge: Collapse All Epic Groups",
      icon: "$(collapse-all)",
    },
    {
      command: "nightgauge.selectAll",
      title: "Nightgauge: Select All Issues",
      icon: "$(check-all)",
    },
    {
      command: "nightgauge.runEpicBatch",
      title: "Nightgauge: Run All Issues in Epic",
      icon: "$(run-all)",
    },
    {
      command: "nightgauge.addEpicToPipeline",
      title: "Nightgauge: Add Epic to Pipeline",
      icon: "$(add)",
    },
    {
      command: "nightgauge.stopQueueAfterCurrent",
      title: "Nightgauge: Stop After Current Issue",
      icon: "$(debug-step-out)",
    },
    {
      command: "nightgauge.startPipelineForIssue",
      title: "Nightgauge: Start Pipeline for Issue",
      icon: "$(play)",
    },
    {
      command: "nightgauge.runPipelineWithModel",
      title: "Nightgauge: Run Pipeline with Model...",
      icon: "$(rocket)",
    },
    {
      command: "nightgauge.removeFromQueue",
      title: "Nightgauge: Remove from Queue",
      icon: "$(remove)",
    },
    {
      command: "nightgauge.clearQueue",
      title: "Nightgauge: Clear Queue",
      icon: "$(clear-all)",
    },
    {
      command: "nightgauge.resumeQueue",
      title: "Nightgauge: Resume Queue",
      icon: "$(debug-continue)",
    },
    {
      command: "nightgauge.configureDiscordNotifications",
      title: "Nightgauge: Configure Discord Notifications",
      icon: "$(bell)",
    },
    {
      command: "nightgauge.configureMattermostNotifications",
      title: "Nightgauge: Configure Mattermost Notifications",
      icon: "$(bell)",
    },
    {
      command: "nightgauge.configureMattermostWorkspace",
      title: "Nightgauge: Configure Mattermost Workspace",
      icon: "$(settings-gear)",
    },
    {
      command: "nightgauge.telemetrySettings",
      title: "Nightgauge: Telemetry Settings",
      icon: "$(pulse)",
    },
    {
      command: "nightgauge.openTelemetrySettingsPanel",
      title: "Nightgauge: Open Telemetry Settings Panel",
      icon: "$(pulse)",
    },
    {
      command: "nightgauge.clearCompletedIssues",
      title: "Nightgauge: Clear Completed Issues",
      icon: "$(clear-all)",
    },
    {
      command: "nightgauge.clearFailedIssues",
      title: "Nightgauge: Clear Failed Issues",
      icon: "$(clear-all)",
    },
    {
      command: "nightgauge.clearPipelineHistory",
      title: "Nightgauge: Clear Pipeline History",
      icon: "$(clear-all)",
    },
    {
      command: "nightgauge.retryFailedIssue",
      title: "Nightgauge: Retry Failed Issue",
      icon: "$(debug-restart)",
    },
    {
      command: "nightgauge.viewQueuedIssue",
      title: "Nightgauge: View Queued Issue on GitHub",
      icon: "$(github)",
    },
    {
      command: "nightgauge.moveQueueItemUp",
      title: "Nightgauge: Move Queue Item Up",
      icon: "$(arrow-up)",
    },
    {
      command: "nightgauge.moveQueueItemDown",
      title: "Nightgauge: Move Queue Item Down",
      icon: "$(arrow-down)",
    },
    {
      command: "nightgauge.removeQueueItem",
      title: "Nightgauge: Remove from Queue",
      icon: "$(trash)",
    },
    {
      command: "nightgauge.retryQueueItem",
      title: "Nightgauge: Retry Failed Queue Item",
      icon: "$(refresh)",
    },
    {
      command: "nightgauge.addIssueToPipeline",
      title: "Nightgauge: Add Issue to Pipeline",
      icon: "$(add)",
    },
    {
      command: "nightgauge.removeIssueFromPipeline",
      title: "Nightgauge: Remove Issue from Pipeline",
      icon: "$(remove)",
    },
    {
      command: "nightgauge.focusPipelineView",
      title: "Nightgauge: Focus Pipeline View",
    },
    {
      command: "nightgauge.focusProjectBoardView",
      title: "Nightgauge: Focus Project Board View",
    },
    {
      command: "nightgauge.openRepoInGitHub",
      title: "Nightgauge: Open Repository in GitHub",
      icon: "$(github)",
    },
    {
      command: "nightgauge.refreshRepositories",
      title: "Nightgauge: Refresh Repositories",
      icon: "$(refresh)",
    },
    {
      command: "nightgauge.refreshRepository",
      title: "Nightgauge: Refresh Repository",
      icon: "$(refresh)",
    },
    {
      command: "nightgauge.debugDumpAutonomousState",
      title: "Nightgauge: [Debug] Dump Autonomous State",
    },
    {
      command: "nightgauge.queryProjectItems",
      title: "Nightgauge: Query Project Items",
      icon: "$(search)",
    },
    {
      command: "nightgauge.saveQuery",
      title: "Nightgauge: Save Query",
      icon: "$(bookmark)",
    },
    {
      command: "nightgauge.loadSavedQuery",
      title: "Nightgauge: Load Saved Query",
      icon: "$(folder-opened)",
    },
    {
      command: "nightgauge.deleteSavedQuery",
      title: "Nightgauge: Delete Saved Query",
      icon: "$(trash)",
    },
    {
      command: "nightgauge.manageSavedQueries",
      title: "Nightgauge: Manage Saved Queries",
      icon: "$(settings-gear)",
    },
    {
      command: "nightgauge.clearQuery",
      title: "Nightgauge: Clear Query",
      icon: "$(close)",
    },
    {
      command: "nightgauge.refreshQueryResults",
      title: "Nightgauge: Refresh Query Results",
      icon: "$(refresh)",
    },
    {
      command: "nightgauge.checkEpicCompletion",
      title: "Nightgauge: Check Epic Completion",
      icon: "$(tasklist)",
    },
    {
      command: "nightgauge.adapterDoctor",
      title: "Nightgauge: Adapter Doctor",
      icon: "$(pulse)",
    },
    {
      command: "nightgauge.runPipelineHealth",
      title: "Nightgauge: Run Pipeline Health Check",
      icon: "$(heart-filled)",
    },
    {
      command: "nightgauge.recalibrateHealth",
      title: "Nightgauge: Recalibrate Health Score Baseline",
      icon: "$(refresh)",
    },
    {
      command: "nightgauge.showBrownfieldDashboard",
      title: "Nightgauge: Show Brownfield Dashboard",
      icon: "$(dashboard)",
    },
    {
      command: "nightgauge.openKnowledgeValueDashboard",
      title: "Nightgauge: Open Knowledge Value Dashboard",
      icon: "$(graph)",
    },
    {
      command: "nightgauge.resetUsageCounter",
      title: "Nightgauge: Reset Usage Counter",
      icon: "$(refresh)",
    },
    {
      command: "nightgauge.searchKnowledge",
      title: "Nightgauge: Search Knowledge",
      icon: "$(search)",
      category: "Nightgauge Knowledge",
    },
    {
      command: "nightgauge.copyWikiLink",
      title: "Nightgauge: Copy Wiki-Link",
      icon: "$(link)",
      category: "Nightgauge Knowledge",
    },
    {
      command: "nightgauge.knowledge.newEntry",
      title: "Nightgauge: New Knowledge Entry",
      icon: "$(new-file)",
      category: "Nightgauge Knowledge",
    },
    {
      command: "nightgauge.knowledge.scaffoldForIssue",
      title: "Nightgauge: Scaffold Knowledge for Issue",
      icon: "$(add)",
      category: "Nightgauge Knowledge",
    },
    {
      command: "nightgauge.knowledge.newADR",
      title: "Nightgauge: New ADR",
      icon: "$(law)",
      category: "Nightgauge Knowledge",
    },
    {
      command: "nightgauge.signIn",
      title: "Nightgauge: Sign In",
      icon: "$(sign-in)",
    },
    {
      command: "nightgauge.signOut",
      title: "Nightgauge: Sign Out",
      icon: "$(sign-out)",
    },
    {
      command: "nightgauge.signInWithGitHub",
      title: "Nightgauge: Sign In with GitHub",
      icon: "$(mark-github)",
    },
    {
      command: "nightgauge.manageSubscription",
      title: "Nightgauge: Manage Subscription",
      icon: "$(credit-card)",
    },
    {
      command: "nightgauge.activateLicense",
      title: "Nightgauge: Activate License",
      icon: "$(key)",
    },
    {
      command: "nightgauge.startTrial",
      title: "Nightgauge: Start Free Trial",
      icon: "$(rocket)",
    },
    {
      command: "nightgauge.openUpgradeUrl",
      title: "Nightgauge: Upgrade to Pro",
      icon: "$(rocket)",
    },
    {
      command: "nightgauge.openManageSubscription",
      title: "Nightgauge: Manage Subscription",
      icon: "$(gear)",
    },
    {
      command: "nightgauge.openSubscriptionUrl",
      title: "Nightgauge: Open Subscription URL",
      icon: "$(link-external)",
    },
    {
      command: "nightgauge.showMachineBinding",
      title: "Nightgauge: Show Machine Binding",
      icon: "$(vm)",
    },
    {
      command: "nightgauge.stopSlot",
      title: "Nightgauge: Stop Issue Pipeline",
      icon: "$(debug-stop)",
    },
    {
      command: "nightgauge.stopEpic",
      title: "Nightgauge: Stop Epic",
      icon: "$(close-all)",
    },
    {
      command: "nightgauge.showPipelineQuickActions",
      title: "Nightgauge: Pipeline Controls",
    },
    {
      command: "nightgauge.setConcurrentSlots",
      title: "Set Concurrent Slots",
      category: "Nightgauge",
      icon: "$(layers)",
    },
    // Autonomous mode commands (Issue #2373)
    {
      command: "nightgauge.autonomousRun",
      title: "Autonomous: Run",
      category: "Nightgauge",
      icon: "$(rocket)",
    },
    {
      command: "nightgauge.autonomousDryRun",
      title: "Autonomous: Dry Run (Preview)",
      category: "Nightgauge",
      icon: "$(eye)",
    },
    {
      command: "nightgauge.autonomousPause",
      title: "Autonomous: Pause",
      category: "Nightgauge",
      icon: "$(debug-pause)",
    },
    {
      command: "nightgauge.autonomousResume",
      title: "Autonomous: Resume",
      category: "Nightgauge",
      icon: "$(debug-continue)",
    },
    {
      command: "nightgauge.autonomousStop",
      title: "Autonomous: Stop",
      category: "Nightgauge",
      icon: "$(debug-stop)",
    },
    {
      command: "nightgauge.autonomousStatus",
      title: "Autonomous: Status",
      category: "Nightgauge",
      icon: "$(info)",
    },
    {
      command: "nightgauge.autonomousSelectRepos",
      title: "Autonomous: Select Repos",
      category: "Nightgauge",
      icon: "$(filter)",
    },
    {
      // Issue #3446 — manual escape hatch for the global Anthropic-quota
      // cooldown (#3431) so the user can resume dispatch without editing
      // .nightgauge/autonomous/state.json by hand.
      command: "nightgauge.autonomousClearQuotaCooldown",
      title: "Autonomous: Clear Quota Cooldown",
      category: "Nightgauge",
      icon: "$(watch)",
    },
    {
      command: "nightgauge.repositoriesToggleAutoRefresh",
      title: "Repositories: Pause Auto-Refresh",
      category: "Nightgauge",
      icon: "$(sync)",
    },
    {
      // Alias registered against the same handler so we can swap the view-title
      // button's icon based on auto-refresh state. When the view is paused the
      // toolbar shows this entry (sync-ignored icon); clicking it resumes.
      command: "nightgauge.repositoriesToggleAutoRefresh.resume",
      title: "Repositories: Resume Auto-Refresh",
      category: "Nightgauge",
      icon: "$(sync-ignored)",
    },
    {
      // Bulk include-all view-title button (Issue #2988). Writes [] to
      // `autonomous.enabled_repos` so the scheduler reverts to scan-all.
      command: "nightgauge.repositories.includeAll",
      title: "Repositories: Include All Repos in Autonomous Scan",
      category: "Nightgauge",
      icon: "$(check-all)",
    },
    {
      // Bulk exclude-all view-title button (Issue #2988). Writes a sentinel
      // non-existent repo name so the scheduler matches nothing.
      command: "nightgauge.repositories.excludeAll",
      title: "Repositories: Exclude All Repos from Autonomous Scan",
      category: "Nightgauge",
      icon: "$(close-all)",
    },
    {
      // Action Center (ADR 015 / #325) — view-title refresh button.
      command: "nightgauge.attentionRefresh",
      title: "Nightgauge: Refresh Action Center",
      category: "Nightgauge",
      icon: "$(refresh)",
    },
    {
      // Opens the resolve quick-pick for a card — bound to click + the inline
      // icon. Not shown in the command palette (needs a request argument).
      command: "nightgauge.attentionResolve",
      title: "Nightgauge: Resolve Decision",
      icon: "$(check)",
    },
  ],

  menus: {
    // ── Command Palette visibility ──────────────────────────────────────
    // Cloud / account features are not offered yet: the entire local product
    // is free and runs without an account. These commands require the hosted
    // platform (sign-in, subscription/billing, license activation, machine
    // binding, team config), so they stay hidden from the palette until the
    // master switch `nightgauge.cloud.enabled` is turned on (which drives
    // the `nightgauge.cloudEnabled` context key from bootstrap/services.ts).
    // Flipping that one setting reveals the whole cloud surface again.
    commandPalette: [
      {
        command: "nightgauge.signIn",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.signOut",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.signInWithGitHub",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.manageSubscription",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.activateLicense",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.startTrial",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.openUpgradeUrl",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.openManageSubscription",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.openSubscriptionUrl",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.showMachineBinding",
        when: "nightgauge.cloudEnabled",
      },
      {
        command: "nightgauge.editTeamConfig",
        when: "nightgauge.cloudEnabled",
      },
    ],
    "view/title": [
      // ── Autonomous mode toolbar buttons (Issue #2433, expanded #3309) ──
      // Status → button matrix:
      //   stopped/complete/budget_exhausted/crashed → Run + Pickup Issue
      //   running                                    → Pause + Stop
      //   paused/safety_tripped                      → Resume + Stop
      // Run and Resume share group@0 (mutually exclusive). Pause and Stop
      // share group@1. The matrix guarantees the user always has a visible
      // recovery action — they should never need to know about the command
      // palette to get unstuck.
      {
        command: "nightgauge.autonomousRun",
        when: "view =~ /^nightgauge\\.pipeline/ && !nightgauge.autonomousRunning && !nightgauge.autonomousResumable && !nightgauge.pipelineRunning",
        group: "navigation@0",
      },
      {
        command: "nightgauge.autonomousResume",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.autonomousResumable && !nightgauge.pipelineRunning",
        group: "navigation@0",
      },
      {
        command: "nightgauge.autonomousPause",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.autonomousRunning",
        group: "navigation@1",
      },
      {
        command: "nightgauge.autonomousStop",
        when: "view =~ /^nightgauge\\.pipeline/ && (nightgauge.autonomousRunning || nightgauge.autonomousResumable)",
        group: "navigation@1",
      },
      {
        command: "nightgauge.pickupIssue",
        when: "view =~ /^nightgauge\\.pipeline/ && !nightgauge.pipelineRunning && !nightgauge.autonomousRunning && !nightgauge.autonomousResumable",
        group: "navigation@2",
      },
      {
        command: "nightgauge.stopPipeline",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.pipelineRunning",
        group: "navigation@2",
      },
      {
        command: "nightgauge.stopEpic",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.hasRunningEpics",
        group: "navigation@2",
      },
      {
        command: "nightgauge.stopBatchAfterCurrent",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.concurrentSlotsActive && !nightgauge.stopAfterCurrentBatch",
        group: "navigation@2",
      },
      {
        command: "nightgauge.stopQueueAfterCurrent",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.pipelineRunning && !nightgauge.concurrentSlotsActive && nightgauge.queueHasActiveItems && !nightgauge.stopAfterCurrentQueue",
        group: "navigation@2",
      },
      {
        command: "nightgauge.pausePipeline",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.pipelineRunning && !nightgauge.pipelinePaused",
        group: "navigation@2",
      },
      {
        command: "nightgauge.resumePipeline",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.pipelinePaused",
        group: "navigation@2",
      },
      {
        command: "nightgauge.refreshPipeline",
        when: "view =~ /^nightgauge\\.pipeline/",
        group: "navigation@3",
      },
      {
        command: "nightgauge.showDashboard",
        when: "view =~ /^nightgauge\\.pipeline/",
        group: "navigation@4",
      },
      {
        command: "nightgauge.showOutputWindow",
        when: "view =~ /^nightgauge\\.pipeline/",
        group: "navigation@5",
      },
      {
        command: "nightgauge.resetPipeline",
        when: "view =~ /^nightgauge\\.pipeline/",
        group: "navigation@6",
      },
      {
        command: "nightgauge.abortPipeline",
        when: "view =~ /^nightgauge\\.pipeline/ && nightgauge.pipelineRunning",
        group: "navigation@6",
      },
      {
        command: "nightgauge.showSettings",
        when: "view =~ /^nightgauge\\.pipeline/",
        group: "navigation@7",
      },
      {
        command: "nightgauge.setConcurrentSlots",
        when: "view =~ /^nightgauge\\.pipeline/",
        group: "navigation@8",
      },
      {
        command: "nightgauge.refreshRepositories",
        when: "view == nightgauge.repositoriesView",
        group: "navigation@1",
      },
      {
        // Shows when auto-refresh is currently ON — click pauses it.
        command: "nightgauge.repositoriesToggleAutoRefresh",
        when: "view == nightgauge.repositoriesView && nightgauge.repositoriesAutoRefresh",
        group: "navigation@2",
      },
      {
        // Shows when auto-refresh is currently OFF — click resumes it.
        // Same command, different icon via the context-key trick below.
        command: "nightgauge.repositoriesToggleAutoRefresh.resume",
        when: "view == nightgauge.repositoriesView && !nightgauge.repositoriesAutoRefresh",
        group: "navigation@2",
      },
      {
        // Bulk include-all view-title button (Issue #2988).
        command: "nightgauge.repositories.includeAll",
        when: "view == nightgauge.repositoriesView",
        group: "navigation@3",
      },
      {
        // Bulk exclude-all view-title button (Issue #2988).
        command: "nightgauge.repositories.excludeAll",
        when: "view == nightgauge.repositoriesView",
        group: "navigation@4",
      },
      {
        command: "nightgauge.searchKnowledge",
        when: "view == nightgauge.knowledgeView",
        group: "navigation@1",
      },
      {
        command: "nightgauge.knowledge.newEntry",
        when: "view == nightgauge.knowledgeView",
        group: "navigation@2",
      },
      {
        command: "nightgauge.knowledge.scaffoldForIssue",
        when: "view == nightgauge.knowledgeView",
        group: "navigation@3",
      },
      {
        // Action Center refresh button (ADR 015 / #325).
        command: "nightgauge.attentionRefresh",
        when: "view == nightgauge.attentionView",
        group: "navigation@1",
      },
    ],
    "view/item/context": [
      {
        command: "nightgauge.refreshRepository",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^repository/",
        group: "inline@1",
      },
      {
        // Inline resolve icon on a DecisionRequest card (ADR 015 / #325) —
        // the same command the card's click (TreeItem.command) invokes.
        command: "nightgauge.attentionResolve",
        when: "view == nightgauge.attentionView && viewItem == attention.request",
        group: "inline@1",
      },
      {
        command: "nightgauge.runStage",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == stage-pending",
        group: "inline@1",
      },
      {
        command: "nightgauge.runStage",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == stage-bookend-pending",
        group: "inline@1",
      },
      {
        command: "nightgauge.retryStage",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == stage-failed",
        group: "inline@1",
      },
      {
        command: "nightgauge.retryStage",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == stage-running && !nightgauge.pipelineRunning",
        group: "inline@1",
      },
      {
        command: "nightgauge.retryFromPhase",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == phase-failed",
        group: "inline@1",
      },
      {
        command: "nightgauge.viewContext",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem =~ /^stage-(complete|failed|skipped|deferred|bookend-complete|bookend-failed)/",
        group: "inline@2",
      },
      {
        command: "nightgauge.moveQueueItemUp",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem =~ /^queuedIssue\\.(pending|ready)$/",
        group: "inline@1",
      },
      {
        command: "nightgauge.moveQueueItemDown",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem =~ /^queuedIssue\\.(pending|ready)$/",
        group: "inline@2",
      },
      {
        command: "nightgauge.removeFromQueue",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem =~ /^queuedIssue\\.(pending|ready|completed|failed)$/",
        group: "inline@3",
      },
      {
        command: "nightgauge.removeIssueFromPipeline",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem =~ /^queuedIssue\\./",
        group: "navigation@1",
      },
      {
        command: "nightgauge.viewQueuedIssue",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem =~ /^queuedIssue\\./",
        group: "inline@4",
      },
      {
        command: "nightgauge.retryQueueItem",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == queuedIssue.failed",
        group: "inline@1",
      },
      {
        command: "nightgauge.clearQueue",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == queueSection",
        group: "inline@1",
      },
      {
        command: "nightgauge.resumeQueue",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == queueSection",
        group: "inline@2",
      },
      {
        command: "nightgauge.clearCompletedIssues",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == completed-section",
        group: "inline@1",
      },
      {
        command: "nightgauge.clearFailedIssues",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == failed-section",
        group: "inline@1",
      },
      {
        command: "nightgauge.retryFailedIssue",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == failed-issue",
        group: "inline@1",
      },
      {
        command: "nightgauge.openRepoInGitHub",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^repository/",
        group: "inline@2",
      },
      {
        // Inline numeric icon for setting per-repo concurrency cap (Issue #2987).
        command: "nightgauge.repo.setMaxConcurrent",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^repository/",
        group: "inline@3",
      },
      {
        command: "nightgauge.repo.toggleSequential",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^repository/",
        group: "2_autonomous@1",
      },
      {
        // Context-menu (right-click) entry for the same Set Max Concurrent
        // command (Issue #2987), so users can reach it from both the inline
        // toolbar icon and the right-click menu.
        command: "nightgauge.repo.setMaxConcurrent",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^repository/",
        group: "2_autonomous@2",
      },
      {
        command: "nightgauge.sortRepositoriesView",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^issueSummary-(ready|inProgress|backlog)/",
        group: "inline@1",
      },
      {
        command: "nightgauge.filterRepositoriesView",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^issueSummary-(ready|inProgress|backlog)/",
        group: "inline@2",
      },
      {
        command: "nightgauge.searchRepositoriesView",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^issueSummary-(ready|inProgress|backlog)/",
        group: "inline@3",
      },
      {
        command: "nightgauge.sortRepositoriesView",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^issueSummary-(ready|inProgress|backlog)/",
        group: "1_sort@1",
      },
      {
        command: "nightgauge.filterRepositoriesView",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^issueSummary-(ready|inProgress|backlog)/",
        group: "1_sort@2",
      },
      {
        command: "nightgauge.searchRepositoriesView",
        when: "view == nightgauge.repositoriesView && viewItem =~ /^issueSummary-(ready|inProgress|backlog)/",
        group: "1_sort@3",
      },
      {
        command: "nightgauge.stopSlot",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem == concurrentSlot.running",
        group: "inline@1",
      },
      {
        command: "nightgauge.stopEpic",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem =~ /^concurrentSlot\\.running/ && nightgauge.hasRunningEpics",
        group: "inline@2",
      },
      {
        command: "nightgauge-pipeline.showSlotOutput",
        when: "view =~ /^nightgauge\\.pipeline/ && viewItem =~ /^concurrentSlot\\./",
        group: "inline@3",
      },
      {
        command: "nightgauge.knowledge.newEntry",
        when: "view == nightgauge.knowledgeView",
        group: "1_knowledge@1",
      },
      {
        command: "nightgauge.knowledge.scaffoldForIssue",
        when: "view == nightgauge.knowledgeView",
        group: "1_knowledge@2",
      },
      {
        command: "nightgauge.knowledge.newADR",
        when: "view == nightgauge.knowledgeView",
        group: "1_knowledge@3",
      },
      {
        command: "nightgauge.copyWikiLink",
        when: "view == nightgauge.knowledgeView && viewItem == knowledgeFile",
        group: "1_knowledge@4",
      },
    ],
  },

  keybindings: [
    {
      command: "nightgauge.searchKnowledge",
      key: "ctrl+shift+k",
      mac: "cmd+shift+k",
    },
    {
      command: "nightgauge.showOutputWindow",
      key: "ctrl+alt+o",
      mac: "cmd+alt+o",
    },
    {
      command: "nightgauge.clearOutputWindow",
      key: "ctrl+alt+shift+o",
      mac: "cmd+alt+shift+o",
    },
    {
      command: "nightgauge.showSettings",
      key: "ctrl+,",
      mac: "cmd+,",
      when: "activeViewlet == workbench.view.extension.nightgauge-pipeline",
    },
    {
      command: "nightgauge.clearPipelineHistory",
      key: "ctrl+shift+x",
      mac: "cmd+shift+x",
    },
    {
      command: "nightgauge.removeIssueFromPipeline",
      key: "ctrl+shift+r",
      mac: "cmd+shift+r",
      when: "focusedView =~ /^nightgauge\\.pipeline/ && viewItem =~ /^queuedIssue\\./",
    },
    {
      command: "nightgauge.moveQueueItemUp",
      key: "ctrl+shift+up",
      mac: "cmd+shift+up",
      when: "focusedView =~ /^nightgauge\\.pipeline/ && viewItem =~ /^queuedIssue\\.(pending|ready)$/",
    },
    {
      command: "nightgauge.moveQueueItemDown",
      key: "ctrl+shift+down",
      mac: "cmd+shift+down",
      when: "focusedView =~ /^nightgauge\\.pipeline/ && viewItem =~ /^queuedIssue\\.(pending|ready)$/",
    },
    {
      command: "nightgauge.focusPipelineView",
      key: "ctrl+alt+p",
      mac: "cmd+alt+p",
    },
    {
      command: "nightgauge.focusProjectBoardView",
      key: "ctrl+alt+b",
      mac: "cmd+alt+b",
    },
  ],
};
