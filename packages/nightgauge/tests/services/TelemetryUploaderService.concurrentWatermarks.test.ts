/**
 * TelemetryUploaderService.concurrentWatermarks.test.ts — two concurrent
 * `saveWatermarks` calls on one watermark file must not destroy each other's
 * temp file (#786).
 *
 * `saveWatermarks` wrote a fixed `${uri.fsPath}.tmp`, then renamed it onto the
 * watermark file (the exact shape #777 fixed in `TelemetryStore.writeIndex`).
 * With N concurrent writers sharing one temp name, the first rename moves the
 * file away and every later rename fails
 * `ENOENT: no such file or directory, rename '...upload-watermarks.json.tmp'
 * -> '...upload-watermarks.json'`.
 *
 * Unlike `TelemetryUploaderService.test.ts`, this file does NOT fully mock
 * `vscode.workspace.fs` — its `writeFile`/`rename`/`readFile` are thin
 * adapters over the real `node:fs/promises`, keyed off `uri.fsPath`, because
 * the defect lives in the ordering of two real filesystem calls that a fully
 * mocked fs cannot race at all. Two concurrent flushes are the ordinary case
 * here: the active-run flush timer and the end-of-cycle flush can both reach
 * `saveWatermarks` for the same file.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as fsSync from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  Uri: {
    file: (p: string) => ({ fsPath: p, toString: () => p }),
  },
  workspace: {
    fs: {
      readFile: vi.fn(async (uri: { fsPath: string }) => {
        const buf = await fs.readFile(uri.fsPath);
        return new Uint8Array(buf);
      }),
      writeFile: vi.fn(async (uri: { fsPath: string }, data: Uint8Array) => {
        await fs.writeFile(uri.fsPath, Buffer.from(data));
      }),
      rename: vi.fn(async (source: { fsPath: string }, target: { fsPath: string }) => {
        await fs.rename(source.fsPath, target.fsPath);
      }),
      delete: vi.fn(async (uri: { fsPath: string }) => {
        await fs.rm(uri.fsPath, { force: true });
      }),
    },
  },
}));

import { saveWatermarks } from "../../src/services/TelemetryUploaderService";
import * as vscode from "vscode";

const CONCURRENT_WRITERS = 8;

let dir: string;
let watermarkPath: string;

beforeEach(async () => {
  dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-watermark-race-"));
  watermarkPath = path.join(dir, "upload-watermarks.json");
});

afterEach(async () => {
  await fs.rm(dir, { recursive: true, force: true });
});

describe("saveWatermarks — concurrent writers on one watermark file (#786)", () => {
  it("all writes succeed; none loses its temp file to another's rename", async () => {
    const uri = vscode.Uri.file(watermarkPath);
    const settled = await Promise.allSettled(
      Array.from({ length: CONCURRENT_WRITERS }, (_, i) =>
        saveWatermarks(uri, { [`2026-05-${10 + i}.jsonl`]: i })
      )
    );

    // Report the actual rejection rather than a bare count — an ENOENT on
    // rename is the specific failure this test exists to keep out.
    const rejected = settled.filter((r) => r.status === "rejected") as PromiseRejectedResult[];
    expect(rejected.map((r) => String(r.reason))).toEqual([]);
  });

  it("leaves one readable JSON file and no temp files behind", async () => {
    const uri = vscode.Uri.file(watermarkPath);
    await Promise.all(
      Array.from({ length: CONCURRENT_WRITERS }, (_, i) =>
        saveWatermarks(uri, { [`2026-05-${10 + i}.jsonl`]: i })
      )
    );

    const content = await fs.readFile(watermarkPath, "utf-8");
    expect(() => JSON.parse(content)).not.toThrow();

    const leftoverTmp = fsSync.readdirSync(dir).filter((f) => f.endsWith(".tmp"));
    expect(leftoverTmp).toEqual([]);
  });

  it("cleans up its own temp file when the rename fails", async () => {
    const uri = vscode.Uri.file(watermarkPath);
    const renameMock = vi.mocked(vscode.workspace.fs.rename);
    renameMock.mockRejectedValueOnce(new Error("simulated rename failure"));

    await expect(saveWatermarks(uri, { "2026-05-10.jsonl": 1 })).rejects.toThrow(
      "simulated rename failure"
    );

    // A per-write temp name only stays leak-free if a failed write cleans up
    // after itself — mutation check for the cleanup half of the fix,
    // independent of the concurrency race the tests above cover.
    const leftoverTmp = fsSync.readdirSync(dir).filter((f) => f.endsWith(".tmp"));
    expect(leftoverTmp).toEqual([]);
  });
});
