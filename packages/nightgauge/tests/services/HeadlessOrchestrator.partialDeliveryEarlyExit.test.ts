/**
 * verify-and-close must not close an issue whose newest partial-delivery
 * comment lists criteria no later merged closing PR has met (#3089). Fails
 * closed when the remainder cannot be read.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

const binaryCalls: string[][] = [];
let binaryImpl: (args: string[]) => string;

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (_cmd: string, args: string[]) => {
    binaryCalls.push(args);
    try {
      return Promise.resolve({ stdout: binaryImpl(args), stderr: "" });
    } catch (err) {
      return Promise.reject(err);
    }
  };
  return { ...actual, execFile: execFileMock };
});

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/bin/nightgauge" }) },
}));

type Internals = {
  refuseCloseOverPartialDelivery(n: number): Promise<string | null>;
  partialRerunPromptTrailer(n: number, stage: string): Promise<string>;
  raiseRunScopedAttention: (r: unknown) => Promise<void>;
  resolvePostMergeNwo: (n: number) => Promise<{ owner: string; repo: string }>;
  blockedTerminalState: { blocker: string; prNumber?: number } | null;
};

function makeOrchestrator() {
  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
  const o = new HeadlessOrchestrator({} as PipelineStateService, logger, {
    contextFileWaitMs: 0,
  }) as unknown as Internals;
  const raised: unknown[] = [];
  o.raiseRunScopedAttention = async (r) => {
    raised.push(r);
  };
  o.resolvePostMergeNwo = async () => ({ owner: "nightgauge", repo: "nightgauge" });
  return { o, raised, logger };
}

describe("refuseCloseOverPartialDelivery (#3089)", () => {
  beforeEach(() => {
    binaryCalls.length = 0;
  });

  it("refuses the close and raises the card over a pending remainder", async () => {
    binaryImpl = () => JSON.stringify({ pending: true, pr: 3085, criteria: ["criterion 1"] });
    const { o, raised } = makeOrchestrator();
    expect(await o.refuseCloseOverPartialDelivery(3084)).toBeTruthy();
    expect(binaryCalls[0]).toEqual([
      "issue",
      "partial-delivery",
      "3084",
      "--repo",
      "nightgauge/nightgauge",
      "--json",
    ]);
    expect(raised).toEqual([{ producer: "partial-delivery-close-refused", issueNumber: 3084 }]);
    expect(o.blockedTerminalState?.blocker).toContain("PR #3085");
    expect(o.blockedTerminalState?.prNumber).toBe(3085);
  });

  it("lets the close proceed when no marker exists", async () => {
    binaryImpl = () => JSON.stringify({ pending: false, criteria: [] });
    const { o, raised } = makeOrchestrator();
    expect(await o.refuseCloseOverPartialDelivery(3084)).toBeNull();
    expect(raised).toEqual([]);
    expect(o.blockedTerminalState).toBeNull();
  });

  it("lets the close proceed when a closing PR merged after the comment", async () => {
    binaryImpl = () =>
      JSON.stringify({ pending: false, pr: 3085, criteria: ["c"], closedBy: 3092 });
    const { o } = makeOrchestrator();
    expect(await o.refuseCloseOverPartialDelivery(3084)).toBeNull();
  });

  it("fails closed when the check cannot run, without a card", async () => {
    binaryImpl = () => {
      throw new Error("exit 2");
    };
    const { o, raised, logger } = makeOrchestrator();
    expect(await o.refuseCloseOverPartialDelivery(3084)).toBeTruthy();
    expect(raised).toEqual([]);
    expect(o.blockedTerminalState?.blocker).toContain("could not run");
    expect(logger.warn).toHaveBeenCalled();
  });

  it("fails closed on unparseable output", async () => {
    binaryImpl = () => "not json";
    const { o } = makeOrchestrator();
    expect(await o.refuseCloseOverPartialDelivery(3084)).toBeTruthy();
  });
});

// #3095: the extension's stage loop scopes a re-run started by the card's
// replan option, appending the binary's block to planning and feature-dev.
describe("partialRerunPromptTrailer (#3095)", () => {
  beforeEach(() => {
    binaryCalls.length = 0;
  });

  const blocks = {
    "feature-planning": "\n\n## Partial-Delivery Re-run Scope\n\nplan block",
    "feature-dev": "\n\n## Partial-Delivery Re-run Scope\n\ndev block",
  };

  it("returns the binary's block for each scoped stage", async () => {
    binaryImpl = () =>
      JSON.stringify({ pending: true, pr: 3141, criteria: ["c"], prompt_blocks: blocks });
    const { o } = makeOrchestrator();
    expect(await o.partialRerunPromptTrailer(3160, "feature-planning")).toBe(
      blocks["feature-planning"]
    );
    expect(await o.partialRerunPromptTrailer(3160, "feature-dev")).toBe(blocks["feature-dev"]);
    expect(binaryCalls[0]).toEqual([
      "issue",
      "partial-delivery",
      "3160",
      "--repo",
      "nightgauge/nightgauge",
      "--json",
    ]);
  });

  it("reads nothing for other stages", async () => {
    binaryImpl = () => JSON.stringify({ pending: true, prompt_blocks: blocks });
    const { o } = makeOrchestrator();
    expect(await o.partialRerunPromptTrailer(3160, "feature-validate")).toBe("");
    expect(binaryCalls).toHaveLength(0);
  });

  it("is empty with nothing pending and when the read fails", async () => {
    binaryImpl = () => JSON.stringify({ pending: false, criteria: [] });
    const { o } = makeOrchestrator();
    expect(await o.partialRerunPromptTrailer(3160, "feature-planning")).toBe("");
    binaryImpl = () => {
      throw new Error("exit 2");
    };
    expect(await o.partialRerunPromptTrailer(3160, "feature-planning")).toBe("");
  });
});
