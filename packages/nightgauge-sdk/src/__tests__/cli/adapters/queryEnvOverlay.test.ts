/**
 * A query's own `env` reaches the spawned agent's environment (#2908).
 *
 * The SDK workflow path marks a stage's implementation agent with
 * NIGHTGAUGE_REVIEW_UNITS_PHASE=1 so feature-dev's Phase 5 does not repeat the
 * review the units phase runs next. An adapter that dropped the variable would
 * review twice, so the CLI and Claude Agent SDK paths are checked here where
 * they build the env (opencode's is in tests/cli/opencodeAdapter.test.ts). The
 * variables only add: one the adapter sets itself keeps the adapter's value.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { EventEmitter } from "node:events";
import { spawn } from "node:child_process";
import { query as sdkQuery } from "@anthropic-ai/claude-agent-sdk";
import { createCliQueryFn } from "../../../cli/adapters/cliQueryHelper.js";
import { ClaudeSdkAdapter } from "../../../cli/adapters/ClaudeSdkAdapter.js";
import {
  REVIEW_UNITS_PHASE_ENV,
  REVIEW_UNITS_PHASE_MARKER,
} from "../../../cli/workflow/WorkflowSpec.js";
import type { SDKMessage, SDKQueryOptions } from "../../../orchestrator/StageExecutor.js";

vi.mock("node:child_process", () => ({ spawn: vi.fn() }));
vi.mock("@anthropic-ai/claude-agent-sdk", () => ({
  query: vi.fn(() => (async function* (): AsyncGenerator<SDKMessage> {})()),
}));
const spawnMock = vi.mocked(spawn);
const sdkQueryMock = vi.mocked(sdkQuery);

function childExiting(stdout: string) {
  const child = new EventEmitter() as any;
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.stdin = { write: vi.fn(), end: vi.fn(), on: vi.fn() };
  process.nextTick(() => {
    child.stdout.emit("data", Buffer.from(stdout));
    child.emit("close", 0);
  });
  return child;
}

async function drain(gen: AsyncGenerator<SDKMessage>): Promise<void> {
  for await (const _ of gen) {
    // drain
  }
}

/** The marker, plus an attempt to replace a variable the adapter sets itself. */
const OVERLAY = { ...REVIEW_UNITS_PHASE_MARKER, PATH: "/overlay/bin" };

describe("CLI query env overlay (#2908)", () => {
  afterEach(() => spawnMock.mockReset());

  it("adds the query's env to the curated child env, and adds nothing without one", async () => {
    spawnMock.mockImplementation(() => childExiting("done\n") as any);
    const query = createCliQueryFn({ command: "gemini", args: [], adapter: "gemini" });

    await drain(query({ prompt: "p", options: { cwd: process.cwd(), env: OVERLAY } }));
    await drain(query({ prompt: "p", options: { cwd: process.cwd() } }));

    const envs = spawnMock.mock.calls.map((c) => (c[2] as { env: NodeJS.ProcessEnv }).env);
    expect(envs).toHaveLength(2);
    expect(envs[0][REVIEW_UNITS_PHASE_ENV]).toBe("1");
    // The curated PATH wins over the query's.
    expect(envs[0].PATH).toBe(process.env.PATH);
    expect(envs[1][REVIEW_UNITS_PHASE_ENV]).toBeUndefined();
  });
});

describe("Claude Agent SDK env overlay (#2908)", () => {
  afterEach(() => sdkQueryMock.mockClear());

  async function callAdapterQuery(q: SDKQueryOptions): Promise<SDKQueryOptions> {
    const query = await new ClaudeSdkAdapter().createQueryFunction();
    await drain(query(q));
    return sdkQueryMock.mock.calls.at(-1)![0] as unknown as SDKQueryOptions;
  }

  it("hands the SDK this process's env with the query's added, since its env replaces", async () => {
    const env = (await callAdapterQuery({ prompt: "p", options: { env: OVERLAY } })).options?.env;
    expect(env?.[REVIEW_UNITS_PHASE_ENV]).toBe("1");
    expect(env?.PATH).toBe(process.env.PATH);
  });

  it("leaves a query without env untouched, so the SDK keeps its default", async () => {
    const q: SDKQueryOptions = { prompt: "p", options: { cwd: "/x" } };
    expect(await callAdapterQuery(q)).toBe(q);
  });
});
