/**
 * skillRunner.toolCallObservation.test.ts
 *
 * Every consumer of a tool call, driven from the shape the Claude CLI ACTUALLY
 * emits (#169).
 *
 * The bug these cover shipped three times because the fixtures lied. The
 * pre-existing suites build `content_block_start` events — the streaming shape
 * — so they exercised a branch that never executes against the Claude CLI,
 * which delivers tool calls inside complete `assistant` messages. Four features
 * were wired to the streaming branch alone and were therefore dead in
 * production while their tests stayed green:
 *
 *   - `promptDetected`, a CORRECTNESS GATE on stage success. Stuck at `false`,
 *     it reported a stage that begged for interactive input as a clean pass.
 *   - the AskUserQuestion loop abort, which never fired, so such a stage spun
 *     until an unrelated stall kill and lost the precise diagnosis.
 *   - the dashboard tool-call feed, which recorded nothing, ever.
 *   - `onToolUse`, in both the headless and resume paths.
 *
 * So the fixtures here are assistant messages, deliberately. A test that
 * asserts a consumer works must feed it the bytes the CLI sends.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(),
  },
  extensions: {
    getExtension: vi.fn(() => null),
  },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
  readFileSync: vi.fn(
    () => `---
name: test-skill
allowed-tools: Read Write Edit Bash
---
# Test Skill
`
  ),
}));

vi.mock("child_process", async () => {
  // Since #79 the extension composes no skill text of its own: it shells out
  // to `nightgauge skill render`. Answer that one call with the shared
  // envelope stub; every other execFileSync caller keeps an empty result.
  const { isSkillRenderCall, skillRenderStdout } = await import("../helpers/skillRender");
  return {
    spawn: vi.fn(),
    execFileSync: vi.fn((_cmd: string, args: string[]) =>
      isSkillRenderCall(args) ? skillRenderStdout(args) : ""
    ),
    execFile: vi.fn(
      (
        _cmd: string,
        _args: string[],
        _opts: unknown,
        cb: (e: Error | null, s: string, t: string) => void
      ) => {
        cb(new Error("no children"), "", "");
      }
    ),
  };
});

import {
  runStageSkillHeadless,
  resumeSessionWithResponse,
  toolStepActivity,
  TOOL_STEP_ACTIVITY_MAX,
} from "../../src/utils/skillRunner";
import { createMockChildProcess } from "../mocks/child-process";

describe("skillRunner — tool call observation across delivery shapes (#169)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  /**
   * A complete `assistant` message — the shape the Claude CLI emits, verbatim
   * down to the envelope fields, so nothing here can pass by accident on a
   * shape the CLI never sends.
   */
  function emitAssistantToolUse(
    calls: { name: string; input: unknown; id?: string }[],
    proc: ChildProcess = mockProcess
  ) {
    const message = JSON.stringify({
      type: "assistant",
      message: {
        id: "msg_01XyZ",
        type: "message",
        role: "assistant",
        model: "claude-opus-4-20250514",
        content: calls.map((c) => ({
          type: "tool_use",
          ...(c.id !== undefined ? { id: c.id } : {}),
          name: c.name,
          input: c.input,
        })),
        stop_reason: null,
        stop_sequence: null,
        usage: {
          input_tokens: 4,
          cache_creation_input_tokens: 0,
          cache_read_input_tokens: 14203,
          output_tokens: 1,
        },
      },
      parent_tool_use_id: null,
      session_id: "sess_test",
    });
    proc.stdout!.emit("data", Buffer.from(message + "\n"));
  }

  /** The streaming shape, for the double-delivery (dedupe) cases. */
  function emitStreamingToolUse(
    name: string,
    input: unknown,
    id: string,
    proc: ChildProcess = mockProcess
  ) {
    const message = JSON.stringify({
      type: "content_block_start",
      index: 0,
      content_block: { type: "tool_use", id, name, input },
    });
    proc.stdout!.emit("data", Buffer.from(message + "\n"));
  }

  // ───────────────────────── promptDetected (#697) ─────────────────────────

  describe("promptDetected — the correctness gate on stage success", () => {
    it("is set by an AskUserQuestion delivered in an assistant message", async () => {
      const onComplete = vi.fn();
      runStageSkillHeadless("issue-pickup", 42, { onComplete });

      emitAssistantToolUse([
        { name: "AskUserQuestion", input: { question: "Reopen issue?" }, id: "toolu_ask1" },
      ]);

      // Exit 0: the stage looks clean, and without promptDetected it would be
      // BOOKED clean despite having stalled on an unanswerable prompt.
      mockProcess.emit("close", 0);
      await new Promise((resolve) => setTimeout(resolve, 10));

      expect(onComplete).toHaveBeenCalledWith(
        expect.objectContaining({ exitCode: 0, promptDetected: true })
      );
    });

    it("stays false for ordinary tool calls in an assistant message", async () => {
      const onComplete = vi.fn();
      runStageSkillHeadless("issue-pickup", 42, { onComplete });

      emitAssistantToolUse([
        { name: "Read", input: { file_path: "/some/path" }, id: "toolu_r1" },
        { name: "Write", input: { file_path: "/some/path", content: "x" }, id: "toolu_w1" },
      ]);

      mockProcess.emit("close", 0);
      await new Promise((resolve) => setTimeout(resolve, 10));

      expect(onComplete).toHaveBeenCalledWith(
        expect.objectContaining({ success: true, promptDetected: false })
      );
    });
  });

  // ────────────────────── AskUserQuestion loop abort (#218) ──────────────────

  describe("AskUserQuestion loop abort", () => {
    it("aborts after 3 identical attempts delivered in assistant messages", () => {
      const onError = vi.fn();
      const onStderr = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onError, onStderr });

      const input = { question: "What should I do?", options: ["A", "B"] };
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_a" }]);
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_b" }]);
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_c" }]);

      expect(onError).toHaveBeenCalledTimes(1);
      expect(onError).toHaveBeenCalledWith(
        expect.objectContaining({
          message: expect.stringContaining("Claude attempted AskUserQuestion 3 times"),
        })
      );
      expect(onStderr).toHaveBeenCalledWith(expect.stringContaining("[skillRunner] Loop detected"));
      expect(mockProcess.kill).toHaveBeenCalledWith("SIGTERM");
    });

    it("counts every tool_use block when one assistant message carries several", () => {
      // The plural shape can deliver a whole loop in ONE message. Observing
      // only the first block would let the abort undercount and never fire.
      const onError = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onError });

      const input = { question: "Same?" };
      emitAssistantToolUse([
        { name: "AskUserQuestion", input, id: "toolu_1" },
        { name: "AskUserQuestion", input, id: "toolu_2" },
        { name: "AskUserQuestion", input, id: "toolu_3" },
      ]);

      expect(onError).toHaveBeenCalledTimes(1);
      expect(mockProcess.kill).toHaveBeenCalledWith("SIGTERM");
    });

    it("resets the counter when a different tool intervenes", () => {
      const onError = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onError });

      const input = { question: "What?" };
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_1" }]);
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_2" }]);
      emitAssistantToolUse([{ name: "Read", input: { file_path: "/x" }, id: "toolu_r" }]);
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_3" }]);
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_4" }]);

      expect(onError).not.toHaveBeenCalled();

      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_5" }]);
      expect(onError).toHaveBeenCalledTimes(1);
    });
  });

  // ─────────────────── Dashboard tool-call feed (#639) ──────────────────────

  describe("dashboard tool-call feed", () => {
    it("records tool calls delivered in an assistant message", () => {
      const onToolCall = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onToolCall });

      emitAssistantToolUse([
        { name: "Bash", input: { command: "go test ./..." }, id: "toolu_bash1" },
        { name: "Read", input: { file_path: "/src/a.ts" }, id: "toolu_read1" },
      ]);

      expect(onToolCall).toHaveBeenCalledTimes(2);
      expect(onToolCall).toHaveBeenNthCalledWith(1, "Bash", { command: "go test ./..." });
      expect(onToolCall).toHaveBeenNthCalledWith(2, "Read", { file_path: "/src/a.ts" });
    });

    it("drives onToolUse from the assistant-message shape too", () => {
      const onToolUse = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onToolUse });

      emitAssistantToolUse([
        { name: "Edit", input: { file_path: "/src/b.ts" }, id: "toolu_edit1" },
      ]);

      expect(onToolUse).toHaveBeenCalledTimes(1);
      expect(onToolUse).toHaveBeenCalledWith("Edit", { file_path: "/src/b.ts" }, "toolu_edit1");
    });
  });

  // ──────────────────────── Double delivery / id dedupe ──────────────────────

  describe("double delivery is processed once", () => {
    it("does not advance the AskUserQuestion counter twice for one call", () => {
      // The abort threshold is 3. Delivering each call in BOTH shapes without
      // deduping would make 2 distinct calls look like 4 attempts and trip the
      // abort at HALF its intended threshold — a stage killed for a loop it
      // never entered.
      const onError = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onError });

      const input = { question: "Same question" };

      emitStreamingToolUse("AskUserQuestion", input, "toolu_dup1");
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_dup1" }]);

      emitStreamingToolUse("AskUserQuestion", input, "toolu_dup2");
      emitAssistantToolUse([{ name: "AskUserQuestion", input, id: "toolu_dup2" }]);

      // 4 raw deliveries, 2 real attempts. Undeduped, the abort fires here.
      expect(onError).not.toHaveBeenCalled();
      expect(mockProcess.kill).not.toHaveBeenCalled();

      // The 3rd real attempt is the one that legitimately trips it.
      emitStreamingToolUse("AskUserQuestion", input, "toolu_dup3");
      expect(onError).toHaveBeenCalledTimes(1);
    });

    it("fires the dashboard feed once for a call delivered in both shapes", () => {
      const onToolCall = vi.fn();
      const onToolUse = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onToolCall, onToolUse });

      emitStreamingToolUse("Bash", { command: "npm test" }, "toolu_same");
      emitAssistantToolUse([{ name: "Bash", input: { command: "npm test" }, id: "toolu_same" }]);

      expect(onToolCall).toHaveBeenCalledTimes(1);
      expect(onToolUse).toHaveBeenCalledTimes(1);
    });

    it("still observes distinct calls that share a name and input", () => {
      // Dedupe must key on the id, not on the payload: an agent legitimately
      // runs the same command twice, and collapsing those would under-report.
      const onToolCall = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onToolCall });

      emitAssistantToolUse([{ name: "Bash", input: { command: "ls" }, id: "toolu_x" }]);
      emitAssistantToolUse([{ name: "Bash", input: { command: "ls" }, id: "toolu_y" }]);

      expect(onToolCall).toHaveBeenCalledTimes(2);
    });

    it("observes every call when the CLI supplies no ids", () => {
      // An id-less call cannot be deduped. Dropping it would be worse than
      // double-counting: the feed would silently lose calls.
      const onToolCall = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onToolCall });

      emitAssistantToolUse([{ name: "Read", input: { file_path: "/a" } }]);
      emitAssistantToolUse([{ name: "Read", input: { file_path: "/b" } }]);

      expect(onToolCall).toHaveBeenCalledTimes(2);
      expect(onToolCall).toHaveBeenNthCalledWith(1, "Read", { file_path: "/a" });
    });
  });

  // ───────────────── resumeSessionWithResponse — the 5th consumer ────────────

  describe("resumeSessionWithResponse", () => {
    it("drives onToolUse from the assistant-message shape", () => {
      // Not listed on the issue; found by sweeping for remaining readers of the
      // singular shape, which is the question none of the three prior fixes
      // asked.
      const onToolUse = vi.fn();
      resumeSessionWithResponse("sess_abc", "my answer", { onToolUse });

      emitAssistantToolUse([
        { name: "Bash", input: { command: "git status" }, id: "toolu_resume1" },
      ]);

      expect(onToolUse).toHaveBeenCalledTimes(1);
      expect(onToolUse).toHaveBeenCalledWith("Bash", { command: "git status" }, "toolu_resume1");
    });
  });

  // ──────────────── live progress activity line (#3455) ────────────────

  describe("live progress tool-step activity", () => {
    it.each<[string, unknown, string]>([
      ["Edit", { file_path: "/repo/internal/platform/analytics.go" }, "Edit analytics.go"],
      ["NotebookEdit", { notebook_path: "/n/a.ipynb" }, "NotebookEdit a.ipynb"],
      ["Bash", { command: "curl -H 'Authorization: Bearer s3cr3t'" }, "Bash"],
      ["Grep", { pattern: "s3cr3t", path: "/repo/src" }, "Grep src"],
      ["Glob", { path: "/repo/src/" }, "Glob src"],
      ["Read", { file_path: "C:\\repo\\a.go" }, "Read a.go"],
      ["Read", { file_path: "  ", path: "/repo/b.go" }, "Read b.go"],
      ["mcp_fetch", { path: "/v1/items?token=s3cr3t" }, "mcp_fetch"],
      ["mcp_fetch", { path: "https://host/x" }, "mcp_fetch"],
      ["Read", "not an object", "Read"],
      ["", { file_path: "/a.go" }, ""],
      ["  ", { file_path: "/a.go" }, ""],
    ])("toolStepActivity(%j, %j) is %j", (name, input, want) => {
      expect(toolStepActivity(name, input)).toBe(want);
    });

    it("caps the tool step", () => {
      expect(toolStepActivity("Read", { file_path: "/" + "x".repeat(300) })).toHaveLength(
        TOOL_STEP_ACTIVITY_MAX
      );
    });

    it("sends the latest tool step on the next progress tick", () => {
      let now = 1_000_000;
      vi.spyOn(Date, "now").mockImplementation(() => now);
      const onStageProgress = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onStageProgress });

      emitAssistantToolUse([
        { name: "Edit", input: { file_path: "/repo/analytics.go" }, id: "toolu_e1" },
      ]);
      now += 6_000;
      emitAssistantToolUse([
        { name: "Bash", input: { command: "echo TOKEN=s3cr3t" }, id: "toolu_b1" },
      ]);
      now += 6_000;
      emitAssistantToolUse([{ name: "  ", input: {}, id: "toolu_x1" }]);
      now += 6_000;
      emitAssistantToolUse([{ name: "Read", input: {}, id: "toolu_r1" }]);

      const activities = onStageProgress.mock.calls.map((c) => c[1]);
      // A nameless call keeps the previous step rather than blanking it.
      expect(activities).toEqual([undefined, "Edit analytics.go", "Bash", "Bash"]);
      expect(JSON.stringify(onStageProgress.mock.calls)).not.toContain("s3cr3t");
    });

    it("sends the latest tool step on the resume path too", () => {
      let now = 1_000_000;
      vi.spyOn(Date, "now").mockImplementation(() => now);
      const onStageProgress = vi.fn();
      // No onToolUse: the step must be tracked without that consumer.
      resumeSessionWithResponse("sess_abc", "my answer", { onStageProgress });

      emitAssistantToolUse([
        { name: "Write", input: { file_path: "/repo/notes.md" }, id: "toolu_w9" },
      ]);
      now += 6_000;
      emitAssistantToolUse([{ name: "Read", input: {}, id: "toolu_r9" }]);

      expect(onStageProgress.mock.calls.map((c) => c[1])).toEqual([undefined, "Write notes.md"]);
    });
  });
});
