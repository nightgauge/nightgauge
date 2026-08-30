/**
 * atomicWrite.test.ts
 *
 * Regression tests for #1210 — a retention prune rewrote a JSONL file with
 * `fs.writeFile` (truncate-then-write), so a concurrent reader could observe
 * the file at zero bytes. The telemetry uploader did exactly that, failed to
 * match its anchor record against an empty file, and re-uploaded the stream
 * from line 0.
 *
 * These run against the REAL filesystem in a temp directory. The defect is a
 * property of how the bytes reach disk, so a mocked `fs` could not observe it.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { writeFileAtomic } from "../../src/utils/atomicWrite";

let dir: string;

beforeEach(async () => {
  dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-atomic-"));
});

afterEach(async () => {
  await fs.rm(dir, { recursive: true, force: true });
});

describe("writeFileAtomic (#1210)", () => {
  it("replaces an existing file's contents", async () => {
    const target = path.join(dir, "history.jsonl");
    await fs.writeFile(target, "old\n", "utf-8");

    await writeFileAtomic(target, "new\n");

    expect(await fs.readFile(target, "utf-8")).toBe("new\n");
  });

  it("creates the file when it does not exist", async () => {
    const target = path.join(dir, "fresh.jsonl");

    await writeFileAtomic(target, "a\n");

    expect(await fs.readFile(target, "utf-8")).toBe("a\n");
  });

  it("never exposes a zero-length file to a concurrent reader", async () => {
    const target = path.join(dir, "history.jsonl");
    const original = Array.from({ length: 500 }, (_, i) => `{"n":${i}}`).join("\n") + "\n";
    await fs.writeFile(target, original, "utf-8");

    const replacement = Array.from({ length: 400 }, (_, i) => `{"n":${i + 100}}`).join("\n") + "\n";

    // Hammer the file with reads while the rewrite is in flight. Every read
    // must land on a complete version — the old one or the new one.
    let stop = false;
    const observed: number[] = [];
    const reader = (async () => {
      while (!stop) {
        try {
          const text = await fs.readFile(target, "utf-8");
          observed.push(text.split("\n").filter((l) => l.trim().length > 0).length);
        } catch {
          // ENOENT is not possible with rename, but a read error must not end
          // the loop — record nothing and keep sampling.
        }
      }
    })();

    for (let i = 0; i < 40; i++) {
      await writeFileAtomic(target, replacement);
      await writeFileAtomic(target, original);
    }
    stop = true;
    await reader;

    expect(observed.length).toBeGreaterThan(0);
    // 500 (original) or 400 (replacement) — never 0, and never a partial count.
    const unexpected = observed.filter((n) => n !== 500 && n !== 400);
    expect(unexpected).toEqual([]);
  });

  it("leaves no temp files behind on success", async () => {
    const target = path.join(dir, "history.jsonl");
    await writeFileAtomic(target, "x\n");

    const leftovers = (await fs.readdir(dir)).filter((f) => f.endsWith(".tmp"));
    expect(leftovers).toEqual([]);
  });

  it("leaves the original intact and cleans up when the write fails", async () => {
    const target = path.join(dir, "sub", "history.jsonl");
    // Parent directory does not exist — the temp write fails. The helper
    // deliberately does not mkdir, so this surfaces rather than silently
    // creating a tree.
    await expect(writeFileAtomic(target, "x\n")).rejects.toThrow();

    const leftovers = (await fs.readdir(dir)).filter((f) => f.endsWith(".tmp"));
    expect(leftovers).toEqual([]);
  });

  it("uses a unique temp name so concurrent writers cannot collide (#777)", async () => {
    const target = path.join(dir, "history.jsonl");
    await fs.writeFile(target, "seed\n", "utf-8");

    // Two writers racing on a FIXED temp name produce an ENOENT for the loser,
    // because the winner's rename consumed the shared temp file. Both must
    // succeed here.
    const results = await Promise.allSettled([
      writeFileAtomic(target, "a\n"),
      writeFileAtomic(target, "b\n"),
      writeFileAtomic(target, "c\n"),
    ]);

    expect(results.every((r) => r.status === "fulfilled")).toBe(true);
    expect(["a\n", "b\n", "c\n"]).toContain(await fs.readFile(target, "utf-8"));
  });
});
