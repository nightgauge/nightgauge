/**
 * stageCredentialEnv.test.ts (#3406)
 *
 * The extension's stage credential allowlist against the Go rule it twins
 * (StageWithholdsCredentialEnv, internal/execution/adapters/stage_env.go):
 * the decisions the Go rule recorded over a corpus of names, for every
 * adapter, with and without an operator opt-in list, must be this rule's too.
 * The golden is regenerated from Go, so a change on either side fails here
 * until both agree.
 */
import { describe, it, expect } from "vitest";
import * as fs from "fs";
import * as path from "node:path";
import {
  credentialEnvName,
  stageEnvAllow,
  stageWithholdsCredentialEnv,
  withoutWithheldStageCredentials,
} from "../../src/utils/stageCredentialEnv";
import { stageInheritedEnv, stageWithheldCredentialEnvNulls } from "../../src/utils/skillRunner";

interface DecisionsGolden {
  corpus: string[];
  cases: { adapter: string; extra_allow: string[]; kept: string[] }[];
}

const golden = JSON.parse(
  fs.readFileSync(
    path.resolve(
      __dirname,
      "../../../../internal/execution/adapters/testdata/stage_env_decisions.json"
    ),
    "utf-8"
  )
) as DecisionsGolden;

describe("stage credential allowlist parity with the Go rule", () => {
  it("covers every adapter the Go rule declares, opt-in and not", () => {
    expect(golden.cases.length).toBeGreaterThanOrEqual(16);
    expect(golden.corpus.length).toBeGreaterThan(100);
  });

  for (const c of golden.cases) {
    const label = `${c.adapter}${c.extra_allow.length ? " with opt-in" : ""}`;
    it(`decides every corpus name as Go does: ${label}`, () => {
      const kept = golden.corpus.filter(
        (k) => !stageWithholdsCredentialEnv(c.adapter, k, c.extra_allow)
      );
      expect(kept).toEqual(c.kept);
    });
  }
});

describe("credentialEnvName", () => {
  it("marks credentials by name segment and leaves token budgets alone", () => {
    for (const n of ["SLACK_BOT_TOKEN", "STRIPE_SECRET_KEY", "DB_PASSWORD", "SENTRY_DSN"]) {
      expect(credentialEnvName(n), n).toBe(true);
    }
    for (const n of ["MAX_THINKING_TOKENS", "SSH_AUTH_SOCK", "PATH", "KEYCHAIN_PATH"]) {
      expect(credentialEnvName(n), n).toBe(false);
    }
  });
});

describe("stageEnvAllow", () => {
  it("reads the operator's comma-separated list, trimmed", () => {
    expect(
      stageEnvAllow({ NIGHTGAUGE_STAGE_ENV_ALLOW: " NPM_TOKEN , ,IB_VERIFY_PASSWORD" })
    ).toEqual(["NPM_TOKEN", "IB_VERIFY_PASSWORD"]);
    expect(stageEnvAllow({})).toEqual([]);
  });
});

const host: NodeJS.ProcessEnv = {
  PATH: "/usr/bin",
  HOME: "/home/op",
  HTTPS_PROXY: "http://proxy:3128",
  no_proxy: "localhost",
  SSL_CERT_FILE: "/etc/ca.pem",
  NODE_EXTRA_CA_CERTS: "/etc/ca.pem",
  GH_TOKEN: "forge",
  ANTHROPIC_API_KEY: "ant",
  CLAUDE_CODE_OAUTH_TOKEN: "oauth",
  OPENAI_API_KEY: "oai",
  XAI_API_KEY: "xai",
  STRIPE_SECRET_KEY: "stripe",
  NPM_TOKEN: "npm",
  CLAUDECODE: "1",
};

describe("withoutWithheldStageCredentials", () => {
  it("returns a copy and leaves the input alone", () => {
    const env = { ...host };
    const out = withoutWithheldStageCredentials("claude", env, {});
    expect(env.OPENAI_API_KEY).toBe("oai");
    expect(out).not.toHaveProperty("OPENAI_API_KEY");
  });
});

describe("stageInheritedEnv (stage, resume and interactive spawns)", () => {
  it("keeps the toolchain and the adapter's own credential, drops the rest", () => {
    const env = stageInheritedEnv("claude", host);
    expect(env).toMatchObject({
      PATH: "/usr/bin",
      HOME: "/home/op",
      HTTPS_PROXY: "http://proxy:3128",
      no_proxy: "localhost",
      SSL_CERT_FILE: "/etc/ca.pem",
      NODE_EXTRA_CA_CERTS: "/etc/ca.pem",
      GH_TOKEN: "forge",
      ANTHROPIC_API_KEY: "ant",
      CLAUDE_CODE_OAUTH_TOKEN: "oauth",
    });
    for (const n of ["OPENAI_API_KEY", "XAI_API_KEY", "STRIPE_SECRET_KEY", "NPM_TOKEN"]) {
      expect(env, n).not.toHaveProperty(n);
    }
    // The launching session's residue is still removed (#2848).
    expect(env).not.toHaveProperty("CLAUDECODE");
  });

  it("passes a name the operator opted in through NIGHTGAUGE_STAGE_ENV_ALLOW", () => {
    const env = stageInheritedEnv("claude", { ...host, NIGHTGAUGE_STAGE_ENV_ALLOW: "NPM_TOKEN" });
    expect(env.NPM_TOKEN).toBe("npm");
    expect(env).not.toHaveProperty("STRIPE_SECRET_KEY");
  });
});

describe("stageWithheldCredentialEnvNulls (codex interactive terminal)", () => {
  it("names every credential the codex harness does not declare with null", () => {
    const nulls = stageWithheldCredentialEnvNulls("codex", host);
    expect(nulls).toEqual({
      ANTHROPIC_API_KEY: null,
      CLAUDE_CODE_OAUTH_TOKEN: null,
      XAI_API_KEY: null,
      STRIPE_SECRET_KEY: null,
      NPM_TOKEN: null,
    });
  });
});
