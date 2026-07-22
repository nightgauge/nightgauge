/**
 * Tests for the Codex allowed-tools → sandbox/approval mapping (#4026).
 */

import { describe, it, expect } from "vitest";
import {
  resolveCodexSandboxMode,
  codexSandboxFlags,
  applyCodexSandboxProfile,
  CODEX_BYPASS_FLAG,
} from "../../../cli/adapters/codexSandbox.js";

describe("resolveCodexSandboxMode (#4026)", () => {
  it("defaults to danger-full-access with no positive evidence (undefined/empty)", () => {
    expect(resolveCodexSandboxMode(undefined)).toBe("danger-full-access");
    expect(resolveCodexSandboxMode([])).toBe("danger-full-access");
    expect(resolveCodexSandboxMode(["  "])).toBe("danger-full-access");
  });

  it("returns danger-full-access when any shell/network/arbitrary tool is present", () => {
    expect(resolveCodexSandboxMode(["Read", "Bash"])).toBe("danger-full-access");
    expect(resolveCodexSandboxMode(["Read", "Bash(git *)"])).toBe("danger-full-access");
    expect(resolveCodexSandboxMode(["Read", "WebFetch"])).toBe("danger-full-access");
    expect(resolveCodexSandboxMode(["Task"])).toBe("danger-full-access");
    // MCP tools are opaque → treated as full access.
    expect(resolveCodexSandboxMode(["Read", "mcp__playwright__browser_click"])).toBe(
      "danger-full-access"
    );
  });

  it("returns workspace-write for file-edit-only tool sets (no shell)", () => {
    expect(resolveCodexSandboxMode(["Read", "Write"])).toBe("workspace-write");
    expect(resolveCodexSandboxMode(["Edit", "MultiEdit", "Grep"])).toBe("workspace-write");
    expect(resolveCodexSandboxMode(["NotebookEdit"])).toBe("workspace-write");
  });

  it("returns read-only for pure analysis tool sets", () => {
    expect(resolveCodexSandboxMode(["Read", "Grep", "Glob"])).toBe("read-only");
    expect(resolveCodexSandboxMode(["Read", "NotebookRead", "TodoWrite"])).toBe("read-only");
  });
});

describe("codexSandboxFlags (#4026)", () => {
  it("uses the single bypass flag for full access", () => {
    expect(codexSandboxFlags("danger-full-access")).toEqual([CODEX_BYPASS_FLAG]);
  });

  it("uses explicit --sandbox + --ask-for-approval never for scoped modes", () => {
    expect(codexSandboxFlags("read-only")).toEqual([
      "--sandbox",
      "read-only",
      "--ask-for-approval",
      "never",
    ]);
    expect(codexSandboxFlags("workspace-write")).toEqual([
      "--sandbox",
      "workspace-write",
      "--ask-for-approval",
      "never",
    ]);
  });
});

describe("applyCodexSandboxProfile (#4026)", () => {
  const baseArgs = ["exec", CODEX_BYPASS_FLAG, "--json"];

  it("leaves full-access args unchanged (default — no regression)", () => {
    expect(applyCodexSandboxProfile(baseArgs, ["Bash"])).toEqual(baseArgs);
    expect(applyCodexSandboxProfile(baseArgs, undefined)).toEqual(baseArgs);
    expect(applyCodexSandboxProfile(baseArgs, [])).toEqual(baseArgs);
  });

  it("swaps the bypass flag in place for a read-only profile", () => {
    expect(applyCodexSandboxProfile(baseArgs, ["Read", "Grep"])).toEqual([
      "exec",
      "--sandbox",
      "read-only",
      "--ask-for-approval",
      "never",
      "--json",
    ]);
  });

  it("swaps the bypass flag in place for a workspace-write profile", () => {
    expect(applyCodexSandboxProfile(baseArgs, ["Read", "Write"])).toEqual([
      "exec",
      "--sandbox",
      "workspace-write",
      "--ask-for-approval",
      "never",
      "--json",
    ]);
  });

  it("does not force-inject when the bypass sentinel is absent (operator override)", () => {
    const overridden = ["exec", "--sandbox", "workspace-write", "--json"];
    expect(applyCodexSandboxProfile(overridden, ["Read", "Grep"])).toEqual(overridden);
  });

  it("never mutates the input args array", () => {
    const input = ["exec", CODEX_BYPASS_FLAG, "--json"];
    const copy = [...input];
    applyCodexSandboxProfile(input, ["Read"]);
    expect(input).toEqual(copy);
  });
});
