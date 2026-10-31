/**
 * The extension's plugin gate reads the same real captures as
 * internal/execution/claude_plugin_gate_test.go (#2723).
 */
import { describe, it, expect } from "vitest";
import * as fs from "fs";
import * as path from "path";
import { parseStreamJsonLine } from "../../src/utils/tokenParser";
import {
  ClaudePluginGate,
  GATE_PLUGIN_MISSING_MARKER,
  GATE_PLUGIN_WARNING_MARKER,
  evaluateClaudePluginInit,
} from "../../src/utils/claudePluginGate";

const FIXTURES = path.resolve(__dirname, "../../../../internal/execution/testdata");

function stream(name: string) {
  return fs
    .readFileSync(path.join(FIXTURES, name), "utf8")
    .split("\n")
    .map((line) => parseStreamJsonLine(line))
    .filter((m) => m !== null);
}

describe("evaluateClaudePluginInit", () => {
  const cases: Array<[string, Parameters<typeof evaluateClaudePluginInit>[1], string]> = [
    ["no entry", [{ name: "cc-plugin-telemetry" }], "no nightgauge plugin"],
    ["a prefix of the name", [{ name: "nightgauge-extras" }], "no nightgauge plugin"],
    ["a duplicate", [{ name: "nightgauge" }, { name: "nightgauge" }], "2 nightgauge plugins"],
    ["no list", undefined, "no plugins list"],
    ["no init before work", null, "before any system/init"],
  ];

  it.each(cases)("stops a bundled build and warns otherwise on %s", (_name, plugins, reason) => {
    expect(evaluateClaudePluginInit(true, plugins)).toEqual({
      verdict: "stop",
      reason: expect.stringContaining(reason),
    });
    expect(evaluateClaudePluginInit(false, plugins).verdict).toBe("warn");
  });

  it("passes on exactly one entry and reports its version", () => {
    expect(evaluateClaudePluginInit(true, [{ name: "nightgauge", version: "1.25.0" }])).toEqual({
      verdict: "pass",
      version: "1.25.0",
    });
  });
});

describe("ClaudePluginGate on captured streams", () => {
  it("passes the present capture and keeps its version", () => {
    const gate = new ClaudePluginGate(true);
    expect(stream("claude_init_plugin_present.jsonl").map((m) => gate.observe(m))).not.toContain(
      true
    );
    expect(gate.stopped).toBe(false);
    expect(gate.version).toBe("1.25.0");
    expect(gate.notice).toBeUndefined();
  });

  it("stops on the absent capture's init, past the hook_started before it", () => {
    const gate = new ClaudePluginGate(true);
    const messages = stream("claude_init_plugin_absent.jsonl");
    expect(messages[0].subtype).toBe("hook_started");
    expect(gate.observe(messages[0])).toBe(false);
    expect(gate.observe(messages[1])).toBe(true);
    expect(gate.notice).toContain(GATE_PLUGIN_MISSING_MARKER);
  });

  it("warns on the absent capture with no bundle, without the stop marker", () => {
    const gate = new ClaudePluginGate(false);
    expect(stream("claude_init_plugin_absent.jsonl").map((m) => gate.observe(m))).not.toContain(
      true
    );
    expect(gate.notice).toContain(GATE_PLUGIN_WARNING_MARKER);
    expect(gate.notice).not.toContain(GATE_PLUGIN_MISSING_MARKER);
  });

  it("stops on work with no init", () => {
    const gate = new ClaudePluginGate(true);
    expect(gate.observe({ type: "assistant" })).toBe(true);
  });

  it("lets only the first init decide", () => {
    const gate = new ClaudePluginGate(true);
    gate.observe(stream("claude_init_plugin_present.jsonl")[1]);
    expect(gate.observe(stream("claude_init_plugin_absent.jsonl")[1])).toBe(false);
    expect(gate.stopped).toBe(false);
  });
});
