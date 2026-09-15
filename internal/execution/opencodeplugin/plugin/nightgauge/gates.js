// gates.js is the MANDATORY gate module nightgauge.js imports statically
// (#1635): a broken or missing gates.js fails nightgauge.js's own import,
// which is how the whole plugin fails closed instead of loading with no
// gate at all.
//
// toolExecuteBefore runs the same careful-gate verb the Claude Code
// PreToolUse hook runs (internal/hooks/careful.go via
// cmd/nightgauge/careful.go's `nightgauge hook careful-gate`), so a stage
// running under OpenCode is blocked by the same rule as one running under
// Claude Code. Only the "bash" tool is gated; every other tool call is
// untouched, EXCEPT "task" (AC9 fallback, ADR-022 amendment 2026-09-14),
// which is always denied — see TASK_MARKER below.
//
// Model-authored command text reaches the verb only as stdin JSON, on a
// fixed argv, never on a shell command line.
import { spawnSync } from "node:child_process";

const CAREFUL_GATE_ARGS = ["hook", "careful-gate"];
const SPAWN_TIMEOUT_MS = 5000;
const MARKER = "[nightgauge-gate:careful]";

// TASK_MARKER prefixes the AC9 fallback's own error, distinct from MARKER
// (careful-gate denials), so a stage's remediation output can tell a
// careful-mode block apart from the unconditional subagent denial below.
const TASK_MARKER = "[nightgauge-gate:task-denied]";

// AC9's spike (ADR-022 amendment 2026-09-14) could not determine, within its
// bound, whether opencode 1.18.30 calls tool.execute.before for a tool a
// subagent (`task`) session runs — only that the top-level `task` call
// itself always does, since it is this session's own tool call. Until that
// question is settled, "task" is denied unconditionally, careful mode on or
// off: a subagent session this plugin cannot verify it gates is worse than
// no subagent at all. This is independent of, and checked before, the
// careful-gate verb below.
export async function toolExecuteBefore(ctx, input, output) {
  if (!input) return;
  if (input.tool === "task") {
    throw new Error(
      `${TASK_MARKER} subagent (task) sessions are denied: opencode 1.18.30's tool.execute.before coverage inside a task session is unverified (AC9, ADR-022 amendment 2026-09-14)`
    );
  }
  if (input.tool !== "bash") return;

  const args = output && output.args ? output.args : {};
  const command = typeof args.command === "string" ? args.command : "";
  const cwd = (ctx && (ctx.directory || ctx.worktree)) || process.cwd();

  const payload = JSON.stringify({
    tool_name: "Bash",
    cwd,
    tool_input: { command },
  });

  const bin = process.env.NIGHTGAUGE_BIN;
  if (!bin || bin[0] !== "/") {
    throw new Error(
      `${MARKER} NIGHTGAUGE_BIN is not set to an absolute path; the careful gate cannot run, so the tool call is blocked closed`
    );
  }

  const result = spawnSync(bin, CAREFUL_GATE_ARGS, {
    input: payload,
    cwd,
    timeout: SPAWN_TIMEOUT_MS,
    shell: false,
    encoding: "utf8",
  });

  if (result.error) {
    throw new Error(
      `${MARKER} the careful gate could not run (${result.error.message}); the tool call is blocked closed`
    );
  }
  if (result.signal) {
    throw new Error(
      `${MARKER} the careful gate timed out or was killed (signal ${result.signal}); the tool call is blocked closed`
    );
  }
  if (result.status !== 0) {
    throw new Error(
      `${MARKER} the careful gate exited ${result.status}; the tool call is blocked closed`
    );
  }

  const stdout = (result.stdout || "").trim();
  if (stdout === "") return; // allow: silence is the documented "no decision"

  let decision;
  try {
    decision = JSON.parse(stdout);
  } catch {
    throw new Error(
      `${MARKER} the careful gate's output did not parse as JSON; the tool call is blocked closed`
    );
  }

  const hookOutput = decision && decision.hookSpecificOutput;
  if (hookOutput && hookOutput.permissionDecision === "deny") {
    const reason = hookOutput.permissionDecisionReason || "blocked by the careful gate";
    throw new Error(`${MARKER} ${reason}`);
  }
}
