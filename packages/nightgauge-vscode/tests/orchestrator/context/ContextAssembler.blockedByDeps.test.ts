/**
 * #189 — the deterministic (primary) issue-pickup path must consult
 * GitHub's native blockedBy edges via `nightgauge hook check-deps`
 * instead of hard-coding `blockedBy: []`, and must FAIL CLOSED (defer
 * pickup) when open dependencies exist.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import type { Logger } from "../../../src/utils/logger";

const { execFileResponses, binaryPath } = vi.hoisted(() => ({
  execFileResponses: { checkDeps: "" as string },
  binaryPath: { value: "/fake/nightgauge" as string | null },
}));

vi.mock("../../../src/services/BinaryResolver", () => ({
  BinaryResolver: {
    fromVSCode: () => ({ resolve: async () => binaryPath.value }),
  },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    if (cmd === "git" && args[0] === "branch") {
      return Promise.resolve({ stdout: "feat/233-demo\n", stderr: "" });
    }
    if (cmd === "gh" && args[0] === "issue") {
      return Promise.resolve({
        stdout: JSON.stringify({
          title: "Demo issue",
          labels: [{ name: "type:feature" }],
          body: "## Summary\n\nDo the thing.\n",
        }),
        stderr: "",
      });
    }
    if (cmd === "gh" && args[0] === "repo") {
      return Promise.resolve({ stdout: "acme/demo-repo\n", stderr: "" });
    }
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args[0] === "hook") {
      if (execFileResponses.checkDeps === "ERROR") {
        return Promise.reject(new Error("check-deps exploded"));
      }
      return Promise.resolve({ stdout: execFileResponses.checkDeps, stderr: "" });
    }
    // git ls-remote / rev-parse / everything else: benign empty output.
    return Promise.resolve({ stdout: "", stderr: "" });
  };

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: "", stderr: "" });

  return { ...actual, exec: execMock, execFile: execFileMock };
});

import { ContextAssembler } from "../../../src/orchestrator/context/ContextAssembler";

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

describe("ContextAssembler deterministic issue-pickup — blockedBy enforcement (#189)", () => {
  let tmpDir: string;
  let assembler: ContextAssembler;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-189-"));
    assembler = new ContextAssembler(makeLogger(), () => tmpDir, null);
    binaryPath.value = "/fake/nightgauge";
    execFileResponses.checkDeps = "";
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
    vi.clearAllMocks();
  });

  function contextPath(): string {
    return path.join(tmpDir, ".nightgauge", "pipeline", "issue-233.json");
  }

  it("populates dependencies.blockedBy from GitHub's native edges when blockers are closed", async () => {
    execFileResponses.checkDeps = JSON.stringify({
      issue_number: 233,
      has_open_dependencies: false,
      open_dependencies: [],
      open_count: 0,
    });

    const result = await assembler.generateDeterministicContext("issue-pickup", 233);
    expect(result.generated).toBe(true);
    expect(result.blockedBy).toBeUndefined();

    const ctx = JSON.parse(fs.readFileSync(contextPath(), "utf-8"));
    expect(ctx.dependencies.blockedBy).toEqual([]);
    expect(ctx._deterministic).toBe(true);
  });

  it("fails closed — defers pickup and writes NO context when blockers are open", async () => {
    execFileResponses.checkDeps = JSON.stringify({
      issue_number: 233,
      has_open_dependencies: true,
      open_dependencies: [
        { number: 47, title: "platform E47.A1", state: "OPEN", repo: "acme/platform" },
      ],
      open_count: 1,
    });

    const result = await assembler.generateDeterministicContext("issue-pickup", 233);
    expect(result.generated).toBe(false);
    expect(result.blockedBy).toEqual([
      { number: 47, title: "platform E47.A1", state: "OPEN", repo: "acme/platform" },
    ]);
    expect(fs.existsSync(contextPath())).toBe(false);
  });

  it("fails open when the binary is unavailable — pickup proceeds with empty deps", async () => {
    binaryPath.value = null;

    const result = await assembler.generateDeterministicContext("issue-pickup", 233);
    expect(result.generated).toBe(true);

    const ctx = JSON.parse(fs.readFileSync(contextPath(), "utf-8"));
    expect(ctx.dependencies.blockedBy).toEqual([]);
  });

  it("fails open when the check-deps command errors", async () => {
    execFileResponses.checkDeps = "ERROR";

    const result = await assembler.generateDeterministicContext("issue-pickup", 233);
    expect(result.generated).toBe(true);
  });
});
