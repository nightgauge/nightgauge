/**
 * OutputWindowTabs.test.ts
 *
 * Unit tests for per-slot tab bar rendering (Issue #2705, #2812).
 * Covers: tab bar visibility, tab button attributes, active-tab class,
 * slot panel data attributes, single-slot tab bar rendering.
 */

import { describe, it, expect } from "vitest";
import { getOutputWindowHtml } from "../../../src/views/outputWindow/OutputWindowHtml";
import type { OutputEntry, SlotInfo } from "../../../src/views/outputWindow/OutputWindowState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const mockWebview = { cspSource: "test-csp" } as any;

const emptyEntries: OutputEntry[] = [];
const emptyStages: never[] = [];

function createSlot(overrides: Partial<SlotInfo> = {}): SlotInfo {
  return {
    slotIndex: 0,
    issueNumber: 100,
    title: "Test Issue",
    ...overrides,
  };
}

function htmlFor(
  activeSlots: SlotInfo[],
  activeSlotIndex: number | null,
  slotEntries?: Map<number, OutputEntry[]>
): string {
  return getOutputWindowHtml(
    mockWebview,
    emptyEntries,
    emptyStages,
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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Tab bar rendering (Issue #2705)", () => {
  describe("zero-slot mode (no tab bar)", () => {
    it("should not render a slot-tab-bar when no slots are registered", () => {
      const html = htmlFor([], null);
      expect(html).not.toContain('class="slot-tab-bar"');
      // id="slotTabBar" should not appear as a DOM element (JS may reference it, but element won't exist)
      expect(html).not.toContain('id="slotTabBar"');
    });

    it("should not add has-slot-tabs class when no slots are registered", () => {
      const html = htmlFor([], null);
      expect(html).not.toMatch(/class="output-content[^"]*has-slot-tabs/);
    });
  });

  describe("single-slot mode with tab bar (Issue #2812)", () => {
    it("should render a slot-tab-bar with exactly one registered slot", () => {
      const html = htmlFor([createSlot()], null);
      expect(html).toContain('class="slot-tab-bar"');
      expect(html).toContain('id="slotTabBar"');
    });

    it("should render an 'All' tab button plus one slot button with one slot", () => {
      const html = htmlFor([createSlot()], null);
      const matches = html.match(/class="slot-tab-btn[^"]*"/g) ?? [];
      // All + 1 slot = 2 buttons
      expect(matches.length).toBe(2);
    });

    it("should apply has-slot-tabs class with one registered slot", () => {
      const html = htmlFor([createSlot()], null);
      expect(html).toMatch(/class="output-content[^"]*has-slot-tabs/);
    });

    it("should render slot-panel-all and slot-panel-0 with one registered slot", () => {
      const html = htmlFor([createSlot({ slotIndex: 0 })], null);
      expect(html).toContain('id="slot-panel-all"');
      expect(html).toContain('id="slot-panel-0"');
    });

    it("should render correct panel for single-slot with entries", () => {
      const slot = createSlot({ slotIndex: 0 });
      const slotEntries = new Map<number, OutputEntry[]>([
        [
          0,
          [
            {
              id: "s0-e1",
              timestamp: new Date(),
              level: "info",
              text: "Single slot output",
              slotIndex: 0,
            },
          ],
        ],
      ]);
      const html = htmlFor([slot], 0, slotEntries);
      expect(html).toContain('id="slot-panel-0"');
      expect(html).toContain("Single slot output");
    });
  });

  describe("multi-slot mode (tab bar present)", () => {
    it("should render slot-tab-bar when two or more slots are active", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);
      expect(html).toContain("slotTabBar");
    });

    it("should render an 'Overview' tab button (Issue #2817 — renamed from 'All')", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);
      expect(html).toContain('data-slot="null"');
      expect(html).toContain(">Overview<");
    });

    it("should render one tab button per slot plus the All button", () => {
      const slots = [
        createSlot({ slotIndex: 0 }),
        createSlot({ slotIndex: 1, issueNumber: 101 }),
        createSlot({ slotIndex: 2, issueNumber: 102 }),
      ];
      const html = htmlFor(slots, null);

      // Count slot-tab-btn buttons
      const matches = html.match(/class="slot-tab-btn[^"]*"/g) ?? [];
      // All + 3 slots = 4 buttons
      expect(matches.length).toBe(4);
    });

    it("should set correct data-slot attributes on tab buttons", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);

      expect(html).toContain('data-slot="null"');
      expect(html).toContain('data-slot="0"');
      expect(html).toContain('data-slot="1"');
    });

    it("should add has-slot-tabs class to output-content when multiple slots exist", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);
      expect(html).toContain("has-slot-tabs");
    });
  });

  describe("active tab class", () => {
    it("should apply active class to the 'All' button when activeSlotIndex is null", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);

      // The All button should have active class
      expect(html).toMatch(
        /slot-tab-btn active[^"]*"[^>]*data-slot="null"|data-slot="null"[^>]*slot-tab-btn active/
      );
    });

    it("should apply active class to slot tab when that slot is active", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, 1);

      // The slot-1 button should have active class
      // data-slot="1" button should have active class
      expect(html).toContain('data-slot="1"');
      // Check there's an active button for slot 1
      const activeButtons = [
        ...html.matchAll(
          /class="slot-tab-btn active"[^>]*data-slot="(\d+)"|data-slot="(\d+)"[^>]*class="slot-tab-btn active"/g
        ),
      ];
      const activeSlots = activeButtons.flatMap((m) => [m[1], m[2]]).filter(Boolean);
      expect(activeSlots).toContain("1");
    });

    it("should NOT apply active class to non-active tabs", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, 0);

      // Slot 1 button should NOT be active
      // Count active buttons — should only be 1
      const activeCount = (html.match(/slot-tab-btn active/g) ?? []).length;
      expect(activeCount).toBe(1);
    });
  });

  describe("slot panel rendering", () => {
    it("should render an 'All' slot panel", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);
      expect(html).toContain('id="slot-panel-all"');
      expect(html).toContain('data-slot="null"');
    });

    it("should render one panel per active slot", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);
      expect(html).toContain('id="slot-panel-0"');
      expect(html).toContain('id="slot-panel-1"');
    });

    it("should set data-slot attribute on each slot panel", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);
      expect(html).toContain('data-slot="0"');
      expect(html).toContain('data-slot="1"');
    });

    it("should mark All panel as active when activeSlotIndex is null", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, null);
      expect(html).toContain('id="slot-panel-all" data-slot="null"');
      // All panel should have active class
      expect(html).toMatch(
        /slot-panel active[^"]*"[^>]*id="slot-panel-all"|id="slot-panel-all"[^>]*slot-panel active/
      );
    });

    it("should mark the correct slot panel as active", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const html = htmlFor(slots, 1);
      // slot-panel-1 should have active class
      expect(html).toMatch(
        /slot-panel active[^"]*"[^>]*id="slot-panel-1"|id="slot-panel-1"[^>]*slot-panel active/
      );
    });

    it("should render slot-specific entries in each slot panel", () => {
      const slots = [createSlot({ slotIndex: 0 }), createSlot({ slotIndex: 1, issueNumber: 101 })];
      const slotEntries = new Map<number, OutputEntry[]>([
        [
          0,
          [
            {
              id: "s0-e1",
              timestamp: new Date(),
              level: "info",
              text: "Slot 0 output line",
              slotIndex: 0,
            },
          ],
        ],
        [
          1,
          [
            {
              id: "s1-e1",
              timestamp: new Date(),
              level: "info",
              text: "Slot 1 output line",
              slotIndex: 1,
            },
          ],
        ],
      ]);

      const html = htmlFor(slots, null, slotEntries);
      expect(html).toContain("Slot 0 output line");
      expect(html).toContain("Slot 1 output line");
    });
  });

  describe("header title with slot context (Issue #2813)", () => {
    it("should include repo slug in title when SlotInfo has repoSlug", () => {
      const slot = createSlot({
        slotIndex: 0,
        issueNumber: 42,
        title: "Fix output window truncation",
        repoSlug: "nightgauge/nightgauge",
      });
      const html = htmlFor([slot], null);

      expect(html).toContain("42");
      expect(html).toContain("Fix output window truncation");
      expect(html).toContain("nightgauge/nightgauge");
    });

    it("should include issue title in header when SlotInfo has title", () => {
      const slot = createSlot({
        slotIndex: 0,
        issueNumber: 99,
        title: "My Feature Issue",
      });
      const html = htmlFor([slot], null);

      expect(html).toContain("My Feature Issue");
      expect(html).toContain("99");
    });

    it("should not include repo slug in title when repoSlug is absent", () => {
      const slot = createSlot({ slotIndex: 0, issueNumber: 10, title: "No Repo" });
      const html = htmlFor([slot], null);

      expect(html).toContain("No Repo");
      // Title should not have trailing repo slug: "... — No Repo · nightgauge/..."
      expect(html).not.toContain("— No Repo ·");
    });

    it("should not have max-width: 250px on output-title", () => {
      const html = htmlFor([createSlot()], null);
      expect(html).not.toContain("max-width: 250px");
    });
  });

  describe("single-slot panel rendering", () => {
    it("should render both slot-panel-all and slot-panel-0 with one registered slot", () => {
      const html = htmlFor([createSlot({ slotIndex: 0 })], null);
      expect(html).toContain('id="slot-panel-all"');
      expect(html).toContain('id="slot-panel-0"');
    });

    it("should mark slot-panel-all as active when activeSlotIndex is null", () => {
      const html = htmlFor([createSlot({ slotIndex: 0 })], null);
      expect(html).toMatch(
        /slot-panel active[^"]*"[^>]*id="slot-panel-all"|id="slot-panel-all"[^>]*slot-panel active/
      );
    });
  });
});

describe("Tab badge rendering (Issue #2815)", () => {
  it("shows spinner badge for running slot", () => {
    const slot = createSlot({
      slotIndex: 0,
      issueNumber: 100,
      status: "running",
      startedAt: Date.now(),
      completedAt: null,
      tokenUsage: {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      },
    });
    const html = htmlFor([slot], null);
    expect(html).toContain("tab-badge-spinner");
    expect(html).toContain("tab-badge-running");
    expect(html).toContain("tab-badge-elapsed");
  });

  it("shows check mark and cost for complete slot", () => {
    const slot = createSlot({
      slotIndex: 0,
      issueNumber: 100,
      status: "complete",
      startedAt: Date.now() - 60000,
      completedAt: Date.now(),
      tokenUsage: {
        inputTokens: 1000,
        outputTokens: 500,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.0042,
      },
    });
    const html = htmlFor([slot], null);
    expect(html).toContain("tab-badge-complete");
    expect(html).toContain("✓");
    expect(html).toContain("$0.0042");
  });

  it("shows error icon for failed slot", () => {
    const slot = createSlot({
      slotIndex: 0,
      issueNumber: 100,
      status: "error",
      startedAt: Date.now() - 5000,
      completedAt: Date.now(),
      tokenUsage: {
        inputTokens: 500,
        outputTokens: 200,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.0009,
      },
    });
    const html = htmlFor([slot], null);
    expect(html).toContain("tab-badge-error");
    expect(html).toContain("✗");
  });

  it("shows no status badge or elapsed for pending slot with no cost", () => {
    const slot = createSlot({
      slotIndex: 0,
      issueNumber: 100,
      status: "pending",
      startedAt: undefined,
      completedAt: null,
      tokenUsage: {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      },
    });
    const html = htmlFor([slot], null);
    // Use span-level checks to avoid matching CSS class names in <style>
    expect(html).not.toContain('class="tab-badge tab-badge-cost"');
    expect(html).not.toContain('class="tab-badge tab-badge-elapsed"');
  });

  it("shows elapsed badge with data-started-at attribute for running slot", () => {
    const startedAt = Date.now() - 30000;
    const slot = createSlot({
      slotIndex: 0,
      issueNumber: 100,
      status: "running",
      startedAt,
      completedAt: null,
      tokenUsage: {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      },
    });
    const html = htmlFor([slot], null);
    expect(html).toContain(`data-started-at="${startedAt}"`);
  });

  it("shows no badge for status-only pending slot with no startedAt", () => {
    const slot = createSlot({
      slotIndex: 0,
      issueNumber: 100,
      status: "pending",
    });
    const html = htmlFor([slot], null);
    // Use span-level checks to avoid matching CSS class names in <style>
    expect(html).not.toContain('class="tab-badge tab-badge-status');
    expect(html).not.toContain('class="tab-badge tab-badge-elapsed"');
    expect(html).not.toContain('class="tab-badge tab-badge-cost"');
  });
});
