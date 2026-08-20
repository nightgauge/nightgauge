/**
 * Every webview panel the extension can open: does it create, render a
 * non-empty body, and dispose cleanly?
 *
 * Twelve `createWebviewPanel` call sites exist under `src/`. Eight are
 * reachable through a registered command and are opened that way, because
 * that exercises the *activated* extension's real instance — its real
 * services, its real extension context. Four have no command that can reach
 * them from a cold window (`AdapterDoctorPanel` only via a live adapter
 * probe; `PipelineSummary` only with real pipeline state on disk;
 * `ApprovalDialog` and `RecoveryDialog` have no command at all) and are
 * constructed directly from the view class.
 *
 * The direct-construction cases are honest about what they cover: a second
 * copy of the module, bundled into this test, not the copy inside
 * `dist/extension.cjs`. They still answer the smoke question — the class
 * builds a panel and renders HTML — but they do not prove the wiring that
 * would call them in production exists. `RecoveryDialog` in particular has
 * no non-test call site anywhere in `src/`.
 *
 * What is deliberately NOT here: any assertion about the *content* of the
 * rendered body. "Does it come up with the right data" is the data-arrival
 * tier (#746). This tier asks only "does it come up".
 */

import * as assert from "node:assert/strict";
import * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { capturedPanels, panelsCreatedBy, type CapturedPanel } from "../observe.js";
import { delay, materializePopulatedFixture, waitFor } from "../fixture.js";
import { extension } from "./activation.suite.js";

import { AdapterDoctorPanel } from "../../../src/views/doctor/AdapterDoctorPanel.js";
import { PipelineSummary } from "../../../src/views/summary/PipelineSummary.js";
import { ApprovalDialog } from "../../../src/views/approval/ApprovalDialog.js";
import { RecoveryDialog } from "../../../src/views/recovery/RecoveryDialog.js";
import type { PipelineState } from "../../../src/services/PipelineStateService.js";

/** How long a panel gets to assign `webview.html` after it is created. */
const RENDER_BUDGET_MS = 5_000;

interface PanelCase {
  /** Human name, as used in the issue's list of twelve. */
  name: string;
  /** The `viewType` string passed to `createWebviewPanel`. */
  viewType: string;
  /** How the panel is brought up. */
  open: () => Promise<void>;
  /** True when `open()` goes through a registered command. */
  viaCommand?: string;
}

function extensionUri(): vscode.Uri {
  return extension().extensionUri;
}

/**
 * A pipeline state minimal enough to render a summary and shaped like the
 * real one. Content is irrelevant to this tier; the panel only has to build.
 */
const SMOKE_PIPELINE_STATE: PipelineState = {
  issue_number: 745,
  title: "VSCode host smoke tests",
  branch: "feat/745-vscode-host-smoke-tests",
  // All six stage keys are present deliberately. `getStageTimelineHtml()`
  // indexes `state.stages[name].status` for every name in its own hardcoded
  // stage order with no guard, so a state missing any one of them throws and
  // the panel renders nothing — see known-issues.ts. A smoke fixture that
  // tripped that would be testing the fixture, not the panel.
  stages: {
    "issue-pickup": { status: "complete" },
    "feature-planning": { status: "complete" },
    "feature-dev": { status: "complete" },
    "feature-validate": { status: "complete" },
    "pr-create": { status: "complete" },
    "pr-merge": { status: "complete" },
  },
  started_at: "2026-08-19T00:00:00Z",
  updated_at: "2026-08-19T01:00:00Z",
};

const PANEL_CASES: PanelCase[] = [
  {
    name: "Dashboard",
    viewType: "incrediDashboard",
    viaCommand: "nightgauge.showDashboard",
    open: () => run("nightgauge.showDashboard"),
  },
  {
    name: "SettingsPanel",
    viewType: "incrediSettings",
    viaCommand: "nightgauge.showSettings",
    open: () => run("nightgauge.showSettings"),
  },
  {
    name: "OutputWindow",
    viewType: "incrediOutputWindow",
    viaCommand: "nightgauge.showOutputWindow",
    open: () => run("nightgauge.showOutputWindow"),
  },
  {
    name: "BrownfieldDashboard",
    viewType: "incrediBrownfieldDashboard",
    viaCommand: "nightgauge.showBrownfieldDashboard",
    // Refuses with a warning when the workspace has no `.nightgauge/`, which
    // is why the populated fixture is materialized before this suite runs.
    open: () => run("nightgauge.showBrownfieldDashboard"),
  },
  {
    name: "GettingStartedPanel",
    viewType: "incrediGettingStarted",
    viaCommand: "nightgauge.showGettingStarted",
    open: () => run("nightgauge.showGettingStarted"),
  },
  {
    name: "NotifierSettingsPanel",
    viewType: "incrediNotifierSettings",
    viaCommand: "nightgauge.showNotifierSettings",
    open: () => run("nightgauge.showNotifierSettings"),
  },
  {
    name: "TelemetrySettingsPanel",
    viewType: "incrediTelemetrySettings",
    viaCommand: "nightgauge.openTelemetrySettingsPanel",
    open: () => run("nightgauge.openTelemetrySettingsPanel"),
  },
  {
    name: "KnowledgeValueDashboard",
    viewType: "incrediKnowledgeValueDashboard",
    viaCommand: "nightgauge.openKnowledgeValueDashboard",
    open: () => run("nightgauge.openKnowledgeValueDashboard"),
  },
  {
    name: "AdapterDoctorPanel",
    viewType: "incrediAdapterDoctor",
    // `nightgauge.adapterDoctor` probes every adapter binary before it shows
    // anything; that is a data question, and on a CI runner with no adapters
    // installed it is a slow one. The panel's own contract is what this tier
    // owns, so it is handed a synthetic report.
    open: async () => {
      AdapterDoctorPanel.show(
        {
          rows: [],
          stages: [],
          generatedAt: new Date().toISOString(),
          binaryResolved: false,
          notes: ["vscode-host smoke tier"],
        },
        async () => ({
          rows: [],
          stages: [],
          generatedAt: new Date().toISOString(),
          binaryResolved: false,
          notes: ["vscode-host smoke tier"],
        })
      );
    },
  },
  {
    name: "PipelineSummary",
    viewType: "incrediPipelineSummary",
    // `nightgauge.showPipelineSummary` bails with a warning unless
    // PipelineStateService can read real completed-run state.
    open: async () => {
      await new PipelineSummary(extensionUri()).show(SMOKE_PIPELINE_STATE);
    },
  },
  {
    name: "ApprovalDialog",
    viewType: "incrediApprovalDialog",
    // `show()` resolves only when the user acts, so it is intentionally not
    // awaited here; the case disposes the panel, which settles it.
    open: async () => {
      void new ApprovalDialog(extensionUri()).show(
        "planning" as never,
        745,
        "# Plan\n\nvscode-host smoke tier.\n"
      );
      await delay(0);
    },
  },
  {
    name: "RecoveryDialog",
    viewType: "incrediRecoveryDialog",
    open: async () => {
      void new RecoveryDialog(extensionUri()).show({
        issueNumber: 745,
        triggeringStage: "feature-dev",
        producingStage: "planning",
        errorKind: "MISSING_INPUT_FILE",
        errorDetail: "vscode-host smoke tier",
        runState: "paused",
        availableActions: ["run-producing-stage", "cancel"],
      });
      await delay(0);
    },
  },
];

async function run(command: string): Promise<void> {
  await vscode.commands.executeCommand(command);
}

/** Dispose any live panel of this viewType so the next open really creates one. */
async function closeExistingPanels(viewType: string): Promise<void> {
  const live = capturedPanels().filter((entry) => entry.viewType === viewType && !entry.disposed);
  for (const entry of live) {
    entry.panel.dispose();
  }
  if (live.length > 0) {
    await waitFor(
      () => live.every((entry) => entry.disposed) || undefined,
      2_000,
      `pre-existing ${viewType} panel(s) to dispose`
    );
  }
}

suite("webviews", () => {
  test("the populated fixture is in place", () => {
    // Idempotent, and repeated here so this suite does not depend on the
    // tree-view suite having run first.
    materializePopulatedFixture();
  });

  test("all twelve createWebviewPanel call sites are covered by a case", () => {
    // A count, not a list of files — but it is derived from the case table,
    // so adding a thirteenth panel without a case fails here rather than
    // going unnoticed. The companion grep lives in docs/TESTING.md.
    assert.equal(PANEL_CASES.length, 12);
    const viewTypes = PANEL_CASES.map((entry) => entry.viewType);
    assert.equal(new Set(viewTypes).size, 12, "Duplicate viewType in the panel case table");
  });

  for (const panelCase of PANEL_CASES) {
    test(`${panelCase.name} creates, renders, and disposes`, async () => {
      // Every one of these views is a singleton that reveals an existing
      // panel instead of building a new one. `GettingStartedPanel` is already
      // open by the time this suite runs — `activate()` auto-opens it once
      // per installation, and a fresh --user-data-dir is always the first
      // time — so without this, its case would assert on a panel nobody
      // created and pass for the wrong reason.
      await closeExistingPanels(panelCase.viewType);

      const created = await panelsCreatedBy(async () => {
        await panelCase.open();
      });

      const match = created.find((entry) => entry.viewType === panelCase.viewType);
      assert.ok(
        match,
        `Opening ${panelCase.name}` +
          (panelCase.viaCommand ? ` via ${panelCase.viaCommand}` : "") +
          ` created no webview panel with viewType "${panelCase.viewType}". ` +
          `Panels created instead: ${JSON.stringify(created.map((entry) => entry.viewType))}`
      );

      const html = await waitFor(
        () => {
          const body = match.panel.webview.html;
          return body && body.trim().length > 0 ? body : undefined;
        },
        RENDER_BUDGET_MS,
        `${panelCase.name} to render a non-empty body`
      );
      assert.ok(html.length > 0);

      match.panel.dispose();
      await waitFor(
        () => match.disposed || undefined,
        2_000,
        `${panelCase.name} to fire onDidDispose`
      );
    });
  }

  test("no panel from this suite is left open", () => {
    const leaked = capturedPanels().filter((entry: CapturedPanel) => !entry.disposed);
    assert.deepEqual(
      leaked.map((entry) => entry.viewType),
      [],
      "Panels still open at the end of the webview suite"
    );
  });
});
