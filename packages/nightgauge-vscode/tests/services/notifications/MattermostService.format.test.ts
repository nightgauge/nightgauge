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

  it("Budget field labels the pre-flight estimate 'Pre-run est.' with an accuracy ratio, not a bare 'Est:' (#267)", () => {
    const state = makeState(42, "productive", {
      pipeline_meta: { budget_ceiling_usd: 75.0, budget_estimate_usd: 2.703 },
    });
    const att = service.buildAttachment(makeRun({ costUsd: 28.259 }) as never, state as never);
    const budgetField = (att.fields as Array<{ title: string; value: string }>).find(
      (f) => f.title === "Budget"
    );
    expect(budgetField).toBeDefined();
    expect(budgetField!.value).toBe(
      "$28.259 / $75.000 (38%)  ·  Pre-run est. $2.703 (actual: 10.5x)"
    );
    expect(budgetField!.value).not.toContain("Est: $2.703");
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
