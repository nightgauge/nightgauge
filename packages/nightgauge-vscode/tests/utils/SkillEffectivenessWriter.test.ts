/**
 * SkillEffectivenessWriter unit tests
 *
 * @see Issue #1414 - Skill effectiveness tracking
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { SkillEffectivenessRecord } from "../../src/schemas/skillEffectiveness";

vi.mock("node:fs/promises");

import { SkillEffectivenessWriter } from "../../src/utils/SkillEffectivenessWriter";

const WORKSPACE = "/workspace";
const EXPECTED_FILE = path.join(WORKSPACE, ".nightgauge/health/skill-effectiveness.jsonl");

function makeRecord(overrides: Partial<SkillEffectivenessRecord> = {}): SkillEffectivenessRecord {
  return {
    schema_version: "1",
    skill_file: "skills/nightgauge-feature-planning/SKILL.md",
    stage: "feature-planning",
    commit_hash: "abc123",
    changed_at: "2026-01-15T12:00:00Z",
    before_sample_count: 10,
    before_success_rate: 0.7,
    after_sample_count: 10,
    after_success_rate: 0.9,
    delta: 0.2,
    classification: "effective",
    confidence: "low",
    analyzed_at: "2026-01-30T00:00:00Z",
    ...overrides,
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(fs.mkdir).mockResolvedValue(undefined);
  vi.mocked(fs.appendFile).mockResolvedValue(undefined);
  vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));
  vi.mocked(fs.writeFile).mockResolvedValue(undefined);
});

describe("SkillEffectivenessWriter", () => {
  describe("getFilePath()", () => {
    it("returns correct path under workspace root", () => {
      const filePath = SkillEffectivenessWriter.getFilePath(WORKSPACE);
      expect(filePath).toBe(EXPECTED_FILE);
    });
  });

  describe("appendRecord()", () => {
    it("creates the health directory and appends valid JSON line", async () => {
      const record = makeRecord();
      await SkillEffectivenessWriter.appendRecord(WORKSPACE, record);

      expect(fs.mkdir).toHaveBeenCalledWith(path.dirname(EXPECTED_FILE), {
        recursive: true,
      });
      expect(fs.appendFile).toHaveBeenCalledWith(
        EXPECTED_FILE,
        JSON.stringify(record) + "\n",
        "utf-8"
      );
    });

    it("skips invalid records without throwing (Zod validation)", async () => {
      const invalid = {
        schema_version: "bad",
      } as unknown as SkillEffectivenessRecord;
      await expect(
        SkillEffectivenessWriter.appendRecord(WORKSPACE, invalid)
      ).resolves.toBeUndefined();
      expect(fs.appendFile).not.toHaveBeenCalled();
    });

    it("does not throw on filesystem errors", async () => {
      vi.mocked(fs.mkdir).mockRejectedValue(new Error("Permission denied"));
      await expect(
        SkillEffectivenessWriter.appendRecord(WORKSPACE, makeRecord())
      ).resolves.toBeUndefined();
    });
  });

  describe("readAll()", () => {
    it("returns empty array when file does not exist", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );
      const records = await SkillEffectivenessWriter.readAll(WORKSPACE);
      expect(records).toEqual([]);
    });

    it("returns parsed records from valid JSONL content", async () => {
      const record = makeRecord();
      vi.mocked(fs.readFile).mockResolvedValue(
        (JSON.stringify(record) + "\n") as unknown as Buffer
      );

      const records = await SkillEffectivenessWriter.readAll(WORKSPACE);
      expect(records).toHaveLength(1);
      expect(records[0]).toEqual(record);
    });

    it("skips malformed JSON lines silently", async () => {
      const record = makeRecord();
      const content = "not-valid-json\n" + JSON.stringify(record) + "\n{bad\n";
      vi.mocked(fs.readFile).mockResolvedValue(content as unknown as Buffer);

      const records = await SkillEffectivenessWriter.readAll(WORKSPACE);
      expect(records).toHaveLength(1);
    });

    it("skips lines that fail Zod validation silently", async () => {
      const valid = makeRecord();
      const invalid = { schema_version: "99", stage: "unknown" };
      const content = JSON.stringify(invalid) + "\n" + JSON.stringify(valid) + "\n";
      vi.mocked(fs.readFile).mockResolvedValue(content as unknown as Buffer);

      const records = await SkillEffectivenessWriter.readAll(WORKSPACE);
      expect(records).toHaveLength(1);
      expect(records[0].schema_version).toBe("1");
    });

    it("skips empty lines", async () => {
      const record = makeRecord();
      const content = "\n" + JSON.stringify(record) + "\n\n";
      vi.mocked(fs.readFile).mockResolvedValue(content as unknown as Buffer);

      const records = await SkillEffectivenessWriter.readAll(WORKSPACE);
      expect(records).toHaveLength(1);
    });
  });

  describe("enforceRetention()", () => {
    it("returns early when file does not exist", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );
      await expect(SkillEffectivenessWriter.enforceRetention(WORKSPACE)).resolves.toBeUndefined();
      expect(fs.writeFile).not.toHaveBeenCalled();
    });

    it("keeps entries within retention window", async () => {
      const recent = makeRecord({
        analyzed_at: new Date().toISOString(),
      });
      vi.mocked(fs.readFile).mockResolvedValue(
        (JSON.stringify(recent) + "\n") as unknown as Buffer
      );

      await SkillEffectivenessWriter.enforceRetention(WORKSPACE, 90);

      expect(fs.writeFile).toHaveBeenCalledWith(
        EXPECTED_FILE,
        JSON.stringify(recent) + "\n",
        "utf-8"
      );
    });

    it("prunes entries older than retention window", async () => {
      const old = makeRecord({
        analyzed_at: "2020-01-01T00:00:00Z", // very old
      });
      const recent = makeRecord({
        analyzed_at: new Date().toISOString(),
      });
      const content = JSON.stringify(old) + "\n" + JSON.stringify(recent) + "\n";
      vi.mocked(fs.readFile).mockResolvedValue(content as unknown as Buffer);

      await SkillEffectivenessWriter.enforceRetention(WORKSPACE, 90);

      const written = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(written).not.toContain("2020-01-01");
      expect(written).toContain(recent.analyzed_at);
    });

    it("writes empty string when all entries are pruned", async () => {
      const old = makeRecord({ analyzed_at: "2020-01-01T00:00:00Z" });
      vi.mocked(fs.readFile).mockResolvedValue((JSON.stringify(old) + "\n") as unknown as Buffer);

      await SkillEffectivenessWriter.enforceRetention(WORKSPACE, 90);

      const written = vi.mocked(fs.writeFile).mock.calls[0][1];
      expect(written).toBe("");
    });

    it("does not throw on filesystem errors", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(
        (JSON.stringify(makeRecord()) + "\n") as unknown as Buffer
      );
      vi.mocked(fs.writeFile).mockRejectedValue(new Error("Disk full"));

      await expect(SkillEffectivenessWriter.enforceRetention(WORKSPACE)).resolves.toBeUndefined();
    });
  });
});
