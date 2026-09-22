import { describe, it, expect } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import {
  OPENCODE_DISABLE_FLAGS,
  curateChildEnv,
  curateOpenCodeChildEnv,
  isChildEnvAllowed,
  isOpenCodeChildEnvAllowed,
  isOpenCodeRunEnvAccepted,
  isOpenCodeRunEnvName,
} from "../../src/cli/adapters/childEnv.js";
import {
  OPENCODE_CATALOG_ENV,
  OPENCODE_PLATFORM_PROVIDERS,
} from "../../src/cli/adapters/opencodeCatalog.js";

const ADAPTERS_DIR = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../src/cli/adapters"
);
const GO_ADAPTERS_DIR = path.resolve(ADAPTERS_DIR, "../../../../../internal/execution/adapters");

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

// ---------------------------------------------------------------------------
// OpenCode (#1637, ADR-022 § 8, § 17)
// ---------------------------------------------------------------------------

describe("curateOpenCodeChildEnv (#1637)", () => {
  const RUN_ENV = {
    XDG_CONFIG_HOME: "/runs/r1/config",
    XDG_DATA_HOME: "/runs/r1/data",
    XDG_CACHE_HOME: "/runs/r1/cache",
    XDG_STATE_HOME: "/runs/r1/state",
    OPENCODE_DISABLE_SHARE: "1",
    OPENCODE_CONFIG_CONTENT: '{"share":"disabled"}',
  };
  const PARENT: NodeJS.ProcessEnv = {
    PATH: "/usr/bin",
    HOME: "/home/fixture",
    OPENAI_API_KEY: "sk-oai",
    ANTHROPIC_API_KEY: "sk-ant",
    ANTHROPIC_BASE_URL: "http://127.0.0.1:9",
    XAI_API_KEY: "xai-x",
    LMSTUDIO_API_KEY: "lm-x",
    OPENCODE_PERMISSION: '{"*":"allow"}',
    OPENCODE_AUTO_SHARE: "1",
    OPENCODE_CONFIG: "/operator/opencode.json",
    OPENCODE_CONFIG_DIR: "/operator/opencode",
    OPENCODE_CONFIG_CONTENT: '{"share":"auto"}',
    OPENCODE_SERVER_PASSWORD: "inherited",
    OPENCODE_AUTH_CONTENT: "{}",
    XDG_CONFIG_HOME: "/home/fixture/.config",
    CLAUDE_CODE_OAUTH_TOKEN: "oauth",
    GH_TOKEN: "ghs_x",
    GITHUB_TOKEN: "ghs_y",
    GH_HOST: "github.com",
    NIGHTGAUGE_STAGE: "feature-dev",
  };

  it("gives an lmstudio run no hosted provider key and no operator OpenCode variable", () => {
    const env = curateOpenCodeChildEnv(PARENT, "lmstudio/qwen/qwen3.8-27b", RUN_ENV);
    for (const name of [
      "OPENAI_API_KEY",
      "ANTHROPIC_API_KEY",
      "ANTHROPIC_BASE_URL",
      "XAI_API_KEY",
      "OPENCODE_PERMISSION",
      "OPENCODE_AUTO_SHARE",
      "OPENCODE_CONFIG",
      "OPENCODE_CONFIG_DIR",
      "OPENCODE_SERVER_PASSWORD",
      "OPENCODE_AUTH_CONTENT",
      "CLAUDE_CODE_OAUTH_TOKEN",
    ]) {
      expect(env[name], name).toBeUndefined();
    }
    // The run's own isolation replaces the operator's XDG directories.
    expect(env.XDG_CONFIG_HOME).toBe(RUN_ENV.XDG_CONFIG_HOME);
    expect(env.OPENCODE_CONFIG_CONTENT).toBe(RUN_ENV.OPENCODE_CONFIG_CONTENT);
    // lmstudio's own catalog variable, the forge and the pipeline's config stay.
    for (const name of [
      "LMSTUDIO_API_KEY",
      "GH_TOKEN",
      "GITHUB_TOKEN",
      "GH_HOST",
      "PATH",
      "HOME",
    ]) {
      expect(env[name], name).toBe(PARENT[name]);
    }
    expect(env.NIGHTGAUGE_STAGE).toBe("feature-dev");
  });

  it("gives an anthropic run ANTHROPIC_API_KEY and no other provider's key", () => {
    const env = curateOpenCodeChildEnv(PARENT, "anthropic/claude-sonnet-5", RUN_ENV);
    expect(env.ANTHROPIC_API_KEY).toBe("sk-ant");
    for (const name of [
      "OPENAI_API_KEY",
      "XAI_API_KEY",
      "LMSTUDIO_API_KEY",
      "ANTHROPIC_BASE_URL",
    ]) {
      expect(env[name], name).toBeUndefined();
    }
  });

  it("never applies a run variable outside the run's own set", () => {
    const env = curateOpenCodeChildEnv({}, "lmstudio/x", { OPENCODE_PERMISSION: "{}", ...RUN_ENV });
    expect(env.OPENCODE_PERMISSION).toBeUndefined();
  });

  it("drift guard: every env var OpenCodeAdapter.ts reads reaches the child it is read for", () => {
    const src = fs.readFileSync(path.join(ADAPTERS_DIR, "OpenCodeAdapter.ts"), "utf-8");
    const reads = new Set<string>();
    for (const m of src.matchAll(/\benv\.([A-Z_][A-Z0-9_]*)/g)) reads.add(m[1]);
    for (const m of src.matchAll(/\benv\[\s*["'`]([A-Z_][A-Z0-9_]*)["'`]\s*\]/g)) reads.add(m[1]);
    // The catalog variables, read through the dispatched provider's entry,
    // except OpenCode's own OPENCODE_API_KEY: no OPENCODE_* variable is ever
    // inherited (the Go adapter withholds them all), so the adapter never
    // requires it.
    for (const vars of Object.values(OPENCODE_CATALOG_ENV)) {
      for (const v of vars) if (!v.startsWith("OPENCODE_")) reads.add(v);
    }
    expect(isOpenCodeChildEnvAllowed("OPENCODE_API_KEY", "opencode/m")).toBe(false);
    expect(reads.has("ANTHROPIC_API_KEY")).toBe(true);
    expect(reads.has("NIGHTGAUGE_MODEL")).toBe(true);

    const notForwarded = [...reads].filter((name) => {
      const provider = Object.entries(OPENCODE_CATALOG_ENV).find(([, vars]) => vars.includes(name));
      const model = provider ? `${provider[0]}/m` : "lmstudio/m";
      return !isOpenCodeChildEnvAllowed(name, model);
    });
    expect(notForwarded).toEqual([]);
  });

  // #1804: two independent reviews of this fix round found that a parity test
  // reading opencodeplugin.go's Env* CONSTANT DEFINITIONS is structurally
  // blind to a variable set any other way — OPENCODE_DISABLE_PROJECT_CONFIG is
  // a bare string literal at InstallNightgaugePlugin's call site
  // (opencode.go), not one of those constants, and HOME is set by
  // OpenCodeIsolationEnv (opencode_isolation.go) — and both slipped through an
  // earlier version of this exact test that only checked definitions, the
  // same blind spot #1969 already named. This test instead reads the KEY SET
  // `nightgauge opencode config --json` actually prints, pinned by
  // TestOpenCodeConfigVerbEnvKeysMatchGoldenFixture (cmd/nightgauge/opencode_test.go)
  // against the same fixture, so a newly-set variable fails a Go test first
  // and this one second — never neither.
  it("parity: every key nightgauge opencode config --json prints is accepted by the TS run-env allowlist", () => {
    const golden = JSON.parse(
      fs.readFileSync(
        path.join(
          GO_ADAPTERS_DIR,
          "../../execution/testdata/opencode_config_verb_env_keys.golden.json"
        ),
        "utf-8"
      )
    ) as { keys: string[] };
    expect(golden.keys.length).toBeGreaterThan(15);
    // Sanity: the fixture is what this test's own doc comment claims —
    // guards against a typo'd path silently reading an empty/stale file.
    expect(golden.keys).toContain("HOME");
    expect(golden.keys).toContain("OPENCODE_DISABLE_PROJECT_CONFIG");
    expect(golden.keys).toContain("NIGHTGAUGE_OPENCODE_PLUGIN_NONCE");

    const notAccepted = golden.keys.filter((k) => !isOpenCodeRunEnvAccepted(k));
    expect(
      notAccepted,
      `these keys a real nightgauge opencode config --json prints are refused by ` +
        `checkRunConfig — add them to childEnv.ts's OPENCODE_RUN_ENV_NAMES:\n` +
        notAccepted.join("\n")
    ).toEqual([]);
    // Every key in this fixture is meant to reach the child (none of them is
    // the operator-risk name), so isOpenCodeRunEnvName (forwarded), not just
    // isOpenCodeRunEnvAccepted, must be true for each.
    const notForwarded = golden.keys.filter((k) => !isOpenCodeRunEnvName(k));
    expect(notForwarded).toEqual([]);
  });

  // The operator-risk name is the one key checkRunConfig accepts but
  // curateOpenCodeChildEnv must never forward — accepting it prevents a
  // fail-closed CONFIG_INVALID on a machine where an operator's OpenCode
  // install directory happens to be at risk (a condition the operator neither
  // set nor controls), while withholding it mirrors the Go adapter's own
  // BuildCommand, which deletes opencodeplugin.EnvOperatorInstallRisk from
  // the child's env right before returning it (opencode.go, pinned by
  // TestOpenCodeBuildCommandWithholdsOperatorInstallRiskFromTheChild) —
  // #1802's child-env leak is already closed there; only the config verb's
  // printed env still carries the name, since the verb prints RunRoot.Env
  // directly rather than BuildCommand's output.
  it("accepts NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK but never forwards it", () => {
    const name = "NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK";
    expect(isOpenCodeRunEnvAccepted(name)).toBe(true);
    expect(isOpenCodeRunEnvName(name)).toBe(false);
  });

  it("OpenCode's catalog, platform providers and switches are the Go adapter's", () => {
    const catalog = fs.readFileSync(path.join(GO_ADAPTERS_DIR, "opencode_catalog_env.go"), "utf-8");
    const goCatalog: Record<string, string[]> = {};
    for (const m of catalog.matchAll(/^\t"([^"]+)":\s+\{([^}]*)\},$/gm)) {
      goCatalog[m[1]] = [...m[2].matchAll(/"([^"]+)"/g)].map((v) => v[1]);
    }
    expect(Object.keys(goCatalog).length).toBeGreaterThan(100);
    expect(OPENCODE_CATALOG_ENV).toEqual(goCatalog);

    const isolation = fs.readFileSync(path.join(GO_ADAPTERS_DIR, "opencode_isolation.go"), "utf-8");
    const block = (name: string) => {
      const start = isolation.indexOf(`var ${name} = []string{`);
      const body = isolation.slice(start, isolation.indexOf("\n}\n", start));
      return [...body.matchAll(/^\t"([^"]+)",/gm)].map((m) => m[1]);
    };
    expect([...OPENCODE_PLATFORM_PROVIDERS]).toEqual(block("openCodePlatformProviders"));
    expect([...OPENCODE_DISABLE_FLAGS]).toEqual(block("openCodeDisableFlags"));
  });
});
