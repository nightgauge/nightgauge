/**
 * #2506 — the deterministic issue-pickup fallback must take base branch and
 * parent from the issue's native GitHub parent, never from an unbound
 * `.branch-context` file left behind in the clone's plans dir.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import type { Logger } from "../../../src/utils/logger";
import { fakeCloneLayout } from "../../helpers/cloneLayout";

const { nativeParent, lsRemoteCalls } = vi.hoisted(() => ({
  nativeParent: {
    value: null as { number: number; repository: { nameWithOwner: string } } | null,
  },
  lsRemoteCalls: [] as string[][],
}));

vi.mock("../../../src/services/BinaryResolver", () => ({
  BinaryResolver: {
    fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }),
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
    if (cmd === "git" && args[0] === "ls-remote") {
      lsRemoteCalls.push(args);
      const pattern = args[args.length - 1];
      const out = pattern.startsWith("epic/50-") ? "abc123\trefs/heads/epic/50-real-epic\n" : "";
      return Promise.resolve({ stdout: out, stderr: "" });
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
    if (cmd === "gh" && args[0] === "api" && args[1] === "graphql") {
      return Promise.resolve({
        stdout: JSON.stringify({
          data: { repository: { issue: { parent: nativeParent.value } } },
        }),
        stderr: "",
      });
    }
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args[0] === "hook") {
      return Promise.resolve({
        stdout: JSON.stringify({ issue_number: 233, has_open_dependencies: false, open_count: 0 }),
        stderr: "",
      });
    }
    return Promise.resolve({ stdout: "", stderr: "" });
  };

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: "", stderr: "" });

  return { ...actual, exec: execMock, execFile: execFileMock };
});

import { ContextAssembler } from "../../../src/orchestrator/context/ContextAssembler";

function makeLogger(): Logger {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger;
}

describe("ContextAssembler deterministic issue-pickup — base/parent source (#2506)", () => {
  let tmpDir: string;
  let layout: ReturnType<typeof fakeCloneLayout>;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-2506-"));
    layout = fakeCloneLayout(tmpDir);
    nativeParent.value = null;
    lsRemoteCalls.length = 0;
    // A stale leftover naming an unrelated issue's epic.
    fs.mkdirSync(layout.plans, { recursive: true });
    fs.writeFileSync(
      path.join(layout.plans, ".branch-context"),
      "BASE_BRANCH=epic/280-old-epic\nPARENT_ISSUE=280\n"
    );
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
    vi.clearAllMocks();
  });

  async function assemble(): Promise<Record<string, unknown>> {
    const assembler = new ContextAssembler(makeLogger(), () => tmpDir, null);
    const result = await assembler.generateDeterministicContext("issue-pickup", 233);
    expect(result.generated).toBe(true);
    return JSON.parse(fs.readFileSync(path.join(layout.pipeline, "issue-233.json"), "utf-8"));
  }

  it("ignores a stale .branch-context for another issue", async () => {
    const ctx = await assemble();
    expect(ctx.native_parent).toBeNull();
    expect(ctx.base_branch).toBe("main");
    expect(lsRemoteCalls).toEqual([]);
  });

  it("takes the epic base from a same-repo native parent", async () => {
    nativeParent.value = { number: 50, repository: { nameWithOwner: "Acme/Demo-Repo" } };
    const ctx = await assemble();
    expect(ctx.native_parent).toBe(50);
    expect(ctx.base_branch).toBe("epic/50-real-epic");
  });

  it("gives no epic base for a cross-repository native parent", async () => {
    nativeParent.value = { number: 50, repository: { nameWithOwner: "acme/other-repo" } };
    const ctx = await assemble();
    expect(ctx.native_parent).toBeNull();
    expect(ctx.base_branch).toBe("main");
  });
});
