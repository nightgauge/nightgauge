/**
 * OutputWindowHtml.snapshot.test.ts
 *
 * HTML snapshot regression tests for getOutputWindowHtml().
 * Captures structural HTML output to catch silent regressions
 * in the output window template generator.
 *
 * @see Issue #1242 - Add HTML snapshot regression tests for *Html.ts
 */

import { describe, it, expect } from "vitest";
import {
  getOutputWindowHtml,
  type SearchState,
} from "../../../src/views/outputWindow/OutputWindowHtml";
import type {
  OutputEntry,
  StageProgress,
  SlotInfo,
} from "../../../src/views/outputWindow/OutputWindowState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const mockWebview = { cspSource: "test-csp" } as any;

function normalize(html: string): string {
  return html
    .replace(/nonce-[A-Za-z0-9]{32}/g, "nonce-NONCE")
    .replace(/nonce="[A-Za-z0-9]{32}"/g, 'nonce="NONCE"')
    .replace(
      // Entry timestamps render via toLocaleTimeString in the machine's local
      // timezone, so the same fixture yields different HH:MM:SS per machine.
      /<span class="entry-time">\d{1,2}:\d{2}:\d{2}<\/span>/g,
      '<span class="entry-time">TIME</span>'
    );
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const emptyStages: StageProgress[] = [];

function createEntry(overrides: Partial<OutputEntry> = {}): OutputEntry {
  return {
    id: "entry-1",
    timestamp: new Date("2026-01-01T10:00:00Z"),
    level: "info",
    text: "Pipeline started",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

describe("getOutputWindowHtml snapshots (Issue #1242)", () => {
  it("empty state — no entries", () => {
    const html = getOutputWindowHtml(mockWebview, [], emptyStages, true, false, false);
    expect(normalize(html)).toMatchSnapshot();
  });

  it("with output content — multiple entries", () => {
    const entries: OutputEntry[] = [
      createEntry({
        id: "e1",
        level: "info",
        text: "Starting pipeline for issue #42",
      }),
      createEntry({
        id: "e2",
        level: "info",
        text: "Running feature-dev stage",
        stage: "feature-dev" as any,
      }),
      createEntry({
        id: "e3",
        level: "success",
        text: "Stage completed successfully",
      }),
    ];

    const html = getOutputWindowHtml(mockWebview, entries, emptyStages, true, false, true, 42);
    expect(normalize(html)).toMatchSnapshot();
  });

  it("with question prompt active — has question entry", () => {
    const entries: OutputEntry[] = [
      createEntry({ id: "e1", level: "info", text: "Analyzing codebase..." }),
      createEntry({
        id: "e2",
        level: "question",
        text: "Should I proceed with the refactor? (y/n)",
        stage: "feature-dev" as any,
      }),
    ];

    const html = getOutputWindowHtml(mockWebview, entries, emptyStages, false, false, false, 99);
    expect(normalize(html)).toMatchSnapshot();
  });

  it("with search bar visible — searchState provided", () => {
    const entries: OutputEntry[] = [
      createEntry({ id: "e1", text: "Error: cannot find module" }),
      createEntry({ id: "e2", text: "Retrying with fallback..." }),
    ];

    const searchState: SearchState = {
      searchText: "error",
      caseSensitive: false,
      useRegex: false,
    };

    const html = getOutputWindowHtml(
      mockWebview,
      entries,
      emptyStages,
      true,
      true,
      true,
      7,
      searchState
    );
    expect(normalize(html)).toMatchSnapshot();
  });

  it("with slot info including repo slug (Issue #2813)", () => {
    const entries: OutputEntry[] = [createEntry({ id: "e1", text: "Pipeline started" })];
    const slots: SlotInfo[] = [
      {
        slotIndex: 0,
        issueNumber: 42,
        title: "Fix truncation bug",
        repoSlug: "nightgauge/nightgauge",
      },
    ];

    const html = getOutputWindowHtml(
      mockWebview,
      entries,
      emptyStages,
      true,
      false,
      false,
      undefined,
      undefined,
      slots,
      0,
      new Map()
    );

    expect(normalize(html)).toMatchSnapshot();
  });

  it("overview dashboard with 3 slots — 2 complete + 1 running (Issue #2817)", () => {
    // Fixed epoch to keep snapshot deterministic. Elapsed values are computed
    // from startedAt/completedAt relative to this baseline inside the HTML.
    const BASE = Date.UTC(2026, 3, 16, 12, 0, 0);
    const slots: SlotInfo[] = [
      {
        slotIndex: 0,
        issueNumber: 101,
        title: "Complete feature",
        repoSlug: "nightgauge/nightgauge",
        status: "complete",
        stage: "pr-merge" as any,
        startedAt: BASE - 600_000,
        completedAt: BASE - 300_000,
        tokenUsage: {
          inputTokens: 12_345,
          outputTokens: 6_789,
          cacheReadTokens: 500,
          cacheCreationTokens: 250,
          costUsd: 0.8765,
        },
      } as SlotInfo,
      {
        slotIndex: 1,
        issueNumber: 102,
        title: "Another complete",
        repoSlug: "nightgauge/nightgauge",
        status: "complete",
        stage: "pr-merge" as any,
        startedAt: BASE - 400_000,
        completedAt: BASE - 100_000,
        tokenUsage: {
          inputTokens: 500,
          outputTokens: 250,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0.0321,
        },
      } as SlotInfo,
      {
        slotIndex: 2,
        issueNumber: 103,
        title: "In-flight work",
        repoSlug: "nightgauge/nightgauge",
        status: "running",
        stage: "feature-dev" as any,
        startedAt: BASE - 120_000,
        completedAt: null,
        tokenUsage: {
          inputTokens: 2_000,
          outputTokens: 1_500,
          cacheReadTokens: 100,
          cacheCreationTokens: 50,
          costUsd: 0.1234,
        },
      } as SlotInfo,
    ];

    const html = getOutputWindowHtml(
      mockWebview,
      [],
      emptyStages,
      true,
      false,
      false,
      undefined,
      undefined,
      slots,
      null,
      new Map()
    );

    // Normalize the running-slot elapsed seconds (Date.now()-relative) so the
    // snapshot is stable across runs. The running slot is the only card
    // without data-completed-at, and its elapsed text sits in the
    // .overview-card-elapsed element for slot 2.
    const stable = normalize(html).replace(
      /(data-started-at="[^"]+">)\d+h?\s*\d*m?\s*\d+s(<\/div>)/g,
      "$1ELAPSED$2"
    );

    expect(stable).toMatchSnapshot();
  });
});
