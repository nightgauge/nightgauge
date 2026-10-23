/**
 * worktreeLocation — the extension asks the binary where the Go manager puts
 * pipeline worktrees (ADR-024 § 9, #2038) instead of re-deriving it.
 */
import { afterEach, describe, expect, it } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  cachedWorktreeBase,
  clearWorktreeBaseCache,
  goWorktreeDirName,
  resolveWorktreeBase,
  setWorktreeBaseBinaryResolver,
} from "../../src/utils/worktreeLocation";

afterEach(() => {
  clearWorktreeBaseCache();
});

/** A stand-in binary that prints `body` for `worktree base` and exits `code`. */
function fakeBinary(body: string, code = 0): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-wt-bin-"));
  const bin = path.join(dir, "nightgauge");
  fs.writeFileSync(bin, `#!/bin/sh\ncat <<'JSON'\n${body}\nJSON\nexit ${code}\n`, { mode: 0o755 });
  return bin;
}

describe("goWorktreeDirName", () => {
  it("names <repo>-issue-<N> from a bare or owner-qualified repo", () => {
    expect(goWorktreeDirName("acme/widget", 7)).toBe("widget-issue-7");
    expect(goWorktreeDirName("widget", 7)).toBe("widget-issue-7");
  });

  it("refuses names and numbers the Go resolver refuses", () => {
    for (const repo of ["", "..", "acme/..", "acme/.", "a b", "acme/-x", "x..y"]) {
      expect(goWorktreeDirName(repo, 7)).toBeUndefined();
    }
    expect(goWorktreeDirName("widget", 0)).toBeUndefined();
    expect(goWorktreeDirName("widget", 1.5)).toBeUndefined();
  });
});

describe.skipIf(process.platform === "win32")("resolveWorktreeBase", () => {
  it("caches the binary's absolute base per repository root", async () => {
    const base = path.join(os.tmpdir(), "state", "worktrees", "0123456789ab");
    setWorktreeBaseBinaryResolver(async () => fakeBinary(JSON.stringify({ base })));
    expect(cachedWorktreeBase("/repo")).toBeUndefined();
    await expect(resolveWorktreeBase("/repo")).resolves.toBe(base);
    expect(cachedWorktreeBase("/repo")).toBe(base);
  });

  it("resolves to undefined when the binary refuses the configuration", async () => {
    setWorktreeBaseBinaryResolver(async () =>
      fakeBinary("Error: invalid pipeline.worktree_base", 1)
    );
    await expect(resolveWorktreeBase("/repo")).resolves.toBeUndefined();
    expect(cachedWorktreeBase("/repo")).toBeUndefined();
  });

  it("ignores a relative answer and a missing binary", async () => {
    setWorktreeBaseBinaryResolver(async () => fakeBinary(JSON.stringify({ base: ".worktrees" })));
    await expect(resolveWorktreeBase("/repo")).resolves.toBeUndefined();
    clearWorktreeBaseCache();
    setWorktreeBaseBinaryResolver(async () => null);
    await expect(resolveWorktreeBase("/repo")).resolves.toBeUndefined();
  });
});
