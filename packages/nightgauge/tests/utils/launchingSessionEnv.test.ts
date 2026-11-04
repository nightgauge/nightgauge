import { describe, expect, it } from "vitest";
import {
  isLaunchingSessionEnv,
  withoutLaunchingSessionEnv,
} from "../../src/utils/launchingSessionEnv";

// The same cases as TestLaunchingSessionEnv in internal/execution/manager_test.go.
describe("isLaunchingSessionEnv (#2848)", () => {
  const cases: Array<[string, boolean]> = [
    ["CLAUDECODE", true],
    ["CLAUDE_EFFORT", true],
    ["CLAUDE_CODE_SESSION_ID", true],
    ["CLAUDE_CODE_ENTRYPOINT", true],
    ["CLAUDE_CODE_OAUTH_TOKEN", false],
    ["CLAUDE_PLUGIN_ROOT", false],
    ["CLAUDE_CODE_DISABLE_THINKING", false],
    ["CLAUDE_CONFIG_DIR", false],
    ["CLAUDE_CODE_USE_BEDROCK", false],
    ["CLAUDE_CODE_USE_VERTEX", false],
    ["CLAUDE_CODE_SKIP_BEDROCK_AUTH", false],
    ["CLAUDE_CODE_SKIP_VERTEX_AUTH", false],
    ["CLAUDE", false],
    ["CLAUDEX", false],
    ["ANTHROPIC_API_KEY", false],
    ["PATH", false],
  ];
  it.each(cases)("%s -> %s", (name, want) => {
    expect(isLaunchingSessionEnv(name)).toBe(want);
  });
});

describe("withoutLaunchingSessionEnv (#2848)", () => {
  const host: NodeJS.ProcessEnv = {
    PATH: "/usr/bin",
    CLAUDECODE: "1",
    CLAUDE_EFFORT: "high",
    CLAUDE_CODE_SESSION_ID: "x",
    CLAUDE_CODE_OAUTH_TOKEN: "tok",
    CLAUDE_PLUGIN_ROOT: "/plugin",
    CLAUDE_CODE_DISABLE_THINKING: "1",
    CLAUDE_CONFIG_DIR: "/home/op/.claude",
    ANTHROPIC_MODEL: "m",
  };

  it("removes residue and keeps the exceptions and unrelated names", () => {
    expect(withoutLaunchingSessionEnv(host)).toEqual({
      PATH: "/usr/bin",
      CLAUDE_CODE_OAUTH_TOKEN: "tok",
      CLAUDE_PLUGIN_ROOT: "/plugin",
      CLAUDE_CODE_DISABLE_THINKING: "1",
      CLAUDE_CONFIG_DIR: "/home/op/.claude",
      ANTHROPIC_MODEL: "m",
    });
  });

  it("does not modify its input", () => {
    const copy = { ...host };
    withoutLaunchingSessionEnv(host);
    expect(host).toEqual(copy);
  });

  it("lets an export spread after the base arrive", () => {
    const env: NodeJS.ProcessEnv = {
      ...withoutLaunchingSessionEnv(host),
      CLAUDE_PLUGIN_ROOT: "/own",
    };
    expect(env.CLAUDE_PLUGIN_ROOT).toBe("/own");
    expect(env.CLAUDECODE).toBeUndefined();
  });

  it("defaults to process.env", () => {
    const saved = process.env.CLAUDE_EFFORT;
    process.env.CLAUDE_EFFORT = "high";
    try {
      expect(withoutLaunchingSessionEnv().CLAUDE_EFFORT).toBeUndefined();
    } finally {
      if (saved === undefined) delete process.env.CLAUDE_EFFORT;
      else process.env.CLAUDE_EFFORT = saved;
    }
  });
});
