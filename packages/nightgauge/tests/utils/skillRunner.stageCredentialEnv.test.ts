/**
 * skillRunner.stageCredentialEnv.test.ts (#3406)
 *
 * A stage the extension spawns through the real dispatch path
 * (`runStageSkillHeadless`), at the `child_process.spawn` seam: it keeps the
 * toolchain, proxy and TLS variables and its own adapter's credential, gets no
 * other provider's key or unrelated credential, and an operator opt-in through
 * NIGHTGAUGE_STAGE_ENV_ALLOW passes the named variable.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import * as fs from "fs";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(() => ({ show: vi.fn(), sendText: vi.fn() })),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  extensions: {
    getExtension: vi.fn(() => null),
  },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
  writeFileSync: vi.fn(),
  mkdirSync: vi.fn(),
}));

vi.mock("child_process", async () => {
  const { isSkillRenderCall, skillRenderStdout } = await import("../helpers/skillRender");
  return {
    spawn: vi.fn(),
    execFileSync: vi.fn((_cmd: string, args: string[]) =>
      isSkillRenderCall(args) ? skillRenderStdout(args) : ""
    ),
    execFile: vi.fn(
      (
        _cmd: string,
        _args: string[],
        _opts: unknown,
        cb: (e: Error | null, s: string, t: string) => void
      ) => {
        cb(new Error("no children"), "", "");
      }
    ),
  };
});

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: false,
  })),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("../../src/utils/nightgaugeConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/nightgaugeConfig");
  return {
    ...actual,
    getAuthProvider: vi.fn(() => "max"),
    getPerformanceMode: vi.fn(() => "elevated"),
  };
});

vi.mock("../../src/services/RepositoryContextLoader", () => ({
  RepositoryContextLoader: {
    getInstance: vi.fn(() => ({
      getCurrentRepository: vi.fn().mockReturnValue(null),
      getWorkingDirectory: vi.fn().mockReturnValue("/test/workspace"),
    })),
  },
}));

import { runStageSkillHeadless, killAllActiveProcesses } from "../../src/utils/skillRunner";
import { createMockChildProcess, type MockChildProcess } from "../mocks/child-process";

const MOCK_SKILL_CONTENT = `---
name: test-skill
allowed-tools: Read Write Edit
---
# Test Skill
`;

function lastSpawnEnv(): Record<string, string> {
  const calls = vi.mocked(spawn).mock.calls;
  expect(calls.length, "spawn was not called").toBeGreaterThan(0);
  const opts = calls[calls.length - 1][2] as { env?: Record<string, string> };
  return opts?.env ?? {};
}

/** Credential-shaped names (`*_API_KEY`, `*_TOKEN`, `*_SECRET`) in a spawn env. */
function credentialShaped(env: Record<string, string>): string[] {
  return Object.keys(env)
    .filter((k) => /_(API_KEY|TOKEN|SECRET)$/.test(k))
    .sort();
}

const originalEnv = process.env;
let proc: MockChildProcess;

beforeEach(() => {
  vi.clearAllMocks();
  process.env = {
    ...originalEnv,
    VITEST: "true",
    PATH: "/usr/local/bin:/usr/bin:/bin",
    HOME: "/home/op",
    HTTPS_PROXY: "http://proxy:3128",
    https_proxy: "http://proxy:3128",
    NO_PROXY: "localhost",
    SSL_CERT_FILE: "/etc/ca.pem",
    NODE_EXTRA_CA_CERTS: "/etc/ca.pem",
    REQUESTS_CA_BUNDLE: "/etc/ca.pem",
    MAX_THINKING_TOKENS: "1024",
    GH_TOKEN: "forge",
    ANTHROPIC_API_KEY: "sk-ant-operator",
    CLAUDE_CODE_OAUTH_TOKEN: "oauth-operator",
    OPENAI_API_KEY: "sk-openai-operator",
    XAI_API_KEY: "xai-operator",
    MISTRAL_API_KEY: "mistral-operator",
    COPILOT_GITHUB_TOKEN: "copilot-operator",
    STRIPE_SECRET: "stripe-operator",
    SLACK_BOT_TOKEN: "slack-operator",
    NPM_TOKEN: "npm-operator",
    NIGHTGAUGE_JIRA_TOKEN: "jira-operator",
  };
  // Not inherited from the test runner's own environment.
  delete process.env.NIGHTGAUGE_STAGE_ENV_ALLOW;
  delete process.env.GITHUB_TOKEN;
  vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
    const filePath = String(p);
    return (
      filePath.includes("SKILL.md") ||
      filePath.includes("skills/") ||
      filePath.includes("nightgauge-sdk/dist/cli/index.js")
    );
  });
  vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);
  proc = createMockChildProcess();
  vi.mocked(spawn).mockReturnValue(proc);
});

afterEach(() => {
  killAllActiveProcesses();
  process.env = { ...originalEnv, VITEST: "true" };
  vi.restoreAllMocks();
});

const TOOLCHAIN = {
  PATH: expect.stringContaining("/usr/bin"),
  HOME: "/home/op",
  HTTPS_PROXY: "http://proxy:3128",
  https_proxy: "http://proxy:3128",
  NO_PROXY: "localhost",
  SSL_CERT_FILE: "/etc/ca.pem",
  NODE_EXTRA_CA_CERTS: "/etc/ca.pem",
  REQUESTS_CA_BUNDLE: "/etc/ca.pem",
  MAX_THINKING_TOKENS: "1024",
};

describe("stage spawn env credential allowlist (#3406)", () => {
  it("a claude stage gets its own credential and the toolchain, and no other credential", () => {
    runStageSkillHeadless("feature-dev", 42, {});
    const env = lastSpawnEnv();
    expect(env).toMatchObject(TOOLCHAIN);
    expect(credentialShaped(env)).toEqual([
      "ANTHROPIC_API_KEY",
      "CLAUDE_CODE_OAUTH_TOKEN",
      "GH_TOKEN",
    ]);
    expect(env.ANTHROPIC_API_KEY).toBe("sk-ant-operator");
    expect(env.CLAUDE_CODE_OAUTH_TOKEN).toBe("oauth-operator");
    expect(env.GH_TOKEN).toBe("forge");
  });

  it("a codex stage gets the OpenAI key, not Anthropic's", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV = "codex";
    runStageSkillHeadless("feature-dev", 42, {});
    const env = lastSpawnEnv();
    expect(env.NIGHTGAUGE_ADAPTER).toBe("codex");
    expect(env).toMatchObject(TOOLCHAIN);
    expect(env.OPENAI_API_KEY).toBe("sk-openai-operator");
    for (const n of [
      "ANTHROPIC_API_KEY",
      "CLAUDE_CODE_OAUTH_TOKEN",
      "XAI_API_KEY",
      "MISTRAL_API_KEY",
      "COPILOT_GITHUB_TOKEN",
      "STRIPE_SECRET",
      "SLACK_BOT_TOKEN",
      "NPM_TOKEN",
      "NIGHTGAUGE_JIRA_TOKEN",
    ]) {
      expect(env, n).not.toHaveProperty(n);
    }
  });

  it("passes a credential the operator opts in with NIGHTGAUGE_STAGE_ENV_ALLOW", () => {
    process.env.NIGHTGAUGE_STAGE_ENV_ALLOW = "NPM_TOKEN, SLACK_BOT_TOKEN";
    runStageSkillHeadless("feature-dev", 42, {});
    const env = lastSpawnEnv();
    expect(env.NPM_TOKEN).toBe("npm-operator");
    expect(env.SLACK_BOT_TOKEN).toBe("slack-operator");
    expect(env).not.toHaveProperty("OPENAI_API_KEY");
    expect(env).not.toHaveProperty("STRIPE_SECRET");
  });
});
