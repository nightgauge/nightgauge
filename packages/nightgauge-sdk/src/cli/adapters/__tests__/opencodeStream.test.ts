/**
 * The OpenCode stream parser against the Go parser's captured fixtures
 * (#1624, #1629) and the expectations file both languages read,
 * internal/execution/testdata/opencode_stream_expected.json (#1637).
 */
import { readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";

// Every write the parser could make goes through one of these; the subagent
// roll-up must make none (its exports are held in memory only).
const writes = vi.hoisted(() => ({ calls: [] as unknown[][] }));
vi.mock("node:fs/promises", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:fs/promises")>();
  const spy =
    (fn: (...a: never[]) => unknown) =>
    (...args: unknown[]) => {
      writes.calls.push(args);
      return (fn as (...a: unknown[]) => unknown)(...args);
    };
  return {
    ...actual,
    default: actual,
    writeFile: spy(actual.writeFile),
    appendFile: spy(actual.appendFile),
  };
});

import {
  OPENCODE_DRIFT_MARKER,
  OPENCODE_ERROR_EVENT_MARKER,
  OPENCODE_PERMISSION_REJECTED_MARKER,
  classifyOpenCodeRun,
  openCodeRedactor,
  openCodeStageCostUsd,
  parseOpenCodeStream,
  redactOpenCodeLine,
  type OpenCodeHelper,
  type OpenCodeTokens,
} from "../opencodeStream.js";

// `__dirname`, not `import.meta`: this package builds to CommonJS.
const REPO_ROOT = resolve(__dirname, "../../../../../..");
const TESTDATA = join(REPO_ROOT, "internal/execution/testdata");

function testdata(name: string): string {
  return readFileSync(join(TESTDATA, name), "utf-8");
}

interface ExpectedTokens {
  input: number;
  output: number;
  reasoning: number;
  cache_read: number;
  cache_write: number;
}

interface ExpectedFixture {
  dispatched: string;
  stream: {
    session_id: string;
    step_finishes: number;
    tokens: ExpectedTokens;
    peak_step_input: number;
    reported_cost_usd: number;
    rejected_tool_calls: number;
  };
  stderr: string | null;
  failures: Array<{ allowed_tools: string[]; marker: string }>;
  fold: null | {
    sessions: Array<{
      id: string;
      parent: string;
      tokens: ExpectedTokens;
      cost: number;
      provider: string;
      model: string;
    }>;
    tokens: ExpectedTokens;
    served: { provider: string; model: string; upstream: string };
    stage_cost_usd: number | null;
  };
}

const EXPECTED = JSON.parse(testdata("opencode_stream_expected.json")) as {
  opencode_version: string;
  fixtures: Record<string, ExpectedFixture>;
};

function tokensOf(t: ExpectedTokens): OpenCodeTokens {
  return {
    input: t.input,
    output: t.output,
    reasoning: t.reasoning,
    cacheRead: t.cache_read,
    cacheWrite: t.cache_write,
  };
}

function closeUsd(a: number | undefined, b: number): boolean {
  return a !== undefined && Math.abs(a - b) < 1e-12;
}

/**
 * A fake `opencode` for the subagent roll-up: answers `export` and `db` from a
 * session tree and each session's export totals, in the shape a sanitized
 * export has, and records every argv.
 */
function fakeHelper(sessions: NonNullable<ExpectedFixture["fold"]>["sessions"]): {
  helper: OpenCodeHelper;
  calls: string[][];
} {
  const calls: string[][] = [];
  const helper: OpenCodeHelper = async (args) => {
    calls.push([...args]);
    if (args[0] === "db") {
      return JSON.stringify(
        sessions.filter((s) => s.parent !== "").map((s) => ({ id: s.id, parent_id: s.parent }))
      );
    }
    if (args[0] === "export") {
      const s = sessions.find((x) => x.id === args[1]);
      if (!s) throw new Error("unknown session");
      return JSON.stringify({
        info: {
          id: s.id,
          title: `[redacted:session-title:${s.id}]`,
          cost: s.cost,
          tokens: {
            input: s.tokens.input,
            output: s.tokens.output,
            reasoning: s.tokens.reasoning,
            cache: { read: s.tokens.cache_read, write: s.tokens.cache_write },
          },
        },
        messages: [
          { info: { role: "user", id: "msg_1" }, parts: [] },
          {
            info: { role: "assistant", providerID: s.provider, modelID: s.model },
            parts: [{ type: "text", text: "[redacted:text:prt_1]" }],
          },
        ],
      });
    }
    throw new Error(`unexpected helper ${args[0]}`);
  };
  return { helper, calls };
}

describe("opencode stream parser: shared Go/TS expectations (#1637)", () => {
  it("covers every opencode capture the Go parser is tested against", () => {
    expect(EXPECTED.opencode_version).toBe("1.18.30");
    const captures = readdirSync(TESTDATA).filter(
      (f) => f.startsWith("opencode_") && f.endsWith(".jsonl")
    );
    expect(captures.length).toBeGreaterThanOrEqual(7);
    expect(Object.keys(EXPECTED.fixtures).sort()).toEqual(captures.sort());
  });

  describe.each(Object.entries(EXPECTED.fixtures))("%s", (name, fx) => {
    it("reports the file's totals, peak input, session and reported cost", () => {
      const s = parseOpenCodeStream(testdata(name));
      expect(s.sessionId).toBe(fx.stream.session_id);
      expect(s.stepFinishes).toBe(fx.stream.step_finishes);
      expect(s.tokens).toEqual(tokensOf(fx.stream.tokens));
      expect(s.peakStepInputTokens).toBe(fx.stream.peak_step_input);
      expect(closeUsd(s.reportedCostUsd, fx.stream.reported_cost_usd)).toBe(true);
      expect(s.rejectedToolCalls).toBe(fx.stream.rejected_tool_calls);
    });

    it("classifies the run as the file says", async () => {
      const stdout = testdata(name);
      if (fx.stderr === null) {
        expect(fx.failures).toEqual([]);
        const summary = await classifyOpenCodeRun({
          stdout,
          stderr: "",
          exitCode: 0,
          dispatched: fx.dispatched,
        });
        expect(summary.failure).toBeUndefined();
        expect(summary.driftMarkers).toEqual([]);
        return;
      }
      const stderr = testdata(fx.stderr);
      for (const f of fx.failures) {
        const summary = await classifyOpenCodeRun({
          stdout,
          stderr,
          exitCode: 0,
          allowedTools: f.allowed_tools,
          dispatched: fx.dispatched,
        });
        expect(summary.failure?.split("\n")[0].startsWith(`${f.marker}:`), summary.failure).toBe(
          true
        );
        expect(summary.driftMarkers).toEqual([]);
      }
    });

    it.runIf(fx.fold !== null)(
      "folds the subagent sessions, records the served model and prices the stage",
      async () => {
        const fold = fx.fold!;
        const { helper } = fakeHelper(fold.sessions);
        const summary = await classifyOpenCodeRun({
          stdout: testdata(name),
          stderr: "",
          exitCode: 0,
          dispatched: fx.dispatched,
          fold: helper,
        });
        expect(summary.tokens).toEqual(tokensOf(fold.tokens));
        expect(summary.usagePartial).toBe(false);
        expect(summary.driftMarkers).toEqual([]);
        expect({
          provider: summary.served?.provider,
          model: summary.served?.model,
          upstream: summary.served?.upstream,
        }).toEqual(fold.served);
        if (fold.stage_cost_usd === null) {
          expect(summary.costUsd).toBeUndefined();
        } else {
          expect(closeUsd(summary.costUsd, fold.stage_cost_usd), `cost ${summary.costUsd}`).toBe(
            true
          );
        }
        // A subagent's session total is not a step.
        expect(summary.peakStepInputTokens).toBe(fx.stream.peak_step_input);
      }
    );
  });
});

describe("opencode stream parser: the token pools (#1637)", () => {
  /** The research sample with each step_finish's part.tokens replaced, keeping its real shape. */
  function withTokens(steps: Array<[number, number, number, number, number]>): string {
    let i = 0;
    return testdata("opencode_stream_research_sample.jsonl")
      .trim()
      .split("\n")
      .map((line) => {
        const ev = JSON.parse(line) as { type: string; part: Record<string, unknown> };
        if (ev.type !== "step_finish") return line;
        const [input, output, reasoning, read, write] = steps[i++];
        ev.part.tokens = {
          total: input + output + reasoning + read + write,
          input,
          output,
          reasoning,
          cache: { read, write },
        };
        return JSON.stringify(ev);
      })
      .join("\n");
  }

  it("sums both cache pools on their own and counts them in a step's prompt", () => {
    // The Go parser's TestParseOpenCodeStream uses the same two steps.
    const s = parseOpenCodeStream(
      withTokens([
        [100, 10, 0, 6000, 400],
        [200, 20, 0, 6400, 0],
      ])
    );
    expect(s.tokens).toEqual({
      input: 300,
      output: 30,
      reasoning: 0,
      cacheRead: 12400,
      cacheWrite: 400,
    });
    expect(s.peakStepInputTokens).toBe(6600);
  });

  it("sums every step, never the last one alone, and keeps reasoning apart from output", () => {
    const s = parseOpenCodeStream(
      withTokens([
        [7550, 40, 30, 0, 0],
        [7750, 61, 24, 0, 0],
      ])
    );
    expect(s.tokens).toEqual({
      input: 15300,
      output: 101,
      reasoning: 54,
      cacheRead: 0,
      cacheWrite: 0,
    });
    expect(s.peakStepInputTokens).toBe(7750);
  });
});

describe("opencode stream parser: failures are never success (#1637)", () => {
  const sample = testdata("opencode_stream_research_sample.jsonl");

  it("an auto-rejected tool call of the run's own session fails an exit-0 run", async () => {
    const summary = await classifyOpenCodeRun({
      stdout: testdata("opencode_auto_reject_stream.jsonl"),
      stderr: testdata("opencode_auto_reject_stderr.txt"),
      exitCode: 0,
      allowedTools: ["Bash"],
      dispatched: "lmstudio/qwen/qwen3.8-27b",
    });
    expect(summary.failure).toMatch(/^\[adapter-permission-rejected\] tool=bash: /);
    // The notice's patterns are the rejected call's input: never kept.
    expect(summary.failure).not.toContain("python3 calc.py");
  });

  it("a subagent's auto-reject fails the stage although its own session finished", async () => {
    const summary = await classifyOpenCodeRun({
      stdout: testdata("opencode_stream_subagent_capture.jsonl"),
      stderr: testdata("opencode_stream_subagent_stderr.txt"),
      exitCode: 0,
      allowedTools: ["Read", "Edit", "Task", "Bash"],
      dispatched: "lmstudio/qwen/qwen3.8-27b",
    });
    expect(summary.failure?.startsWith(`${OPENCODE_PERMISSION_REJECTED_MARKER} tool=bash:`)).toBe(
      true
    );
  });

  it("a rejected call with no stderr notice names the tool_use event's own tool (AC3, #1638 fix round)", async () => {
    // A "deny" match (the generated permission map's only rejection shape,
    // ADR-022 § 9) never prints the "auto-rejecting" notice, so the marker
    // must come from the rejected tool_use event's own part.tool ("bash" in
    // this fixture) instead of the unnamed "unknown" fallback, classified
    // differently by whether the stage's own allowed tools grant it.
    for (const tc of [
      { allowedTools: ["Bash"], want: /^\[adapter-permission-rejected\] tool=bash: / },
      { allowedTools: ["Read"], want: /^\[permission-denied\] tool=bash: / },
    ] as const) {
      const summary = await classifyOpenCodeRun({
        stdout: testdata("opencode_auto_reject_stream.jsonl"),
        stderr: "",
        exitCode: 0,
        allowedTools: tc.allowedTools,
        dispatched: "lmstudio/qwen/qwen3.8-27b",
      });
      expect(summary.failure).toMatch(tc.want);
    }
  });

  it('a real "deny"-only capture (no stderr notice at all) is never a success (AC3, #1638 fix round)', async () => {
    // opencode_deny_rejected_stream.jsonl/_stderr.txt is a real 1.18.30
    // capture of a "deny"-matched tool call: its stderr file is empty, the
    // shape the map's own rejection always takes (ADR-022 § 9's "pattern
    // matching" amendment). Go parity: TestOpenCodeDenyRejectedNeverSuccess.
    const stdout = testdata("opencode_deny_rejected_stream.jsonl");
    expect(stdout).not.toContain("auto-rejecting");
    for (const tc of [
      { allowedTools: ["Bash"], want: /^\[adapter-permission-rejected\] tool=bash: / },
      { allowedTools: ["Read"], want: /^\[permission-denied\] tool=bash: / },
    ] as const) {
      const summary = await classifyOpenCodeRun({
        stdout,
        stderr: "",
        exitCode: 0,
        allowedTools: tc.allowedTools,
        dispatched: "guard/stub-model",
      });
      expect(summary.failure).toMatch(tc.want);
    }
  });

  it("an error event fails the run", async () => {
    const error = JSON.stringify({
      type: "error",
      timestamp: 1789336543200,
      sessionID: "ses_fixture0000000000000000001",
      error: {
        name: "APIError",
        data: { message: "the model server returned 500", metadata: { url: "[REDACTED:url]" } },
      },
    });
    const summary = await classifyOpenCodeRun({
      stdout: `${sample.trim()}\n${error}\n`,
      stderr: "",
      exitCode: 0,
      dispatched: "lmstudio/qwen/qwen3.8-27b",
    });
    expect(
      summary.failure?.startsWith(
        `${OPENCODE_ERROR_EVENT_MARKER} the stream carried 1 error event(s): APIError`
      )
    ).toBe(true);
    // Usage is still what the steps spent.
    expect(summary.tokens.input).toBe(3089);
  });

  it("an exit-0 run with zero step_finish events fails", async () => {
    const noSteps = sample
      .split("\n")
      .filter((l) => !l.includes('"type":"step_finish"'))
      .join("\n");
    const summary = await classifyOpenCodeRun({
      stdout: noSteps,
      stderr: "",
      exitCode: 0,
      dispatched: "lmstudio/qwen/qwen3.8-27b",
    });
    expect(summary.failure).toBe(
      `${OPENCODE_DRIFT_MARKER} the run exited 0 without a step_finish event, so it recorded no token usage`
    );
  });

  it("an unknown event type or a step_finish missing tokens is a drift warning, not a crash", async () => {
    const last = sample.trim().split("\n").at(-1)!;
    const ev = JSON.parse(last) as { part: Record<string, unknown> };
    delete ev.part.tokens;
    const drifted = [
      sample.trim(),
      JSON.stringify(ev),
      '{"type":"step_summary","timestamp":1,"sessionID":"ses_fixture0000000000000000001","part":{}}',
    ].join("\n");
    const summary = await classifyOpenCodeRun({
      stdout: drifted,
      stderr: "",
      exitCode: 0,
      dispatched: "lmstudio/qwen/qwen3.8-27b",
    });
    expect(summary.failure).toBeUndefined();
    expect(summary.driftMarkers).toEqual([
      `${OPENCODE_DRIFT_MARKER} a step_finish event has no part.tokens`,
      `${OPENCODE_DRIFT_MARKER} unknown event type "step_summary"`,
    ]);
  });
});

describe("opencode subagent roll-up (#1637)", () => {
  it("folds the subagent fixture's children into the total and writes nothing to disk", async () => {
    const fx = EXPECTED.fixtures["opencode_stream_subagent_capture.jsonl"];
    const { helper, calls } = fakeHelper(fx.fold!.sessions);
    writes.calls.length = 0;
    const summary = await classifyOpenCodeRun({
      stdout: testdata("opencode_stream_subagent_capture.jsonl"),
      stderr: "",
      exitCode: 0,
      dispatched: fx.dispatched,
      fold: helper,
    });
    // Parent (the stream) plus each child's info.tokens.
    const [parent, ...children] = fx.fold!.sessions;
    const sum = (k: keyof ExpectedTokens) =>
      parent.tokens[k] + children.reduce((acc, c) => acc + c.tokens[k], 0);
    expect(summary.tokens).toEqual({
      input: sum("input"),
      output: sum("output"),
      reasoning: sum("reasoning"),
      cacheRead: sum("cache_read"),
      cacheWrite: sum("cache_write"),
    });
    expect(summary.tokens.input).toBe(49794);
    for (const child of children) {
      expect(calls).toContainEqual(["export", child.id, "--sanitize", "--pure"]);
    }
    expect(writes.calls).toEqual([]);
  });

  it("a failing export marks usage partial with a drift marker, never throws", async () => {
    const summary = await classifyOpenCodeRun({
      stdout: testdata("opencode_stream_subagent_capture.jsonl"),
      stderr: "",
      exitCode: 0,
      dispatched: "lmstudio/qwen/qwen3.8-27b",
      fold: async (args) => {
        if (args[0] === "db") {
          return '[{"id":"ses_fixture0000000000000000002","parent_id":"ses_fixture0000000000000000001"}]';
        }
        throw new Error("exit code 1");
      },
    });
    expect(summary.usagePartial).toBe(true);
    expect(summary.tokens.input).toBe(30632);
    expect(summary.driftMarkers).toContain(
      `${OPENCODE_DRIFT_MARKER} usage partial: exporting a subagent session failed: exit code 1`
    );
  });
});

describe("opencode stage cost (ADR-022 § 3, #1637)", () => {
  const usage = { input: 3089, output: 14, cacheRead: 0, cacheWrite: 0 };

  it("is a stamped 0 for a local provider", async () => {
    const summary = await classifyOpenCodeRun({
      stdout: testdata("opencode_stream_local_capture.jsonl"),
      stderr: "",
      exitCode: 0,
      dispatched: "lmstudio/qwen/qwen3.8-27b",
    });
    expect(summary.costUsd).toBe(0);
    expect(openCodeStageCostUsd("ollama/qwen3-coder:30b", usage)).toBe(0);
  });

  it("is undefined, never 0, for a hosted stage OpenCode reports at cost 0 and the registry cannot price", async () => {
    const zeroCost = testdata("opencode_stream_cloud_capture.jsonl").replace(
      /"cost":[0-9.]+/g,
      '"cost":0'
    );
    const summary = await classifyOpenCodeRun({
      stdout: zeroCost,
      stderr: "",
      exitCode: 0,
      dispatched: "openai/gpt-9-preview",
    });
    expect(summary.reportedCostUsd).toBe(0);
    expect(summary.costUsd).toBeUndefined();
    // An undeclared endpoint id normalizes to `other`: unstamped too.
    expect(openCodeStageCostUsd("lmstudio-remote/qwen/qwen3.8-27b", usage)).toBeUndefined();
  });

  it("prices a registry model from the registry, never from OpenCode's figure", async () => {
    const summary = await classifyOpenCodeRun({
      stdout: testdata("opencode_stream_cloud_capture.jsonl"),
      stderr: "",
      exitCode: 0,
      dispatched: "xai/grok-4.6",
    });
    expect(summary.reportedCostUsd).toBeCloseTo(0.006262, 12);
    expect(closeUsd(summary.costUsd, 0.00106454)).toBe(true);
  });
});

describe("opencode stderr redaction (ADR-022 § 22, #1637)", () => {
  const stdout = testdata("opencode_stream_research_sample.jsonl");

  it("removes credentials from stderr before it reaches the failure text", async () => {
    const longKey = `sk-${"A1b2C3d4".repeat(4)}`;
    const summary = await classifyOpenCodeRun({
      stdout,
      stderr:
        "ERROR service=provider key=sk-test123 fetch failed https://u:p@h/v1\n" +
        `ERROR Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123 and ${longKey}\n`,
      exitCode: 1,
      dispatched: "openai/gpt-5.5",
      redact: openCodeRedactor({ OPENAI_API_KEY: "sk-test123" }),
    });
    expect(summary.failure).toBeDefined();
    expect(summary.failure).not.toContain("sk-test123");
    expect(summary.failure).not.toContain("https://u:p@h");
    expect(summary.failure).not.toContain("abcdefghijklmnopqrstuvwxyz0123");
    expect(summary.failure).not.toContain(longKey);
    expect(summary.failure).toContain("[REDACTED:OPENAI_API_KEY]");
    expect(summary.failure).toContain("https://[REDACTED:userinfo]@h");
  });
});

describe("opencode stdout redaction (ADR-022 § 22, #1637)", () => {
  const sample = testdata("opencode_stream_research_sample.jsonl").trim();
  const SESSION = "ses_fixture0000000000000000001";
  // The child's own key, as a model that ran `env` in its bash tool could print it.
  const KEY = `sk-ant-api03-${"AbCdEfGh12".repeat(4)}`;
  const OTHER_KEY = `sk-proj-${"Zy9Xw8Vu7T".repeat(3)}`;
  const BEARER = "abcdefghijklmnop0123456789";

  function event(type: string, fields: Record<string, unknown>): string {
    return JSON.stringify({ type, timestamp: 1789336543200, sessionID: SESSION, ...fields });
  }

  it("removes a secret the child held and a credential shape from the model's text", async () => {
    const text = event("text", {
      part: {
        type: "text",
        text: `the key is ${KEY}, another is ${OTHER_KEY}\nAuthorization:\tBearer\t${BEARER}`,
      },
    });
    const summary = await classifyOpenCodeRun({
      stdout: `${sample}\n${text}\n`,
      stderr: "",
      exitCode: 0,
      dispatched: "anthropic/claude-sonnet-5",
      redact: openCodeRedactor({ ANTHROPIC_API_KEY: KEY }),
    });
    expect(summary.failure).toBeUndefined();
    const shown = summary.stream.displayText;
    expect(shown).not.toContain(KEY);
    expect(shown).not.toContain(OTHER_KEY);
    // A tab is `\t` in the event: only the decoded string shows the bearer shape.
    expect(shown).not.toContain(BEARER);
    expect(shown).toContain("the key is [REDACTED:ANTHROPIC_API_KEY]");
    expect(shown).toContain("[REDACTED:api-key]");
    expect(shown).toContain("[REDACTED:bearer-token]");
  });

  it("removes them from an error event's name and an unknown event type too", async () => {
    const stdout = [
      sample,
      event("error", { error: { name: `ProviderAuthError ${KEY}` } }),
      event(`step:${OTHER_KEY}`, { part: {} }),
    ].join("\n");
    const summary = await classifyOpenCodeRun({
      stdout,
      stderr: "",
      exitCode: 0,
      dispatched: "anthropic/claude-sonnet-5",
      redact: openCodeRedactor({ ANTHROPIC_API_KEY: KEY }),
    });
    expect(summary.failure).toContain("ProviderAuthError [REDACTED:ANTHROPIC_API_KEY]");
    expect(summary.failure).not.toContain(KEY);
    expect(summary.driftMarkers.join("\n")).not.toContain(OTHER_KEY.slice(0, 20));
    expect(summary.driftMarkers.join("\n")).toContain("[REDACTED:api-key]");
  });

  it("re-encodes only the strings it changed, so the line stays the same JSON event", () => {
    const line = event("text", { part: { type: "text", text: `a "quoted" ${KEY}`, n: 1.5 } });
    const out = redactOpenCodeLine(line, openCodeRedactor({ ANTHROPIC_API_KEY: KEY }));
    expect(JSON.parse(out)).toEqual({
      ...(JSON.parse(line) as object),
      part: { type: "text", text: 'a "quoted" [REDACTED:ANTHROPIC_API_KEY]', n: 1.5 },
    });
    // A line with nothing to redact is returned as it was.
    expect(redactOpenCodeLine(sample.split("\n")[0], openCodeRedactor({}))).toBe(
      sample.split("\n")[0]
    );
  });

  it("keeps a provider setting's value, as the Go manager does, and redacts its credential", () => {
    const secret = "fixture-aws-secret-access-value";
    const redact = openCodeRedactor({ AWS_REGION: "us-east-1", AWS_SECRET_ACCESS_KEY: secret });
    expect(redact(`region us-east-1 secret ${secret}`)).toBe(
      "region us-east-1 secret [REDACTED:AWS_SECRET_ACCESS_KEY]"
    );
  });
});
