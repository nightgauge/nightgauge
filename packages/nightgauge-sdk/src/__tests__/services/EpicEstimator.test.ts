/**
 * EpicEstimator reads an epic's subIssues connection to its end (#1701). A
 * fixed `subIssues(first: 50)` read stopped at the first page with no sign
 * more existed, so an epic of 120 sub-issues was estimated from 50 of them.
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { Mock } from "vitest";

vi.mock("child_process", () => ({
  exec: vi.fn(),
  execFile: vi.fn(),
}));

import * as childProcess from "child_process";
import { EpicEstimator } from "../../services/EpicEstimator.js";

// No complexity model: estimates come from the size defaults.
const MODEL_PATH = "/nonexistent/complexity-model.yaml";

interface FakePage {
  hasNextPage: boolean;
  endCursor: string | null;
  numbers: number[];
}

function page(numbers: number[], hasNextPage: boolean, endCursor: string | null): FakePage {
  return { numbers, hasNextPage, endCursor };
}

function range(from: number, count: number): number[] {
  return Array.from({ length: count }, (_, i) => from + i);
}

function graphqlResponse(p: FakePage): string {
  return JSON.stringify({
    data: {
      repository: {
        issue: {
          title: "Epic",
          subIssues: {
            pageInfo: { hasNextPage: p.hasNextPage, endCursor: p.endCursor },
            nodes: p.numbers.map((n) => ({
              number: n,
              title: `Sub ${n}`,
              state: "OPEN",
              labels: { nodes: [{ name: "size:S" }] },
            })),
          },
        },
      },
    },
  });
}

/**
 * Serve `pages` in order, one per `gh api graphql` call, and record each
 * call's arguments.
 */
function serve(pages: FakePage[]): string[][] {
  const calls: string[][] = [];
  (childProcess.exec as unknown as Mock).mockImplementation(
    (_cmd: string, _opts: unknown, cb: (err: Error | null, out?: unknown) => void) => {
      cb(null, { stdout: "acme/app\n", stderr: "" });
    }
  );
  (childProcess.execFile as unknown as Mock).mockImplementation(
    (
      _file: string,
      args: string[],
      _opts: unknown,
      cb: (err: Error | null, out?: unknown) => void
    ) => {
      calls.push(args);
      const p = pages[calls.length - 1];
      if (!p) {
        cb(new Error(`unexpected request ${calls.length}`));
        return;
      }
      cb(null, { stdout: graphqlResponse(p), stderr: "" });
    }
  );
  return calls;
}

/** The cursor a request continued from, if any. */
function afterArg(args: string[]): string | undefined {
  return args.find((a) => a.startsWith("after="))?.slice("after=".length);
}

describe("EpicEstimator sub-issue paging (#1701)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("reads the next page while the connection reports one, and estimates every sub-issue", async () => {
    const calls = serve([page(range(1, 100), true, "c1"), page(range(101, 20), false, null)]);

    const estimate = await new EpicEstimator(MODEL_PATH, "/work").estimateEpic(7);

    expect(calls).toHaveLength(2);
    expect(afterArg(calls[0])).toBeUndefined();
    expect(afterArg(calls[1])).toBe("c1");
    for (const args of calls) {
      expect(args).toContain("owner=acme");
      expect(args).toContain("repo=app");
      expect(args).toContain("epicNumber=7");
    }
    expect(estimate.epic_title).toBe("Epic");
    expect(estimate.sub_issues).toHaveLength(120);
    expect(estimate.sub_issues.map((s) => s.number)).toEqual(range(1, 120));
    // 120 size:S sub-issues at the 120-minute default.
    expect(estimate.total_remaining_minutes).toBe(Math.round(120 * 120 * 1.15));
  });

  it("makes one request for a connection that fits on one page", async () => {
    const calls = serve([page(range(1, 3), false, "c1")]);

    const estimate = await new EpicEstimator(MODEL_PATH, "/work").estimateEpic(7);

    expect(calls).toHaveLength(1);
    expect(estimate.sub_issues).toHaveLength(3);
  });

  it("fails rather than estimating from part of the epic when the page cap is reached", async () => {
    const pages = range(1, 25).map((i) => page([i], true, `c${i}`));
    const calls = serve(pages);

    await expect(new EpicEstimator(MODEL_PATH, "/work").estimateEpic(7)).rejects.toThrow(
      /Failed to fetch epic #7: sub-issue list truncated: 20 read and more remain after the 20-page cap/
    );
    expect(calls).toHaveLength(20);
  });

  it("fails when a page reports a next page but no cursor to continue from", async () => {
    serve([page(range(1, 100), true, null)]);

    await expect(new EpicEstimator(MODEL_PATH, "/work").estimateEpic(7)).rejects.toThrow(
      /sub-issue list truncated: page 1 reports a next page but no cursor/
    );
  });

  it("fails when a later page cannot be read", async () => {
    // Only the first page is served; the second request errors.
    serve([page(range(1, 100), true, "c1")]);

    await expect(new EpicEstimator(MODEL_PATH, "/work").estimateEpic(7)).rejects.toThrow(
      /Failed to fetch epic #7: unexpected request 2/
    );
  });
});
