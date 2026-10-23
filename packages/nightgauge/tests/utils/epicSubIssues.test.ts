/**
 * readEpicSubIssues reads an issue's sub-issues through the nightgauge binary,
 * which pages the connection to its end or exits non-zero (#1701). These tests
 * pin that the reader passes the binary's whole list through, and that a
 * failed or malformed read throws instead of yielding a short or empty list.
 * The binary's own paging (a first page reporting hasNextPage makes it read
 * the next one, and the page cap is an error) is tested in Go, in
 * internal/github/connection_paging_test.go.
 */

import { describe, it, expect, vi } from "vitest";
import { readEpicSubIssues, type RunFile } from "../../src/utils/epicSubIssues";

const BINARY = "/usr/local/bin/nightgauge";
const OPTIONS = { cwd: "/work", timeout: 1000 };

function runReturning(stdout: string) {
  return vi.fn<RunFile>().mockResolvedValue({ stdout });
}

function progress(subIssues: unknown, total: number): string {
  return JSON.stringify({ number: 7, title: "Epic", repo: "acme/app", subIssues, total });
}

describe("readEpicSubIssues (#1701)", () => {
  it("asks the binary for the whole list and returns every sub-issue with its repository", async () => {
    // More than the 50 a fixed first page held, across two repositories.
    const nodes = Array.from({ length: 120 }, (_, i) => ({
      nodeId: `I_${i}`,
      number: i + 1,
      title: `Sub ${i + 1}`,
      state: i % 2 === 0 ? "CLOSED" : "OPEN",
      repo: i < 100 ? "acme/app" : "acme/platform",
    }));
    const run = runReturning(progress(nodes, nodes.length));

    const got = await readEpicSubIssues(BINARY, "acme/app", 7, OPTIONS, run);

    expect(run).toHaveBeenCalledWith(
      BINARY,
      ["issue", "list", "--epic", "7", "--repo", "acme/app", "--json"],
      OPTIONS
    );
    expect(got).toHaveLength(120);
    expect(got[0]).toEqual({ number: 1, state: "CLOSED", repo: "acme/app" });
    expect(got[119]).toEqual({ number: 120, state: "OPEN", repo: "acme/platform" });
  });

  it("returns an empty list for an issue with no sub-issues", async () => {
    // The binary marshals an issue with no sub-issues as subIssues: null.
    const got = await readEpicSubIssues(
      BINARY,
      "acme/app",
      7,
      OPTIONS,
      runReturning(progress(null, 0))
    );
    expect(got).toEqual([]);
  });

  it("throws when the binary cannot read the list to its end", async () => {
    const truncated = Object.assign(new Error("Command failed: nightgauge issue list"), {
      code: 1,
      stderr:
        "Error: fetch issue #7: subIssues of acme/app#7: relationship connection truncated: " +
        "more remain after the 20-page cap",
    });
    const run = vi.fn<RunFile>().mockRejectedValue(truncated);

    await expect(readEpicSubIssues(BINARY, "acme/app", 7, OPTIONS, run)).rejects.toBe(truncated);
  });

  it("throws when the list is shorter than the count the binary reports", async () => {
    const run = runReturning(progress([{ number: 1, state: "CLOSED", repo: "acme/app" }], 2));
    await expect(readEpicSubIssues(BINARY, "acme/app", 7, OPTIONS, run)).rejects.toThrow(
      /returned 1 sub-issues but counted 2/
    );
  });

  it.each([
    ["output that is not JSON", "fetch issue #7: boom", /invalid JSON/],
    ["JSON that is not an object", "[]", /no sub-issue total/],
    ["an object with no total", JSON.stringify({ subIssues: [] }), /no sub-issue total/],
    ["subIssues that are not a list", progress({ number: 1 }, 1), /not a list/],
    ["a sub-issue with no repository", progress([{ number: 1, state: "OPEN" }], 1), /sub-issue 0/],
  ])("throws on %s", async (_name, stdout, want) => {
    await expect(
      readEpicSubIssues(BINARY, "acme/app", 7, OPTIONS, runReturning(stdout))
    ).rejects.toThrow(want);
  });
});
