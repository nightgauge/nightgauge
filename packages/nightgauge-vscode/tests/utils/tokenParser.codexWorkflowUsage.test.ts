/**
 * Codex token capture, end-to-end over a REAL SDK-CLI stdout transcript.
 *
 * Every codex-adapter stage on record booked zero tokens (#111). Codex is not
 * spawned by the extension directly — `skillRunner` runs `node <sdk-cli> stage
 * <stage> <issue>`, so what it parses is the SDK CLI's workflow-node stream,
 * not Codex's own `turn.completed` envelope. The existing unit tests assert the
 * parser against a hand-written node shape; this file pins the whole envelope
 * instead.
 *
 * The transcript below was CAPTURED, not authored: the repo's real captured
 * Codex JSONL (`nightgauge-sdk/tests/cli/fixtures/codex-jsonl-success.txt`,
 * `turn.completed` usage `input_tokens: 13246, cached_input_tokens: 7296,
 * output_tokens: 5`) was replayed through `CodexAdapter` → `TokenTracker` →
 * `PipelineRunEmitter` → `OutputFormatter('json')`. So the numbers here are the
 * genuine cache-disjoint normalization (13246 - 7296 = 5950 non-cached input)
 * arriving in the genuine emission shape, and a drift in ANY link of that chain
 * breaks this test.
 *
 * @see Issue #111 — codex token/cost capture verification
 */

import { describe, it, expect } from "vitest";
import {
  parseStreamJsonLine,
  extractTokenUsage,
  resolveStageBookedUsage,
  TokenAccumulator,
  LiveStageEstimator,
} from "../../src/utils/tokenParser";

/** Stage-start seed: the agent node exists but nothing has been spent yet. */
const AGENT_RUNNING_ZERO_USAGE =
  '{"schemaVersion":4,"kind":"agent","nodeId":"agent:111:feature-dev",' +
  '"parentId":"phase:111:feature-dev","seq":1,"ts":"2026-07-26T00:35:39.631Z",' +
  '"status":"running","agentId":"feature-dev","provider":"codex","usage":' +
  '{"inputTokens":0,"outputTokens":0,"cacheReadTokens":0,"cacheCreationTokens":0,' +
  '"costUsd":0,"estimated":false},"label":"feature-dev"}';

/** Terminal node: the authoritative stage total folded in from the TokenTracker. */
const AGENT_SUCCEEDED_WITH_USAGE =
  '{"schemaVersion":4,"kind":"agent","nodeId":"agent:111:feature-dev",' +
  '"parentId":"phase:111:feature-dev","seq":3,"ts":"2026-07-26T00:35:39.669Z",' +
  '"status":"succeeded","agentId":"feature-dev","provider":"codex","usage":' +
  '{"inputTokens":5950,"outputTokens":5,"cacheReadTokens":7296,"cacheCreationTokens":0,' +
  '"costUsd":0,"estimated":true},"terminalKind":"success","label":"feature-dev"}';

const PHASE_RUNNING =
  '{"schemaVersion":4,"kind":"phase","nodeId":"phase:111:feature-dev","parentId":"run:111",' +
  '"seq":0,"ts":"2026-07-26T00:35:39.628Z","status":"running","name":"feature-dev",' +
  '"index":3,"total":6,"label":"feature-dev"}';

const PHASE_SUCCEEDED =
  '{"schemaVersion":4,"kind":"phase","nodeId":"phase:111:feature-dev","parentId":"run:111",' +
  '"seq":4,"ts":"2026-07-26T00:35:39.669Z","status":"succeeded","name":"feature-dev",' +
  '"index":3,"total":6,"label":"feature-dev"}';

/** The full stdout a `stage` invocation writes for a successful Codex stage. */
const CODEX_STAGE_STDOUT = [
  PHASE_RUNNING,
  AGENT_RUNNING_ZERO_USAGE,
  AGENT_SUCCEEDED_WITH_USAGE,
  PHASE_SUCCEEDED,
].join("\n");

describe("codex stage token capture (real SDK-CLI transcript)", () => {
  it("books the terminal workflow-agent total and prices it off the codex rate card", () => {
    const accumulator = new TokenAccumulator("codex", "gpt-5.6-terra");
    const estimator = new LiveStageEstimator("codex", "gpt-5.6-terra");

    for (const line of CODEX_STAGE_STDOUT.split("\n")) {
      const parsed = parseStreamJsonLine(line);
      if (parsed?.usage) accumulator.add(parsed.usage);
      if (parsed?.incrementalUsage) estimator.observe(parsed.incrementalUsage);
    }

    expect(accumulator.hasTokens()).toBe(true);
    const booked = resolveStageBookedUsage(accumulator, estimator);
    expect(booked?.estimated).toBe(false);
    // Codex reports no native cost, so the budget enforcer only sees non-zero
    // spend if the rate card resolves: 5950 in @ $2.50/Mtok + 5 out @ $15/Mtok.
    expect(booked?.usage).toEqual({
      inputTokens: 5950,
      outputTokens: 5,
      cacheReadTokens: 7296,
      cacheCreationTokens: 0,
      costUsd: 0.01495,
      costSource: "computed",
    });
  });

  it("does not treat the zero-usage stage-start snapshot as an observed burn", () => {
    // The stage-start seed is the ONLY non-terminal snapshot a Codex stage
    // emits in practice — its later non-zero progress tick lands inside the
    // EventBus 1 Hz coalescing window and is dropped. Counting that empty
    // snapshot as "observed" would make a stage killed before its terminal node
    // book an estimated $0.0000 instead of recording nothing observed.
    const parsed = parseStreamJsonLine(AGENT_RUNNING_ZERO_USAGE);
    expect(parsed?.type).toBe("assistant");
    expect(parsed?.incrementalUsage).toBeUndefined();
    expect(parsed?.usage).toBeUndefined();

    const estimator = new LiveStageEstimator("codex", "gpt-5.6-terra");
    if (parsed?.incrementalUsage) estimator.observe(parsed.incrementalUsage);
    expect(estimator.hasObserved()).toBe(false);
    expect(resolveStageBookedUsage(new TokenAccumulator("codex", "gpt-5.6-terra"), estimator)).toBe(
      undefined
    );
  });

  it("still surfaces a non-terminal snapshot once real tokens are reported", () => {
    const parsed = parseStreamJsonLine(
      AGENT_RUNNING_ZERO_USAGE.replace(
        '"inputTokens":0,"outputTokens":0,"cacheReadTokens":0',
        '"inputTokens":4000,"outputTokens":120,"cacheReadTokens":900'
      )
    );

    expect(parsed?.usage).toBeUndefined();
    expect(parsed?.incrementalUsage).toEqual({
      inputTokens: 4000,
      outputTokens: 120,
      cacheReadTokens: 900,
      cacheCreationTokens: 0,
      costUsd: 0,
    });
  });

  it("rescues the terminal workflow-agent usage from the raw stdout tail", () => {
    // The #2919 safety net fires when streaming line-parsing missed the
    // terminal envelope on an exit-0 stage. It only knew the Claude CLI's
    // `type:"result"` shape, so an SDK-routed stage still booked zeros there.
    expect(extractTokenUsage(CODEX_STAGE_STDOUT)).toEqual({
      inputTokens: 5950,
      outputTokens: 5,
      cacheReadTokens: 7296,
      cacheCreationTokens: 0,
      costUsd: 0,
    });
  });
});
