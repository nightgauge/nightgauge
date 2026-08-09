import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { vi } from "vitest";
import { makeConfigBridge, makeLogger, makeRun, makeState } from "./_helpers";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

vi.mock("../../../src/services/SecretStorageService", () => ({
  SecretStorageService: { getInstance: () => null },
  SECRET_KEYS: { mattermostWebhookUrl: "mattermostWebhookUrl" },
}));

const { MattermostService } = await import("../../../src/services/notifications/MattermostService");

// ─── Attachment shape per pipeline event type ──────────────────────────────

const PIPELINE_STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

const STAGE_LABEL: Record<string, string> = {
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Feature Planning",
  "feature-dev": "Feature Dev",
  "feature-validate": "Feature Validate",
  "pr-create": "PR Create",
  "pr-merge": "PR Merge",
};

describe("MattermostService.format — attachment shape per pipeline event type", () => {
  let service: InstanceType<typeof MattermostService>;

  beforeEach(() => {
    service = new MattermostService(
      {} as never,
      makeConfigBridge() as never,
      makeLogger() as never
    );
  });

  afterEach(() => {
    service.dispose();
  });

  for (const stage of PIPELINE_STAGES) {
    it(`running state for ${stage} emits blurple color #5865f2`, () => {
      const state = makeState(42, undefined, {
        stages: { [stage]: { status: "running", startTime: Date.now() - 5000 } },
      });
      const att = service.buildAttachment(makeRun() as never, state as never);
      expect(att.color).toBe("#5865f2");
    });

    it(`running state for ${stage} includes the stage label in the attachment text`, () => {
      const state = makeState(42, undefined, {
        stages: { [stage]: { status: "running", startTime: Date.now() - 1000 } },
      });
      const att = service.buildAttachment(makeRun() as never, state as never);
      expect(att.text).toContain(STAGE_LABEL[stage]);
    });

    it(`running state for ${stage} has required Slack-compatible fields`, () => {
      const state = makeState(42, undefined, {
        stages: { [stage]: { status: "running" } },
      });
      const att = service.buildAttachment(makeRun() as never, state as never);
      expect(att).toHaveProperty("color");
      expect(att).toHaveProperty("title");
      expect(att).toHaveProperty("text");
      expect(att).toHaveProperty("fields");
      expect(att).toHaveProperty("footer");
      expect(att).toHaveProperty("ts");
      expect(Array.isArray(att.fields)).toBe(true);
      expect(typeof att.ts).toBe("number");
    });
  }
});

// ─── Attachment for outcome types ──────────────────────────────────────────

describe("MattermostService.format — attachment for outcome types", () => {
  let service: InstanceType<typeof MattermostService>;

  beforeEach(() => {
    service = new MattermostService(
      {} as never,
      makeConfigBridge() as never,
      makeLogger() as never
    );
  });

  afterEach(() => {
    service.dispose();
  });

  it("productive → green color #57f287", () => {
    const att = service.buildAttachment(makeRun() as never, makeState(42, "productive") as never);
    expect(att.color).toBe("#57f287");
  });

  it("failure → red color #ed4245", () => {
    const att = service.buildAttachment(makeRun() as never, makeState(42, "failure") as never);
    expect(att.color).toBe("#ed4245");
  });

  it("budget-ceiling → yellow color #fee75c", () => {
    const att = service.buildAttachment(
      makeRun() as never,
      makeState(42, "budget-ceiling") as never
    );
    expect(att.color).toBe("#fee75c");
  });

  it("cancelled → grey color #95a5a6", () => {
    const att = service.buildAttachment(makeRun() as never, makeState(42, "cancelled") as never);
    expect(att.color).toBe("#95a5a6");
  });

  it("running (no outcome) → blurple #5865f2", () => {
    const att = service.buildAttachment(makeRun() as never, makeState(42) as never);
    expect(att.color).toBe("#5865f2");
  });

  it("skipped_stage outcome emits note in description text", () => {
    const state = makeState(42, "productive", {
      pipeline_meta: { skip_stages: ["feature-validate"] },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    expect(att.text).toContain("Skipped");
    expect(att.text).toContain("Feature Validate");
  });

  it("promotes estimate-vs-actual to its own Cost Accuracy field, not a Budget suffix (#267/#333)", () => {
    const state = makeState(42, "productive", {
      pipeline_meta: { budget_ceiling_usd: 75.0, budget_estimate_usd: 2.703 },
    });
    const att = service.buildAttachment(makeRun({ costUsd: 28.259 }) as never, state as never);
    const fields = att.fields as Array<{ title: string; value: string }>;

    // Plain title — Mattermost's other field titles carry no emoji.
    const accuracyField = fields.find((f) => f.title === "Cost Accuracy");
    expect(accuracyField).toBeDefined();
    expect(accuracyField!.value).toBe("Est. $2.70 → Actual $28.26  ·  **10.5x over**");
    expect(accuracyField!.value).not.toContain("Est: $2.703");

    // 38% of the ceiling carries no signal — the Budget field is suppressed.
    expect(fields.find((f) => f.title === "Budget")).toBeUndefined();
  });

  it("renders the Budget field only once spend crosses half the ceiling (#333 decision F)", () => {
    const state = makeState(42, "productive", {
      pipeline_meta: { budget_ceiling_usd: 75.0 },
    });
    const att = service.buildAttachment(makeRun({ costUsd: 60.0 }) as never, state as never);
    const budgetField = (att.fields as Array<{ title: string; value: string }>).find(
      (f) => f.title === "Budget"
    );
    expect(budgetField).toBeDefined();
    expect(budgetField!.value).toBe("$60.00 / $75.00 (80%)");
  });

  it("inherits the outcome/stage cross-check from outcomeDisplay (#333 decision B)", () => {
    const state = makeState(42, "productive", {
      stages: {
        "issue-pickup": { status: "complete" },
        "feature-dev": { status: "failed", error: "boom" },
      },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    expect(att.title).toContain("Complete — 1 stage failed ⚠️");
    expect(att.color).toBe("#fee75c");
  });

  it("states the mode exactly once — in the Limits value, with the ceiling (#333 decision I)", () => {
    const state = makeState(42, "productive", {
      pipeline_meta: { performance_mode: "frontier" },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const fields = att.fields as Array<{ title: string; value: string }>;

    expect(att.title).toContain("🚀");
    expect(att.text).not.toContain("Frontier");
    expect(fields.find((f) => f.title === "Mode")).toBeUndefined();
    const limits = fields.find((f) => f.title === "Limits");
    expect(limits).toBeDefined();
    expect(limits!.value).toBe("Frontier  ·  up to Fable");
  });

  // The badge is an icon and Elevated (the default) has none, so "exactly
  // once" has to be checked for every mode, not just the ones with an icon.
  const MODES: ReadonlyArray<readonly [string | undefined, string]> = [
    ["efficiency", "Efficiency"],
    ["elevated", "Elevated"],
    ["maximum", "Maximum"],
    ["frontier", "Frontier"],
    [undefined, "Elevated"],
  ];

  for (const [mode, label] of MODES) {
    it(`${mode ?? "(unset)"} → "${label}" appears exactly once in the attachment`, () => {
      const state = makeState(42, "productive", {
        pipeline_meta: mode ? { performance_mode: mode } : {},
      });
      const att = service.buildAttachment(makeRun() as never, state as never);
      expect(JSON.stringify(att).split(label).length - 1).toBe(1);

      const limits = (att.fields as Array<{ title: string; value: string }>).find(
        (f) => f.title === "Limits"
      );
      expect(limits!.value.startsWith(label)).toBe(true);
    });
  }
});

// ─── Cache hit rate parity with Discord (AC11 / #333 decision D) ────────────

describe("MattermostService.format — cache hit rate (AC11 parity)", () => {
  let service: InstanceType<typeof MattermostService>;
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    logger = makeLogger();
    service = new MattermostService({} as never, makeConfigBridge() as never, logger as never);
  });

  afterEach(() => {
    service.dispose();
  });

  function cacheFieldFor(totalInput: number, cacheRead: number) {
    const state = makeState(42, "productive", {
      tokens: { estimated_cost_usd: 0.05, total_input: totalInput, total_cache_read: cacheRead },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    return (att.fields as Array<{ title: string; value: string }>).find((f) => f.title === "Cache");
  }

  it("divides by total_input alone — total_input already includes cache reads (#207/#262)", () => {
    // Pre-fix Mattermost used `cacheRead + totalInput` as the denominator,
    // double-counting the cache reads and pinning the display near 50%.
    expect(cacheFieldFor(90000, 40000)!.value).toBe("44% hit rate");
  });

  it("reports 100% when every input token came from cache", () => {
    expect(cacheFieldFor(40000, 40000)!.value).toBe("100% hit rate");
  });

  it("suppresses an impossible >100% rate and warns naming both operands (#333 decision C)", () => {
    expect(cacheFieldFor(1000, 160610)).toBeUndefined();
    const warned = logger.warn.mock.calls.find(
      (c: unknown[]) => typeof c[0] === "string" && (c[0] as string).includes("cache hit rate")
    );
    expect(warned).toBeDefined();
    expect(warned![1]).toMatchObject({ cacheRead: 160610, totalInput: 1000 });
  });
});

// ─── Snapshot parity ────────────────────────────────────────────────────────

describe("MattermostService.format — snapshot parity", () => {
  let service: InstanceType<typeof MattermostService>;

  beforeEach(() => {
    vi.setSystemTime(new Date("2026-01-15T10:00:00Z"));
    service = new MattermostService(
      {} as never,
      makeConfigBridge() as never,
      makeLogger() as never
    );
  });

  afterEach(() => {
    service.dispose();
    vi.useRealTimers();
  });

  it("attachment snapshot matches for issue-pickup running", () => {
    const run = makeRun({ startTime: new Date("2026-01-15T09:55:00Z").getTime() });
    const state = makeState(42, undefined, {
      title: "Implement feature X",
      branch: "feat/42-feature-x",
      stages: { "issue-pickup": { status: "running", startTime: Date.now() - 10000 } },
    });
    const att = service.buildAttachment(run as never, state as never);
    // Strip ts (timestamp) and footer (contains elapsed time) for stable snapshot
    const { ts: _ts, footer: _footer, ...stableAtt } = att;
    expect(stableAtt).toMatchSnapshot();
  });

  it("attachment snapshot matches for pr-merge productive", () => {
    const run = makeRun({
      startTime: new Date("2026-01-15T09:50:00Z").getTime(),
      costUsd: 0.123,
      prUrl: "https://github.com/example/repo/pull/99",
      isFinal: true,
    });
    const state = makeState(42, "productive", {
      title: "Implement feature X",
      branch: "feat/42-feature-x",
      stages: {
        "issue-pickup": { status: "complete", duration_ms: 30000 },
        "feature-planning": { status: "complete", duration_ms: 45000 },
        "feature-dev": { status: "complete", duration_ms: 120000 },
        "feature-validate": { status: "complete", duration_ms: 60000 },
        "pr-create": { status: "complete", duration_ms: 15000 },
        "pr-merge": { status: "complete", duration_ms: 10000 },
      },
      tokens: { estimated_cost_usd: 0.123 },
      pr_url: "https://github.com/example/repo/pull/99",
    });
    const att = service.buildAttachment(run as never, state as never);
    const { ts: _ts, footer: _footer, ...stableAtt } = att;
    expect(stableAtt).toMatchSnapshot();
  });
});
