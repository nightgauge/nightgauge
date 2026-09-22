/**
 * OpenCodeAdapter (#1637): registration, the argv the Go adapter emits, the
 * inert-until-#1648 run config, the fail-closed version floor, the model and
 * credential checks, the child environment a real spawn gets, the redaction
 * of stderr and of the model's text, and killing the run's process group on
 * abort and when this process exits or is interrupted.
 */
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { spawn as spawnProcess } from "node:child_process";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  OPENCODE_FORBIDDEN_FLAGS,
  OPENCODE_MIN_KNOWN_VERSION,
  OpenCodeAdapter,
  buildOpenCodeArgv,
  type OpenCodeRunConfig,
  type OpenCodeRunConfigProvider,
} from "../../src/cli/adapters/OpenCodeAdapter.js";
import { defaultRegistry, isAgenticAdapter } from "../../src/cli/adapters/AdapterRegistry.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";
import type { PreflightCommandRunner } from "../../src/cli/codexPreflight.js";
import type { SDKMessage } from "../../src/orchestrator/StageExecutor.js";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const TESTDATA = join(REPO_ROOT, "internal/execution/testdata");

const LOCAL_MODEL = "lmstudio/qwen/qwen3.8-27b";

const tmpDirs: string[] = [];
function tmp(prefix: string): string {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  tmpDirs.push(dir);
  return dir;
}
afterEach(() => {
  for (const d of tmpDirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

/**
 * A run config the way `nightgauge opencode config` would build one, under
 * `root` — every key a real invocation of the verb prints (#1804: HOME,
 * OPENCODE_DISABLE_PROJECT_CONFIG and the plugin/handshake trio are real
 * verb output, not test-only additions; a fixture missing them let the SDK's
 * allowlist gap in each pass unnoticed).
 */
function runConfig(root: string): OpenCodeRunConfig {
  const env: Record<string, string> = {
    HOME: join(root, "home"),
    XDG_CONFIG_HOME: join(root, "config"),
    XDG_DATA_HOME: join(root, "data"),
    XDG_CACHE_HOME: join(root, "cache"),
    XDG_STATE_HOME: join(root, "state"),
    GH_CONFIG_DIR: join(root, "gh"),
    OPENCODE_DISABLE_PROJECT_CONFIG: "1",
    NIGHTGAUGE_OPENCODE_PLUGIN_PATH: join(
      root,
      "config",
      "opencode",
      "nightgauge-plugin",
      "nightgauge.js"
    ),
    NIGHTGAUGE_OPENCODE_PLUGIN_NONCE: "fixture-nonce",
    NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL: join(root, ".opencode-plugin-fixture.json"),
  };
  for (const flag of [
    "OPENCODE_DISABLE_MODELS_FETCH",
    "OPENCODE_DISABLE_AUTOUPDATE",
    "OPENCODE_DISABLE_LSP_DOWNLOAD",
    "OPENCODE_DISABLE_DEFAULT_PLUGINS",
    "OPENCODE_DISABLE_SHARE",
    "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT",
    "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS",
    "OPENCODE_DISABLE_EXTERNAL_SKILLS",
  ]) {
    env[flag] = "1";
  }
  return {
    configContent: JSON.stringify({ model: LOCAL_MODEL, share: "disabled" }),
    env,
    runDir: root,
  };
}

function providerFor(config: OpenCodeRunConfig): OpenCodeRunConfigProvider {
  return async () => config;
}

/**
 * A stand-in `opencode` on PATH: `run` records its argv, stdin and environment
 * under `dir`, then runs `runBody`; `export` and `db` answer the post-run
 * roll-up from the research sample's session.
 */
function writeStub(dir: string, runBody: string): string {
  const bin = join(dir, "bin");
  mkdirSync(bin, { recursive: true });
  const exportJson = JSON.stringify({
    info: {
      id: "ses_fixture0000000000000000001",
      cost: 0,
      tokens: { input: 3089, output: 14, reasoning: 0, cache: { read: 0, write: 0 } },
    },
    messages: [
      { info: { role: "assistant", providerID: "lmstudio", modelID: "qwen/qwen3.8-27b" } },
    ],
  });
  const script = `#!/bin/sh
case "$1" in
run)
  printf '%s\\n' "$@" > '${dir}/argv'
  env > '${dir}/env'
  cat > '${dir}/stdin'
${runBody}
  ;;
export)
  pwd >> '${dir}/helper-cwd'
  env > '${dir}/helper-env'
  printf '%s' '${exportJson}'
  ;;
db) printf '[]' ;;
--version) echo 1.18.30 ;;
esac
`;
  const path = join(bin, "opencode");
  writeFileSync(path, script);
  chmodSync(path, 0o755);
  return bin;
}

async function drain(gen: AsyncGenerator<SDKMessage>): Promise<SDKMessage[]> {
  const out: SDKMessage[] = [];
  for await (const m of gen) out.push(m);
  return out;
}

function readEnvFile(path: string): Record<string, string> {
  const env: Record<string, string> = {};
  for (const line of readFileSync(path, "utf-8").split("\n")) {
    const eq = line.indexOf("=");
    if (eq > 0) env[line.slice(0, eq)] = line.slice(eq + 1);
  }
  return env;
}

// ---------------------------------------------------------------------------

describe("OpenCodeAdapter registration (#1637)", () => {
  it("is registered, agentic, sdk-fanout and needs no up-front API key", () => {
    const adapter = defaultRegistry.get("opencode");
    expect(adapter).toBeInstanceOf(OpenCodeAdapter);
    expect(adapter.agentic).toBe(true);
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
    expect(adapter.requiresDirectApiKey()).toBe(false);
    expect(isAgenticAdapter("opencode")).toBe(true);
  });
});

/** The option name a token spells, the way opencode's argument parser reads it (flag_contract_test.go). */
function forbiddenFlagIn(arg: string): string | undefined {
  let name = arg.replace(/^-+/, "");
  if (name === arg || name === "") return undefined;
  name = name.split("=")[0].split(".")[0].replace(/_/g, "-");
  const kebab = name.replace(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase();
  return OPENCODE_FORBIDDEN_FLAGS.find((f) => {
    const option = f.replace(/^-+/, "");
    return kebab === option || name.toLowerCase() === option;
  });
}

describe("OpenCodeAdapter argv (#1637)", () => {
  it("equals the Go adapter's argv golden for the same inputs", () => {
    const goTest = readFileSync(
      join(REPO_ROOT, "internal/execution/adapters/opencode_test.go"),
      "utf-8"
    );
    const body = goTest.slice(goTest.indexOf("func TestOpenCodeBuildCommandArgv("));
    const fn = body.slice(0, body.indexOf("\n}\n"));
    const model = /Model:\s+"([^"]+)"/.exec(fn)?.[1];
    const worktree = /WorktreeDir:\s+"([^"]+)"/.exec(fn)?.[1];
    const wantBlock = /want := \[\]string\{([\s\S]*?)\n\t\}/.exec(fn)?.[1];
    expect(model && worktree && wantBlock, "the Go golden moved").toBeTruthy();
    const want = [...wantBlock!.matchAll(/"([^"]*)"/g)].map((m) => m[1]);
    expect(buildOpenCodeArgv(model!, worktree!)).toEqual(want);
  });

  it("never carries a forbidden flag in any spelling", () => {
    for (const model of [LOCAL_MODEL, "anthropic/claude-sonnet-5", "openai/gpt-5.5"]) {
      const argv = buildOpenCodeArgv(model, "/work/nightgauge-issue-1637");
      expect(argv.map(forbiddenFlagIn).filter(Boolean)).toEqual([]);
    }
    // The guard itself catches the spellings opencode accepts.
    for (const spelled of [
      "--auto",
      "--yolo=true",
      "--auto.x",
      "--dangerouslySkipPermissions",
      "--continue",
    ]) {
      expect(forbiddenFlagIn(spelled), spelled).toBeDefined();
    }
  });
});

describe("OpenCodeAdapter without a run config provider (#1637)", () => {
  it("createQueryFunction fails CONFIG_INVALID naming #1648 and spawns nothing", async () => {
    const spawn = vi.fn();
    const adapter = new OpenCodeAdapter({
      env: { PATH: "/usr/bin" },
      model: LOCAL_MODEL,
      spawn: spawn as never,
    });
    const err = await adapter.createQueryFunction({ cwd: tmp("oc-wt-") }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("CONFIG_INVALID");
    expect((err as AdapterError).message).toContain("#1648");
    expect(spawn).not.toHaveBeenCalled();
  });

  it("a run config provider that returns nothing usable is refused too", async () => {
    const spawn = vi.fn();
    const adapter = new OpenCodeAdapter({
      env: {},
      model: LOCAL_MODEL,
      spawn: spawn as never,
      runConfigProvider: async () => ({}) as OpenCodeRunConfig,
    });
    await expect(adapter.createQueryFunction({ cwd: tmp("oc-wt-") })).rejects.toMatchObject({
      category: "CONFIG_INVALID",
    });
    expect(spawn).not.toHaveBeenCalled();
  });

  it("a run config that sets a variable outside the run's own set is refused", async () => {
    const root = tmp("oc-run-");
    const config = runConfig(root);
    const adapter = new OpenCodeAdapter({
      env: {},
      model: LOCAL_MODEL,
      runConfigProvider: providerFor({
        ...config,
        env: { ...config.env, OPENCODE_PERMISSION: '{"*":"allow"}' },
      }),
    });
    await expect(adapter.createQueryFunction({ cwd: tmp("oc-wt-") })).rejects.toThrow(
      /OPENCODE_PERMISSION, which is not a run variable/
    );
  });

  // #1804: InstallNightgaugePlugin (internal/execution/adapters/opencode.go)
  // sets the plugin path, handshake and OPENCODE_DISABLE_PROJECT_CONFIG
  // variables on every run with a run identity, and OpenCodeIsolationEnv
  // sets the isolated HOME — all real `nightgauge opencode config --json`
  // output (verified against the built verb). runConfig() now carries every
  // one of them by default, so every test in this file that spawns through it
  // already exercises acceptance; this asserts it directly too.
  it("accepts a run config carrying the plugin/handshake variables, HOME and OPENCODE_DISABLE_PROJECT_CONFIG", async () => {
    const root = tmp("oc-run-");
    const adapter = new OpenCodeAdapter({
      env: {},
      model: LOCAL_MODEL,
      runConfigProvider: providerFor(runConfig(root)),
    });
    await expect(adapter.createQueryFunction({ cwd: tmp("oc-wt-") })).resolves.toBeInstanceOf(
      Function
    );
  });

  // A NIGHTGAUGE_OPENCODE_PLUGIN_PATH/_SENTINEL outside the run's own root is
  // refused rather than trusted: the plugin's init writes the sentinel file
  // verbatim at this path (fs.writeFileSync, plugin/nightgauge.js), so an
  // arbitrary absolute path would let a run config truncate a file anywhere
  // on disk the SDK process can write to.
  it("refuses a plugin path or sentinel outside the run's own root", async () => {
    for (const name of ["NIGHTGAUGE_OPENCODE_PLUGIN_PATH", "NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"]) {
      const root = tmp("oc-run-");
      const config = runConfig(root);
      const adapter = new OpenCodeAdapter({
        env: {},
        model: LOCAL_MODEL,
        runConfigProvider: providerFor({
          ...config,
          env: { ...config.env, [name]: "/etc/passwd" },
        }),
      });
      await expect(adapter.createQueryFunction({ cwd: tmp("oc-wt-") }), name).rejects.toThrow(
        new RegExp(`its ${name} is not an absolute path in the run's root`)
      );
    }
  });

  // #1802's child-env half (the Go adapter forwarding this operator directory
  // path to the opencode child) is already closed: BuildCommand deletes
  // opencodeplugin.EnvOperatorInstallRisk from the child's env right before
  // returning it (opencode.go, pinned by
  // TestOpenCodeBuildCommandWithholdsOperatorInstallRiskFromTheChild). Only
  // the config verb's *printed* env still carries it, because the verb prints
  // RunRoot.Env directly, not BuildCommand's output. checkRunConfig must
  // accept the name — refusing it would fail CONFIG_INVALID closed on every
  // machine where an operator's $HOME/.opencode happens to be unsatisfied,
  // which the operator neither set nor controls — while never letting it
  // reach the child, mirroring BuildCommand's own delete. The spawn-level
  // proof (the value is absent from the actual child env) lives in the
  // "OpenCodeAdapter spawn" describe block below.
  it("accepts NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK, an operator directory path, without forwarding it", async () => {
    const root = tmp("oc-run-");
    const config = runConfig(root);
    const adapter = new OpenCodeAdapter({
      env: {},
      model: LOCAL_MODEL,
      runConfigProvider: providerFor({
        ...config,
        env: {
          ...config.env,
          NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK: "/home/operator/.opencode",
        },
      }),
    });
    await expect(adapter.createQueryFunction({ cwd: tmp("oc-wt-") })).resolves.toBeInstanceOf(
      Function
    );
  });
});

describe("OpenCodeAdapter.validateAuth (#1637)", () => {
  const runner =
    (version: string): PreflightCommandRunner =>
    async (command, args) =>
      command === "opencode" && args[0] === "--version"
        ? { code: 0, stdout: `${version}\n`, stderr: "" }
        : { code: 1, stdout: "", stderr: "unexpected" };

  it("reads its floor from the compat manifest the Go doctor reads", () => {
    const manifest = JSON.parse(
      readFileSync(join(REPO_ROOT, "internal/adaptercompat/manifests/opencode.json"), "utf-8")
    ) as { min_version: string; floor_policy: string };
    expect(OPENCODE_MIN_KNOWN_VERSION).toBe(manifest.min_version);
    expect(manifest.floor_policy).toBe("fail_closed");
  });

  it("fails closed one patch below the floor, naming the floor", async () => {
    const [major, minor, patch] = OPENCODE_MIN_KNOWN_VERSION.split(".").map(Number);
    const below = `${major}.${minor}.${patch - 1}`;
    const adapter = new OpenCodeAdapter({ env: {} });
    const err = await adapter
      .validateAuth({ runner: runner(below), cwd: "/tmp" })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("VERSION_MISMATCH");
    expect((err as AdapterError).message).toContain(OPENCODE_MIN_KNOWN_VERSION);
    await expect(
      adapter.validateAuth({ runner: runner(OPENCODE_MIN_KNOWN_VERSION), cwd: "/tmp" })
    ).resolves.toBe("passed");
  });

  it("fails closed when no version can be read", async () => {
    await expect(
      new OpenCodeAdapter({ env: {} }).validateAuth({ runner: runner("opencode"), cwd: "/tmp" })
    ).rejects.toMatchObject({ category: "VERSION_MISMATCH" });
  });

  it("requires ANTHROPIC_API_KEY for anthropic/* and no key for local providers", async () => {
    await expect(
      new OpenCodeAdapter({ env: {}, model: "anthropic/claude-sonnet-5" }).validateAuth()
    ).rejects.toMatchObject({ category: "AUTH_MISSING" });
    await expect(
      new OpenCodeAdapter({
        env: { ANTHROPIC_API_KEY: "sk-ant-test" },
        model: "anthropic/claude-sonnet-5",
      }).validateAuth()
    ).resolves.toBe("passed");
    for (const model of [LOCAL_MODEL, "ollama/qwen3-coder:30b"]) {
      await expect(new OpenCodeAdapter({ env: {}, model }).validateAuth()).resolves.toBe("passed");
    }
  });

  it("requires a hosted provider's own key and refuses a platform provider", async () => {
    await expect(
      new OpenCodeAdapter({ env: {}, model: "openai/gpt-5.5" }).validateAuth()
    ).rejects.toMatchObject({ category: "AUTH_MISSING" });
    await expect(
      new OpenCodeAdapter({
        env: { GITHUB_TOKEN: "ghs_x" },
        model: "github-copilot/gpt-5",
      }).validateAuth()
    ).rejects.toMatchObject({ category: "CONFIG_INVALID" });
  });
});

describe("OpenCodeAdapter model check (#1637)", () => {
  it.each(["--auto", "a b", "lmstudio/x\n", "lmstudio/-x", "sonnet", "Anthropic/claude-sonnet-5"])(
    "refuses %j before spawn",
    async (model) => {
      const spawn = vi.fn();
      const provider = vi.fn(providerFor(runConfig(tmp("oc-run-"))));
      const adapter = new OpenCodeAdapter({
        env: {},
        model,
        spawn: spawn as never,
        runConfigProvider: provider,
      });
      await expect(adapter.createQueryFunction({ cwd: tmp("oc-wt-") })).rejects.toMatchObject({
        category: "CONFIG_INVALID",
      });
      expect(provider).not.toHaveBeenCalled();
      expect(spawn).not.toHaveBeenCalled();
    }
  );
});

describe("OpenCodeAdapter spawn (#1637)", () => {
  const PARENT = {
    OPENAI_API_KEY: "sk-openai-parent-value",
    ANTHROPIC_API_KEY: "sk-ant-parent-value",
    ANTHROPIC_BASE_URL: "http://127.0.0.1:9",
    OPENCODE_PERMISSION: '{"*":"allow"}',
    OPENCODE_AUTO_SHARE: "1",
    OPENCODE_CONFIG: "/operator/opencode.json",
    OPENCODE_CONFIG_DIR: "/operator/opencode",
    OPENCODE_CONFIG_CONTENT: '{"share":"auto"}',
    OPENCODE_SERVER_PASSWORD: "inherited-password",
    OPENCODE_AUTH_CONTENT: '{"anthropic":{"type":"oauth"}}',
    XDG_CONFIG_HOME: "/operator/.config",
    CLAUDE_CODE_OAUTH_TOKEN: "oauth-token-value",
    AWS_SECRET_ACCESS_KEY: "aws-secret-value",
    GH_TOKEN: "ghs_parenttoken",
    HOME: "/home/fixture",
  };

  async function runStage(model: string, runBody: string, extraEnv: Record<string, string> = {}) {
    const dir = tmp("oc-stub-");
    const bin = writeStub(dir, runBody);
    const worktree = tmp("oc-wt-");
    const config = runConfig(tmp("oc-run-"));
    const adapter = new OpenCodeAdapter({
      env: { ...PARENT, PATH: `${bin}:${process.env.PATH}`, ...extraEnv },
      model,
      runConfigProvider: providerFor(config),
    });
    const query = await adapter.createQueryFunction({ cwd: worktree, stage: "feature-dev" });
    return { dir, worktree, config, query };
  }

  it("runs the Go argv with the prompt on stdin, and records the run's usage", async () => {
    const research = join(TESTDATA, "opencode_stream_research_sample.jsonl");
    const { dir, worktree, config, query } = await runStage(LOCAL_MODEL, `  cat '${research}'`);
    const prompt = `--auto ${"implement the issue as specified. ".repeat(64)}`;
    const messages = await drain(query({ prompt, options: { cwd: worktree } }));

    const argv = readFileSync(join(dir, "argv"), "utf-8").trimEnd().split("\n");
    expect(argv).toEqual(buildOpenCodeArgv(LOCAL_MODEL, worktree));
    expect(readFileSync(join(dir, "stdin"), "utf-8")).toBe(prompt);

    const result = messages.find((m) => m.type === "result")!;
    expect(result.usage).toEqual({
      input_tokens: 3089,
      output_tokens: 14,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    });
    expect(result.total_cost_usd).toBe(0);
    expect(result.model).toBe("lm-studio/qwen/qwen3.8-27b");
    expect(result.session_id).toBe("ses_fixture0000000000000000001");

    // The post-run export ran from the run's root, holding no credential.
    expect(realpathSync(readFileSync(join(dir, "helper-cwd"), "utf-8").trim().split("\n")[0])).toBe(
      realpathSync(config.runDir)
    );
    const helperEnv = readEnvFile(join(dir, "helper-env"));
    expect(helperEnv.GH_TOKEN).toBeUndefined();
    expect(helperEnv.OPENCODE_SERVER_PASSWORD).toBeUndefined();
    expect(helperEnv.XDG_DATA_HOME).toBe(config.env.XDG_DATA_HOME);
  });

  it("gives a local run no provider key and none of the operator's OpenCode or XDG state", async () => {
    const research = join(TESTDATA, "opencode_stream_research_sample.jsonl");
    const { dir, config, query } = await runStage(LOCAL_MODEL, `  cat '${research}'`);
    await drain(query({ prompt: "p" }));
    const env = readEnvFile(join(dir, "env"));
    for (const name of [
      "OPENAI_API_KEY",
      "ANTHROPIC_API_KEY",
      "ANTHROPIC_BASE_URL",
      "OPENCODE_PERMISSION",
      "OPENCODE_AUTO_SHARE",
      "OPENCODE_CONFIG",
      "OPENCODE_CONFIG_DIR",
      "OPENCODE_AUTH_CONTENT",
      "CLAUDE_CODE_OAUTH_TOKEN",
      "AWS_SECRET_ACCESS_KEY",
    ]) {
      expect(env[name], name).toBeUndefined();
    }
    for (const [name, value] of Object.entries(config.env)) expect(env[name], name).toBe(value);
    expect(env.OPENCODE_CONFIG_CONTENT).toBe(config.configContent);
    expect(env.OPENCODE_SERVER_PASSWORD).toBeDefined();
    expect(env.OPENCODE_SERVER_PASSWORD).not.toBe(PARENT.OPENCODE_SERVER_PASSWORD);
    expect(env.OPENCODE_SERVER_PASSWORD.length).toBeGreaterThanOrEqual(20);
    expect(env.GH_TOKEN).toBe(PARENT.GH_TOKEN);
    expect(env.NIGHTGAUGE_ADAPTER).toBe("opencode");
    expect(env.NIGHTGAUGE_DISPATCH_MODEL).toBe(LOCAL_MODEL);
  });

  // #1804/#1802: checkRunConfig accepts NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK
  // (see the acceptance test above), but curateOpenCodeChildEnv must never
  // apply it — the TS twin of the Go adapter's BuildCommand deleting it from
  // the child's own env right before returning it.
  it("accepts NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK but never forwards it to the child", async () => {
    const research = join(TESTDATA, "opencode_stream_research_sample.jsonl");
    const dir = tmp("oc-stub-");
    const bin = writeStub(dir, `  cat '${research}'`);
    const worktree = tmp("oc-wt-");
    const config = runConfig(tmp("oc-run-"));
    const adapter = new OpenCodeAdapter({
      env: { ...PARENT, PATH: `${bin}:${process.env.PATH}` },
      model: LOCAL_MODEL,
      runConfigProvider: providerFor({
        ...config,
        env: {
          ...config.env,
          NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK: "/home/operator/.opencode",
        },
      }),
    });
    const query = await adapter.createQueryFunction({ cwd: worktree, stage: "feature-dev" });
    await drain(query({ prompt: "p" }));
    const env = readEnvFile(join(dir, "env"));
    expect(env.NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK).toBeUndefined();
  });

  it("gives an anthropic run ANTHROPIC_API_KEY and no other provider's key", async () => {
    const cloud = join(TESTDATA, "opencode_stream_research_sample.jsonl");
    const { dir, query } = await runStage("anthropic/claude-sonnet-5", `  cat '${cloud}'`);
    await drain(query({ prompt: "p" }));
    const env = readEnvFile(join(dir, "env"));
    expect(env.ANTHROPIC_API_KEY).toBe(PARENT.ANTHROPIC_API_KEY);
    expect(env.OPENAI_API_KEY).toBeUndefined();
    expect(env.ANTHROPIC_BASE_URL).toBeUndefined();
    expect(env.CLAUDE_CODE_OAUTH_TOKEN).toBeUndefined();
  });

  it("redacts captured stderr before it reaches the thrown error", async () => {
    const { query } = await runStage(
      "openai/gpt-5.5",
      `  echo 'ERROR key=sk-test123 fetch https://u:p@h/v1 failed' >&2
  exit 1`,
      { OPENAI_API_KEY: "sk-test123" }
    );
    const err = await drain(query({ prompt: "p" })).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(Error);
    const message = (err as Error).message;
    expect(message).toContain("opencode runner command failed (exit code 1)");
    expect(message).not.toContain("sk-test123");
    expect(message).not.toContain("https://u:p@h");
  });

  it("redacts the model's text before it is yielded, as Go redacts every stdout line", async () => {
    const research = join(TESTDATA, "opencode_stream_research_sample.jsonl");
    // A model that ran `env` and repeated it: the forge token and the run's password.
    const { dir, query } = await runStage(
      LOCAL_MODEL,
      `  printf '{"type":"text","timestamp":1,"sessionID":"ses_fixture0000000000000000001","part":{"type":"text","text":"token %s password %s"}}\\n' "$GH_TOKEN" "$OPENCODE_SERVER_PASSWORD"
  cat '${research}'`
    );
    const messages = await drain(query({ prompt: "p" }));
    const password = readEnvFile(join(dir, "env")).OPENCODE_SERVER_PASSWORD;
    const text = messages
      .filter((m) => m.type === "assistant")
      .map((m) => String(m.text))
      .join("\n");
    expect(text).toContain(
      "token [REDACTED:GH_TOKEN] password [REDACTED:OPENCODE_SERVER_PASSWORD]"
    );
    expect(text).not.toContain(PARENT.GH_TOKEN);
    expect(text).not.toContain(password);
  });

  it("an auto-rejected permission fails the exit-0 run", async () => {
    const stream = join(TESTDATA, "opencode_auto_reject_stream.jsonl");
    const notice = join(TESTDATA, "opencode_auto_reject_stderr.txt");
    const { query } = await runStage(LOCAL_MODEL, `  cat '${stream}'\n  cat '${notice}' >&2`);
    await expect(
      drain(query({ prompt: "p", options: { allowedTools: ["Read", "Bash"] } }))
    ).rejects.toThrow(/^\[adapter-permission-rejected\] tool=bash: /);
  });

  it("aborting the query kills the opencode process group", async () => {
    const pidFile = join(tmp("oc-pid-"), "grandchild.pid");
    const { query } = await runStage(
      LOCAL_MODEL,
      `  sleep 30 &
  echo $! > '${pidFile}'
  wait`
    );
    const controller = new AbortController();
    const pending = drain(query({ prompt: "p", options: { abortSignal: controller.signal } }));
    for (let i = 0; i < 200 && !existsSync(pidFile); i++)
      await new Promise((r) => setTimeout(r, 25));
    const pid = Number(readFileSync(pidFile, "utf-8").trim());
    expect(pid).toBeGreaterThan(0);
    controller.abort();
    await expect(pending).rejects.toThrow(/aborted/);

    // The grandchild was in the run's group: nothing of it remains.
    let alive = true;
    for (let i = 0; i < 100 && alive; i++) {
      try {
        process.kill(pid, 0);
        await new Promise((r) => setTimeout(r, 20));
      } catch {
        alive = false;
      }
    }
    if (alive) process.kill(pid, "SIGKILL");
    expect(alive).toBe(false);
  });
});

/**
 * An opencode run is detached into its own process group, out of the
 * terminal's, so neither Ctrl-C nor this process's exit reaches it on its own.
 * Each case runs the adapter in a separate Node process that then exits or is
 * interrupted, and checks that nothing of the run survives it.
 */
describe("OpenCodeAdapter when its own process ends (#1637)", () => {
  const TSX_LOADER = pathToFileURL(join(REPO_ROOT, "node_modules/tsx/dist/loader.mjs")).href;
  const ADAPTER_URL = pathToFileURL(
    join(REPO_ROOT, "packages/nightgauge-sdk/src/cli/adapters/OpenCodeAdapter.ts")
  ).href;

  const DRIVER = `
import { existsSync } from "node:fs";
const { OpenCodeAdapter } = await import(process.env.ADAPTER_URL);
const [mode, pidFile, worktree] = process.argv.slice(2);
const config = JSON.parse(process.env.RUN_CONFIG);
const adapter = new OpenCodeAdapter({
  env: { PATH: process.env.STUB_PATH, HOME: process.env.HOME },
  model: ${JSON.stringify(LOCAL_MODEL)},
  runConfigProvider: async () => config,
});
const query = await adapter.createQueryFunction({ cwd: worktree, stage: "feature-dev" });
if (mode === "handled") process.on("SIGINT", () => {});
(async () => {
  for await (const _ of query({ prompt: "p" })) {
  }
})().then(
  () => process.exit(0),
  (err) => {
    console.log(err.message);
    process.exit(3);
  }
);
for (let i = 0; i < 500 && !existsSync(pidFile); i++) await new Promise((r) => setTimeout(r, 20));
if (mode === "exit") process.exit(130);
// Bounded: a run the signal did not stop ends the driver after 5 s, not the test's budget.
setTimeout(() => {
  console.log("the query was still running");
  process.exit(4);
}, 5000).unref();
process.kill(process.pid, "SIGINT");
`;

  function isAlive(pid: number): boolean {
    try {
      process.kill(pid, 0);
      return true;
    } catch {
      return false;
    }
  }

  async function runDriver(mode: "exit" | "unhandled" | "handled") {
    const dir = tmp("oc-parent-");
    const pidFile = join(dir, "grandchild.pid");
    const stubPidFile = join(dir, "stub.pid");
    const bin = writeStub(
      dir,
      `  echo $$ > '${stubPidFile}'
  sleep 30 &
  echo $! > '${pidFile}.tmp'
  mv '${pidFile}.tmp' '${pidFile}'
  wait`
    );
    const driver = join(dir, "driver.mjs");
    writeFileSync(driver, DRIVER);
    const worktree = tmp("oc-wt-");
    const child = spawnProcess(
      process.execPath,
      ["--import", TSX_LOADER, driver, mode, pidFile, worktree],
      {
        env: {
          PATH: process.env.PATH,
          HOME: process.env.HOME,
          ADAPTER_URL,
          STUB_PATH: `${bin}:${process.env.PATH}`,
          RUN_CONFIG: JSON.stringify(runConfig(tmp("oc-run-"))),
        },
        stdio: ["ignore", "pipe", "pipe"],
      }
    );
    let output = "";
    child.stdout.on("data", (c) => (output += String(c)));
    child.stderr.on("data", (c) => (output += String(c)));
    const killer = setTimeout(() => child.kill("SIGKILL"), 15_000);
    const exit = await new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((r) =>
      child.on("close", (code, signal) => r({ code, signal }))
    );
    clearTimeout(killer);
    const pids = [pidFile, stubPidFile]
      .filter((f) => existsSync(f))
      .map((f) => Number(readFileSync(f, "utf-8").trim()));
    // Give a killed group a moment to be reaped, then clean up whatever survived.
    const deadline = Date.now() + 3000;
    while (pids.some(isAlive) && Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 25));
    }
    const survivors = pids.filter(isAlive);
    for (const pid of survivors) process.kill(pid, "SIGKILL");
    return { exit, output, pids, survivors };
  }

  it("kills the run's process group when this process exits", async () => {
    const r = await runDriver("exit");
    expect(r.exit.code, r.output).toBe(130);
    expect(r.pids).toHaveLength(2);
    expect(r.survivors).toEqual([]);
  }, 20_000);

  it("kills it when SIGINT ends this process, and the process still ends by SIGINT", async () => {
    const r = await runDriver("unhandled");
    expect(r.exit.signal, r.output).toBe("SIGINT");
    expect(r.pids).toHaveLength(2);
    expect(r.survivors).toEqual([]);
  }, 20_000);

  it("aborts the run on SIGINT when another handler keeps this process alive", async () => {
    const r = await runDriver("handled");
    expect(r.exit.code, r.output).toBe(3);
    expect(r.output).toMatch(/opencode query aborted/);
    expect(r.pids).toHaveLength(2);
    expect(r.survivors).toEqual([]);
  }, 20_000);
});
