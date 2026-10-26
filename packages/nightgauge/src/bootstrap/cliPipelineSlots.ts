/**
 * cliPipelineSlots.ts
 *
 * The UI side of CLI-run discovery, extracted out of the `registerServices()`
 * bootstrap (#586).
 *
 * `CliPipelineReconciliationService` finds `nightgauge run` processes started
 * outside this extension host and hands them to three callbacks. Those
 * callbacks used to be an inline closure in `services.ts` that told the
 * pipeline TREE about the run and nothing else — no Output-window slot was
 * ever registered for it. The consequence was not an empty Output window but a
 * confidently wrong one: with no slot of its own, the window kept showing
 * whichever extension-launched run had registered last, with nothing on screen
 * saying which issue those bytes belonged to.
 *
 * So this module owns both halves of the same decision — the tree slot and the
 * Output-window slot are created together and torn down together — and it is a
 * module rather than a closure because a closure inside a 4,600-line
 * `registerServices()` cannot be executed by a test, which is why the missing
 * half stayed missing.
 *
 * What it deliberately does NOT do is fabricate a stream. A CLI run's raw
 * adapter stdout goes to the launching terminal, so the registered slot carries
 * its identity (issue, repo, run id, `cli` origin) and renders an honest empty
 * state. Identity without content is the truth here; content borrowed from
 * another run is the defect. What the CLI does write is a session log of stage,
 * phase and scheduler narration (`cmd/nightgauge/run_session_log.go`); once it
 * exists the slot node names it and `Nightgauge: Open Run Output` opens it.
 */

import type { PipelineStateService } from "../services/PipelineStateService";
import type {
  CliPipelineReconciliationCallbacks,
  ReconciledCliRun,
} from "../services/CliPipelineReconciliationService";
import type { SlotRegistrationOptions } from "../views/outputWindow/OutputWindowState";

/** The `PipelineTreeProvider` surface these callbacks touch. */
export interface CliSlotTreeView {
  // Slots are named by repository and number (#2403).
  getConcurrentSlot(issueNumber: number, repo?: string): CliRunSlot | undefined;
  addConcurrentSlot(
    slotIndex: number,
    issueNumber: number,
    title: string,
    stateService: PipelineStateService,
    epicNumber?: number,
    epicRepo?: string,
    repo?: string
  ): void;
  removeConcurrentSlotIfOwned(
    issueNumber: number,
    stateService: PipelineStateService,
    repo?: string
  ): void;
}

/** The slot-node surface that shows where a CLI run's output lives. */
export interface CliRunSlot {
  setRunOutput?(logPath: string): void;
}

/** The `OutputWindow` surface these callbacks touch. */
export interface CliSlotOutputWindow {
  registerSlotInfo(
    slotIndex: number,
    issueNumber: number,
    title: string,
    repoSlug?: string,
    options?: SlotRegistrationOptions
  ): void;
  setSlotLogRoot(slotIndex: number, repoRoot: string | null): void;
  removeSlotInfoIfOwned(slotIndex: number, runId: string): void;
}

/**
 * Everything the callbacks touch, passed in rather than closed over.
 *
 * The tree and the Output window arrive as accessors, not values, because
 * `registerServices()` builds the reconciler before it builds either of them;
 * a value here would be read in their temporal dead zone.
 */
export interface CliPipelineSlotDeps {
  tree: () => CliSlotTreeView;
  output: () => CliSlotOutputWindow;
  /** Builds the state relay for a discovered run; `PipelineStateService.createForWorktree`. */
  createStateService: (root: string, issueNumber: number) => PipelineStateService;
  /**
   * Resolves the run's session log under its root, or null while the CLI has
   * not written one yet; `LogFileWriter.findLatestLogForIssue`.
   */
  findRunLog?: (root: string, issueNumber: number) => Promise<string | null>;
  /** Allocates the next free slot index for a CLI run. */
  nextSlotIndex: () => number;
  /** Optional structured logger. */
  logger?: {
    info(message: string, data?: object): void;
  };
}

/** A discovered CLI run, and the UI state created for it. */
interface TrackedCliRun {
  issueNumber: number;
  /** The run's repository, `owner/name`: the tree names its slot by it (#2403). */
  repo?: string;
  slotIndex: number;
  runId: string;
  service: PipelineStateService;
  /** The checkout the run executes in, where its session log lives. */
  root: string;
  /** The session log once found; looked up again on each update until then. */
  runLog?: string;
}

/**
 * The callbacks plus the teardown the extension's `subscriptions` needs.
 */
export interface CliPipelineSlotBinding {
  callbacks: CliPipelineReconciliationCallbacks;
  /** Dispose every state relay still tracked (extension deactivation). */
  disposeAll(): void;
}

/** The tab label for a run whose snapshot carries no issue title. */
function slotTitle(run: ReconciledCliRun): string {
  return run.snapshot.title || `Issue #${run.snapshot.issueNumber}`;
}

/**
 * Build the discovered / updated / settled callbacks for CLI-run reconciliation.
 */
export function createCliPipelineSlotCallbacks(deps: CliPipelineSlotDeps): CliPipelineSlotBinding {
  const tracked = new Map<string, TrackedCliRun>();

  // Point the slot node at the run's session log. The CLI creates the log at
  // its first stage, which can follow discovery, so a miss is retried on the
  // next snapshot update rather than recorded as absent.
  const resolveRunLog = async (key: string): Promise<void> => {
    const entry = tracked.get(key);
    if (!entry || entry.runLog || !deps.findRunLog) return;
    const logPath = await deps.findRunLog(entry.root, entry.issueNumber);
    if (!logPath || tracked.get(key) !== entry) return;
    entry.runLog = logPath;
    deps.tree().getConcurrentSlot(entry.issueNumber, entry.repo)?.setRunOutput?.(logPath);
  };

  return {
    disposeAll: () => {
      for (const run of tracked.values()) run.service.dispose();
      tracked.clear();
    },
    callbacks: {
      onDiscovered: (run) => {
        // An IPC-managed slot with the same issue is already authoritative:
        // that run is streamed, so its Output-window slot has real content and
        // must not be replaced by a disk-reconciled one.
        if (deps.tree().getConcurrentSlot(run.snapshot.issueNumber, run.snapshot.repo)) return;

        const stateService = deps.createStateService(run.root, run.snapshot.issueNumber);
        stateService.applyRuntimeSnapshot(run.snapshot);

        const slotIndex = deps.nextSlotIndex();
        tracked.set(run.key, {
          issueNumber: run.snapshot.issueNumber,
          repo: run.snapshot.repo,
          slotIndex,
          runId: run.snapshot.runId,
          service: stateService,
          root: run.root,
        });

        deps
          .tree()
          .addConcurrentSlot(
            slotIndex,
            run.snapshot.issueNumber,
            slotTitle(run),
            stateService,
            undefined,
            undefined,
            run.snapshot.repo
          );

        // The Output-window half. `setSlotLogRoot` points the slot's log
        // reads at the root the run is actually executing in (#191
        // precedence), so the Overview card's "Open Log" looks for this
        // issue's log in the right repository instead of the extension's
        // bootstrap workspace.
        deps
          .output()
          .registerSlotInfo(
            slotIndex,
            run.snapshot.issueNumber,
            slotTitle(run),
            run.snapshot.repo,
            { runId: run.snapshot.runId, origin: "cli" }
          );
        deps.output().setSlotLogRoot(slotIndex, run.root);
        void resolveRunLog(run.key);

        deps.logger?.info("Discovered direct CLI pipeline", {
          repo: run.snapshot.repo,
          issueNumber: run.snapshot.issueNumber,
          runId: run.snapshot.runId,
          slotIndex,
        });
      },

      onUpdated: (run) => {
        tracked.get(run.key)?.service.applyRuntimeSnapshot(run.snapshot);
        void resolveRunLog(run.key);
      },

      onSettled: (run) => {
        const entry = tracked.get(run.key);
        if (!entry) return;
        deps.tree().removeConcurrentSlotIfOwned(entry.issueNumber, entry.service, entry.repo);
        deps.output().removeSlotInfoIfOwned(entry.slotIndex, entry.runId);
        deps.output().setSlotLogRoot(entry.slotIndex, null);
        entry.service.dispose();
        tracked.delete(run.key);

        deps.logger?.info("Direct CLI pipeline settled", {
          repo: run.snapshot.repo,
          issueNumber: run.snapshot.issueNumber,
          runId: run.snapshot.runId,
          slotIndex: entry.slotIndex,
        });
      },
    },
  };
}
