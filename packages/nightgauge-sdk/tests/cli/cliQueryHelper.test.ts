import { describe, it, expect } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { parseCliArgs, selectCodexOutput } from "../../src/cli/adapters/cliQueryHelper.js";
import {
  summarizeCodexJsonOutput,
  summarizeGeminiStreamJsonOutput,
} from "../../src/cli/adapterQuery.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

describe("parseCliArgs", () => {
  it("parses space-separated string into array", () => {
    const result = parseCliArgs("--print --output-format text", ["--default"]);

    expect(result).toEqual(["--print", "--output-format", "text"]);
  });

  it("returns fallback for undefined input", () => {
    const fallback = ["--print", "--output-format", "text"];

    const result = parseCliArgs(undefined, fallback);

    expect(result).toEqual(fallback);
  });

  it("returns fallback for empty string", () => {
    const fallback = ["--default-flag"];

    const result = parseCliArgs("", fallback);

    expect(result).toEqual(fallback);
  });

  it("handles extra whitespace gracefully", () => {
    const result = parseCliArgs("  --print   --text  ", ["--fallback"]);

    expect(result).toEqual(["--print", "--text"]);
  });
});

describe("summarizeCodexJsonOutput with fixtures", () => {
  let successFixture: string;
  let failureFixture: string;

  async function loadFixture(filename: string): Promise<string> {
    return fs.readFile(path.join(__dirname, "fixtures", filename), "utf-8");
  }

  it("reports no failure for successful pipeline output", async () => {
    successFixture = await loadFixture("codex-jsonl-success.txt");

    const summary = summarizeCodexJsonOutput(successFixture);

    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.failureReason).toBeUndefined();
    expect(summary.displayText).toContain(
      "All tests passing. Photo upload feature implemented and context file written successfully."
    );
  });

  it("detects explicit failure and captures reason from failure output", async () => {
    failureFixture = await loadFixture("codex-jsonl-failure.txt");

    const summary = summarizeCodexJsonOutput(failureFixture);

    expect(summary.hasExplicitFailure).toBe(true);
    expect(summary.failureReason).toContain("Execution halted");
  });

  it("extracts the last agent message as displayText from success output", async () => {
    successFixture = await loadFixture("codex-jsonl-success.txt");

    const summary = summarizeCodexJsonOutput(successFixture);

    expect(summary.displayText).toBe(
      "All tests passing. Photo upload feature implemented and context file written successfully."
    );
  });
});

describe("summarizeCodexJsonOutput usage parsing (#4027)", () => {
  async function loadFixture(filename: string): Promise<string> {
    return fs.readFile(path.join(__dirname, "fixtures", filename), "utf-8");
  }

  it("extracts real token usage from the turn.completed event in the success fixture", async () => {
    const fixture = await loadFixture("codex-jsonl-success.txt");

    const summary = summarizeCodexJsonOutput(fixture);

    // Fixture turn.completed: input_tokens 13246 (cache-inclusive),
    // cached_input_tokens 7296, output_tokens 5. The cached subset is stored as
    // cache_read_input_tokens and SUBTRACTED out of input_tokens (13246-7296)
    // so the two pools are disjoint — total stays 13246 + 5, never 20547.
    expect(summary.usage).toEqual({
      input_tokens: 5950,
      output_tokens: 5,
      cache_read_input_tokens: 7296,
      cache_creation_input_tokens: 0,
    });
  });

  it("returns undefined usage when turn.completed carries no usage payload", async () => {
    // The failure fixture ends with a bare {"type":"turn.completed"} (no usage).
    const fixture = await loadFixture("codex-jsonl-failure.txt");

    const summary = summarizeCodexJsonOutput(fixture);

    expect(summary.usage).toBeUndefined();
  });

  it("returns undefined usage when no turn.completed event exists", () => {
    const output = [
      JSON.stringify({ type: "thread.started", thread_id: "t1" }),
      JSON.stringify({
        type: "item.completed",
        item: { id: "i1", type: "agent_message", text: "Done." },
      }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);

    expect(summary.usage).toBeUndefined();
  });

  it("defaults missing individual usage fields to 0", () => {
    const output = JSON.stringify({
      type: "turn.completed",
      usage: { input_tokens: 800 },
    });

    const summary = summarizeCodexJsonOutput(output);

    expect(summary.usage).toEqual({
      input_tokens: 800,
      output_tokens: 0,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    });
  });

  it("sums non-cached usage across multiple turn.completed events (e.g. exec resume)", () => {
    const output = [
      JSON.stringify({
        type: "turn.completed",
        usage: { input_tokens: 100, cached_input_tokens: 10, output_tokens: 20 },
      }),
      JSON.stringify({
        type: "turn.completed",
        usage: { input_tokens: 200, cached_input_tokens: 30, output_tokens: 40 },
      }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);

    // Non-cached input per turn: (100-10)+(200-30)=260; cached: 10+30=40.
    expect(summary.usage).toEqual({
      input_tokens: 260,
      output_tokens: 60,
      cache_read_input_tokens: 40,
      cache_creation_input_tokens: 0,
    });
  });

  it("ignores non-numeric usage fields rather than fabricating counts", () => {
    const output = JSON.stringify({
      type: "turn.completed",
      usage: { input_tokens: "lots", output_tokens: 12 },
    });

    const summary = summarizeCodexJsonOutput(output);

    expect(summary.usage).toEqual({
      input_tokens: 0,
      output_tokens: 12,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    });
  });

  it("clamps a malformed cached > input payload and never goes negative", () => {
    const output = JSON.stringify({
      type: "turn.completed",
      usage: { input_tokens: 100, cached_input_tokens: 250, output_tokens: 5 },
    });

    const summary = summarizeCodexJsonOutput(output);

    // cached is clamped to the prompt total (100), leaving 0 non-cached input.
    expect(summary.usage).toEqual({
      input_tokens: 0,
      output_tokens: 5,
      cache_read_input_tokens: 100,
      cache_creation_input_tokens: 0,
    });
  });

  it("coerces negative token counts to 0", () => {
    const output = JSON.stringify({
      type: "turn.completed",
      usage: { input_tokens: -100, cached_input_tokens: -10, output_tokens: -5 },
    });

    const summary = summarizeCodexJsonOutput(output);

    expect(summary.usage).toEqual({
      input_tokens: 0,
      output_tokens: 0,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    });
  });
});

describe("selectCodexOutput", () => {
  const baseJsonlSummary = {
    displayText: "JSONL fallback text",
    hasExplicitFailure: false,
    failureReason: undefined,
  };

  it("returns file content when non-empty", () => {
    const result = selectCodexOutput("  Direct file output.  ", baseJsonlSummary);

    expect(result).toBe("Direct file output.");
  });

  it("returns JSONL displayText when file content is empty string", () => {
    const result = selectCodexOutput("", baseJsonlSummary);

    expect(result).toBe("JSONL fallback text");
  });

  it("returns JSONL displayText when file content is whitespace-only", () => {
    const result = selectCodexOutput("   \n  ", baseJsonlSummary);

    expect(result).toBe("JSONL fallback text");
  });

  it("returns JSONL displayText when file content is undefined (file missing)", () => {
    const result = selectCodexOutput(undefined, baseJsonlSummary);

    expect(result).toBe("JSONL fallback text");
  });

  it("trims file content before returning", () => {
    const result = selectCodexOutput("\n  Leading and trailing whitespace.\n", baseJsonlSummary);

    expect(result).toBe("Leading and trailing whitespace.");
  });
});

describe("summarizeGeminiStreamJsonOutput with fixtures", () => {
  async function loadFixture(filename: string): Promise<string> {
    return fs.readFile(path.join(__dirname, "fixtures", filename), "utf-8");
  }

  it("reports no failure for successful pipeline output", async () => {
    const fixture = await loadFixture("gemini-stream-json-success.txt");

    const summary = summarizeGeminiStreamJsonOutput(fixture);

    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.failureReason).toBeUndefined();
    expect(summary.displayText).toContain(
      "All tests passing. Photo upload feature implemented and context file written successfully."
    );
  });

  it("detects explicit failure from error events and result status", async () => {
    const fixture = await loadFixture("gemini-stream-json-failure.txt");

    const summary = summarizeGeminiStreamJsonOutput(fixture);

    expect(summary.hasExplicitFailure).toBe(true);
    expect(summary.failureReason).toBeDefined();
  });

  it("extracts the last non-delta assistant message as displayText", async () => {
    const fixture = await loadFixture("gemini-stream-json-success.txt");

    const summary = summarizeGeminiStreamJsonOutput(fixture);

    expect(summary.displayText).toBe(
      "All tests passing. Photo upload feature implemented and context file written successfully."
    );
  });

  it("handles mixed JSON and non-JSON lines gracefully", () => {
    const mixed = [
      "some plain text line",
      '{"type":"message","timestamp":"2026-01-01T00:00:00Z","role":"assistant","content":"Hello from Gemini"}',
      "another plain line",
      '{"type":"result","timestamp":"2026-01-01T00:00:01Z","status":"success"}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(mixed);

    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toBe("Hello from Gemini");
  });

  it("detects failure from result status error with error message", () => {
    const output = [
      '{"type":"message","timestamp":"2026-01-01T00:00:00Z","role":"assistant","content":"Working on it...","delta":true}',
      '{"type":"result","timestamp":"2026-01-01T00:00:01Z","status":"error","error":{"type":"MaxSessionTurnsError","message":"Maximum session turns exceeded"}}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.hasExplicitFailure).toBe(true);
    expect(summary.failureReason).toBe("Maximum session turns exceeded");
  });

  it("treats error events as recovered when result status is success", () => {
    const output = [
      '{"type":"error","timestamp":"2026-01-01T00:00:00Z","severity":"error","message":"Authentication failed"}',
      '{"type":"message","timestamp":"2026-01-01T00:00:01Z","role":"assistant","content":"Recovered and completed."}',
      '{"type":"result","timestamp":"2026-01-01T00:00:02Z","status":"success"}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toBe("Recovered and completed.");
  });

  it("detects failure from error events when result status is not success", () => {
    const output = [
      '{"type":"error","timestamp":"2026-01-01T00:00:00Z","severity":"error","message":"Authentication failed"}',
      '{"type":"result","timestamp":"2026-01-01T00:00:01Z","status":"error"}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.hasExplicitFailure).toBe(true);
  });

  it("extracts token usage from success fixture stats field", async () => {
    const fixture = await loadFixture("gemini-stream-json-success.txt");

    const summary = summarizeGeminiStreamJsonOutput(fixture);

    expect(summary.usage).toEqual({
      input_tokens: 800,
      output_tokens: 700,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    });
  });

  it("extracts token usage from failure fixture stats field", async () => {
    const fixture = await loadFixture("gemini-stream-json-failure.txt");

    const summary = summarizeGeminiStreamJsonOutput(fixture);

    expect(summary.usage).toEqual({
      input_tokens: 500,
      output_tokens: 400,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    });
  });

  it("returns undefined usage when stats field is absent", () => {
    const output = ['{"type":"result","timestamp":"2026-01-01T00:00:01Z","status":"success"}'].join(
      "\n"
    );

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.usage).toBeUndefined();
  });

  it("defaults missing individual token fields to 0", () => {
    const output = [
      '{"type":"result","timestamp":"2026-01-01T00:00:01Z","status":"success","stats":{"total_tokens":100}}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.usage).toEqual({
      input_tokens: 0,
      output_tokens: 0,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    });
  });

  it("returns undefined usage when no result event exists", () => {
    const output = [
      '{"type":"message","timestamp":"2026-01-01T00:00:00Z","role":"assistant","content":"Hello"}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.usage).toBeUndefined();
  });

  it("subtracts the cached subset out of the cache-inclusive input (#4036)", () => {
    const output = [
      '{"type":"result","timestamp":"2026-01-01T00:00:01Z","status":"success","stats":{"input_tokens":100,"output_tokens":50,"cached":25}}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    // Gemini's input_tokens is cache-INCLUSIVE; store only the non-cached
    // remainder so input/cache pools stay disjoint in totalTokens() — no
    // double-count (mirrors the #4027 Codex convention).
    expect(summary.usage).toEqual({
      input_tokens: 75, // 100 prompt - 25 cached
      output_tokens: 50,
      cache_read_input_tokens: 25,
      cache_creation_input_tokens: 0,
    });
  });

  it("clamps a malformed Gemini cached > input and never goes negative (#4036)", () => {
    const output = [
      '{"type":"result","timestamp":"2026-01-01T00:00:01Z","status":"success","stats":{"input_tokens":500,"output_tokens":50,"cached":900}}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.usage?.input_tokens).toBe(0);
    expect(summary.usage?.cache_read_input_tokens).toBe(500);
  });

  it("falls back to stats.input when input_tokens is absent", () => {
    const output = [
      '{"type":"result","timestamp":"2026-01-01T00:00:01Z","status":"success","stats":{"input":200,"output_tokens":75}}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.usage!.input_tokens).toBe(200);
  });

  it("ignores warning-severity error events", () => {
    const output = [
      '{"type":"error","timestamp":"2026-01-01T00:00:00Z","severity":"warning","message":"Loop detected"}',
      '{"type":"message","timestamp":"2026-01-01T00:00:01Z","role":"assistant","content":"Completed successfully."}',
      '{"type":"result","timestamp":"2026-01-01T00:00:02Z","status":"success"}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toBe("Completed successfully.");
  });

  it("skips delta messages when extracting displayText", () => {
    const output = [
      '{"type":"message","timestamp":"2026-01-01T00:00:00Z","role":"assistant","content":"partial...","delta":true}',
      '{"type":"message","timestamp":"2026-01-01T00:00:01Z","role":"assistant","content":"Final complete message."}',
      '{"type":"result","timestamp":"2026-01-01T00:00:02Z","status":"success"}',
    ].join("\n");

    const summary = summarizeGeminiStreamJsonOutput(output);

    expect(summary.displayText).toBe("Final complete message.");
  });
});
