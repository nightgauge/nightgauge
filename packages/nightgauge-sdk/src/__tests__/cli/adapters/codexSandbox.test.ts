/**
 * Tests for the Codex allowed-tools → sandbox/approval mapping (#4026).
 */

import { describe, it, expect } from "vitest";
import {
  resolveCodexSandboxMode,
  codexSandboxFlags,
  codexApprovalFlags,
  codexResumeSandboxFlags,
  applyCodexSandboxProfile,
  CODEX_BYPASS_FLAG,
  codexCloneWritableRoot,
} from "../../../cli/adapters/codexSandbox.js";
import { mkdtempSync, rmSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { initCloneRepo } from "../../helpers/gitRepo.js";

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

  it("uses explicit --sandbox for scoped modes", () => {
    expect(codexSandboxFlags("read-only")).toEqual(["--sandbox", "read-only"]);
    expect(codexSandboxFlags("workspace-write")).toEqual(["--sandbox", "workspace-write"]);
  });
});

describe("codexApprovalFlags (#1715)", () => {
  it("pins --ask-for-approval never for scoped modes and nothing for full access", () => {
    expect(codexApprovalFlags("read-only")).toEqual(["--ask-for-approval", "never"]);
    expect(codexApprovalFlags("workspace-write")).toEqual(["--ask-for-approval", "never"]);
    expect(codexApprovalFlags("danger-full-access")).toEqual([]);
  });
});

describe("codexResumeSandboxFlags (#2342)", () => {
  it("carries a scoped mode as config, since exec resume refuses --sandbox", () => {
    expect(codexResumeSandboxFlags("read-only")).toEqual(["-c", 'sandbox_mode="read-only"']);
    expect(codexResumeSandboxFlags("workspace-write")).toEqual([
      "-c",
      'sandbox_mode="workspace-write"',
    ]);
  });

  it("keeps the bypass flag for full access only, as a fresh start does", () => {
    expect(codexResumeSandboxFlags("danger-full-access")).toEqual([CODEX_BYPASS_FLAG]);
    expect(codexResumeSandboxFlags("read-only")).not.toContain(CODEX_BYPASS_FLAG);
    expect(codexResumeSandboxFlags("workspace-write")).not.toContain(CODEX_BYPASS_FLAG);
  });
});

describe("applyCodexSandboxProfile (#4026)", () => {
  const baseArgs = ["exec", CODEX_BYPASS_FLAG, "--json"];

  it("leaves full-access args unchanged (default — no regression)", () => {
    expect(applyCodexSandboxProfile(baseArgs, ["Bash"])).toEqual(baseArgs);
    expect(applyCodexSandboxProfile(baseArgs, undefined)).toEqual(baseArgs);
    expect(applyCodexSandboxProfile(baseArgs, [])).toEqual(baseArgs);
  });

  // `--ask-for-approval` is a top-level codex option: after `exec`, codex
  // 0.145.0 refuses the argv with "unexpected argument" (#1715).
  it("swaps the bypass flag in place and puts the approval policy before exec (read-only)", () => {
    expect(applyCodexSandboxProfile(baseArgs, ["Read", "Grep"])).toEqual([
      "--ask-for-approval",
      "never",
      "exec",
      "--sandbox",
      "read-only",
      "--json",
    ]);
  });

  it("swaps the bypass flag in place and puts the approval policy before exec (workspace-write)", () => {
    expect(applyCodexSandboxProfile(baseArgs, ["Read", "Write"])).toEqual([
      "--ask-for-approval",
      "never",
      "exec",
      "--sandbox",
      "workspace-write",
      "--json",
    ]);
  });

  it("puts the approval policy first when no exec precedes the bypass flag", () => {
    expect(applyCodexSandboxProfile([CODEX_BYPASS_FLAG, "--json"], ["Read"])).toEqual([
      "--ask-for-approval",
      "never",
      "--sandbox",
      "read-only",
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

describe("codexCloneWritableRoot (ADR-024 § 7)", () => {
  it("adds the clone directory as a writable root under workspace-write only", () => {
    const dir = mkdtempSync(join(tmpdir(), "ng-codex-clone-"));
    try {
      const { clone } = initCloneRepo(dir);
      const want = ["-c", `sandbox_workspace_write.writable_roots=[${JSON.stringify(clone)}]`];
      expect(codexCloneWritableRoot("workspace-write", dir)).toEqual(want);
      expect(codexCloneWritableRoot("read-only", dir)).toEqual([]);
      expect(codexCloneWritableRoot("danger-full-access", dir)).toEqual([]);
      expect(codexCloneWritableRoot("workspace-write", undefined)).toEqual([]);
      expect(
        applyCodexSandboxProfile(["exec", CODEX_BYPASS_FLAG, "--json"], ["Read", "Edit"], dir)
      ).toEqual([
        "--ask-for-approval",
        "never",
        "exec",
        "--sandbox",
        "workspace-write",
        ...want,
        "--json",
      ]);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
