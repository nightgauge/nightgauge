/**
 * SkillEffectivenessWriter unit tests
 *
 * The file is a SNAPSHOT keyed by (skill_file, commit_hash): each analysis
 * re-classifies every recent skill change, so appending every pass grew it
 * without bound (96 MB of mostly duplicate rows, #1900). `upsertRecords`
 * keeps the latest record per key inside the retention window; `readLatest`
 * streams the file, so its memory is bounded by distinct keys, not file size.
 *
 * @see Issue #1414 - Skill effectiveness tracking
 * @see Issue #1900 - skill-drift reads this file; retention policy
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { SkillEffectivenessRecord } from "../../src/schemas/skillEffectiveness";
import { SkillEffectivenessWriter } from "../../src/utils/SkillEffectivenessWriter";
import { mkFakeCloneLayout } from "../helpers/cloneLayout";

let root: string;
let file: string;

function makeRecord(overrides: Partial<SkillEffectivenessRecord> = {}): SkillEffectivenessRecord {
  return {
    schema_version: "1",
    skill_file: "skills/nightgauge-feature-planning/SKILL.md",
    stage: "feature-planning",
    commit_hash: "abc123",
    changed_at: new Date(Date.now() - 5 * 86_400_000).toISOString(),
    before_sample_count: 10,
    before_success_rate: 0.7,
    after_sample_count: 10,
    after_success_rate: 0.9,
    delta: 0.2,
    classification: "effective",
    confidence: "low",
    analyzed_at: new Date(Date.now() - 86_400_000).toISOString(),
    ...overrides,
  };
}

const lines = () => fs.readFileSync(file, "utf-8").trim().split("\n");

beforeEach(() => {
  root = fs.mkdtempSync(path.join(os.tmpdir(), "ng-skill-eff-"));
  const layout = mkFakeCloneLayout(root);
  file = path.join(layout.checkout, "health", "skill-effectiveness.jsonl");
});

afterEach(() => {
  fs.rmSync(root, { recursive: true, force: true });
});

describe("SkillEffectivenessWriter", () => {
  it("getFilePath resolves under the checkout's health directory", () => {
    expect(SkillEffectivenessWriter.getFilePath(root)).toBe(file);
  });

  it("readLatest returns null when the file is absent (absence is not an empty corpus)", async () => {
    expect(await SkillEffectivenessWriter.readLatest(root)).toBeNull();
  });

  it("upsertRecords keeps one record per (skill_file, commit_hash), the latest analysis", async () => {
    await SkillEffectivenessWriter.upsertRecords(root, [
      makeRecord({ analyzed_at: "2026-01-01T00:00:00Z", classification: "neutral" }),
      makeRecord({ commit_hash: "def456" }),
    ]);
    await SkillEffectivenessWriter.upsertRecords(root, [
      makeRecord({ analyzed_at: new Date().toISOString(), classification: "regression" }),
    ]);

    expect(lines()).toHaveLength(2);
    const latest = await SkillEffectivenessWriter.readLatest(root);
    expect(latest).toHaveLength(2);
    expect(latest!.find((r) => r.commit_hash === "abc123")!.classification).toBe("regression");
  });

  it("upsertRecords drops changes older than the retention window", async () => {
    await SkillEffectivenessWriter.upsertRecords(
      root,
      [
        makeRecord(),
        makeRecord({
          commit_hash: "old",
          changed_at: new Date(Date.now() - 200 * 86_400_000).toISOString(),
        }),
      ],
      90
    );
    expect(lines()).toHaveLength(1);
  });

  it("upsertRecords collapses a legacy append-only file of duplicate rows", async () => {
    fs.mkdirSync(path.dirname(file), { recursive: true });
    const dup = Array.from({ length: 50 }, (_, i) =>
      JSON.stringify(makeRecord({ analyzed_at: new Date(Date.now() - i * 1000).toISOString() }))
    );
    fs.writeFileSync(file, dup.join("\n") + "\nnot json\n");
    await SkillEffectivenessWriter.upsertRecords(root, []);
    expect(lines()).toHaveLength(1);
  });

  it("upsertRecords skips invalid records without throwing", async () => {
    const invalid = {
      ...makeRecord(),
      classification: "bogus",
    } as unknown as SkillEffectivenessRecord;
    await expect(SkillEffectivenessWriter.upsertRecords(root, [invalid])).resolves.toBeUndefined();
    expect(await SkillEffectivenessWriter.readLatest(root)).toEqual([]);
  });

  it("readLatest filters by changed_at when a range is given", async () => {
    await SkillEffectivenessWriter.upsertRecords(
      root,
      [
        makeRecord({ commit_hash: "a", changed_at: "2026-03-01T00:00:00Z" }),
        makeRecord({ commit_hash: "b", changed_at: "2026-05-01T00:00:00Z" }),
      ],
      100_000
    );
    const inRange = await SkillEffectivenessWriter.readLatest(root, {
      since: new Date("2026-04-01T00:00:00Z"),
      until: new Date("2026-06-01T00:00:00Z"),
    });
    expect(inRange!.map((r) => r.commit_hash)).toEqual(["b"]);
  });
});
