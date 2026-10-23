/**
 * OutputWindowOverview.test.ts
 *
 * Unit tests for the Overview dashboard panel (Issue #2817).
 * Covers card generation per slot, status badge rendering, elapsed time
 * formatting, cost/token display, action button wiring, and grid layout.
 */

import { describe, it, expect } from "vitest";
import {
  getOverviewPanelHtml,
  getOutputWindowHtml,
} from "../../../src/views/outputWindow/OutputWindowHtml";
import type { OutputEntry, SlotInfo } from "../../../src/views/outputWindow/OutputWindowState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const mockWebview = { cspSource: "test-csp" } as any;

const NOW = Date.UTC(2026, 3, 16, 12, 0, 0); // 2026-04-16T12:00:00Z

function createSlot(overrides: Partial<SlotInfo> = {}): SlotInfo {
  return {
    slotIndex: 0,
    issueNumber: 100,
    title: "Test Issue",
    tokenUsage: {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    },
    ...overrides,
  } as SlotInfo;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getOverviewPanelHtml (Issue #2817)", () => {
  describe("empty state", () => {
    it("renders empty placeholder when no slots are registered", () => {
      const html = getOverviewPanelHtml([], NOW);
      expect(html).toContain("overview-panel-empty");
      expect(html).toContain("No active pipeline slots");
      expect(html).not.toContain("overview-card");
    });
  });

  describe("single running slot", () => {
    const slot = createSlot({
      slotIndex: 0,
      issueNumber: 42,
      title: "Fix truncation bug",
      repoSlug: "nightgauge/nightgauge",
      status: "running",
      stage: "feature-dev",
      startedAt: NOW - 90_000, // 90s ago
      completedAt: null,
      tokenUsage: {
        inputTokens: 1234,
        outputTokens: 5678,
        cacheReadTokens: 100,
        cacheCreationTokens: 134,
        costUsd: 0.4512,
      },
    });

    it("renders exactly one overview-card", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      const cards = html.match(/class="overview-card"/g) ?? [];
      expect(cards.length).toBe(1);
    });

    it("renders issue header with number and title", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("#42");
      expect(html).toContain("Fix truncation bug");
    });

    it("renders repo slug when present", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("nightgauge/nightgauge");
    });

    it("renders 'Running' status badge with blue variant", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("overview-status-running");
      expect(html).toContain(">Running<");
    });

    it("renders stage label from slot.stage", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("Feature Development");
    });

    it("renders elapsed time (90s → '1m 30s')", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("1m 30s");
    });

    it("renders cost with 4-decimal USD formatting", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("$0.4512");
    });

    it("renders token counts with comma separators", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("1,234 in");
      expect(html).toContain("5,678 out");
      expect(html).toContain("234 cache"); // 100 + 134
    });

    it("includes data-started-at attribute for live timer ticking", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain(`data-started-at="${NOW - 90_000}"`);
      expect(html).not.toContain("data-completed-at=");
    });
  });

  describe("mixed statuses (2 complete + 1 running)", () => {
    const slots: SlotInfo[] = [
      createSlot({
        slotIndex: 0,
        issueNumber: 101,
        title: "First",
        status: "complete",
        stage: "pr-merge",
        startedAt: NOW - 600_000, // 10m ago
        completedAt: NOW - 300_000, // finished 5m ago, duration 5m
        tokenUsage: {
          inputTokens: 10_000,
          outputTokens: 20_000,
          cacheReadTokens: 500,
          cacheCreationTokens: 0,
          costUsd: 1.2345,
        },
      }),
      createSlot({
        slotIndex: 1,
        issueNumber: 102,
        title: "Second",
        status: "complete",
        stage: "pr-merge",
        startedAt: NOW - 400_000,
        completedAt: NOW - 100_000, // duration 5m
        tokenUsage: {
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0.01,
        },
      }),
      createSlot({
        slotIndex: 2,
        issueNumber: 103,
        title: "Third",
        status: "running",
        stage: "feature-dev",
        startedAt: NOW - 120_000, // 2m ago
        completedAt: null,
        tokenUsage: {
          inputTokens: 500,
          outputTokens: 300,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0.05,
        },
      }),
    ];

    it("renders three overview cards in slot order", () => {
      const html = getOverviewPanelHtml(slots, NOW);
      const cards = html.match(/class="overview-card"/g) ?? [];
      expect(cards.length).toBe(3);
      const idx101 = html.indexOf("#101");
      const idx102 = html.indexOf("#102");
      const idx103 = html.indexOf("#103");
      expect(idx101).toBeGreaterThan(-1);
      expect(idx102).toBeGreaterThan(idx101);
      expect(idx103).toBeGreaterThan(idx102);
    });

    it("renders two complete badges and one running badge", () => {
      const html = getOverviewPanelHtml(slots, NOW);
      const completeMatches = html.match(/overview-status-complete/g) ?? [];
      const runningMatches = html.match(/overview-status-running/g) ?? [];
      expect(completeMatches.length).toBe(2);
      expect(runningMatches.length).toBe(1);
    });

    it("completed cards include data-completed-at attribute for static elapsed", () => {
      const html = getOverviewPanelHtml(slots, NOW);
      expect(html).toContain(`data-completed-at="${NOW - 300_000}"`);
      expect(html).toContain(`data-completed-at="${NOW - 100_000}"`);
    });

    it("running card has no data-completed-at attribute", () => {
      const html = getOverviewPanelHtml(slots, NOW);
      const runningCardMatch = html.match(/slot="2"[\s\S]*?<\/div>\s*<\/div>/);
      expect(runningCardMatch?.[0] ?? "").not.toContain("data-completed-at=");
    });
  });

  describe("error status", () => {
    const slot = createSlot({
      slotIndex: 3,
      issueNumber: 404,
      title: "Broken build",
      status: "error",
      stage: "feature-validate",
      startedAt: NOW - 60_000,
      completedAt: NOW - 30_000,
      tokenUsage: {
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.02,
      },
    });

    it("renders red error badge", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("overview-status-error");
      expect(html).toContain(">Error<");
    });

    it("uses completedAt for duration calculation (30s)", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain("30s");
    });
  });

  describe("missing optional fields", () => {
    it("renders placeholder '—' when stage is missing", () => {
      const slot = createSlot({ slotIndex: 0, issueNumber: 1, stage: undefined });
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain(`class="overview-card-stage">—<`);
    });

    it("renders '—' elapsed when startedAt is missing", () => {
      const slot = createSlot({ slotIndex: 0, issueNumber: 1, startedAt: undefined });
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toMatch(/class="overview-card-elapsed"[^>]*>—</);
    });

    it("renders status 'Pending' when status is undefined", () => {
      const slot = createSlot({ slotIndex: 0, issueNumber: 1, status: undefined });
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain(">Pending<");
    });

    it("omits repo subline when repoSlug is undefined", () => {
      const slot = createSlot({ slotIndex: 0, issueNumber: 1, repoSlug: undefined });
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).not.toContain("overview-card-repo");
    });
  });

  describe("button wiring", () => {
    const slot = createSlot({
      slotIndex: 7,
      issueNumber: 77,
      status: "running",
      startedAt: NOW - 1000,
    });

    it("renders three action buttons per card with data-slot", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain(
        'class="overview-card-btn" data-overview-action="open-tab" data-slot="7"'
      );
      expect(html).toContain(
        'class="overview-card-btn" data-overview-action="reveal-github" data-slot="7"'
      );
      expect(html).toContain(
        'class="overview-card-btn" data-overview-action="open-log" data-slot="7"'
      );
    });

    it("cards carry role=button and are keyboard focusable", () => {
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).toContain('role="button"');
      expect(html).toContain('tabindex="0"');
    });
  });

  describe("HTML escaping", () => {
    it("escapes issue title and repoSlug to prevent XSS", () => {
      const slot = createSlot({
        slotIndex: 0,
        issueNumber: 1,
        title: '<script>alert("xss")</script>',
        repoSlug: "<b>ev&il</b>",
      });
      const html = getOverviewPanelHtml([slot], NOW);
      expect(html).not.toContain("<script>alert");
      expect(html).toContain("&lt;script&gt;alert");
      expect(html).toContain("&lt;b&gt;ev&amp;il&lt;/b&gt;");
    });
  });
});

// ---------------------------------------------------------------------------
// Integration: overview is the default panel when slots are registered
// ---------------------------------------------------------------------------

describe("Overview is default panel in getOutputWindowHtml (Issue #2817)", () => {
  const slot = createSlot({
    slotIndex: 0,
    issueNumber: 42,
    title: "t",
    status: "running",
    stage: "feature-dev",
    startedAt: Date.now() - 5000,
  });

  function renderWithSlots(activeSlotIndex: number | null) {
    return getOutputWindowHtml(
      mockWebview,
      [] as OutputEntry[],
      [],
      true,
      false,
      false,
      undefined,
      undefined,
      [slot],
      activeSlotIndex,
      new Map()
    );
  }

  it("renders Overview tab label instead of All", () => {
    const html = renderWithSlots(null);
    expect(html).toContain(">Overview<");
    expect(html).not.toMatch(/title="All slots combined"/);
  });

  it("renders overview-panel inside the slot-panel-all container when activeSlotIndex=null", () => {
    const html = renderWithSlots(null);
    const allIdx = html.indexOf('id="slot-panel-all"');
    const overviewIdx = html.indexOf('<div class="overview-panel">');
    expect(allIdx).toBeGreaterThan(-1);
    expect(overviewIdx).toBeGreaterThan(allIdx);
  });

  it("overview-panel-all is marked active when activeSlotIndex=null", () => {
    const html = renderWithSlots(null);
    expect(html).toContain('class="slot-panel active" id="slot-panel-all"');
  });

  it("overview-panel-all is inactive when a specific slot tab is active", () => {
    const html = renderWithSlots(0);
    // "slot-panel-all" has no "active" modifier when a specific slot is active
    expect(html).toMatch(/<div class="slot-panel" id="slot-panel-all"/);
    expect(html).toMatch(/<div class="slot-panel active" id="slot-panel-0"/);
  });
});
