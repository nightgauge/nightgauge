/**
 * OutputWindowHtml.cliSlotIdentity.test.ts
 *
 * #586 — the Output window could render one run's bytes while another run was
 * the live one, with nothing on screen saying which issue it was showing. The
 * fix is not "show more content": it is that every per-slot panel names the
 * run it is showing, and that a slot with no content says so honestly instead
 * of leaving the previous panel's content to be read as the current run.
 *
 * These assertions are over the real rendered HTML, so the header strip and
 * the slot-aware empty state cannot be deleted without turning them red.
 */

import { describe, it, expect } from "vitest";
import { getOutputWindowHtml } from "../../../src/views/outputWindow/OutputWindowHtml";
import type { OutputEntry, SlotInfo } from "../../../src/views/outputWindow/OutputWindowState";

const mockWebview = { cspSource: "test-csp" } as any;
const RUN_ID = "01a0bea9-1669-745e-8be0-4dc1a7ccb83c";

function createSlot(overrides: Partial<SlotInfo> = {}): SlotInfo {
  return {
    slotIndex: 0,
    issueNumber: 100,
    title: "Test Issue",
    stages: new Map(),
    tokenUsage: {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    },
    ...overrides,
  };
}

function createEntry(text: string, slotIndex: number): OutputEntry {
  return {
    id: `${slotIndex}-${text}`,
    text,
    level: "info",
    timestamp: new Date(0),
    slotIndex,
  } as OutputEntry;
}

function htmlFor(
  activeSlots: SlotInfo[],
  activeSlotIndex: number | null,
  slotEntries?: Map<number, OutputEntry[]>
): string {
  return getOutputWindowHtml(
    mockWebview,
    [],
    [],
    true,
    false,
    false,
    undefined,
    undefined,
    activeSlots,
    activeSlotIndex,
    slotEntries ?? new Map()
  );
}

/** Extract one slot panel's markup so assertions cannot match another panel. */
function panelFor(html: string, slotIndex: number): string {
  const open = html.indexOf(`id="slot-panel-${slotIndex}"`);
  expect(open, `slot panel ${slotIndex} is rendered`).toBeGreaterThan(-1);
  const next = html.indexOf('id="slot-panel-', open + 1);
  return next === -1 ? html.slice(open) : html.slice(open, next);
}

describe("per-slot identity strip (#586)", () => {
  it("names the issue and title on every slot panel", () => {
    const html = htmlFor([createSlot({ issueNumber: 1644, title: "Egress check" })], 0);
    const panel = panelFor(html, 0);

    expect(panel).toContain('class="slot-panel-header"');
    expect(panel).toContain("#1644");
    expect(panel).toContain("Egress check");
  });

  it("names the run id when the slot carries one, full value on the tooltip", () => {
    const html = htmlFor([createSlot({ issueNumber: 1644, runId: RUN_ID })], 0);
    const panel = panelFor(html, 0);

    expect(panel).toContain("run 01a0bea9");
    expect(panel).toContain(`title="Run ${RUN_ID}"`);
  });

  it("marks a CLI-discovered run as such on its panel and its tab", () => {
    const html = htmlFor([createSlot({ issueNumber: 1644, origin: "cli", runId: RUN_ID })], 0);

    expect(panelFor(html, 0)).toContain("slot-panel-origin");
    expect(html).toContain('class="slot-tab-chip slot-tab-chip-cli"');
  });

  it("omits the run id and the CLI chip for an extension-launched slot", () => {
    const html = htmlFor([createSlot({ origin: "extension" })], 0);
    const panel = panelFor(html, 0);

    expect(panel).not.toContain("slot-panel-run");
    expect(html).not.toContain('class="slot-tab-chip slot-tab-chip-cli"');
  });
});

describe("slot empty state (#586)", () => {
  it("tells the operator where a CLI run's stream actually is", () => {
    const html = htmlFor([createSlot({ issueNumber: 1644, origin: "cli", runId: RUN_ID })], 0);
    const panel = panelFor(html, 0);

    expect(panel).toContain("No live stream for this run");
    expect(panel).toContain("#1644");
    expect(panel).toContain("launching terminal");
    // Never the generic copy: a pipeline IS running, and starting another one
    // would not put its stream here.
    expect(panel).not.toContain("Start a pipeline to see activity here");
  });

  it("names the issue in an extension-launched slot's empty state", () => {
    const html = htmlFor([createSlot({ issueNumber: 777 })], 0);
    const panel = panelFor(html, 0);

    expect(panel).toContain("No output yet for issue #777");
    expect(panel).not.toContain("Start a pipeline to see activity here");
  });

  it("drops the empty state once the slot has entries", () => {
    const entries = new Map([[0, [createEntry("first line", 0)]]]);
    const panel = panelFor(htmlFor([createSlot({ issueNumber: 1644 })], 0, entries), 0);

    expect(panel).toContain("first line");
    expect(panel).not.toContain("No output yet for issue");
  });
});

describe("cross-slot content isolation (#586)", () => {
  it("does not render slot A's content in slot B's panel", () => {
    const slots = [
      createSlot({ slotIndex: 0, issueNumber: 100, title: "Older run" }),
      createSlot({ slotIndex: 1, issueNumber: 1644, title: "CLI run", origin: "cli" }),
    ];
    const entries = new Map([[0, [createEntry("older run output", 0)]]]);

    // Slot 1 (the CLI run) is the selected tab.
    const html = htmlFor(slots, 1, entries);
    const cliPanel = panelFor(html, 1);

    expect(cliPanel).not.toContain("older run output");
    expect(cliPanel).toContain("No live stream for this run");
    expect(panelFor(html, 0)).toContain("older run output");
  });
});
