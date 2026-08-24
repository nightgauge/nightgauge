/**
 * Tests for PR detection helpers
 */

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";
import { exec } from "child_process";
import {
  parsePRFromGHCLI,
  hasInReviewLabel,
  parseOpenPRList,
  findPRForIssueInList,
  getOpenPRsForRepo,
  clearOpenPRsCache,
} from "../../src/utils/prDetection";

// Only used by the getOpenPRsForRepo describe block below — mocked at module
// scope (vi.mock is hoisted) so the real batching/TTL logic under test runs
// against a controllable `gh` shell-out instead of a live one.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  return {
    ...actual,
    exec: vi.fn(),
  };
});

type ExecCallback = (err: unknown, result: { stdout: string; stderr: string }) => void;

/** Queue one canned result for the next `exec()` call. */
function mockExecOnce(run: (cb: ExecCallback) => void): void {
  (exec as unknown as Mock).mockImplementationOnce(
    (_cmd: string, optsOrCb: unknown, cb?: unknown) => {
      const callback = (typeof optsOrCb === "function" ? optsOrCb : cb) as ExecCallback;
      run(callback);
    }
  );
}

describe("prDetection", () => {
  describe("parsePRFromGHCLI", () => {
    it("parses valid gh pr list output", () => {
      const output = JSON.stringify([
        {
          number: 123,
          url: "https://github.com/org/repo/pull/123",
          title: "Fix: authentication bug",
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result).toEqual({
        number: 123,
        url: "https://github.com/org/repo/pull/123",
        title: "Fix: authentication bug",
      });
    });

    it("returns null for empty array", () => {
      const output = JSON.stringify([]);

      const result = parsePRFromGHCLI(output);

      expect(result).toBeNull();
    });

    it("returns first PR when multiple exist", () => {
      const output = JSON.stringify([
        {
          number: 123,
          url: "https://github.com/org/repo/pull/123",
          title: "First PR",
        },
        {
          number: 124,
          url: "https://github.com/org/repo/pull/124",
          title: "Second PR",
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result?.number).toBe(123);
      expect(result?.title).toBe("First PR");
    });

    it("handles malformed JSON", () => {
      const output = "not valid json";

      const result = parsePRFromGHCLI(output);

      expect(result).toBeNull();
    });

    it("handles missing required fields", () => {
      const output = JSON.stringify([
        {
          // Missing number and url
          title: "PR without required fields",
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result).toBeNull();
    });

    it("handles PR without title", () => {
      const output = JSON.stringify([
        {
          number: 123,
          url: "https://github.com/org/repo/pull/123",
          // title is optional
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result).toEqual({
        number: 123,
        url: "https://github.com/org/repo/pull/123",
        title: undefined,
      });
    });

    it("handles invalid types", () => {
      const output = JSON.stringify([
        {
          number: "123", // Should be number
          url: "https://github.com/org/repo/pull/123",
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result).toBeNull();
    });
  });

  describe("hasInReviewLabel", () => {
    it("detects status:in-review label", () => {
      const labels = ["type:bug", "status:in-review", "priority:high"];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(true);
    });

    it("is case insensitive", () => {
      const labels = ["STATUS:IN-REVIEW"];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(true);
    });

    it("returns false for other status labels", () => {
      const labels = ["status:ready", "status:in-progress"];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(false);
    });

    it("returns false for empty array", () => {
      const labels: string[] = [];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(false);
    });

    it("returns false when no status labels", () => {
      const labels = ["type:bug", "priority:high"];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(false);
    });

    it("handles partial matches", () => {
      // Should only match exact status:in-review, not other variations
      const labels = ["in-review-mode"]; // Not a status label

      const result = hasInReviewLabel(labels);

      // This will be true because it contains 'status:in-review' substring
      // which is acceptable for this heuristic
      expect(result).toBe(false);
    });
  });

  // #483 — batched, cached open-PR listing that replaces the per-issue
  // `gh pr list --search` shell-out in the stall watchdog.
  describe("parseOpenPRList", () => {
    it("parses valid gh pr list output including body", () => {
      const output = JSON.stringify([
        {
          number: 701,
          url: "https://github.com/org/repo/pull/701",
          title: "fix",
          body: "Closes #201",
        },
      ]);

      const result = parseOpenPRList(output);

      expect(result).toEqual([
        {
          number: 701,
          url: "https://github.com/org/repo/pull/701",
          title: "fix",
          body: "Closes #201",
        },
      ]);
    });

    it("returns an empty array for an empty list", () => {
      expect(parseOpenPRList(JSON.stringify([]))).toEqual([]);
    });

    it("returns an empty array for malformed JSON", () => {
      expect(parseOpenPRList("not valid json")).toEqual([]);
    });

    it("drops entries missing required fields but keeps valid ones", () => {
      const output = JSON.stringify([
        { title: "no number or url" },
        { number: 701, url: "https://github.com/org/repo/pull/701" },
      ]);

      const result = parseOpenPRList(output);

      expect(result).toEqual([
        {
          number: 701,
          url: "https://github.com/org/repo/pull/701",
          title: undefined,
          body: undefined,
        },
      ]);
    });

    it("returns an empty array when the payload is not an array", () => {
      expect(parseOpenPRList(JSON.stringify({ number: 1 }))).toEqual([]);
    });
  });

  describe("findPRForIssueInList", () => {
    const prs = [
      {
        number: 701,
        url: "https://github.com/org/repo/pull/701",
        title: "fix: issue 201",
        body: "Closes #201",
      },
      {
        number: 702,
        url: "https://github.com/org/repo/pull/702",
        title: "chore: unrelated",
        body: "no issue reference here",
      },
    ];

    it("matches a PR whose body references #<issueNumber>", () => {
      const result = findPRForIssueInList(201, prs);
      expect(result).toEqual({
        number: 701,
        url: "https://github.com/org/repo/pull/701",
        title: "fix: issue 201",
      });
    });

    it("returns null when no PR references the issue", () => {
      expect(findPRForIssueInList(999, prs)).toBeNull();
    });

    it("matches via title when body doesn't reference the issue", () => {
      const titleOnly = [
        { number: 703, url: "https://github.com/org/repo/pull/703", title: "fix #55", body: "" },
      ];
      expect(findPRForIssueInList(55, titleOnly)?.number).toBe(703);
    });

    it("does not match a longer issue number as a substring (word boundary)", () => {
      // #201 must not match a PR that only references #2010.
      const longer = [
        {
          number: 704,
          url: "https://github.com/org/repo/pull/704",
          title: "",
          body: "Closes #2010",
        },
      ];
      expect(findPRForIssueInList(201, longer)).toBeNull();
    });

    it("returns null for an empty PR list", () => {
      expect(findPRForIssueInList(201, [])).toBeNull();
    });

    // #483 should-fix — ranked resolution: a closing-keyword reference beats
    // a bare mention, and an ambiguous bare mention (2+ PRs) refuses to guess.
    it("prefers a closing-keyword reference over a bare title mention, regardless of array order", () => {
      const mirrorFirst = [
        {
          number: 402,
          url: "https://github.com/org/repo/pull/402",
          title: "fix(forensics): mirror #201 onto the tool-call log",
          body: "unrelated context",
        },
        {
          number: 701,
          url: "https://github.com/org/repo/pull/701",
          title: "fix: issue 201",
          body: "Closes #201",
        },
      ];
      expect(findPRForIssueInList(201, mirrorFirst)?.number).toBe(701);

      // Order must not matter — same result with the closer listed first.
      const closerFirst = [mirrorFirst[1], mirrorFirst[0]];
      expect(findPRForIssueInList(201, closerFirst)?.number).toBe(701);
    });

    it("matches an issue URL reference (not just #N), with the same word boundary", () => {
      const urlRef = [
        {
          number: 9,
          url: "https://github.com/org/repo/pull/9",
          title: "fix",
          body: "Fixes https://github.com/org/repo/issues/201",
        },
      ];
      expect(findPRForIssueInList(201, urlRef)?.number).toBe(9);
      // Must not match issue 20 as a substring of ".../issues/201".
      expect(findPRForIssueInList(20, urlRef)).toBeNull();
    });

    it("refuses to guess when two PRs only bare-mention the same issue", () => {
      const ambiguous = [
        {
          number: 11,
          url: "https://github.com/org/repo/pull/11",
          title: "mirror #201",
          body: "",
        },
        {
          number: 12,
          url: "https://github.com/org/repo/pull/12",
          title: "also mentions #201 here",
          body: "",
        },
      ];
      expect(findPRForIssueInList(201, ambiguous)).toBeNull();
    });
  });

  // #483 must-fix — a failed (or unparseable) fetch must never be treated as
  // "this repo has no open PRs". See the integration-level pin in
  // tests/commands/autonomousCommands.watchdogPRCache.test.ts for the
  // downstream effect on the watchdog's per-issue cache.
  describe("getOpenPRsForRepo", () => {
    beforeEach(() => {
      vi.clearAllMocks();
      clearOpenPRsCache();
    });

    afterEach(() => {
      clearOpenPRsCache();
    });

    it("returns null (not []) on a fetch failure with no prior snapshot", async () => {
      mockExecOnce((cb) => cb(new Error("gh: command timed out"), { stdout: "", stderr: "" }));

      const result = await getOpenPRsForRepo("/tmp/repo", "org", "repo", 1_000);

      expect(result).toBeNull();
    });

    it("serves the last-known snapshot (not null, not []) when a LATER fetch fails", async () => {
      mockExecOnce((cb) =>
        cb(null, {
          stdout: JSON.stringify([
            { number: 1, url: "https://github.com/org/repo/pull/1", title: "t" },
          ]),
          stderr: "",
        })
      );
      const first = await getOpenPRsForRepo("/tmp/repo", "org", "repo", 1_000);
      expect(first).toEqual([
        { number: 1, url: "https://github.com/org/repo/pull/1", title: "t", body: undefined },
      ]);

      // Past the TTL, so this call re-fetches — and the re-fetch fails.
      mockExecOnce((cb) => cb(new Error("network blip"), { stdout: "", stderr: "" }));
      const second = await getOpenPRsForRepo("/tmp/repo", "org", "repo", 1_000 + 6 * 60_000);

      expect(second).toEqual(first);
    });

    it("does not cache a zero-exit call whose stdout doesn't parse as a PR list", async () => {
      mockExecOnce((cb) => cb(null, { stdout: "gh: authentication required", stderr: "" }));

      const result = await getOpenPRsForRepo("/tmp/repo", "org", "repo", 1_000);

      expect(result).toBeNull();
    });

    it("treats a genuine empty list ('[]') as a real, cacheable result — not unparseable", async () => {
      mockExecOnce((cb) => cb(null, { stdout: "[]", stderr: "" }));

      const result = await getOpenPRsForRepo("/tmp/repo", "org", "repo", 1_000);

      expect(result).toEqual([]);
    });
  });
});
