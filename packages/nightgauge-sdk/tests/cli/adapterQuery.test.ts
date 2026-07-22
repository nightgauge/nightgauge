import { describe, expect, it } from "vitest";
import { summarizeCodexJsonOutput } from "../../src/cli/adapterQuery.js";

describe("summarizeCodexJsonOutput", () => {
  it("extracts final agent message and detects explicit halt failures", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_1",
          type: "agent_message",
          text: "Execution halted at issue-pickup due to dependency check failure.",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(true);
    expect(summary.failureReason).toContain("Execution halted");
    expect(summary.displayText).toContain("Execution halted");
  });

  it("does not mark success message as failure", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_2",
          type: "agent_message",
          text: "Issue pickup complete. Context file written.",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toContain("Issue pickup complete");
  });

  it("detects failed command_execution events (Issue #628)", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_3",
          type: "command_execution",
          command: "npm test",
          status: "failed",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toContain("npm test");
  });

  it("uses last agent_message as displayText when both events present", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_4",
          type: "command_execution",
          command: "git checkout -b feat/test",
          status: "completed",
        },
      }),
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_5",
          type: "agent_message",
          text: "Branch created and context file written.",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toBe("Branch created and context file written.");
  });

  it("handles non-JSON lines gracefully", () => {
    const output = [
      "Some non-JSON debug output",
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_6",
          type: "agent_message",
          text: "Stage completed successfully.",
        },
      }),
      "",
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toBe("Stage completed successfully.");
  });

  it("returns raw output when no structured events are present", () => {
    const output = "Plain text output from Codex CLI";

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toBe("Plain text output from Codex CLI");
  });

  it("marks unrecovered critical gh command failures as explicit failure", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_7",
          type: "command_execution",
          command: "gh api user --jq .login",
          status: "failed",
          aggregated_output: "error connecting to api.github.com\ncheck your internet connection",
        },
      }),
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_8",
          type: "agent_message",
          text: "I will retry this later.",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(true);
    expect(summary.failureReason).toContain(
      "GitHub API connectivity failure (api.github.com) detected"
    );
  });

  it("does not mark benign rg no-match failures as explicit failure", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_9",
          type: "command_execution",
          command: 'git branch -a | rg "934|effort|opus"',
          status: "failed",
          aggregated_output: "",
        },
      }),
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_10",
          type: "agent_message",
          text: "No matching branches were found.",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toContain("No matching branches");
  });

  it("does not mark recovered critical failures as explicit failure", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_11",
          type: "command_execution",
          command: "gh api user --jq .login",
          status: "failed",
          aggregated_output: "error connecting to api.github.com\ncheck your internet connection",
        },
      }),
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_12",
          type: "command_execution",
          command:
            'python3 -m json.tool .nightgauge/pipeline/issue-934.json > /dev/null && echo "Context file written: .nightgauge/pipeline/issue-934.json"',
          status: "completed",
          aggregated_output: "Context file written: .nightgauge/pipeline/issue-934.json",
        },
      }),
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_13",
          type: "agent_message",
          text: "Issue pickup complete. Context file written.",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toContain("Issue pickup complete");
  });

  it("does not mark failed nested stage-wrapper command as explicit failure", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_14",
          type: "command_execution",
          command: "/bin/zsh -lc 'scripts/run-stage.sh codex feature-validate 934'",
          status: "failed",
          aggregated_output: '{"level":"info","message":"codex preflight checks passed."}',
        },
      }),
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_15",
          type: "agent_message",
          text: "Validation complete. Context written.",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toContain("Validation complete");
  });

  it("does not mark Go binary project sync-status hook failures as explicit failure", () => {
    const output = [
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_16",
          type: "command_execution",
          command:
            '/bin/zsh -lc \'BINARY=$(command -v nightgauge 2>/dev/null); [ -n "$BINARY" ] && "$BINARY" project sync-status "934" "in-progress" 2>/dev/null || true\'',
          status: "failed",
          aggregated_output: "[nightgauge-hook] ERROR: Could not determine repository owner/name",
        },
      }),
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_17",
          type: "agent_message",
          text: "Validation context written. Proceed to PR create.",
        },
      }),
      JSON.stringify({ type: "turn.completed" }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toContain("Validation context written");
  });
});
