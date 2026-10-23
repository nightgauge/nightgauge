/**
 * Branch-name safety in `HeadlessOrchestrator.tryRecoverExpectedBranch`.
 *
 * #498 — this file used to define its OWN `sanitizeBranchName()` and assert
 * against that, citing an allowlist regex `[^a-zA-Z0-9/_.-]` in
 * `tryRecoverExpectedBranch`. Two things were wrong with it:
 *
 *   1. It was a mirror. Every "strips shell injection" case exercised the copy
 *      in the test file, so a regression in the shipped control was invisible
 *      to all twelve assertions (docs/TESTING.md § Testing Anti-Patterns ### 7).
 *   2. The citation was stale. Production no longer strips anything. The
 *      recovery path is guarded by TWO shipped controls instead:
 *        (a) `isValidBranchName` — a DENYLIST validator that refuses the name
 *            outright, before any git process is spawned; and
 *        (b) `execFileAsync("git", [...])` — argv, never a shell string, so a
 *            metacharacter that survives (a) is still only ever an argument.
 *
 * These tests drive the real private method on a real orchestrator with
 * `child_process.execFile` mocked, and assert both controls. `isValidBranchName`
 * is `src/utils/branchUtils.ts`'s single exported copy — `HeadlessOrchestrator`
 * carried a byte-for-byte duplicate until #498 pointed it at the shared one.
 *
 * @see Issue #2492 - Branch name sanitization only escapes double quotes
 * @see Issue #2490 - Security epic
 * @see Issue #498  - Mirror-test conversion
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [],
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(),
      onDidCreate: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: { executeCommand: vi.fn(), registerCommand: vi.fn() },
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  Disposable: { from: vi.fn() },
  Uri: { file: (p: string) => ({ fsPath: p }) },
  ExtensionMode: { Production: 1, Development: 2, Test: 3 },
}));

// Every git invocation the recovery path makes is recorded here, as
// (command, argv) — the shape that proves no shell is involved.
const { execFileCalls, failLocalCheckout } = vi.hoisted(() => ({
  execFileCalls: [] as Array<{ cmd: string; args: string[] }>,
  // When true, `git checkout <branch>` rejects, driving the recovery down its
  // remote-tracking fallback arm so that arm's argv can be inspected too.
  failLocalCheckout: { value: false },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    execFileCalls.push({ cmd, args: [...(args ?? [])] });
    if (failLocalCheckout.value && args?.[0] === "checkout" && args.length === 2) {
      return Promise.reject(new Error("no such branch"));
    }
    return Promise.resolve({ stdout: "", stderr: "" });
  };

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: "", stderr: "" });

  return { ...actual, exec: execMock, execFile: execFileMock };
});

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import { isValidBranchName } from "../../src/utils/branchUtils";
import type { Logger } from "../../src/utils/logger";

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

describe("tryRecoverExpectedBranch — branch-name safety", () => {
  let tmpDir: string;
  let orchestrator: HeadlessOrchestrator;

  function recover(expectedBranch: string): Promise<boolean> {
    return (
      orchestrator as unknown as {
        tryRecoverExpectedBranch: (n: number, b: string, c: string) => Promise<boolean>;
      }
    ).tryRecoverExpectedBranch(2492, expectedBranch, "main");
  }

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-2492-"));
    execFileCalls.length = 0;
    orchestrator = new HeadlessOrchestrator(null, createMockLogger());
    orchestrator.setWorktreeOverride(tmpDir);
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
    vi.clearAllMocks();
  });

  describe("control (a): the validator gate refuses before spawning git", () => {
    // Names the SHIPPED denylist rejects. Each must abort the recovery with no
    // process spawned at all — the strongest possible outcome for a name the
    // orchestrator cannot vouch for.
    const rejected: Array<[string, string]> = [
      ["a space", "feat/foo bar"],
      ["a NUL byte", "feat/foo\0bar"],
      ["a newline", "feat/foo\nbar"],
      ["a control character", "feat/foo\x1fbar"],
      ["a backslash", "feat\\foo"],
      ["a git revision suffix", "feat/foo@{upstream}"],
      ["a leading slash", "/etc/passwd"],
      ["a leading dot", ".hidden"],
      ["a trailing slash", "feat/foo/"],
      ["consecutive dots (path traversal)", "feat/../../../etc/passwd"],
      ["a refspec wildcard", "feat/*"],
      ["a colon (refspec separator)", "feat:foo"],
      ["a tilde (rev walk)", "feat/foo~1"],
      ["a caret (rev walk)", "feat/foo^2"],
      ["a .lock suffix", "feat/foo.lock"],
      ["an empty name", ""],
    ];

    for (const [label, branch] of rejected) {
      it(`refuses ${label} and runs no git command`, async () => {
        expect(await recover(branch)).toBe(false);
        expect(execFileCalls).toEqual([]);
      });
    }

    it("accepts ordinary branch names and proceeds to checkout", async () => {
      for (const branch of [
        "main",
        "feat/123-fix-something",
        "fix/ISSUE-42.patch",
        "release/1.2.3",
        "feat/my_feature",
      ]) {
        execFileCalls.length = 0;
        expect(await recover(branch)).toBe(true);
        expect(execFileCalls[0]).toEqual({ cmd: "git", args: ["checkout", branch] });
      }
    });

    it("delegates to the shared branchUtils validator, not a private copy", () => {
      // Unpinned-wiring guard: the orchestrator used to carry a duplicate of
      // this denylist. If a second copy ever reappears and drifts, the gate
      // outcomes above and this predicate stop agreeing.
      for (const [, branch] of rejected) {
        expect([branch, isValidBranchName(branch)]).toEqual([branch, false]);
      }
      expect(isValidBranchName("feat/123-fix-something")).toBe(true);
    });
  });

  describe("control (b): names reach git as argv, never as a shell string", () => {
    // The denylist deliberately permits `$ ( ) ; | & \` "` — they are legal in
    // a git ref. They are safe ONLY because the orchestrator uses execFile with
    // an argument vector. Pin that: each must arrive as ONE argv element,
    // byte-identical, with no shell anywhere in the call.
    const shellMetacharacterNames = [
      "feat/foo$(evil)",
      "feat/foo`id`",
      "feat/foo;rm -rf",
      "feat/foo|cat",
      "feat/foo&&evil",
      'feat/foo"bar"',
      "feat/foo'bar'",
      "feat/foo>out",
    ].filter((n) => !n.includes(" ")); // a space is rejected by control (a)

    for (const branch of shellMetacharacterNames) {
      it(`passes ${JSON.stringify(branch)} as a single unmodified argv element`, async () => {
        expect(await recover(branch)).toBe(true);
        // Exactly one call, `git` with an argv — no `sh -c`, no joined string.
        expect(execFileCalls).toHaveLength(1);
        expect(execFileCalls[0].cmd).toBe("git");
        expect(execFileCalls[0].args).toEqual(["checkout", branch]);
      });
    }

    it("never spawns a shell on the remote-tracking fallback either", async () => {
      // Force the local-checkout arm to fail so the remote-tracking arms —
      // which embed the name in `refs/remotes/origin/<branch>` and
      // `origin/<branch>` — are exercised with the same hostile name.
      const branch = "feat/foo$(evil)";
      failLocalCheckout.value = true;
      try {
        expect(await recover(branch)).toBe(true);
      } finally {
        failLocalCheckout.value = false;
      }

      expect(execFileCalls.length).toBeGreaterThan(1);
      const permitted = new Set([branch, `refs/remotes/origin/${branch}`, `origin/${branch}`]);
      for (const { cmd, args } of execFileCalls) {
        // `git`, never `sh`/`bash`/`zsh` — no shell in the chain.
        expect(cmd).toBe("git");
        // Wherever the name appears it is a WHOLE argv element (optionally
        // under a git ref prefix), never concatenated with another token.
        for (const a of args) {
          if (a.includes(branch)) {
            expect([a, permitted.has(a)]).toEqual([a, true]);
          }
        }
      }
    });
  });
});
