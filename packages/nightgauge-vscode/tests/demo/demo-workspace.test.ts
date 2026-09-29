import { describe, expect, it } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { ExecutionHistoryRecordSchema } from "../../src/schemas/executionHistory";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "demo",
  "workspace"
);

function files(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    return e.isDirectory() ? files(p) : [p];
  });
}

describe("demo workspace fixture (#2107)", () => {
  const all = files(root);

  it("holds every seed the file-backed tabs read", () => {
    const rel = all.map((f) => path.relative(root, f));
    expect(rel).toEqual(
      expect.arrayContaining([
        path.join(".nightgauge", "config.yaml"),
        path.join(".nightgauge", "pipeline", "state.json"),
        path.join(".nightgauge", "health", "trends.jsonl"),
        path.join(".nightgauge", "knowledge", "issue-112", "PRD.md"),
      ])
    );
    expect(rel.filter((f) => f.includes("history") && f.endsWith(".jsonl")).length).toBeGreaterThan(
      0
    );
  });

  it("history records validate against the extension's run-record schema", () => {
    const lines = all
      .filter((f) => f.includes(`${path.sep}history${path.sep}`))
      .flatMap((f) => fs.readFileSync(f, "utf8").split("\n").filter(Boolean));
    expect(lines.length).toBeGreaterThan(0);
    for (const line of lines) {
      const parsed = ExecutionHistoryRecordSchema.safeParse(JSON.parse(line));
      expect(parsed.success ? [] : parsed.error.issues.slice(0, 3)).toEqual([]);
    }
  });

  it("keeps the platform off (ADR-026 section 6)", () => {
    const config = fs.readFileSync(path.join(root, ".nightgauge", "config.yaml"), "utf8");
    expect(config).toMatch(/platform:\s*\n\s+enabled: false/);
  });

  it("is fictional: no real host, credential or personal path", () => {
    const forbidden = [
      /https?:\/\//i,
      /\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b/,
      /gh[pousr]_[A-Za-z0-9]{20,}/,
      /sk-[A-Za-z0-9]{20,}/,
      /AKIA[0-9A-Z]{16}/,
      /\/Users\//,
      /\/home\//,
      /@[A-Za-z0-9-]+\.[a-z]{2,}/,
      /nightgauge\/nightgauge/,
    ];
    for (const file of all) {
      const text = fs.readFileSync(file, "utf8");
      for (const pattern of forbidden) {
        expect(text, `${file} matches ${pattern}`).not.toMatch(pattern);
      }
    }
  });
});
