// gates.js is the MANDATORY gate module nightgauge.js imports statically
// (#1635): a broken or missing gates.js fails nightgauge.js's own import,
// which is how the whole plugin fails closed instead of loading with no
// gate at all.
//
// toolExecuteBefore (#1635, extended by #1640) runs the same verbs the
// Claude Code PreToolUse hooks run (internal/hooks via cmd/nightgauge/main.go's
// `nightgauge hook <verb>`), so a stage running under OpenCode is blocked by
// the same rules as one running under Claude Code:
//
//   - "bash"        -> workflow-gate, careful-gate, stage-gate (in that
//                      order, matching claude-plugins/nightgauge/hooks/hooks.json's
//                      PreToolUse:Bash chain — first deny wins).
//   - "edit"/"write" -> workflow-gate only, with a Claude-shaped `file_path`
//                      payload (claude-plugins/nightgauge/hooks/hooks.json's
//                      PreToolUse:Edit|Write).
//   - "task"        -> always denied (AC9 fallback below), independent of
//                      every other gate.
//   - every other tool id in TOOL_CLASSIFICATION -> passthrough (read-only,
//                      or governed elsewhere, e.g. webfetch by #1638's
//                      permission map) or, for a mutating tool this table
//                      cannot map safely, blocked.
//   - a tool id ABSENT from TOOL_CLASSIFICATION -> blocked closed
//                      ([nightgauge-gate:unknown-tool]), so a new upstream
//                      mutating tool can never bypass every gate here by
//                      simply not being on the list.
//
// commandExecuteBefore (#1640) is `command.execute.before`'s gate: the
// expanded slash-command text is screened by the same `hook sanitize-prompt`
// verb Claude Code's PreToolUse:Task hook runs, Task-shaped. It is exported
// here and unit-tested directly (plugin_gates_test.go); nightgauge.js's own
// `command.execute.before` registration currently delegates to
// ./nightgauge/session.js (#1641's file), not to this export — see the #1640
// PR description for that wiring gap, which is out of this issue's file
// ownership (plugin/nightgauge.js) to close.
//
// Model-authored command text and tool arguments reach every verb only as
// stdin JSON, on a fixed argv, never on a shell command line.
import { spawnSync } from "node:child_process";

const SPAWN_TIMEOUT_MS = 5000;

// MARKER prefixes a careful-gate denial (#1635, unchanged).
const MARKER = "[nightgauge-gate:careful]";

// WORKFLOW_MARKER prefixes a workflow-gate denial: push-to-main, force-push,
// destructive git, secret read/write, sensitive-file edits, and (for a
// mutating tool id this plugin cannot safely map to a Claude-shaped payload)
// an unmapped-mutation denial — see TOOL_CLASSIFICATION's "blocked" entries.
const WORKFLOW_MARKER = "[nightgauge-gate:workflow]";

// STAGE_MARKER prefixes a stage-gate denial: an analysis pipeline stage
// advancing git/forge state outside its mandate, or an `--admin`/`--auto`
// merge bypass in any stage.
const STAGE_MARKER = "[nightgauge-gate:stage]";

// SANITIZE_MARKER prefixes a sanitize-prompt denial for a
// `command.execute.before` expansion.
const SANITIZE_MARKER = "[nightgauge-gate:sanitize]";

// UNKNOWN_TOOL_MARKER prefixes the fail-closed denial for a tool id absent
// from TOOL_CLASSIFICATION.
const UNKNOWN_TOOL_MARKER = "[nightgauge-gate:unknown-tool]";

// TASK_MARKER prefixes the AC9 fallback's own error, distinct from every
// other marker, so a stage's remediation output can tell a careful-mode/
// workflow/stage block apart from the unconditional subagent denial below.
const TASK_MARKER = "[nightgauge-gate:task-denied]";

// TOOL_CLASSIFICATION is the pinned table of every tool id opencode 1.18.30
// exposes (#1640 AC4), captured two ways and reconciled:
//
//   - `opencode debug agent build` (testdata/opencode-1.18.30-tools.txt):
//     invalid, question, bash, read, glob, grep, task, webfetch, todowrite,
//     skill, apply_patch.
//   - Real `tool.execute.before`/stream captures already committed to this
//     repository before #1640 (internal/execution/testdata/opencode_stream_
//     research_sample.jsonl, opencode_stream_cloud_capture.jsonl, both
//     `"tool":"edit"`) and docs/decisions/022-opencode-multi-provider-adapter.md
//     § 6's own "observed default tools" list (bash, edit, write, read, grep,
//     glob, task, todowrite, skill, webfetch) plus its § 9 "explore" subagent
//     permission dump (list, websearch, grep, glob, bash, webfetch, read).
//
// The two captures disagree on the file-mutation tool surface: the static
// `debug agent build` dump names `apply_patch` and omits `edit`/`write`/
// `list`/`websearch` entirely, while the real dispatch captures and ADR-022
// name `edit`/`write` (with the Claude-shaped `{filePath, oldString,
// newString}` / `{filePath, content}` argument shapes #1640's issue assumed)
// and never observed `apply_patch` actually firing. This table is the UNION
// of both: every id either capture named. `edit`/`write` are gated as file
// mutations (kind "file", the mapping ADR-022 and the real captures already
// confirm). `apply_patch` is real (the static dump enables it) but its
// argument shape was never observed on a real dispatch, so it cannot be
// safely mapped to a Claude-shaped `file_path` payload — per the issue's own
// technical notes ("maps to the nearest Claude-shaped payload or is
// blocked"), it is blocked (kind "blocked") rather than guessed at. File a
// follow-up to capture a real `apply_patch` call (scripted fake
// OpenAI-compatible provider, per this issue's own probe requirement) and
// either add its mapping or confirm it never fires from opencode's own
// model-facing tool catalog.
export const TOOL_CLASSIFICATION = {
  bash: "bash",
  edit: "file",
  write: "file",
  task: "task", // intercepted unconditionally above; listed for completeness
  apply_patch: "blocked",
  read: "passthrough",
  glob: "passthrough",
  grep: "passthrough",
  list: "passthrough",
  webfetch: "passthrough", // governed by #1638's permission map, not here
  websearch: "passthrough",
  todowrite: "passthrough",
  skill: "passthrough", // Claude's own PostToolUse:Skill hook only logs usage
  question: "passthrough",
  invalid: "passthrough",
};

// runGateVerb spawns `nightgauge hook <verb>` with payload on stdin (the
// same PreToolUse JSON envelope every Claude Code hook verb reads) and
// throws, prefixed with marker, on anything but a clean allow: NIGHTGAUGE_BIN
// missing/relative, the verb failing to spawn, timing out, exiting non-zero,
// printing non-JSON, or printing a `permissionDecision:"deny"` block. Silence
// (or a parsed decision that is not a deny) is allow. Shared by every verb
// this module runs (careful-gate, workflow-gate, stage-gate, sanitize-prompt)
// so the fail-closed contract can never drift between them.
function runGateVerb(args, payload, cwd, marker) {
  const bin = process.env.NIGHTGAUGE_BIN;
  if (!bin || bin[0] !== "/") {
    throw new Error(
      `${marker} NIGHTGAUGE_BIN is not set to an absolute path; the gate cannot run, so the tool call is blocked closed`
    );
  }

  const result = spawnSync(bin, args, {
    input: JSON.stringify(payload),
    cwd,
    timeout: SPAWN_TIMEOUT_MS,
    shell: false,
    encoding: "utf8",
  });

  if (result.error) {
    throw new Error(
      `${marker} the gate could not run (${result.error.message}); the tool call is blocked closed`
    );
  }
  if (result.signal) {
    throw new Error(
      `${marker} the gate timed out or was killed (signal ${result.signal}); the tool call is blocked closed`
    );
  }
  if (result.status !== 0) {
    throw new Error(`${marker} the gate exited ${result.status}; the tool call is blocked closed`);
  }

  const stdout = (result.stdout || "").trim();
  if (stdout === "") return; // allow: silence is the documented "no decision"

  let decision;
  try {
    decision = JSON.parse(stdout);
  } catch {
    throw new Error(
      `${marker} the gate's output did not parse as JSON; the tool call is blocked closed`
    );
  }

  const hookOutput = decision && decision.hookSpecificOutput;
  if (hookOutput && hookOutput.permissionDecision === "deny") {
    const reason = hookOutput.permissionDecisionReason || "blocked by the gate";
    throw new Error(`${marker} ${reason}`);
  }
}

// gateCwd resolves the directory every spawned verb runs in and reads its
// config from: the run's worktree, exactly what the Claude Code hook process
// sees (ctx.directory/ctx.worktree), falling back to this process's own cwd
// outside a Nightgauge run.
function gateCwd(ctx) {
  return (ctx && (ctx.directory || ctx.worktree)) || process.cwd();
}

// AC9's spike (ADR-022 amendment 2026-09-14) could not determine, within its
// bound, whether opencode 1.18.30 calls tool.execute.before for a tool a
// subagent (`task`) session runs — only that the top-level `task` call
// itself always does, since it is this session's own tool call. Until that
// question is settled, "task" is denied unconditionally, careful mode on or
// off: a subagent session this plugin cannot verify it gates is worse than
// no subagent at all. This is independent of, and checked before, every
// other gate below — including sanitize-prompt: the issue that added the
// gates below (#1640) reads its own AC3 ("task calls ... pass hook
// sanitize-prompt") as superseded by this still-open fallback, since a
// sanitize-prompt pass would let a `task` call proceed past this check. See
// the #1640 PR description for that reading, recorded as a deviation rather
// than a silent relaxation of TestNodeHarnessDeniesTask's contract.
export async function toolExecuteBefore(ctx, input, output) {
  if (!input) return;
  if (input.tool === "task") {
    throw new Error(
      `${TASK_MARKER} subagent (task) sessions are denied: opencode 1.18.30's tool.execute.before coverage inside a task session is unverified (AC9, ADR-022 amendment 2026-09-14)`
    );
  }

  const kind = TOOL_CLASSIFICATION[input.tool];
  if (kind === undefined) {
    throw new Error(`${UNKNOWN_TOOL_MARKER} ${input.tool}`);
  }
  if (kind === "passthrough" || kind === "task") return;

  const cwd = gateCwd(ctx);
  const args = output && output.args ? output.args : {};

  if (kind === "blocked") {
    throw new Error(
      `${WORKFLOW_MARKER} ${input.tool} has no verified Claude-shaped payload mapping on opencode 1.18.30 (file it); a mutating tool call this plugin cannot safely gate is blocked closed`
    );
  }

  if (kind === "file") {
    const filePath = typeof args.filePath === "string" ? args.filePath : "";
    const toolName = input.tool === "edit" ? "Edit" : "Write";
    const payload = {
      tool_name: toolName,
      cwd,
      tool_input: { file_path: filePath },
    };
    runGateVerb(["hook", "workflow-gate"], payload, cwd, WORKFLOW_MARKER);
    return;
  }

  // kind === "bash": workflow-gate, careful-gate (#1635), stage-gate, in
  // hooks.json's PreToolUse:Bash order — first deny wins.
  const command = typeof args.command === "string" ? args.command : "";
  const payload = { tool_name: "Bash", cwd, tool_input: { command } };
  runGateVerb(["hook", "workflow-gate"], payload, cwd, WORKFLOW_MARKER);
  runGateVerb(["hook", "careful-gate"], payload, cwd, MARKER);
  runGateVerb(["hook", "stage-gate"], payload, cwd, STAGE_MARKER);
}

// commandExecuteBefore is `command.execute.before`'s gate (#1640 AC3): the
// command's expanded text is screened by `hook sanitize-prompt`, Task-shaped
// (the same verb and payload contract PreToolUse:Task drives on Claude
// Code), and a block throws to abort the command (opencode 1.18.30's
// documented `command.execute.before` contract: a throw aborts).
//
// The screened text is every `type:"text"` part of `output.parts` (the
// expansion this hook fires to let a plugin inspect/override, mirroring
// tool.execute.before's already-relied-upon output.args — #1635) joined with
// the command's own name and raw argument string, so a hostile expansion
// cannot hide behind a benign command name and an injection cannot hide
// behind hostile-looking argv the model never actually resolved into parts.
export async function commandExecuteBefore(ctx, input, output) {
  if (!input) return;
  const cwd = gateCwd(ctx);
  const parts = output && Array.isArray(output.parts) ? output.parts : [];
  const partsText = parts
    .filter((p) => p && p.type === "text" && typeof p.text === "string")
    .map((p) => p.text)
    .join("\n");
  const prompt = [input.command, input.arguments, partsText]
    .filter((s) => typeof s === "string" && s !== "")
    .join("\n");
  if (prompt === "") return;

  const payload = { tool_name: "Task", cwd, tool_input: { prompt } };
  runGateVerb(["hook", "sanitize-prompt"], payload, cwd, SANITIZE_MARKER);
}
