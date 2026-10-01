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
//   - "lsp"         -> the same as "read", on its filePath (#1818).
//   - "read", "edit", "write" also refuse a path that matches the
//                      permission map's secret (and, for edit/write,
//                      project-config) deny patterns once letter case is
//                      ignored (#1827; see caseFoldedDeny).
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
import path from "node:path";

const SPAWN_TIMEOUT_MS = 5000;

// MARKER prefixes a careful-gate denial (#1635, unchanged).
const MARKER = "[nightgauge-gate:careful]";

// WORKFLOW_MARKER prefixes a workflow-gate denial: configured push gate,
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

// CASE_FOLD_MARKER prefixes a case-folded deny (#1827): a read, edit or
// write whose path matches the permission map's secret or project-config
// deny patterns only once letter case is ignored.
const CASE_FOLD_MARKER = "[nightgauge-gate:case-folded-deny]";

// SECRET_DENY_PATTERNS and PROJECT_CONFIG_DENY_PATTERNS mirror
// openCodeSecretDenyBackstop and openCodeProjectConfigDenyBackstop
// (internal/execution/adapters/opencode_guard.go); a Go test holds the two
// lists equal. opencode's own permission matcher (Wildcard.match) is
// case-sensitive and escapes "[" and "]", so the map cannot say ".env" in
// any case: on a case-insensitive filesystem (APFS's default) a read of
// ".ENV" or an edit of "OPENCODE.JSON" reaches the same file while matching
// neither deny. caseFoldedDeny closes that here, before the tool runs.
export const SECRET_DENY_PATTERNS = Object.freeze([
  "*.env",
  ".env*",
  "**/*.env",
  "**/.env*",
  "*.env.*",
  "**/*.env.*",
  "**/.ssh/**",
  "**/id_rsa*",
  "**/gh/hosts.yml",
]);
export const PROJECT_CONFIG_DENY_PATTERNS = Object.freeze(["opencode.json*", ".opencode/**"]);

// wildcardMatch is opencode 1.18.30's own Wildcard.match (the same rule
// openCodeWildcardMatch models in Go): regex metacharacters other than "*"
// and "?" are escaped, "*" becomes ".*" (crossing "/"), "?" becomes ".", and
// the result is anchored at both ends.
export function wildcardMatch(target, pattern) {
  const re = pattern
    .replace(/[.+^${}()|[\]\\]/g, "\\$&")
    .replace(/\*/g, ".*")
    .replace(/\?/g, ".");
  return new RegExp("^" + re + "$", "s").test(target);
}

// caseFoldedDeny returns the first of patterns that filePath matches with
// both sides lower-cased, or "" when none does. A path inside cwd is matched
// in its worktree-relative form, as opencode matches it; a path outside cwd
// in its absolute form.
export function caseFoldedDeny(filePath, cwd, patterns) {
  if (typeof filePath !== "string" || filePath === "") return "";
  const base = cwd || process.cwd();
  const abs = path.resolve(base, filePath);
  const rel = path.relative(base, abs);
  const outside = rel === ".." || rel.startsWith(".." + path.sep) || path.isAbsolute(rel);
  const target = (outside ? abs : rel).split(path.sep).join("/").toLowerCase();
  for (const pattern of patterns) {
    if (wildcardMatch(target, pattern.toLowerCase())) return pattern;
  }
  return "";
}

function enforceCaseFoldedDeny(tool, filePath, cwd, patterns) {
  const hit = caseFoldedDeny(filePath, cwd, patterns);
  if (hit) {
    throw new Error(
      `${CASE_FOLD_MARKER} ${tool} of ${filePath} is denied: it matches the permission map's ${JSON.stringify(hit)} deny once letter case is ignored`
    );
  }
}

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
//     `list` there is a permission key only: no 1.18.x tool registers that
//     id (#1818), so it is not on this table and is refused as unknown.
//   - Three ids `ToolRegistry` adds only behind an experimental flag the
//     adapter never sets (#1818): `execute`, `lsp` and `plan_exit`. Each is
//     classified below rather than left to the unknown-tool refusal.
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
    webfetch: "passthrough", // governed by #1638's permission map, not here
    websearch: "passthrough",
    todowrite: "passthrough",
    skill: "passthrough", // Claude's own PostToolUse:Skill hook only logs usage
    question: "passthrough",
    invalid: "passthrough",
    // Experimental tools (#1818). `ToolRegistry` adds each only when its flag
    // is set, and the adapter sets none of them.
    // OPENCODE_EXPERIMENTAL_CODE_MODE: runs an orchestration script that
    // calls connected MCP tools, which no gate here can see into.
    execute: "blocked",
    // OPENCODE_EXPERIMENTAL_LSP_TOOL: read-only language-server queries on
    // `{operation, filePath, line, character}`, gated like a read of filePath.
    lsp: "read",
    // OPENCODE_EXPERIMENTAL_PLAN_MODE, cli client only: switches the session
    // from the plan agent to the build agent, a mode change no stage makes.
    plan_exit: "blocked",
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
// READ_MAX_LINES_ENV is opencodeplugin.EnvReadMaxLines (plugin.go): the
// most lines one read returns (#2178), set per stage by the Go side.
const READ_MAX_LINES_ENV = "NIGHTGAUGE_OPENCODE_READ_MAX_LINES";

// capReadLimit gives a read that names no limit, or a larger one, the
// stage's read cap. opencode 1.18.32 passes this hook's output.args object
// to the tool's execute unchanged, so setting a field on it is what the read
// runs with; its read tool then ends a capped result with "(Showing lines
// A-B of N. Use offset=B+1 to continue.)", the visible marker the model
// pages from. 1.18.32 has no config key for read's default limit (2000
// lines), and its tool_output thresholds skip read. An unset, non-numeric or
// non-positive cap leaves the read as the model asked.
export function capReadLimit(output) {
  const cap = Number.parseInt(process.env[READ_MAX_LINES_ENV] || "", 10);
  if (!Number.isInteger(cap) || cap <= 0) return;
  if (!output || typeof output.args !== "object" || output.args === null) return;
  const asked = output.args.limit;
  if (typeof asked === "number" && Number.isFinite(asked) && asked > 0 && asked <= cap) return;
  output.args.limit = cap;
}

// EXPLORATION_BUDGET_ENV is opencodeplugin.EnvExplorationBudget (plugin.go):
// how many exploration tool calls the stage may make (#2188), set by the Go
// side for feature-planning on a local endpoint only. 0 or unset is off.
const EXPLORATION_BUDGET_ENV = "NIGHTGAUGE_OPENCODE_EXPLORATION_BUDGET";

// KNOWLEDGE_DIR_ENV is opencodeplugin.EnvKnowledgeDir (plugin.go): the
// absolute knowledge-base directory, whose reads never count (#2193).
const KNOWLEDGE_DIR_ENV = "NIGHTGAUGE_OPENCODE_KNOWLEDGE_DIR";

// EXPLORATION_BUDGET_MARKER prefixes the refusal of an exploration call
// past the budget.
const EXPLORATION_BUDGET_MARKER = "[nightgauge-gate:exploration-budget]";

// WRITE_COMMANDS mark a bash call as a write (#2190): a call that runs one
// of these is never counted against the exploration budget.
const WRITE_COMMANDS = new Set(["tee", "mkdir", "cp", "mv"]);

// MARKER_COMMANDS only print or move the shell (#2190): a bash call whose
// every command is one of these is phase-marker output, not exploration.
const MARKER_COMMANDS = new Set(["printf", "echo", "cd", "true", ":"]);

// LAYOUT_PATH_SUBST matches `$(nightgauge layout path <class> [name])`, the
// way a stage names per-clone state it reads (ADR-024 § 7). bashExplorationPaths
// replaces each match with LAYOUT_PATH_TARGET, which isExemptPath exempts.
const LAYOUT_PATH_SUBST =
  /\$\(\s*nightgauge\s+layout\s+path\s+(?:pipeline|plans|retros|logs)(?:\s+[^\s()`$]+)?\s*\)/g;
const LAYOUT_PATH_TARGET = "nightgauge-layout:/";

// explorationCount is this process's count of budgeted calls. One opencode
// process runs one stage, so the count is the stage's.
let explorationCount = 0;

// resetExplorationCount is for tests only.
export function resetExplorationCount() {
  explorationCount = 0;
}

// isExemptPath reports whether p is skill-directed or pipeline context
// (#2190): a path through a skills/_shared/, _includes/ or *feature-planning/
// directory, one under a .nightgauge/knowledge/ directory or the configured
// knowledge base (#2193), one under the worktree's own .nightgauge/, or
// per-clone state (ADR-024 § 7): a path under a .git/nightgauge/ directory
// or a `$(nightgauge layout path ...)` reference. Other skill sources
// (skills/nightgauge-*/ in the core repository) are code under exploration
// and count.
export function isExemptPath(p, cwd) {
  if (typeof p !== "string" || p === "") return false;
  if (p.startsWith(LAYOUT_PATH_TARGET)) return true;
  const abs = path.resolve(cwd, p);
  const knowledge = process.env[KNOWLEDGE_DIR_ENV];
  if (typeof knowledge === "string" && path.isAbsolute(knowledge)) {
    const k = path.relative(path.resolve(knowledge), abs);
    if (k === "" || (!k.startsWith("..") && !path.isAbsolute(k))) return true;
  }
  const segs = abs.split(path.sep);
  const dirs = segs.slice(0, -1);
  for (let i = 0; i < dirs.length; i++) {
    if (dirs[i] === ".nightgauge" && dirs[i + 1] === "knowledge") return true;
    if (dirs[i] === ".git" && dirs[i + 1] === "nightgauge") return true;
    if (dirs[i] === "_includes" || dirs[i].endsWith("feature-planning")) return true;
    if (dirs[i] === "skills" && dirs[i + 1] === "_shared") return true;
  }
  const rel = path.relative(cwd, abs);
  if (rel.startsWith("..") || path.isAbsolute(rel)) return false;
  return rel.split(path.sep)[0] === ".nightgauge";
}

// bashWritesFile reports whether command clearly writes a file: a redirect
// to a path other than /dev/null (a heredoc into a file included), a tee,
// mkdir, cp or mv, or a `nightgauge layout write|append` (ADR-024 § 7).
function bashWritesFile(command, segments) {
  const stripped = command.replace(/[0-9&]?>>?\s*\/dev\/null|[0-9]?>&[0-9-]/g, "");
  if (/>>?\s*[^\s&|;]/.test(stripped)) return true;
  return segments.some((words) => WRITE_COMMANDS.has(words[0]) || isLayoutWrite(words));
}

// isLayoutWrite reports whether words run `nightgauge layout write` or
// `nightgauge layout append`, the way a stage writes per-clone state.
function isLayoutWrite(words) {
  return (
    words[0] === "nightgauge" &&
    words[1] === "layout" &&
    (words[2] === "write" || words[2] === "append")
  );
}

// bashSegments splits command into its commands' words, skipping leading
// VAR=value assignments.
function bashSegments(command) {
  const out = [];
  for (const seg of command.split(/\|\||&&|[|;\n]/)) {
    const words = seg
      .trim()
      .split(/\s+/)
      .filter((w) => w !== "");
    let i = 0;
    while (i < words.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[i])) i++;
    if (i < words.length) out.push(words.slice(i));
  }
  return out;
}

// bashExplorationPaths returns the path-like arguments of a bash call that
// counts as exploration, or null when it is clearly not exploration (#2190):
// a write, or only phase-marker output. Anything uncertain (command
// substitution, loops, jq, python -c, ...) counts.
function bashExplorationPaths(raw) {
  if (typeof raw !== "string" || raw.trim() === "") return null;
  const command = raw.replace(LAYOUT_PATH_SUBST, LAYOUT_PATH_TARGET);
  const segments = bashSegments(command);
  if (segments.length === 0) return null;
  if (bashWritesFile(command, segments)) return null;
  if (
    !/[`]|\$\(/.test(command) &&
    segments.every(
      (w) =>
        MARKER_COMMANDS.has(w[0]) && (w[0] === "cd" || !w.slice(1).some((a) => a.includes("/")))
    )
  )
    return null;
  const paths = [];
  for (const words of segments) {
    for (const w of words.slice(1)) {
      const t = w.replace(/^['"]+|['"]+$/g, "");
      if (!t.startsWith("-") && t.includes("/")) paths.push(t);
    }
  }
  return paths;
}

// explorationTarget returns the paths an exploration call reads, or null
// when the call is not exploration (a write, an edit, a mutating command).
function explorationTarget(tool, args) {
  switch (tool) {
    case "read":
      return [args.filePath];
    case "grep":
    case "glob":
      return [typeof args.path === "string" && args.path !== "" ? args.path : "."];
    case "bash":
      return bashExplorationPaths(args.command);
    default:
      return null;
  }
}

// enforceExplorationBudget counts exploration calls against the stage's
// budget (#2188) and, once the budget is spent, refuses further ones with
// an error telling the model to write the plan. A call whose every target
// is a skill file, .nightgauge/ or per-clone context is exempt. Writes, edits and any
// non-read command are never counted or refused.
export function enforceExplorationBudget(ctx, input, output) {
  const budget = Number.parseInt(process.env[EXPLORATION_BUDGET_ENV] || "", 10);
  if (!Number.isInteger(budget) || budget <= 0) return;
  if (!input) return;
  const args = output && output.args && typeof output.args === "object" ? output.args : {};
  const targets = explorationTarget(input.tool, args);
  if (targets === null) return;
  const cwd = gateCwd(ctx);
  if (targets.length > 0 && targets.every((t) => isExemptPath(t, cwd))) return;
  if (explorationCount >= budget) {
    const issue = process.env.NIGHTGAUGE_ISSUE_NUMBER || "{N}";
    throw new Error(
      `${EXPLORATION_BUDGET_MARKER} exploration budget of ${budget} reads spent; write the plan now with nightgauge layout write plans <name>, then planning-${issue}.json with nightgauge layout write pipeline`
    );
  }
  explorationCount++;
}

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
  enforceExplorationBudget(ctx, input, output);
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
    enforceCaseFoldedDeny(input.tool, filePath, cwd, [
      ...SECRET_DENY_PATTERNS,
      ...PROJECT_CONFIG_DENY_PATTERNS,
    ]);
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
    enforceCaseFoldedDeny(input.tool, filePath, cwd, SECRET_DENY_PATTERNS);
    // `limit` is read's own argument; lsp has none to cap.
    if (input.tool === "read") capReadLimit(output);
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
// tool.execute.before's already-relied-upon output.args — #1635), plus the
// description and expanded prompt of every `type:"subtask"` part, the shape
// 1.18.30 sends for a command whose agent is a subagent or that sets
// `subtask: true` (#1818). That is all joined with
// the command's own name and raw argument string, so a hostile expansion
// cannot hide behind a benign command name and an injection cannot hide
// behind hostile-looking argv the model never actually resolved into parts.
export async function commandExecuteBefore(ctx, input, output) {
  if (!input) return;
  const cwd = gateCwd(ctx);
  const parts = output && Array.isArray(output.parts) ? output.parts : [];
  const partsText = parts
    .flatMap((p) => {
      if (!p) return [];
      if (p.type === "text") return [p.text];
      if (p.type === "subtask") return [p.description, p.prompt];
      return [];
    })
    .filter((s) => typeof s === "string" && s !== "")
    .join("\n");
  const prompt = [input.command, input.arguments, partsText]
    .filter((s) => typeof s === "string" && s !== "")
    .join("\n");
  if (prompt === "") return;

  const payload = { tool_name: "Task", cwd, tool_input: { prompt } };
  runGateVerb(["hook", "sanitize-prompt"], payload, cwd, SANITIZE_MARKER);
}
