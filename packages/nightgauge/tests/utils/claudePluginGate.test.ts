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
  CLAUDE_CLI_FLOOR,
} from "../../src/utils/claudePluginGate";

const FIXTURES = path.resolve(__dirname, "../../../../internal/execution/testdata");

/** Stands in for the bundled plugin; fixtures redact each plugin's path. */
const BUNDLE = "/bundle/claude-plugins/nightgauge";
const OPTS = { expectedDir: BUNDLE };

function stream(name: string) {
  return fs
    .readFileSync(path.join(FIXTURES, name), "utf8")
    .replaceAll("<redacted>", BUNDLE)
    .split("\n")
    .map((line) => parseStreamJsonLine(line))
    .filter((m) => m !== null);
}

describe("ClaudePluginGate.takeNotice", () => {
  it("warns once when work with no init decides a build with no bundle", () => {
    const gate = new ClaudePluginGate(false);
    expect(gate.observe({ type: "assistant" })).toBe(false);
    expect(gate.takeNotice()).toContain(GATE_PLUGIN_WARNING_MARKER);
    expect(gate.takeNotice()).toBeUndefined();
  });

  it("does not repeat the warning on a later subagent init", () => {
    const gate = new ClaudePluginGate(false);
    gate.observe({ type: "system", subtype: "init", initPlugins: [] });
    expect(gate.takeNotice()).toContain(GATE_PLUGIN_WARNING_MARKER);
    gate.observe({ type: "system", subtype: "init", initPlugins: [] });
    expect(gate.takeNotice()).toBeUndefined();
  });

  it("emits nothing before a decision or on a pass", () => {
    const gate = new ClaudePluginGate(true, BUNDLE);
    expect(gate.takeNotice()).toBeUndefined();
    gate.observe({
      type: "system",
      subtype: "init",
      initPlugins: [{ name: "nightgauge", path: BUNDLE }],
    });
    expect(gate.takeNotice()).toBeUndefined();
  });
});

describe("the floor capture (#2807)", () => {
  it("is from min_version and passes a bundled gate", () => {
    const name = "claude_init_plugin_floor.jsonl";
    const manifest = JSON.parse(
      fs.readFileSync(
        path.resolve(FIXTURES, "../../adaptercompat/manifests/claude-headless.json"),
        "utf8"
      )
    );
    const init = JSON.parse(fs.readFileSync(path.join(FIXTURES, name), "utf8").split("\n")[0]);
    expect(init.claude_code_version).toBe(manifest.min_version);

    const gate = new ClaudePluginGate(true, BUNDLE);
    expect(stream(name).some((m) => gate.observe(m))).toBe(false);
    expect(gate.stopped).toBe(false);
    expect(gate.version).toBeDefined();
  });
});

describe("evaluateClaudePluginInit", () => {
  const cases: Array<[string, Parameters<typeof evaluateClaudePluginInit>[1], string]> = [
    ["no entry", [{ name: "cc-plugin-telemetry" }], "no nightgauge plugin"],
    ["a prefix of the name", [{ name: "nightgauge-extras" }], "no nightgauge plugin"],
    ["a duplicate", [{ name: "nightgauge" }, { name: "nightgauge" }], "2 nightgauge plugins"],
    ["no list", undefined, "no plugins list"],
    ["no init before work", null, "before any system/init"],
  ];

  it.each(cases)("stops a bundled build and warns otherwise on %s", (_name, plugins, reason) => {
    expect(evaluateClaudePluginInit(true, plugins, OPTS)).toEqual({
      verdict: "stop",
      reason: expect.stringContaining(reason),
    });
    expect(evaluateClaudePluginInit(false, plugins).verdict).toBe("warn");
  });

  it("passes on exactly one entry and reports its version", () => {
    expect(
      evaluateClaudePluginInit(
        true,
        [{ name: "nightgauge", version: "1.25.0", path: BUNDLE }],
        OPTS
      )
    ).toEqual({
      verdict: "pass",
      version: "1.25.0",
    });
  });
});

describe("ClaudePluginGate on captured streams", () => {
  it("passes the present capture and keeps its version", () => {
    const gate = new ClaudePluginGate(true, BUNDLE);
    expect(stream("claude_init_plugin_present.jsonl").map((m) => gate.observe(m))).not.toContain(
      true
    );
    expect(gate.stopped).toBe(false);
    expect(gate.version).toBe("1.25.0");
    expect(gate.notice).toBeUndefined();
  });

  it("stops on the absent capture's init, past the hook_started before it", () => {
    const gate = new ClaudePluginGate(true, BUNDLE);
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
    const gate = new ClaudePluginGate(true, BUNDLE);
    expect(gate.observe({ type: "assistant" })).toBe(true);
  });

  it("lets only the first init decide", () => {
    const gate = new ClaudePluginGate(true, BUNDLE);
    gate.observe(stream("claude_init_plugin_present.jsonl")[1]);
    expect(gate.observe(stream("claude_init_plugin_absent.jsonl")[1])).toBe(false);
    expect(gate.stopped).toBe(false);
  });
});

describe("bundled-build gaps (#2883)", () => {
  it("holds CLAUDE_CLI_FLOOR to the claude-headless manifest", () => {
    const manifest = JSON.parse(
      fs.readFileSync(
        path.resolve(FIXTURES, "../../adaptercompat/manifests/claude-headless.json"),
        "utf8"
      )
    );
    expect(CLAUDE_CLI_FLOOR).toBe(manifest.min_version);
  });

  it("stops a single entry from another copy and names its source and path", () => {
    const got = evaluateClaudePluginInit(
      true,
      [{ name: "nightgauge", source: "nightgauge@inline", path: "/elsewhere/nightgauge" }],
      OPTS
    );
    expect(got.verdict).toBe("stop");
    expect(got.reason).toContain("nightgauge@inline");
    expect(got.reason).toContain("/elsewhere/nightgauge");
    expect(got.reason).toContain(BUNDLE);
    expect(
      evaluateClaudePluginInit(false, [{ name: "nightgauge", path: "/elsewhere/nightgauge" }])
        .verdict
    ).toBe("pass");
  });

  it("stops a packaged build whose bundle has no plugin hooks", () => {
    const gate = new ClaudePluginGate(true, undefined);
    expect(gate.observe(stream("claude_init_plugin_present.jsonl")[1])).toBe(true);
    expect(gate.notice).toContain("hooks/hooks.json");
  });

  it.each([
    ["2.0.1", "Claude CLI 2.0.1 is below the supported floor"],
    [undefined, "reported no version"],
  ])("names the floor on a stop from CLI %s", (cli, text) => {
    const gate = new ClaudePluginGate(true, BUNDLE);
    gate.observe({ type: "system", subtype: "init", initPlugins: [], claudeCodeVersion: cli });
    expect(gate.notice).toContain(GATE_PLUGIN_MISSING_MARKER);
    expect(gate.notice).toContain(text);
    expect(gate.notice).toContain(`upgrade Claude Code to ${CLAUDE_CLI_FLOOR} or later`);
  });

  it("adds no floor clause at or above the floor", () => {
    for (const cli of [CLAUDE_CLI_FLOOR, "99.0.0"]) {
      const gate = new ClaudePluginGate(true, BUNDLE);
      gate.observe({ type: "system", subtype: "init", initPlugins: [], claudeCodeVersion: cli });
      expect(gate.notice).not.toContain("floor");
    }
  });

  it("parses claude_code_version off init", () => {
    expect(stream("claude_init_plugin_present.jsonl")[1].claudeCodeVersion).toBe("2.1.316");
  });
});
