import { describe, expect, it } from "vitest";
import { isGrokAuthMessage, isGrokQuotaMessage, summarizeGrokStream } from "../grokStream.js";
import { grokCliEffortFlag, mapGrokEffortToNightgauge } from "../grokEffort.js";

describe("grok stream summary", () => {
  it("parses streaming-json text + end usage", () => {
    const stdout = [
      `{"type":"text","data":"hello "}`,
      `{"type":"text","data":"world"}`,
      `{"type":"end","sessionId":"abc","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":0,"reasoning_tokens":3},"total_cost_usd":0.012}`,
    ].join("\n");
    const s = summarizeGrokStream(stdout);
    expect(s.displayText).toBe("hello world");
    expect(s.sessionId).toBe("abc");
    expect(s.usage.input_tokens).toBe(10);
    expect(s.usage.output_tokens).toBe(4);
    expect(s.usage.cache_read_input_tokens).toBe(2);
    expect(s.usage.reasoning_tokens).toBe(3);
    expect(s.totalCostUsd).toBe(0.012);
    expect(s.costIsPartial).toBe(false);
  });

  it("omits cost when partial", () => {
    const s = summarizeGrokStream(
      `{"type":"end","usage":{"input_tokens":1,"output_tokens":1},"cost_is_partial":true,"total_cost_usd":0}`
    );
    expect(s.totalCostUsd).toBeUndefined();
    expect(s.costIsPartial).toBe(true);
  });

  it("flags quota and auth failures", () => {
    const quota = summarizeGrokStream(`{"type":"error","message":"weekly usage pool exhausted"}`);
    expect(quota.isQuotaExhausted).toBe(true);
    expect(quota.hasExplicitFailure).toBe(true);
    const auth = summarizeGrokStream(`{"type":"error","message":"please run grok login"}`);
    expect(auth.isAuthFailure).toBe(true);
  });
});

describe("grok quota/auth detectors", () => {
  it("matches pool wording", () => {
    expect(isGrokQuotaMessage("weekly pool reset tomorrow")).toBe(true);
    expect(isGrokAuthMessage("Authentication failed")).toBe(true);
    expect(isGrokQuotaMessage("all tests passed")).toBe(false);
  });
});

describe("grok effort mapping", () => {
  it("collapses none/minimal onto low", () => {
    expect(mapGrokEffortToNightgauge("none")).toBe("low");
    expect(mapGrokEffortToNightgauge("minimal")).toBe("low");
    expect(mapGrokEffortToNightgauge("xhigh")).toBe("xhigh");
  });

  it("forwards vendor flags", () => {
    expect(grokCliEffortFlag("none")).toBe("none");
    expect(grokCliEffortFlag("high")).toBe("high");
    expect(grokCliEffortFlag("nope")).toBeUndefined();
  });
});
