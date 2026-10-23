/**
 * terminalParity.test.ts
 *
 * Terminal-path behavior parity (#257) — TypeScript twin of
 * internal/orchestrator/terminal_parity_test.go.
 *
 * Nightgauge has two pipeline execution paths — the Go scheduler loop
 * (Scheduler.runPipeline) and the extension path (ConcurrentPipelineManager →
 * HeadlessOrchestrator + the IPC pipeline.notifyComplete funnel). A behavior
 * wired to only one of them is invisible on the other with no error and no
 * failed test (#210, #254). Both suites enforce the same manifest,
 * internal/orchestrator/testdata/terminal_behaviors.json:
 *
 *   1. every behavior's anchor must exist in its file on each path (or the
 *      side must carry an explicit pathSpecific reason);
 *   2. the fenced terminal-funnel regions (terminal-parity markers) are
 *      content-pinned by sha256 of their normalized source. Any edit inside a
 *      fence fails here until the manifest is updated — which is the moment
 *      to answer: which of the two paths reaches the new behavior, and is the
 *      other intentionally excluded?
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import path from "node:path";

const REPO_ROOT = path.resolve(__dirname, "../../../..");
const MANIFEST_PATH = path.join(
  REPO_ROOT,
  "internal/orchestrator/testdata/terminal_behaviors.json"
);

interface ParitySide {
  file?: string;
  anchor?: string;
  pathSpecific?: string;
}

interface ParityBehavior {
  name: string;
  note?: string;
  go?: ParitySide;
  extension?: ParitySide;
}

interface ParityFence {
  id: string;
  file: string;
  begin: string;
  end: string;
  sha256: string;
}

interface ParityManifest {
  behaviors: ParityBehavior[];
  fences: ParityFence[];
}

const manifest: ParityManifest = JSON.parse(readFileSync(MANIFEST_PATH, "utf-8"));

const sourceCache = new Map<string, string>();
function repoFile(rel: string): string {
  const cached = sourceCache.get(rel);
  if (cached !== undefined) return cached;
  const content = readFileSync(path.join(REPO_ROOT, rel), "utf-8");
  sourceCache.set(rel, content);
  return content;
}

/**
 * Must stay identical to normalizeFence in terminal_parity_test.go: per line,
 * trim whitespace; drop empty lines; drop lines starting with "//"; join "\n".
 */
function normalizeFence(src: string): string {
  return src
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("//"))
    .join("\n");
}

describe("terminal-path behavior parity manifest (#257)", () => {
  it("lists behaviors and fences", () => {
    expect(manifest.behaviors.length).toBeGreaterThan(0);
    expect(manifest.fences.length).toBeGreaterThan(0);
  });

  for (const behavior of manifest.behaviors) {
    for (const pathName of ["go", "extension"] as const) {
      it(`${behavior.name}: ${pathName} side is wired or explicitly path-specific`, () => {
        const side = behavior[pathName];
        expect(side, `${behavior.name}: ${pathName} side missing`).toBeDefined();
        const hasAnchor = Boolean(side!.anchor);
        const hasReason = Boolean(side!.pathSpecific);
        expect(
          hasAnchor !== hasReason,
          `${behavior.name}: ${pathName} side must have exactly one of anchor/pathSpecific`
        ).toBe(true);
        if (hasReason) return; // explicitly path-specific, reason recorded
        expect(side!.file, `${behavior.name}: ${pathName} anchor needs a file`).toBeTruthy();
        const source = repoFile(side!.file!);
        expect(
          source.includes(side!.anchor!),
          `${behavior.name}: anchor ${JSON.stringify(side!.anchor)} not found in ${side!.file}. ` +
            `The ${pathName}-path call site moved or was removed. If the behavior was rewired, ` +
            `update terminal_behaviors.json; if it was removed from this path only, record a ` +
            `pathSpecific reason (and an issue if the gap is a defect).`
        ).toBe(true);
      });
    }
  }

  for (const fence of manifest.fences) {
    it(`fence ${fence.id} is content-pinned`, () => {
      const lines = repoFile(fence.file).split("\n");
      const markerHits = (marker: string): number[] =>
        lines.reduce<number[]>((hits, line, i) => {
          if (line.includes(marker) && !line.includes('"')) hits.push(i);
          return hits;
        }, []);
      const beginHits = markerHits(fence.begin);
      const endHits = markerHits(fence.end);
      expect(beginHits, `${fence.id}: begin marker must appear exactly once`).toHaveLength(1);
      expect(endHits, `${fence.id}: end marker must appear exactly once`).toHaveLength(1);
      const [beginIdx] = beginHits;
      const [endIdx] = endHits;
      expect(endIdx, `${fence.id}: markers out of order`).toBeGreaterThan(beginIdx);

      const normalized = normalizeFence(lines.slice(beginIdx + 1, endIdx).join("\n"));
      const got = createHash("sha256").update(normalized).digest("hex");
      expect(
        got,
        `${fence.id}: terminal funnel content changed in ${fence.file}.\n` +
          `This fence pins the terminal-path funnel (#257). If your change is deliberate:\n` +
          `  1. Answer the parity question: does the OTHER execution path need the same behavior?\n` +
          `     (Go: runPipeline terminal defer; extension: runSlotPipeline finally + pipeline.notifyComplete)\n` +
          `  2. Add/update the behavior row in internal/orchestrator/testdata/terminal_behaviors.json\n` +
          `     (anchor on both paths, or pathSpecific with a reason/issue).\n` +
          `  3. Update the fence sha256 to: ${got}`
      ).toBe(fence.sha256);
    });
  }
});
