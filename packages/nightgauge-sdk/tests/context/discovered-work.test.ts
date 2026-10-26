import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, it, expect } from "vitest";
import {
  DiscoveredWorkContextSchema,
  DiscoveredWorkRecordSchema,
} from "../../src/context/schemas/index.js";

// The fixture is shared with the Go peer (internal/cmd/discoveredwork), so both
// languages pin the same key set (#478).
const FIXTURE = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "../../../../internal/cmd/discoveredwork/testdata/record-v1.json"
);
const fixture = JSON.parse(readFileSync(FIXTURE, "utf8"));
const fixtureRecord = fixture.records[0];

const sorted = (o: object) => Object.keys(o).sort();

describe("DiscoveredWorkContextSchema", () => {
  it("parses the shared fixture with the same key set as the Go peer", () => {
    const parsed = DiscoveredWorkContextSchema.parse(fixture);
    expect(sorted(parsed)).toEqual(sorted(fixture));
    expect(sorted(parsed)).toEqual(sorted(DiscoveredWorkContextSchema.shape));
    expect(sorted(parsed.records[0])).toEqual(sorted(fixtureRecord));
    expect(sorted(parsed.records[0])).toEqual(sorted(DiscoveredWorkRecordSchema.shape));
  });

  it("refuses a record whose issue_number differs from the envelope", () => {
    const bad = { ...fixture, records: [{ ...fixtureRecord, issue_number: 1 }] };
    expect(DiscoveredWorkContextSchema.safeParse(bad).success).toBe(false);
  });

  it("refuses an unknown envelope key", () => {
    expect(DiscoveredWorkContextSchema.safeParse({ ...fixture, extra: 1 }).success).toBe(false);
  });
});

describe("DiscoveredWorkRecordSchema", () => {
  const refuses = (patch: Record<string, unknown>, field: string) => {
    const r = DiscoveredWorkRecordSchema.safeParse({ ...fixtureRecord, ...patch });
    expect(r.success).toBe(false);
    expect(r.error?.issues[0]?.path[0]).toBe(field);
  };

  it("refuses a missing summary", () => {
    const { summary: _summary, ...rest } = fixtureRecord;
    expect(DiscoveredWorkRecordSchema.safeParse(rest).success).toBe(false);
    refuses({ summary: "two\nlines" }, "summary");
  });

  it("refuses a priority outside the spike vocabulary", () => {
    refuses({ suggested_priority: "P2" }, "suggested_priority");
  });

  it("refuses absolute and escaping target paths", () => {
    refuses({ candidate_target_files: ["/etc/passwd"] }, "candidate_target_files");
    refuses({ candidate_target_files: ["a/../../b"] }, "candidate_target_files");
    refuses({ candidate_target_files: ["-rf"] }, "candidate_target_files");
    expect(
      DiscoveredWorkRecordSchema.safeParse({
        ...fixtureRecord,
        candidate_target_files: ["a/../b.go"],
      }).success
    ).toBe(true);
  });

  it("refuses an unknown key", () => {
    const r = DiscoveredWorkRecordSchema.safeParse({ ...fixtureRecord, target_repo: "x/y" });
    expect(r.success).toBe(false);
    expect(r.error?.issues[0]?.code).toBe("unrecognized_keys");
  });

  it("admits producer:<kebab> sources and refuses malformed ones", () => {
    expect(
      DiscoveredWorkRecordSchema.safeParse({
        ...fixtureRecord,
        source_stage: "producer:post-merge-impact-set",
      }).success
    ).toBe(true);
    refuses({ source_stage: "producer:Bad_Name" }, "source_stage");
  });

  it("admits a null run_id", () => {
    expect(DiscoveredWorkRecordSchema.safeParse({ ...fixtureRecord, run_id: null }).success).toBe(
      true
    );
  });
});
