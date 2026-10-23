/**
 * OutputWindowOverviewLiveTick.test.ts
 *
 * Issue #3010 — verifies:
 *   1. The 1 Hz elapsed tick advances on Overview cards (frozen-tick fix).
 *   2. The `overview-card-update` patch handler does NOT clobber
 *      `data-started-at` mid-stage (root-cause fix).
 *   3. `data-completed-at` is established on transition and never cleared
 *      by a patch that lacks it.
 *   4. Initial HTML render emits the `.overview-card-phase` span.
 *   5. The patch handler updates the phase label in place.
 *   6. Passing `currentPhase: null` clears the phase text.
 *
 * Tests run under `environment: "node"` (vitest config), so this file
 * uses a minimal in-test fake DOM rather than jsdom — keeps the suite
 * dependency-free while exercising the same helpers the WebView script
 * uses at runtime via `OutputWindowOverviewDom.ts`.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  tickOverviewCardElapsed,
  applyOverviewCardUpdate,
  type OverviewCardUpdatePayload,
} from "../../../src/views/outputWindow/OutputWindowOverviewDom";
import { getOverviewPanelHtml } from "../../../src/views/outputWindow/OutputWindowHtml";
import type { SlotInfo } from "../../../src/views/outputWindow/OutputWindowState";

// ---------------------------------------------------------------------------
// Tiny fake DOM. Implements only the surface area the helpers consume.
// ---------------------------------------------------------------------------

class FakeEl {
  classList: Set<string> = new Set();
  dataset: Record<string, string | undefined> = {};
  textContent = "";
  children: FakeEl[] = [];

  // className setter that also resets classList — mirrors browser semantics
  // because the patch handler does `statusBadge.className = "..."`.
  private _className = "";
  get className(): string {
    return this._className;
  }
  set className(v: string) {
    this._className = v;
    this.classList = new Set(v.split(/\s+/).filter(Boolean));
  }

  constructor(classes: string[] = [], dataset: Record<string, string> = {}) {
    if (classes.length > 0) this.className = classes.join(" ");
    Object.assign(this.dataset, dataset);
  }

  appendChild(child: FakeEl): FakeEl {
    this.children.push(child);
    return child;
  }

  /** Walk descendants depth-first, returning ones matching `predicate`. */
  private collect(predicate: (el: FakeEl) => boolean, out: FakeEl[]): void {
    for (const c of this.children) {
      if (predicate(c)) out.push(c);
      c.collect(predicate, out);
    }
  }

  querySelector(selector: string): FakeEl | null {
    const found: FakeEl[] = [];
    this.collect(matchesSelector(selector), found);
    return found[0] ?? null;
  }

  querySelectorAll(selector: string): FakeEl[] {
    const found: FakeEl[] = [];
    this.collect(matchesSelector(selector), found);
    return found;
  }
}

/** Parse a tiny subset of CSS selectors used by the helpers. */
function matchesSelector(selector: string): (el: FakeEl) => boolean {
  // Forms supported:
  //   .class
  //   .class[data-attr]
  //   .class[data-attr="value"]
  const reAttrEq = /^\.([\w-]+)\[data-([\w-]+)="([^"]+)"\]$/;
  const reAttrPresent = /^\.([\w-]+)\[data-([\w-]+)\]$/;
  const reClassOnly = /^\.([\w-]+)$/;

  let m = reAttrEq.exec(selector);
  if (m) {
    const [, cls, attr, val] = m;
    const k = camelize(attr);
    return (el) => el.classList.has(cls) && el.dataset[k] === val;
  }
  m = reAttrPresent.exec(selector);
  if (m) {
    const [, cls, attr] = m;
    const k = camelize(attr);
    return (el) => el.classList.has(cls) && el.dataset[k] !== undefined;
  }
  m = reClassOnly.exec(selector);
  if (m) {
    const [, cls] = m;
    return (el) => el.classList.has(cls);
  }
  throw new Error(`fake-dom: unsupported selector "${selector}"`);
}

function camelize(attr: string): string {
  return attr.replace(/-([a-z])/g, (_, c: string) => c.toUpperCase());
}

/** Build a mock card matching the production initial-render structure. */
function createCard(opts: {
  slotIndex: number;
  startedAt?: number;
  status?: string;
  statusLabel?: string;
  stageLabel?: string;
  phase?: { name: string; index: number; total: number } | null;
}): { root: FakeEl; card: FakeEl } {
  const root = new FakeEl();
  const dataset: Record<string, string> = { slot: String(opts.slotIndex) };
  const card = new FakeEl(["overview-card"], dataset);

  const status = new FakeEl([
    "overview-status-badge",
    "overview-status-" + (opts.status ?? "running"),
  ]);
  status.textContent = opts.statusLabel ?? "Running";
  card.appendChild(status);

  const stage = new FakeEl(["overview-card-stage"]);
  stage.textContent = opts.stageLabel ?? "Feature Planning";
  card.appendChild(stage);

  const phaseEl = new FakeEl(["overview-card-phase"]);
  if (opts.phase) {
    phaseEl.textContent = `${opts.phase.name} · ${opts.phase.index}/${opts.phase.total}`;
  }
  card.appendChild(phaseEl);

  const elapsed = new FakeEl(["overview-card-elapsed"]);
  if (opts.startedAt != null) elapsed.dataset.startedAt = String(opts.startedAt);
  elapsed.textContent = "0s";
  card.appendChild(elapsed);

  const cost = new FakeEl(["overview-card-cost"]);
  cost.textContent = "$0.0000";
  card.appendChild(cost);

  const tokens = new FakeEl(["overview-card-tokens"]);
  tokens.textContent = "0 in · 0 out · 0 cache";
  card.appendChild(tokens);

  root.appendChild(card);
  return { root, card };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Issue #3010 — Overview card live tick + phase indicator", () => {
  const BASE = Date.UTC(2026, 3, 25, 12, 0, 0);

  beforeEach(() => {
    vi.useFakeTimers({ now: BASE });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  describe("frozen-tick fix", () => {
    it("elapsed text advances when Date.now() advances", () => {
      const { root, card } = createCard({ slotIndex: 0, startedAt: BASE - 1000 });
      const elapsed = card.querySelector(".overview-card-elapsed")!;

      // First tick — 1s in
      tickOverviewCardElapsed(root, Date.now());
      expect(elapsed.textContent).toBe("1s");

      // Advance 5s and tick again
      vi.advanceTimersByTime(5000);
      tickOverviewCardElapsed(root, Date.now());
      expect(elapsed.textContent).toBe("6s");
    });

    it("survives an overview-card-update patch mid-stage (data-started-at not clobbered)", () => {
      const { root, card } = createCard({ slotIndex: 0, startedAt: BASE - 2000 });
      const elapsed = card.querySelector(".overview-card-elapsed")!;

      // Tick at t=BASE → 2s
      tickOverviewCardElapsed(root, Date.now());
      expect(elapsed.textContent).toBe("2s");

      // Simulate a token-delta patch that re-stamps startedAt with a fresh
      // value — the bug pre-fix: this rewrote dataset.startedAt to BASE,
      // resetting elapsed to 0 on the next tick.
      const patch: OverviewCardUpdatePayload = {
        slotIndex: 0,
        status: "running",
        statusLabel: "Running",
        stageLabel: "Feature Planning",
        costUsd: 0.01,
        inputTokens: 100,
        outputTokens: 50,
        cacheTokens: 0,
        startedAt: BASE, // <-- transient/fresh value
        completedAt: null,
        currentPhase: null,
      };
      applyOverviewCardUpdate(root, patch);

      // The original startedAt anchor must be preserved.
      expect(elapsed.dataset.startedAt).toBe(String(BASE - 2000));

      // 3s later — elapsed should be 5s, not ~3s (which would prove the
      // anchor was reset by the patch).
      vi.advanceTimersByTime(3000);
      tickOverviewCardElapsed(root, Date.now());
      expect(elapsed.textContent).toBe("5s");
    });

    it("does not delete data-completed-at on a subsequent patch lacking it", () => {
      const { root, card } = createCard({ slotIndex: 0, startedAt: BASE - 5000 });
      const elapsed = card.querySelector(".overview-card-elapsed")!;

      // Stamp completion via a patch
      applyOverviewCardUpdate(root, {
        slotIndex: 0,
        status: "complete",
        statusLabel: "Complete",
        stageLabel: "Pipeline Finish",
        costUsd: 0.5,
        inputTokens: 1000,
        outputTokens: 500,
        cacheTokens: 0,
        startedAt: BASE - 5000,
        completedAt: BASE,
        currentPhase: null,
      });
      expect(elapsed.dataset.completedAt).toBe(String(BASE));
      expect(elapsed.textContent).toBe("5s");

      // A subsequent late-arriving patch lacks completedAt — must not erase
      // the completion stamp.
      applyOverviewCardUpdate(root, {
        slotIndex: 0,
        status: "complete",
        statusLabel: "Complete",
        stageLabel: "Pipeline Finish",
        costUsd: 0.5,
        inputTokens: 1000,
        outputTokens: 500,
        cacheTokens: 0,
        startedAt: BASE - 5000,
        completedAt: null,
        currentPhase: null,
      });
      expect(elapsed.dataset.completedAt).toBe(String(BASE));
    });
  });

  describe("phase indicator (initial render)", () => {
    it("getOverviewCardHtml emits .overview-card-phase span when phase is set", () => {
      const slot: SlotInfo = {
        slotIndex: 0,
        issueNumber: 3010,
        title: "Live tick + phase",
        stages: new Map(),
        tokenUsage: {
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0,
        },
        status: "running",
        startedAt: BASE - 1000,
        currentPhase: { name: "research", index: 2, total: 4 },
      } as SlotInfo;
      const html = getOverviewPanelHtml([slot], BASE);
      expect(html).toContain('class="overview-card-phase"');
      expect(html).toContain("research · 2/4");
    });

    it("getOverviewCardHtml emits an empty .overview-card-phase span when no phase", () => {
      const slot: SlotInfo = {
        slotIndex: 0,
        issueNumber: 3010,
        title: "No phase",
        stages: new Map(),
        tokenUsage: {
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0,
        },
        status: "running",
        startedAt: BASE - 1000,
      } as SlotInfo;
      const html = getOverviewPanelHtml([slot], BASE);
      expect(html).toContain('<span class="overview-card-phase"></span>');
    });
  });

  describe("phase indicator (patch handler)", () => {
    it("patches the phase span in place when currentPhase is supplied", () => {
      const { root, card } = createCard({ slotIndex: 0, startedAt: BASE - 1000 });
      const phaseEl = card.querySelector(".overview-card-phase")!;
      expect(phaseEl.textContent).toBe("");

      applyOverviewCardUpdate(root, {
        slotIndex: 0,
        status: "running",
        statusLabel: "Running",
        stageLabel: "Feature Dev",
        costUsd: 0,
        inputTokens: 0,
        outputTokens: 0,
        cacheTokens: 0,
        startedAt: BASE - 1000,
        completedAt: null,
        currentPhase: { name: "implementation", index: 3, total: 7 },
      });

      expect(phaseEl.textContent).toBe("implementation · 3/7");
    });

    it("clears the phase text when currentPhase is null", () => {
      const { root, card } = createCard({
        slotIndex: 0,
        startedAt: BASE - 1000,
        phase: { name: "research", index: 1, total: 4 },
      });
      const phaseEl = card.querySelector(".overview-card-phase")!;
      expect(phaseEl.textContent).toBe("research · 1/4");

      applyOverviewCardUpdate(root, {
        slotIndex: 0,
        status: "running",
        statusLabel: "Running",
        stageLabel: "Feature Dev",
        costUsd: 0,
        inputTokens: 0,
        outputTokens: 0,
        cacheTokens: 0,
        startedAt: BASE - 1000,
        completedAt: null,
        currentPhase: null,
      });

      expect(phaseEl.textContent).toBe("");
    });
  });
});
