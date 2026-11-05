import * as path from "node:path";
import { describe, expect, it } from "vitest";
import {
  agentHomeEnv,
  isInheritedHostVar,
  scrubInheritedHostEnv,
  secretStoreArgs,
} from "./hostIsolation";

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

describe("isInheritedHostVar (#2743)", () => {
  it("matches ELECTRON_RUN_AS_NODE and VSCODE_* keys", () => {
    expect(isInheritedHostVar("ELECTRON_RUN_AS_NODE")).toBe(true);
    expect(isInheritedHostVar("VSCODE_PID")).toBe(true);
  });
  it("leaves near misses and ordinary keys alone", () => {
    expect(isInheritedHostVar("ELECTRON_RUN_AS_NODEX")).toBe(false);
    expect(isInheritedHostVar("PATH")).toBe(false);
  });
});

describe("scrubInheritedHostEnv (#2743)", () => {
  // runTests merges the launcher's process.env, so an inherited
  // ELECTRON_RUN_AS_NODE started VS Code as plain Node.
  it("removes inherited host variables in place and names them", () => {
    const env: NodeJS.ProcessEnv = {
      VSCODE_IPC_HOOK_CLI: "/tmp/sock",
      ELECTRON_RUN_AS_NODE: "1",
      PATH: "/usr/bin",
      HOME: "/h",
    };
    expect(scrubInheritedHostEnv(env)).toEqual(["ELECTRON_RUN_AS_NODE", "VSCODE_IPC_HOOK_CLI"]);
    expect(env).toEqual({ PATH: "/usr/bin", HOME: "/h" });
  });
  it("returns nothing and leaves a clean env untouched", () => {
    const env: NodeJS.ProcessEnv = { PATH: "/usr/bin" };
    expect(scrubInheritedHostEnv(env)).toEqual([]);
    expect(env).toEqual({ PATH: "/usr/bin" });
  });
});
