/**
 * #1084 — the gate-metrics store was unreadable in two independent ways.
 *
 * Either break alone empties the tally, and BOTH render as `Gates: N/A`, which
 * is indistinguishable from "this run had no gates". That is why they are
 * pinned together: fixing one leaves the other looking like the first bug
 * recurring.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import { GateMetricsWriter } from "../../src/utils/gateMetricsWriter";

async function seed(root: string, gateName: string): Promise<void> {
  const dir = path.join(root, ".nightgauge", "health");
  await fs.mkdir(dir, { recursive: true });
  await fs.writeFile(
    path.join(dir, "gate-metrics.jsonl"),
    JSON.stringify({
      schema_version: "1",
      timestamp: new Date().toISOString(),
      issue_number: 338,
      gate_name: gateName,
      result: "pass",
    }) + "\n",
    "utf-8"
  );
}

describe("#1084 — gate metrics survive the names the shipped skills emit", () => {
  let tmp: string;

  beforeEach(async () => {
    tmp = await fs.mkdtemp(path.join(os.tmpdir(), "ng-gate-"));
  });

  afterEach(async () => {
    await fs.rm(tmp, { recursive: true, force: true });
  });

  // The two names the feature-validate skill actually writes
  // (_includes/ci-and-knowledge.md and _includes/verify-ui-gate.md). Neither was
  // in the reader's enum, so every record it wrote was dropped by safeParse.
  it.each(["adversarial-review", "verify-ui"])(
    "keeps a record whose gate_name is %s",
    async (gateName) => {
      await seed(tmp, gateName);
      const records = await GateMetricsWriter.readAll(tmp);
      expect(records).toHaveLength(1);
      expect(records[0]!.gate_name).toBe(gateName);
    }
  );

  it("still keeps the deterministic gate names", async () => {
    await seed(tmp, "unit-tests");
    expect(await GateMetricsWriter.readAll(tmp)).toHaveLength(1);
  });

  // The silent drop is the reason the enum mismatch survived: a rejected record
  // and an absent file produce the identical empty list.
  it("reports dropped records instead of swallowing them", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const dir = path.join(tmp, ".nightgauge", "health");
    await fs.mkdir(dir, { recursive: true });
    await fs.writeFile(
      path.join(dir, "gate-metrics.jsonl"),
      // Missing issue_number — rejected on any schema.
      JSON.stringify({ schema_version: "1", gate_name: "lint", result: "pass" }) + "\n",
      "utf-8"
    );

    const records = await GateMetricsWriter.readAll(tmp);
    expect(records).toHaveLength(0);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("dropped 1 unreadable record"));
    warn.mockRestore();
  });

  it("an absent file is silent — it is not a dropped record", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    expect(await GateMetricsWriter.readAll(tmp)).toHaveLength(0);
    expect(warn).not.toHaveBeenCalled();
    warn.mockRestore();
  });
});
