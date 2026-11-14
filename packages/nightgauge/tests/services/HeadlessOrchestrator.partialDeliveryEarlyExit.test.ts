/**
 * verify-and-close must not close an issue whose newest partial-delivery
 * comment lists criteria no later merged closing PR has met (#3089). Fails
 * closed when the remainder cannot be read.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { execFileSync } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

const binaryCalls: string[][] = [];
let binaryImpl: (args: string[]) => string;
let resolvedBinary: string | undefined = "/bin/nightgauge";

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
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => resolvedBinary }) },
}));

type Internals = {
  refuseCloseOverPartialDelivery(n: number): Promise<string | null>;
  partialRerunPromptTrailer(n: number, stage: string): Promise<string>;
  raiseRunScopedAttention: (r: unknown) => Promise<void>;
  resolvePostMergeNwo: (n: number) => Promise<{ owner: string; repo: string }>;
  blockedTerminalState: { blocker: string; prNumber?: number } | null;
  partialRerunIssues: Set<number>;
  getWorkingDirectory: () => string;
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
    // #3163: before planning, Go also writes the partial-delivery-{N}.json
    // record ContextAssembler splices; the feature-dev read only renders.
    expect(binaryCalls[0]).toEqual([
      "issue",
      "partial-delivery",
      "3160",
      "--repo",
      "nightgauge/nightgauge",
      "--json",
      "--write-context",
    ]);
    expect(binaryCalls[1]).toEqual([
      "issue",
      "partial-delivery",
      "3160",
      "--repo",
      "nightgauge/nightgauge",
      "--json",
    ]);
    // The scoped feature-dev marks the run a partial re-run (#3163).
    expect(o.partialRerunIssues.has(3160)).toBe(true);
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

// #3163: a stale partial-delivery-{N}.json from an earlier run must never reach
// planning, even when the trailer returns before Go can rewrite it.
describe("partialRerunPromptTrailer stale partial-delivery record (#3163)", () => {
  let repo: string;
  let record: string;

  beforeEach(() => {
    binaryCalls.length = 0;
    resolvedBinary = "/bin/nightgauge";
    repo = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "ng-3163-")));
    execFileSync("git", ["init", "-q", repo]);
    const pipelineDir = path.join(repo, ".git", "nightgauge", "pipeline");
    fs.mkdirSync(pipelineDir, { recursive: true });
    record = path.join(pipelineDir, "partial-delivery-3160.json");
    fs.writeFileSync(record, JSON.stringify({ pending: true, criteria: ["stale"] }));
  });

  afterEach(() => {
    resolvedBinary = "/bin/nightgauge";
    fs.rmSync(repo, { recursive: true, force: true });
  });

  function inRepo() {
    const made = makeOrchestrator();
    made.o.getWorkingDirectory = () => repo;
    return made;
  }

  it("removes it when no binary resolves", async () => {
    resolvedBinary = undefined;
    const { o } = inRepo();
    expect(await o.partialRerunPromptTrailer(3160, "feature-planning")).toBe("");
    expect(fs.existsSync(record)).toBe(false);
    expect(binaryCalls).toHaveLength(0);
  });

  it("removes it when the repository is unresolved", async () => {
    const { o } = inRepo();
    o.resolvePostMergeNwo = async () => ({ owner: "", repo: "" });
    expect(await o.partialRerunPromptTrailer(3160, "feature-planning")).toBe("");
    expect(fs.existsSync(record)).toBe(false);
    expect(binaryCalls).toHaveLength(0);
  });

  it("removes it when the binary read fails", async () => {
    binaryImpl = () => {
      throw new Error("exit 2");
    };
    const { o } = inRepo();
    expect(await o.partialRerunPromptTrailer(3160, "feature-planning")).toBe("");
    expect(fs.existsSync(record)).toBe(false);
  });

  it("removes it before Go runs, so Go's rewrite is what survives", async () => {
    binaryImpl = () => {
      // The record is already gone when Go is asked to rewrite it.
      expect(fs.existsSync(record)).toBe(false);
      fs.writeFileSync(record, JSON.stringify({ pending: true, criteria: ["fresh"] }));
      return JSON.stringify({ pending: true, prompt_blocks: { "feature-planning": "b" } });
    };
    const { o } = inRepo();
    expect(await o.partialRerunPromptTrailer(3160, "feature-planning")).toBe("b");
    expect(JSON.parse(fs.readFileSync(record, "utf-8")).criteria).toEqual(["fresh"]);
  });

  it("keeps it on feature-dev, which reads the planning that spliced it", async () => {
    resolvedBinary = undefined;
    const { o } = inRepo();
    expect(await o.partialRerunPromptTrailer(3160, "feature-dev")).toBe("");
    expect(fs.existsSync(record)).toBe(true);
  });

  it("logs and still runs when the record cannot be located", async () => {
    resolvedBinary = undefined;
    const { o, logger } = inRepo();
    o.getWorkingDirectory = () => "relative/not-a-root";
    expect(await o.partialRerunPromptTrailer(3160, "feature-planning")).toBe("");
    expect(logger.warn).toHaveBeenCalledWith(
      "Stale partial-delivery record could not be removed",
      expect.objectContaining({ issueNumber: 3160 })
    );
  });
});
