import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { agentHomeEnv, secretStoreArgs } from "./hostIsolation";

describe("secretStoreArgs (#2935)", () => {
  // The launchers fake HOME; without these flags macOS raised a
  // "Keychain Not Found" dialog once per window.
  it("uses a mock keychain on macOS", () => {
    expect(secretStoreArgs("darwin")).toEqual([
      "--use-inmemory-secretstorage",
      "--use-mock-keychain",
    ]);
  });
  it("uses the basic password store on Linux", () => {
    expect(secretStoreArgs("linux")).toEqual([
      "--use-inmemory-secretstorage",
      "--password-store=basic",
    ]);
  });
  it("keeps SecretStorage in memory on Windows", () => {
    expect(secretStoreArgs("win32")).toEqual(["--use-inmemory-secretstorage"]);
  });
});

describe("agentHomeEnv (#2935)", () => {
  it("moves HOME and every agent-home override off the real home", () => {
    expect(agentHomeEnv("/s/home", "/s/agents", "darwin")).toEqual({
      HOME: "/s/home",
      CODEX_HOME: path.join("/s/agents", "codex"),
      GROK_HOME: path.join("/s/agents", "grok"),
      XDG_CONFIG_HOME: path.join("/s/agents", "config"),
      CLAUDE_CONFIG_DIR: path.join("/s/agents", "claude"),
    });
  });
  it("moves USERPROFILE on Windows", () => {
    expect(agentHomeEnv("/s/home", "/s/agents", "win32").USERPROFILE).toBe("/s/home");
  });
});
