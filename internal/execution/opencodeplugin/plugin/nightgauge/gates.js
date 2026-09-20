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
//   - "read"        -> external-directory-gate: resolves symlinks and
//                      refuses a read whose resolved directory falls outside
//                      the worktree and every OpenCode external_directory
//                      allow-listed root (#1816) — opencode's own permission
//                      map matches a tool call's filePath lexically, so a
//                      symlink planted inside an allow-listed directory
//                      after the config is written is invisible to it.
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
// here, unit-tested directly (plugin_gates_test.go's TestCommandExecuteBeforeSanitizes
// and TestGatesParityCorpus), and called from nightgauge.js's own
// `command.execute.before` registration BEFORE that hook's optional
// ./nightgauge/session.js delegate (#1641's file) runs — a review finding
// this round (plugin_gates_test.go's TestCommandExecuteBeforeWiredThroughEntry
// drives nightgauge.js's own hook, not this export directly, to prove the
// wiring rather than only the decision).
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

// EXTERNAL_DIRECTORY_MARKER prefixes an external-directory-gate denial: a
// Read whose resolved (symlink-followed) target falls outside the worktree
// and every OpenCode external_directory allow-listed root (#1816).
const EXTERNAL_DIRECTORY_MARKER = "[nightgauge-gate:external-directory]";

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
// The two captures do NOT disagree on the file-mutation tool surface — a
// review finding this round (see the ADR-022 amendment dated 2026-09-15,
// #1640 fix round, for the full correction) traced the difference to
// 1.18.30's OWN per-model tool selection, not to two inconsistent captures:
// `ToolRegistry.tools` enables `apply_patch` and disables `edit`/`write`
// exactly when the dispatch model's id matches `modelID.includes("gpt-") &&
// !modelID.includes("oss") && !modelID.includes("gpt-4")` (verified against
// the pinned binary's own minified source), never the other way around for
// any other model family. `debug agent build`'s empty-HOME default model
// happened to match that rule; the real dispatch captures used models that
// did not. This table is still the UNION of both — `edit`/`write` are gated
// as file mutations (kind "file", the Claude-shaped `{filePath, oldString,
// newString}` / `{filePath, content}` argument shapes ADR-022 and the real
// captures confirm) — but `apply_patch` is real and reachable from a
// GPT-family dispatch, not a static-dump artifact. Its argument shape IS
// known (`{patchText}`, a single string using `*** Add File:`/
// `*** Update File:`/`*** Delete File:`/`*** Move to:` headers per path,
// confirmed against the pinned binary's own patch parser), just never
// mapped to a Claude-shaped `file_path` payload nor observed on a real
// Nightgauge dispatch stream — per the issue's own technical notes ("maps to
// the nearest Claude-shaped payload or is blocked"), it stays blocked (kind
// "blocked") rather than guessed at. The practical consequence: any OpenCode
// dispatch against a matching GPT-family model gets no working file-edit
// tool at all under this plugin (`apply_patch` blocked, `edit`/`write` not
// offered by opencode itself). File a follow-up to map `apply_patch`'s
// `patchText` hunks to one or more Claude-shaped `file_path` payloads.
//
// TOOL_CLASSIFICATION is built on a null-prototype object and frozen so it
// can never be read through Object.prototype (a tool id of "constructor",
// "__proto__", "toString", "hasOwnProperty", ... would otherwise look up an
// inherited function/object instead of undefined and skip the unknown-tool
// fail-closed check below — a review finding this round; see
// TestPrototypeKeyToolIDsAreBlocked) or mutated by a co-loaded module.
export const TOOL_CLASSIFICATION = Object.freeze(
  Object.assign(Object.create(null), {
    bash: "bash",
    edit: "file",
    write: "file",
    task: "task", // intercepted unconditionally above; listed for completeness
    apply_patch: "blocked",
    read: "read",
    glob: "passthrough",
    grep: "passthrough",
    list: "passthrough",
    webfetch: "passthrough", // governed by #1638's permission map, not here
    websearch: "passthrough",
    todowrite: "passthrough",
    skill: "passthrough", // Claude's own PostToolUse:Skill hook only logs usage
    question: "passthrough",
    invalid: "passthrough",
    // opencode's own read-only MCP resource tools (never a repository's own
    // MCP server tool, which arrives as "<server>_<tool>" and is not in this
    // table at all — see the ADR-022 amendment this round for that gap and
    // its policy). These three only ever list or read resource metadata a
    // configured MCP server advertises, the same "read" permission category
    // opencode itself groups them under alongside read/glob/grep.
    list_mcp_resources: "passthrough",
    list_mcp_resource_templates: "passthrough",
    read_mcp_resource: "passthrough",
  })
);

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

  // Object.hasOwn is the explicit, belt-and-suspenders form of this check:
  // TOOL_CLASSIFICATION is already a frozen, null-prototype object (so a
  // prototype-key id such as "constructor" reads back undefined on its
  // own), but hasOwn keeps this check correct even if that construction
  // ever changes, and reads unambiguously as "is this id ON the table" —
  // not "is this id's value not undefined".
  if (!Object.hasOwn(TOOL_CLASSIFICATION, input.tool)) {
    throw new Error(`${UNKNOWN_TOOL_MARKER} ${input.tool}`);
  }
  const kind = TOOL_CLASSIFICATION[input.tool];
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

  if (kind === "bash") {
    // workflow-gate, careful-gate (#1635), stage-gate, in hooks.json's
    // PreToolUse:Bash order — first deny wins.
    const command = typeof args.command === "string" ? args.command : "";
    const payload = { tool_name: "Bash", cwd, tool_input: { command } };
    runGateVerb(["hook", "workflow-gate"], payload, cwd, WORKFLOW_MARKER);
    runGateVerb(["hook", "careful-gate"], payload, cwd, MARKER);
    runGateVerb(["hook", "stage-gate"], payload, cwd, STAGE_MARKER);
    return;
  }

  if (kind === "read") {
    const filePath = typeof args.filePath === "string" ? args.filePath : "";
    const payload = {
      tool_name: "Read",
      cwd,
      tool_input: { file_path: filePath },
    };
    runGateVerb(["hook", "external-directory-gate"], payload, cwd, EXTERNAL_DIRECTORY_MARKER);
    return;
  }

  // Every kind TOOL_CLASSIFICATION can hold is handled above; reaching here
  // means the table maps this id to something this function does not
  // recognise (a typo'd kind string, for example) — fail closed rather than
  // silently falling through to an implicit allow or an implicit bash run.
  throw new Error(
    `${UNKNOWN_TOOL_MARKER} ${input.tool} (unrecognised classification kind ${JSON.stringify(kind)})`
  );
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
