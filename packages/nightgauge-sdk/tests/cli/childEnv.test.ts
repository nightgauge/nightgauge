import { describe, it, expect } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { curateChildEnv, isChildEnvAllowed } from "../../src/cli/adapters/childEnv.js";

const ADAPTERS_DIR = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../src/cli/adapters"
);

describe("curateChildEnv (least-privilege #4094)", () => {
  it("strips secrets no adapter needs", () => {
    const curated = curateChildEnv({
      PATH: "/usr/bin",
      AWS_SECRET_ACCESS_KEY: "akia-secret",
      STRIPE_SECRET_KEY: "sk_live_x",
      DATABASE_URL: "postgres://u:p@h/db",
      MY_RANDOM_TOKEN: "nope",
    });
    expect(curated.PATH).toBe("/usr/bin");
    expect(curated.AWS_SECRET_ACCESS_KEY).toBeUndefined();
    expect(curated.STRIPE_SECRET_KEY).toBeUndefined();
    expect(curated.DATABASE_URL).toBeUndefined();
    expect(curated.MY_RANDOM_TOKEN).toBeUndefined();
  });

  it("keeps provider auth, system essentials, and project/cli prefixes", () => {
    const curated = curateChildEnv({
      HOME: "/home/x",
      PATH: "/usr/bin",
      ANTHROPIC_API_KEY: "sk-ant",
      ANTHROPIC_BASE_URL: "https://api.example.com",
      COPILOT_GITHUB_TOKEN: "ghp_x",
      GITHUB_TOKEN: "ghp_y",
      OPENAI_API_KEY: "sk-oai",
      GEMINI_API_KEY: "g-x",
      CODEX_HOME: "/home/x/.codex",
      GH_HOST: "github.example.com",
      NIGHTGAUGE_CODEX_MODEL: "gpt-5",
      CLAUDE_CODE_SOMETHING: "1",
    });
    for (const k of [
      "HOME",
      "PATH",
      "ANTHROPIC_API_KEY",
      "ANTHROPIC_BASE_URL",
      "COPILOT_GITHUB_TOKEN",
      "GITHUB_TOKEN",
      "OPENAI_API_KEY",
      "GEMINI_API_KEY",
      "CODEX_HOME",
      "GH_HOST",
      "NIGHTGAUGE_CODEX_MODEL",
      "CLAUDE_CODE_SOMETHING",
    ]) {
      expect(curated[k], `${k} should survive curation`).toBeDefined();
    }
  });

  it("honors per-call extraAllow and does not mutate the source env", () => {
    const src: NodeJS.ProcessEnv = { PATH: "/usr/bin", SPECIAL_TOKEN: "x" };
    const curated = curateChildEnv(src, ["SPECIAL_TOKEN"]);
    expect(curated.SPECIAL_TOKEN).toBe("x");
    // Without extraAllow it is stripped, and the source is never mutated.
    expect(curateChildEnv(src).SPECIAL_TOKEN).toBeUndefined();
    expect(src.SPECIAL_TOKEN).toBe("x");
  });

  it("drift guard: every env var an adapter reads is allowlisted", () => {
    const files = fs
      .readdirSync(ADAPTERS_DIR)
      .filter((f) => f.endsWith(".ts") && !f.endsWith(".test.ts") && f !== "childEnv.ts");

    const reads = new Set<string>();
    const dotted = /process\.env\.([A-Za-z_][A-Za-z0-9_]*)/g;
    const bracket = /process\.env\[\s*["'`]([A-Za-z_][A-Za-z0-9_]*)["'`]\s*\]/g;
    for (const f of files) {
      const src = fs.readFileSync(path.join(ADAPTERS_DIR, f), "utf-8");
      for (const m of src.matchAll(dotted)) reads.add(m[1]);
      for (const m of src.matchAll(bracket)) reads.add(m[1]);
    }
    // CopilotCliAdapter reads its auth cascade via a runtime array
    // (process.env[envVar]), which a literal scan can't see — assert its
    // members explicitly so the guard still covers them.
    for (const v of ["GH_TOKEN", "GITHUB_TOKEN", "COPILOT_GITHUB_TOKEN"]) reads.add(v);

    const notAllowed = [...reads].filter((name) => !isChildEnvAllowed(name));
    expect(
      notAllowed,
      `These vars are read by an adapter but stripped by curateChildEnv — add them ` +
        `to childEnv.ts PROVIDER_ALLOW (or a prefix) or curation will break the adapter:\n` +
        notAllowed.join("\n")
    ).toEqual([]);

    // Sanity: the scan actually found the known reads (guards against a broken regex).
    expect(reads.has("ANTHROPIC_API_KEY")).toBe(true);
    expect(reads.has("GEMINI_API_KEY")).toBe(true);
    expect(reads.size).toBeGreaterThan(5);
  });
});
