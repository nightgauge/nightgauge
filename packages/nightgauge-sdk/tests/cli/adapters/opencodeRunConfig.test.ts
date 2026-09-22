/**
 * The SDK's OpenCode run config comes from `nightgauge opencode config` and
 * nowhere else (#1648), and an SDK spawn verifies the plugin handshake (#1804).
 *
 * The parity test feeds the golden that cmd/nightgauge's
 * TestOpenCodeConfigGolden generates from the real verb and the real Go spawn
 * path (internal/execution/adapters/testdata/opencode_config_golden.json)
 * through a stub `nightgauge` on NIGHTGAUGE_BIN, runs a stage, and compares
 * the environment the SDK's opencode child actually got with the Go spawn's.
 */
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";

import { OpenCodeAdapter } from "../../../src/cli/adapters/OpenCodeAdapter.js";
import {
  OPENCODE_CONFIG_VERB_MAX_BUFFER,
  OPENCODE_CONFIG_VERB_TIMEOUT_MS,
  createOpenCodeRunConfigProvider,
  type OpenCodeConfigExecFile,
} from "../../../src/cli/adapters/opencodeRunConfig.js";
import { AdapterError } from "../../../src/cli/adapters/errors.js";
import { killGroupUntilGone } from "../../../src/cli/adapters/cliQueryHelper.js";
import type { SDKMessage } from "../../../src/orchestrator/StageExecutor.js";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../..");
const GOLDEN = join(REPO_ROOT, "internal/execution/adapters/testdata/opencode_config_golden.json");
const RESEARCH = join(
  REPO_ROOT,
  "internal/execution/testdata/opencode_stream_research_sample.jsonl"
);
const ADAPTERS_SRC = join(REPO_ROOT, "packages/nightgauge-sdk/src/cli/adapters");

interface Golden {
  inputs: { model: string; stage: string; repo: string; max_turns: number; run_id: string };
  verb: Record<string, unknown> & {
    config_content: string;
    env: Record<string, string>;
    run_id: string;
  };
  go_spawn_env: Record<string, string>;
}

const tmpDirs: string[] = [];
function tmp(prefix: string): string {
  const dir = realpathSync(mkdtempSync(join(tmpdir(), prefix)));
  tmpDirs.push(dir);
  return dir;
}
afterEach(() => {
  vi.restoreAllMocks();
  for (const d of tmpDirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

const NONCE = "0123456789abcdef0123456789abcdef";

/** The golden, with its placeholders replaced by `base` and a nonce. */
function loadGolden(base: string): Golden {
  const text = readFileSync(GOLDEN, "utf-8")
    .split("@BASE@")
    .join(base)
    .split("@NONCE@")
    .join(NONCE);
  return JSON.parse(text) as Golden;
}

function writeExecutable(path: string, script: string): void {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, script);
  chmodSync(path, 0o755);
}

/**
 * A stand-in `opencode` at `path`: `run` records its environment, writes the
 * handshake sentinel the way the plugin's init does, then runs `runBody`.
 * `sentinel` breaks the handshake: "none" writes no sentinel, "foreign" one
 * with another run's nonce, "late" one dated now rather than before the run.
 * (The switch is baked into the script: the child's environment is curated,
 * so a test variable would never reach it.)
 */
type SentinelMode = "ok" | "none" | "foreign" | "late";
function writeOpenCodeStub(
  path: string,
  record: string,
  runBody: string,
  sentinel: SentinelMode = "ok"
): void {
  const vars =
    sentinel === "none"
      ? "NO_SENTINEL=1"
      : sentinel === "foreign"
        ? "SENTINEL_NONCE=not-this-runs-nonce"
        : sentinel === "late"
          ? "SENTINEL_NOW=1"
          : "";
  writeExecutable(
    path,
    `#!/bin/sh
${vars}
case "$1" in
run)
  env > '${record}'
  cat > /dev/null
  if [ -z "$NO_SENTINEL" ]; then
    mkdir -p "$(dirname "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL")"
    printf '{"nonce":"%s","plugin_version":"1","hooks":[]}' "\${SENTINEL_NONCE:-$NIGHTGAUGE_OPENCODE_PLUGIN_NONCE}" > "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
    [ -z "$SENTINEL_NOW" ] && touch -t 202001010000 "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
  fi
${runBody}
  ;;
export) printf '{}' ;;
db) printf '[]' ;;
esac
`
  );
}

/** A stand-in `nightgauge` that records its argv, then runs `body`. */
function writeNightgaugeStub(dir: string, body: string): string {
  const path = join(dir, "nightgauge");
  writeExecutable(
    path,
    `#!/bin/sh
if [ "$2" = cleanup ]; then echo "$4" >> '${dir}/cleanups'; exit 0; fi
printf '%s\\n' "$@" > '${dir}/verb-argv'
pwd > '${dir}/verb-cwd'
${body}
`
  );
  return path;
}

/** The run ids the stub verb was asked to clean up, in order. */
function cleanups(dir: string): string[] {
  try {
    return readFileSync(join(dir, "cleanups"), "utf-8").trim().split("\n").filter(Boolean);
  } catch {
    return [];
  }
}

/** A stage skill directory in the `<root>/skills/<skill>` layout loadStageSkill resolves. */
function skillDirIn(root: string): string {
  const dir = join(root, "skills", "nightgauge-feature-dev");
  mkdirSync(dir, { recursive: true });
  return dir;
}

function readEnvFile(path: string): Record<string, string> {
  const env: Record<string, string> = {};
  for (const line of readFileSync(path, "utf-8").split("\n")) {
    const eq = line.indexOf("=");
    if (eq > 0) env[line.slice(0, eq)] = line.slice(eq + 1);
  }
  return env;
}

/** Create the query function and run one query; the verb runs per query. */
async function runOnce(
  adapter: OpenCodeAdapter,
  opts: { cwd: string; stage?: string; skillDir?: string }
): Promise<SDKMessage[]> {
  const query = await adapter.createQueryFunction({ cwd: opts.cwd, stage: opts.stage });
  return drain(
    query({
      prompt: "p",
      options: { cwd: opts.cwd, ...(opts.skillDir !== undefined && { skillDir: opts.skillDir }) },
    })
  );
}

async function drain(gen: AsyncGenerator<SDKMessage>): Promise<SDKMessage[]> {
  const out: SDKMessage[] = [];
  for await (const m of gen) out.push(m);
  return out;
}

/**
 * One stage through the default (verb-backed) provider: the golden's verb
 * output under a fresh base, the stub opencode at the verb's `binary`.
 */
async function stageFromGolden(
  opts: { runBody?: string; parentEnv?: Record<string, string>; sentinel?: SentinelMode } = {}
) {
  const base = tmp("oc-golden-");
  const golden = loadGolden(base);
  const worktree = join(base, "worktree");
  mkdirSync(worktree, { recursive: true });
  writeFileSync(join(base, "verb.json"), JSON.stringify(golden.verb));
  const record = join(base, "child-env");
  writeOpenCodeStub(
    golden.verb.binary as string,
    record,
    opts.runBody ?? `  cat '${RESEARCH}'`,
    opts.sentinel
  );
  const nightgauge = writeNightgaugeStub(base, `cat '${join(base, "verb.json")}'`);
  const adapter = new OpenCodeAdapter({
    env: {
      PATH: process.env.PATH ?? "/usr/bin:/bin",
      NIGHTGAUGE_BIN: nightgauge,
      NIGHTGAUGE_TARGET_REPO: golden.inputs.repo,
      ...opts.parentEnv,
    },
    model: golden.inputs.model,
  });
  const raw = await adapter.createQueryFunction({ cwd: worktree, stage: golden.inputs.stage });
  // Every query carries the stage's skill directory, as StageExecutor's do.
  const skillDir = skillDirIn(join(base, "skills-root"));
  const query: typeof raw = (q) => raw({ ...q, options: { skillDir, ...q.options } });
  return { base, golden, worktree, record, query, skillDir };
}

// ---------------------------------------------------------------------------

describe("SDK/Go parity through the golden (#1648)", () => {
  it("the SDK child gets byte-identical OPENCODE_CONFIG_CONTENT and every XDG/OPENCODE_* value of the Go spawn", async () => {
    const { base, golden, worktree, record, query } = await stageFromGolden();
    // The same inputs the Go golden was generated with, as StageExecutor
    // hands them to a query: the stage's turn budget and the run's identity.
    await drain(
      query({
        prompt: "p",
        options: {
          cwd: worktree,
          maxTurns: golden.inputs.max_turns,
          runId: golden.inputs.run_id,
        },
      })
    );
    const child = readEnvFile(record);

    expect(child.OPENCODE_CONFIG_CONTENT).toBe(golden.go_spawn_env.OPENCODE_CONFIG_CONTENT);
    expect(child.OPENCODE_CONFIG_CONTENT).toBe(golden.verb.config_content);
    for (const [name, value] of Object.entries(golden.go_spawn_env)) {
      expect(child[name], name).toBe(value);
    }
    // Every variable the verb's env names reaches the child with the verb's
    // value (GH_CONFIG_DIR and NIGHTGAUGE_CONFIG_HOME included), except the
    // one the Go spawn also withholds.
    for (const [name, value] of Object.entries(golden.verb.env)) {
      if (name === "NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK") continue;
      expect(child[name], name).toBe(value);
    }
    // No XDG_*, OPENCODE_* or NIGHTGAUGE_OPENCODE_* variable, and none of the
    // verb's, that the Go spawn does not set (the per-spawn server password
    // is minted on both paths).
    const compared = (n: string) =>
      n !== "OPENCODE_SERVER_PASSWORD" &&
      (n in golden.verb.env ||
        n === "HOME" ||
        n.startsWith("XDG_") ||
        n.startsWith("OPENCODE_") ||
        n.startsWith("NIGHTGAUGE_OPENCODE_"));
    expect(Object.keys(child).filter(compared).sort()).toEqual(
      Object.keys(golden.go_spawn_env).sort()
    );

    // The verb got the dispatch as argv, one element per value.
    expect(readFileSync(join(base, "verb-argv"), "utf-8").trimEnd().split("\n")).toEqual([
      "opencode",
      "config",
      "--stage",
      golden.inputs.stage,
      "--worktree",
      worktree,
      "--model",
      golden.inputs.model,
      "--skills-root",
      join(base, "skills-root"),
      "--repo",
      golden.inputs.repo,
      "--max-turns",
      String(golden.inputs.max_turns),
      "--run-id",
      golden.inputs.run_id,
      "--json",
    ]);
  });

  it("the query's stage, turn budget and run identity reach the verb; a non-identity run id does not", async () => {
    const { base, golden, worktree, query } = await stageFromGolden();
    const argv = () => readFileSync(join(base, "verb-argv"), "utf-8").trimEnd().split("\n");
    const after = (list: string[], flag: string) =>
      list.includes(flag) ? list[list.indexOf(flag) + 1] : undefined;

    await drain(
      query({
        prompt: "p",
        options: {
          cwd: worktree,
          stage: "feature-validate",
          maxTurns: 17,
          runId: golden.inputs.run_id,
        },
      })
    );
    expect(after(argv(), "--stage")).toBe("feature-validate");
    expect(after(argv(), "--max-turns")).toBe("17");
    expect(after(argv(), "--run-id")).toBe(golden.inputs.run_id);

    // A run id that is not a canonical UUIDv7 run identity is not passed (the
    // verb mints a root, as the Go manager does), and neither is a zero budget.
    await drain(
      query({ prompt: "p", options: { cwd: worktree, maxTurns: 0, runId: "wf-12-feature-dev" } })
    );
    expect(argv()).not.toContain("--run-id");
    expect(argv()).not.toContain("--max-turns");
  });

  it("the verb's env overrides an inherited XDG_CONFIG_HOME and OPENCODE_CONFIG_CONTENT", async () => {
    const { golden, worktree, record, query } = await stageFromGolden({
      parentEnv: {
        XDG_CONFIG_HOME: "/tmp/op",
        OPENCODE_CONFIG_CONTENT: "{}",
        HOME: "/home/operator",
        OPENCODE_CONFIG_DIR: "/tmp/op/opencode",
      },
    });
    await drain(query({ prompt: "p", options: { cwd: worktree } }));
    const child = readEnvFile(record);
    expect(child.XDG_CONFIG_HOME).toBe(golden.go_spawn_env.XDG_CONFIG_HOME);
    expect(child.OPENCODE_CONFIG_CONTENT).toBe(golden.go_spawn_env.OPENCODE_CONFIG_CONTENT);
    expect(child.HOME).toBe(golden.go_spawn_env.HOME);
    expect(child.OPENCODE_CONFIG_DIR).toBeUndefined();
  });

  it("the verb's env_withhold keeps a withheld inherited variable from the child", async () => {
    const { golden, worktree, record, query } = await stageFromGolden({
      parentEnv: { ANTHROPIC_BASE_URL: "http://127.0.0.1:9", GH_TOKEN: "ghs_kept_value" },
    });
    expect((golden.verb.env_withhold as { names: string[] }).names).toContain("ANTHROPIC_BASE_URL");
    await drain(query({ prompt: "p", options: { cwd: worktree } }));
    const child = readEnvFile(record);
    expect(child.ANTHROPIC_BASE_URL).toBeUndefined();
    expect(child.GH_TOKEN).toBe("ghs_kept_value");
  });

  it("logs neither config_content nor any env value at info level or above", async () => {
    const lines: string[] = [];
    for (const level of ["log", "info", "warn", "error"] as const) {
      vi.spyOn(console, level).mockImplementation((...args: unknown[]) => {
        lines.push(args.map(String).join(" "));
      });
    }
    const { golden, worktree, query } = await stageFromGolden();
    await drain(query({ prompt: "p", options: { cwd: worktree } }));
    const logged = lines.join("\n");
    expect(logged).not.toContain(golden.verb.config_content);
    for (const [name, value] of Object.entries(golden.verb.env)) {
      if (value.length >= 8) expect(logged, name).not.toContain(value);
    }
  });
});

describe("no TypeScript builds the OpenCode config (#1648)", () => {
  it("src/cli/adapters holds no OpenCode config-key literal", () => {
    const key =
      /(^|[^A-Za-z0-9_.$])["'`]?(permission|compaction|share|autoupdate|enabled_providers|small_model|default_agent|subagent_depth|tool_output)["'`]?\s*:\s*(["'`{[]|true\b|false\b|\d)/;
    const hits: string[] = [];
    for (const file of readdirSync(ADAPTERS_SRC)) {
      if (!file.endsWith(".ts")) continue;
      readFileSync(join(ADAPTERS_SRC, file), "utf-8")
        .split("\n")
        .forEach((line, i) => {
          if (key.test(line)) hits.push(`${file}:${i + 1}: ${line.trim()}`);
        });
    }
    expect(hits).toEqual([]);
  });
});

describe("running the verb (#1648)", () => {
  function captureExec(stdout: string) {
    const calls: Array<{ file: string; args: readonly string[]; options: unknown }> = [];
    const execFile: OpenCodeConfigExecFile = (file, args, options, callback) => {
      calls.push({ file, args, options });
      callback(null, stdout, "");
      return undefined;
    };
    return { calls, execFile };
  }

  it("uses execFile with an argv array, the worktree one element, a 15 s timeout and bounded output", async () => {
    const base = tmp("oc-exec-");
    const golden = loadGolden(base);
    const { calls, execFile } = captureExec(JSON.stringify(golden.verb));
    const worktree = "/tmp/a worktree; rm -rf $HOME `x`";
    const provider = createOpenCodeRunConfigProvider({
      env: { NIGHTGAUGE_BIN: "/opt/nightgauge/bin/nightgauge" },
      execFile,
    });
    const skillDir = skillDirIn(tmp("oc-sk-"));
    await provider({ model: golden.inputs.model, worktree, stage: "feature-dev", skillDir });
    expect(calls).toHaveLength(1);
    expect(calls[0].file).toBe("/opt/nightgauge/bin/nightgauge");
    expect(Array.isArray(calls[0].args)).toBe(true);
    expect(calls[0].args.filter((a) => a === worktree)).toHaveLength(1);
    expect(calls[0].args[calls[0].args.indexOf("--worktree") + 1]).toBe(worktree);
    expect(calls[0].args[calls[0].args.indexOf("--skills-root") + 1]).toBe(
      dirname(dirname(skillDir))
    );
    expect(calls[0].options).toMatchObject({
      cwd: worktree,
      timeout: 15_000,
      maxBuffer: OPENCODE_CONFIG_VERB_MAX_BUFFER,
    });
    expect(OPENCODE_CONFIG_VERB_TIMEOUT_MS).toBe(15_000);
    expect(OPENCODE_CONFIG_VERB_MAX_BUFFER).toBeLessThanOrEqual(16 * 1024 * 1024);
  });

  it("uses nightgauge on PATH without NIGHTGAUGE_BIN, and refuses a relative NIGHTGAUGE_BIN", async () => {
    const golden = loadGolden(tmp("oc-exec-"));
    const { calls, execFile } = captureExec(JSON.stringify(golden.verb));
    await createOpenCodeRunConfigProvider({ env: {}, execFile })({
      model: golden.inputs.model,
      worktree: "/w",
      stage: "feature-dev",
      skillDir: "/r/skills/nightgauge-feature-dev",
    });
    expect(calls[0].file).toBe("nightgauge");
    await expect(
      createOpenCodeRunConfigProvider({ env: { NIGHTGAUGE_BIN: "bin/nightgauge" }, execFile })({
        model: golden.inputs.model,
        worktree: "/w",
        stage: "feature-dev",
      })
    ).rejects.toThrow(/NIGHTGAUGE_BIN "bin\/nightgauge" is not an absolute path/);
  });
});

describe("a verb that fails fails the stage before any opencode starts (#1648)", () => {
  async function refusal(body: string, parentEnv: Record<string, string> = {}) {
    const dir = tmp("oc-verb-");
    const spawn = vi.fn();
    const adapter = new OpenCodeAdapter({
      env: {
        PATH: process.env.PATH ?? "/usr/bin:/bin",
        NIGHTGAUGE_BIN: writeNightgaugeStub(dir, body),
        ...parentEnv,
      },
      model: "lmstudio/qwen/qwen3.8-27b",
      spawn: spawn as never,
    });
    const err = await runOnce(adapter, {
      cwd: tmp("oc-wt-"),
      stage: "feature-dev",
      skillDir: skillDirIn(tmp("oc-sk-")),
    }).catch((e: unknown) => e);
    expect(spawn).not.toHaveBeenCalled();
    expect(err).toBeInstanceOf(AdapterError);
    return err as AdapterError;
  }

  it("a tamper-gate refusal (non-zero exit) surfaces the verb's reason", async () => {
    const err = await refusal(`echo 'opencode: opencode.json differs from base' >&2; exit 3`);
    expect(err.category).toBe("CONFIG_INVALID");
    expect(err.message).toContain("exit code 3");
    expect(err.message).toContain("opencode.json differs from base");
  });

  it("the verb's stderr is redacted", async () => {
    const secret = "sk-ant-fixture-secret-value-1648";
    const err = await refusal(`echo "refused with $ANTHROPIC_API_KEY" >&2; exit 1`, {
      ANTHROPIC_API_KEY: secret,
    });
    expect(err.message).not.toContain(secret);
    expect(err.message).toContain("[REDACTED:ANTHROPIC_API_KEY]");
  });

  it("an unknown schema_version major", async () => {
    const err = await refusal(`echo '{"schema_version":"2.0"}'`);
    expect(err.category).toBe("VERSION_MISMATCH");
    expect(err.message).toContain("schema_version 2.0");
  });

  it("malformed JSON, and output missing a field", async () => {
    expect((await refusal(`echo 'not json'`)).message).toContain("not JSON");
    const golden = loadGolden(tmp("oc-g-"));
    const partial = { ...golden.verb } as Record<string, unknown>;
    delete partial.plugin_version;
    const err = await refusal(`echo '${JSON.stringify(partial).replace(/'/g, "'\\''")}'`);
    expect(err.message).toContain("plugin_version");
  });

  it("a verb killed at its timeout", async () => {
    const spawn = vi.fn();
    const execFile: OpenCodeConfigExecFile = (_f, _a, _o, callback) => {
      const e = Object.assign(new Error("killed"), { killed: true, signal: "SIGTERM" as const });
      callback(e, "", "still reading the forge");
      return undefined;
    };
    const adapter = new OpenCodeAdapter({
      env: {},
      model: "lmstudio/qwen/qwen3.8-27b",
      spawn: spawn as never,
      runConfigProvider: createOpenCodeRunConfigProvider({ env: {}, execFile }),
    });
    const err = await runOnce(adapter, {
      cwd: tmp("oc-wt-"),
      stage: "feature-dev",
      skillDir: skillDirIn(tmp("oc-sk-")),
    }).catch((e: unknown) => e);
    expect(spawn).not.toHaveBeenCalled();
    expect((err as AdapterError).category).toBe("TIMEOUT");
    expect((err as AdapterError).message).toContain("15 s");
  });

  it("no stage", async () => {
    const dir = tmp("oc-verb-");
    const adapter = new OpenCodeAdapter({
      env: { NIGHTGAUGE_BIN: writeNightgaugeStub(dir, "exit 0") },
      model: "lmstudio/qwen/qwen3.8-27b",
    });
    await expect(runOnce(adapter, { cwd: tmp("oc-wt-") })).rejects.toThrow(/stage is not known/);
  });
});

describe("the plugin handshake on the SDK spawn path (#1804)", () => {
  it("a run that never writes the sentinel fails, naming the handshake", async () => {
    const { worktree, query } = await stageFromGolden({ sentinel: "none" });
    await expect(drain(query({ prompt: "p", options: { cwd: worktree } }))).rejects.toThrow(
      /adapter_incompatible: no plugin handshake sentinel at .*the Nightgauge OpenCode plugin did not load/
    );
  });

  it("a sentinel with another run's nonce fails", async () => {
    const { worktree, query } = await stageFromGolden({ sentinel: "foreign" });
    await expect(drain(query({ prompt: "p", options: { cwd: worktree } }))).rejects.toThrow(
      /carries the wrong nonce/
    );
  });

  it("a sentinel written after the first tool call started fails at exit", async () => {
    // The research sample's tool call started in the past; a sentinel dated now is late.
    const { worktree, query } = await stageFromGolden({ sentinel: "late" });
    await expect(drain(query({ prompt: "p", options: { cwd: worktree } }))).rejects.toThrow(
      /was written after the run's first tool call started/
    );
  });

  it("kills the run at its first step_start when the sentinel is missing", async () => {
    const stepStart = readFileSync(RESEARCH, "utf-8").split("\n")[0];
    const { worktree, query } = await stageFromGolden({
      sentinel: "none",
      runBody: `  printf '%s\\n' '${stepStart}'\n  sleep 30`,
    });
    const started = Date.now();
    await expect(drain(query({ prompt: "p", options: { cwd: worktree } }))).rejects.toThrow(
      /no plugin handshake sentinel/
    );
    expect(Date.now() - started).toBeLessThan(10_000);
  });
});

describe("the verb builds the stage's permission map from the stage's own skill (#1648)", () => {
  it("passes the stage's skills root and runs the verb in the worktree", async () => {
    const { base, worktree, skillDir, query } = await stageFromGolden();
    await drain(query({ prompt: "p", options: { cwd: worktree } }));
    const argv = readFileSync(join(base, "verb-argv"), "utf-8").trimEnd().split("\n");
    expect(argv[argv.indexOf("--skills-root") + 1]).toBe(dirname(dirname(skillDir)));
    expect(realpathSync(readFileSync(join(base, "verb-cwd"), "utf-8").trim())).toBe(
      realpathSync(worktree)
    );
  });

  it("fails before any opencode starts without the stage's skill directory, or with one outside a skills directory", async () => {
    for (const skillDir of [undefined, join(tmp("oc-sk-"), "nightgauge-feature-dev")]) {
      const spawn = vi.fn();
      const adapter = new OpenCodeAdapter({
        env: { NIGHTGAUGE_BIN: writeNightgaugeStub(tmp("oc-verb-"), "exit 0") },
        model: "lmstudio/qwen/qwen3.8-27b",
        spawn: spawn as never,
      });
      await expect(
        runOnce(adapter, { cwd: tmp("oc-wt-"), stage: "feature-dev", skillDir }),
        String(skillDir)
      ).rejects.toThrow(/skill directory/);
      expect(spawn).not.toHaveBeenCalled();
    }
  });

  it("warns when the stage's repository is not owner/name, rather than dropping it silently", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { base, worktree, query } = await stageFromGolden({
      parentEnv: { NIGHTGAUGE_TARGET_REPO: "not a repository" },
    });
    await drain(query({ prompt: "p", options: { cwd: worktree } }));
    expect(readFileSync(join(base, "verb-argv"), "utf-8")).not.toContain("--repo");
    expect(warn.mock.calls.map((c) => String(c[0])).join("\n")).toMatch(
      /"not a repository" is not owner\/name/
    );
  });
});

describe("the per-run root is deleted as the Go scheduler deletes it (#1648, ADR-022 § 22)", () => {
  it("a root the verb minted for one query is deleted when the query ends, whatever its outcome", async () => {
    const ok = await stageFromGolden();
    await drain(ok.query({ prompt: "p", options: { cwd: ok.worktree } }));
    expect(cleanups(ok.base)).toEqual([ok.golden.verb.run_id]);

    const failed = await stageFromGolden({ sentinel: "none" });
    await expect(
      drain(failed.query({ prompt: "p", options: { cwd: failed.worktree } }))
    ).rejects.toThrow(/handshake/);
    expect(cleanups(failed.base)).toEqual([failed.golden.verb.run_id]);
  });

  it("a run's shared root is left for the run's end", async () => {
    const { base, golden, worktree, query } = await stageFromGolden();
    await drain(query({ prompt: "p", options: { cwd: worktree, runId: golden.inputs.run_id } }));
    expect(cleanups(base)).toEqual([]);
  });
});

describe("a failed handshake keeps killing the run's group (manager.go's killProcessTreeUntilGone)", () => {
  it("repeats SIGKILL for the window until every holder of the run's stdio is gone", async () => {
    const kills: NodeJS.Signals[] = [];
    killGroupUntilGone(
      4242,
      () => false,
      (_pid, signal) => kills.push(signal),
      200
    );
    await new Promise((r) => setTimeout(r, 300));
    expect(kills.length).toBeGreaterThanOrEqual(5);
    expect(new Set(kills)).toEqual(new Set(["SIGKILL"]));

    const stopped: NodeJS.Signals[] = [];
    let done = false;
    killGroupUntilGone(
      4243,
      () => done,
      (_pid, signal) => stopped.push(signal),
      200
    );
    done = true;
    await new Promise((r) => setTimeout(r, 100));
    expect(stopped).toEqual(["SIGKILL"]);
  });
});
