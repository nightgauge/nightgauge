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
 * What it deliberately does NOT do is fabricate a stream. A CLI run's stdout
 * goes to the launching terminal and nothing in this tree writes a session log
 * for it, so the registered slot carries its identity (issue, repo, run id,
 * `cli` origin) and renders an honest empty state. Identity without content is
 * the truth here; content borrowed from another run is the defect.
 */

import type { PipelineStateService } from "../services/PipelineStateService";
import type {
  CliPipelineReconciliationCallbacks,
  ReconciledCliRun,
} from "../services/CliPipelineReconciliationService";
import type { SlotRegistrationOptions } from "../views/outputWindow/OutputWindowState";

/** The `PipelineTreeProvider` surface these callbacks touch. */
export interface CliSlotTreeView {
  getConcurrentSlot(issueNumber: number): unknown;
  addConcurrentSlot(
    slotIndex: number,
    issueNumber: number,
    title: string,
    stateService: PipelineStateService
  ): void;
  removeConcurrentSlotIfOwned(issueNumber: number, stateService: PipelineStateService): void;
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
  slotIndex: number;
  runId: string;
  service: PipelineStateService;
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
        if (deps.tree().getConcurrentSlot(run.snapshot.issueNumber)) return;

        const stateService = deps.createStateService(run.root, run.snapshot.issueNumber);
        stateService.applyRuntimeSnapshot(run.snapshot);

        const slotIndex = deps.nextSlotIndex();
        tracked.set(run.key, {
          issueNumber: run.snapshot.issueNumber,
          slotIndex,
          runId: run.snapshot.runId,
          service: stateService,
        });

        deps
          .tree()
          .addConcurrentSlot(slotIndex, run.snapshot.issueNumber, slotTitle(run), stateService);

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

        deps.logger?.info("Discovered direct CLI pipeline", {
          repo: run.snapshot.repo,
          issueNumber: run.snapshot.issueNumber,
          runId: run.snapshot.runId,
          slotIndex,
        });
      },

      onUpdated: (run) => {
        tracked.get(run.key)?.service.applyRuntimeSnapshot(run.snapshot);
      },

      onSettled: (run) => {
        const entry = tracked.get(run.key);
        if (!entry) return;
        deps.tree().removeConcurrentSlotIfOwned(entry.issueNumber, entry.service);
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
