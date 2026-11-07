import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  clearStalePrMergeFixMarker,
  consumePrMergeFixMarker,
  prMergeFixMarkerPath,
  PR_MERGE_FIX_PUSHED,
} from "../../src/services/prMergeFixMarker";
import { fakeCloneLayout } from "../helpers/cloneLayout";

describe("prMergeFixMarker (#2833)", () => {
  let dir: string;
  let marker: string;
  const warn = vi.fn();

  beforeEach(() => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "pr-merge-fix-"));
    marker = path.join(dir, "pr-merge-fix-7.json");
    warn.mockReset();
  });
  afterEach(() => fs.rmSync(dir, { recursive: true, force: true }));

  it("resolves under the clone's pipeline state directory", () => {
    fakeCloneLayout(dir);
    expect(prMergeFixMarkerPath(dir, 7)).toBe(
      path.join(dir, ".git", "nightgauge", "pipeline", "pr-merge-fix-7.json")
    );
  });

  it("absent → null, no warning", async () => {
    expect(await consumePrMergeFixMarker(marker, warn)).toBeNull();
    expect(warn).not.toHaveBeenCalled();
  });

  it("a fix-pushed marker is returned and deleted", async () => {
    const body = { result: PR_MERGE_FIX_PUSHED, pr_number: 9, head_sha: "abc", attempt: 1 };
    fs.writeFileSync(marker, JSON.stringify(body));
    expect(await consumePrMergeFixMarker(marker, warn)).toEqual(body);
    expect(fs.existsSync(marker)).toBe(false);
    expect(warn).not.toHaveBeenCalled();
  });

  it("malformed JSON → null, warned, deleted", async () => {
    fs.writeFileSync(marker, "{not json");
    expect(await consumePrMergeFixMarker(marker, warn)).toBeNull();
    expect(fs.existsSync(marker)).toBe(false);
    expect(warn).toHaveBeenCalledOnce();
  });

  it("another result → null, warned, deleted", async () => {
    fs.writeFileSync(marker, JSON.stringify({ result: "gave-up" }));
    expect(await consumePrMergeFixMarker(marker, warn)).toBeNull();
    expect(fs.existsSync(marker)).toBe(false);
    expect(warn).toHaveBeenCalledOnce();
  });

  it("clearStale removes a marker and ignores an absent one", async () => {
    fs.writeFileSync(marker, "{}");
    await clearStalePrMergeFixMarker(marker);
    expect(fs.existsSync(marker)).toBe(false);
    await expect(clearStalePrMergeFixMarker(marker)).resolves.toBeUndefined();
  });
});
