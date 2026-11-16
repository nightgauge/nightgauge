// gates.js is the MANDATORY gate module nightgauge.js imports statically
// (#1635): a broken or missing gates.js fails nightgauge.js's own import,
// which is how the whole plugin fails closed instead of loading with no
// gate at all.
//
// toolExecuteBefore (#1635, extended by #1640, ported to OpenCode 2.x by
// #3302) is registered through ctx.tool.hook("execute.before") and receives
// 2.x's one argument, {tool, sessionID, agent, messageID, id, input}, with the
// tool's arguments under `input` (file tools name their file `input.path`).
// It runs the same verbs the Claude Code PreToolUse hooks run (internal/hooks
// via `nightgauge hook <verb>`), so a stage running under OpenCode is blocked
// by the same rules as one running under Claude Code:
//
//   - "shell"        -> workflow-gate, careful-gate, stage-gate (in that
//                       order, matching claude-plugins/nightgauge/hooks/hooks.json's
//                       PreToolUse:Bash chain — first deny wins).
//   - "edit"/"write" -> workflow-gate only, with a Claude-shaped `file_path`
//                       payload (hooks.json's PreToolUse:Edit|Write).
//   - "read"         -> external-directory-gate: resolves symlinks and
//                       refuses a read whose resolved directory falls outside
//                       the worktree and every OpenCode external_directory
//                       allow-listed root (#1816) — opencode's own permission
//                       map matches a path lexically, so a symlink planted
//                       inside an allow-listed directory after the config is
//                       written is invisible to it.
//   - "read", "edit", "write" also refuse a path that matches the
//                       permission map's secret (and, for edit/write,
//                       project-config) deny patterns once letter case is
//                       ignored (#1827; see caseFoldedDeny).
//   - "subagent"     -> always denied (see the subagent note below),
//                       independent of every other gate.
//   - every other tool id in TOOL_CLASSIFICATION -> passthrough (read-only,
//                       or governed elsewhere, e.g. webfetch by #1638's
//                       permission map) or, for a tool this table cannot map
//                       safely, blocked.
//   - a tool id ABSENT from TOOL_CLASSIFICATION -> blocked closed
//                       ([nightgauge-gate:unknown-tool]), so a new upstream
//                       mutating tool can never bypass every gate here by
//                       simply not being on the list.
//
// A throw blocks the tool: on 2.x the hook is awaited and the thrown message
// becomes the tool's error result, which the model reads (observed on 2.2.7,
// #3302).
//
// Model-authored tool arguments reach every verb only as stdin JSON, on a
// fixed argv, never on a shell command line.
import { spawnSync } from "node:child_process";
import path from "node:path";

// Bounds one gate verb. The gate is fail-closed and runs synchronously on
// opencode's thread, so a longer bound only delays a hung gate, while a short
// one falsely denies tool calls under load: 5000ms timed out (spawnSync
// ETIMEDOUT) in TestExperimentalToolIDsClassified under -race at load avg ~42
// (#3025).
const SPAWN_TIMEOUT_MS = 15000;

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

// wildcardMatch is opencode's own Wildcard.match (the same rule
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

// SUBAGENT_MARKER prefixes the unconditional subagent denial's own error,
// distinct from every other marker, so a stage's remediation output can tell
// a careful-mode/workflow/stage block apart from it.
const SUBAGENT_MARKER = "[nightgauge-gate:subagent-denied]";

// TOOL_CLASSIFICATION is the pinned table of every tool id OpenCode 2.x
// offers the model: edit, execute, glob, grep, question, read, shell, skill,
// subagent, webfetch, websearch and write (spike #3197 § 3, the tool list a
// 2.2.7 request carried). 1.x's bash and task are 2.x's shell and subagent;
// 1.x's list, todowrite, apply_patch, lsp, plan_exit, invalid and MCP
// resource tools are not offered on 2.x and are refused as unknown.
//
//   - edit {path, oldString, newString} and write {path, content} are gated
//     as file mutations (kind "file"), observed on 2.2.7.
//   - execute is new on 2.x and its argument shape has not been observed on
//     a Nightgauge dispatch. It runs code, so it is blocked closed rather than
//     guessed at.
//   - question asks the user; a headless run cannot answer, and it changes
//     nothing, so it passes through.
//
// TOOL_CLASSIFICATION is built on a null-prototype object and frozen so it
// can never be read through Object.prototype (a tool id of "constructor",
// "__proto__", "toString", ... would otherwise look up an inherited value
// instead of undefined and skip the unknown-tool check below; see
// TestPrototypeKeyToolIDsAreBlocked) or mutated by a co-loaded module.
export const TOOL_CLASSIFICATION = Object.freeze(
  Object.assign(Object.create(null), {
    shell: "shell",
    edit: "file",
    write: "file",
    subagent: "subagent", // intercepted unconditionally; listed for completeness
    execute: "blocked",
    read: "read",
    glob: "passthrough",
    grep: "passthrough",
    webfetch: "passthrough", // governed by #1638's permission map, not here
    websearch: "passthrough",
    skill: "passthrough", // Claude's own PostToolUse:Skill hook only logs usage
    question: "passthrough",
  })
);

// runGateVerb spawns `nightgauge hook <verb>` with payload on stdin (the
// same PreToolUse JSON envelope every Claude Code hook verb reads) and
// throws, prefixed with marker, on anything but a clean allow: NIGHTGAUGE_BIN
// missing/relative, the verb failing to spawn, timing out, exiting non-zero,
// printing non-JSON, or printing a `permissionDecision:"deny"` block. Silence
// (or a parsed decision that is not a deny) is allow. Shared by every verb
// this module runs (careful-gate, workflow-gate, stage-gate)
// so the fail-closed contract can never drift between them.
function runGateVerb(args, payload, cwd, marker) {
  const bin = process.env.NIGHTGAUGE_BIN;
  if (!bin || bin[0] !== "/") {
    throw new Error(
      `${marker} NIGHTGAUGE_BIN is not set to an absolute path; the gate cannot run, so the tool call is blocked closed`
    );
  }

  const startedAt = Date.now();
  const result = spawnSync(bin, args, {
    input: JSON.stringify(payload),
    cwd,
    timeout: SPAWN_TIMEOUT_MS,
    shell: false,
    encoding: "utf8",
  });

  if (result.error) {
    throw new Error(
      `${marker} the gate could not run (${result.error.message}) after ${Date.now() - startedAt}ms (bound ${SPAWN_TIMEOUT_MS}ms); the tool call is blocked closed`
    );
  }
  if (result.signal) {
    throw new Error(
      `${marker} the gate timed out or was killed (signal ${result.signal}) after ${Date.now() - startedAt}ms (bound ${SPAWN_TIMEOUT_MS}ms); the tool call is blocked closed`
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
// config from: the run's worktree, which OpenCode 2.x gives setup as
// ctx.location.directory (observed on 2.2.7), falling back to this process's
// own cwd outside a Nightgauge run.
export function gateCwd(ctx) {
  const dir = ctx && ctx.location && ctx.location.directory;
  return typeof dir === "string" && dir !== "" ? dir : process.cwd();
}

// Subagents. On 2.2.7 the execute.before hook fires for a subagent
// session's own tool calls, with that session's sessionID and agent (spike
// #3197 § 4, re-observed by #3302), so the gates below would bound them. What
// nothing bounds yet is a subagent's consumption: its steps and tokens never
// reach the stage's stream, so the stage's turn and token ceilings and its
// cost budget (#1748) are blind to it while it runs. Until a subagent counts
// against those caps, "subagent" is denied unconditionally, careful mode on
// or off, before every other gate (the ADR-022 amendment dated 2026-10-05).
// READ_MAX_LINES_ENV is opencodeplugin.EnvReadMaxLines (plugin.go): the
// most lines one read returns (#2178), set per stage by the Go side.
const READ_MAX_LINES_ENV = "NIGHTGAUGE_OPENCODE_READ_MAX_LINES";

// capReadLimit gives a read that names no limit, or a larger one, the
// stage's read cap. On 2.2.7 setting arg.input.limit in execute.before is what
// the read runs with: a probe that set limit 2 on a 30-line file got "Read
// file calc.py, lines 1-2 ... [Output truncated. Continue reading with
// offset: 3]", the visible marker the model pages from (#3302). An unset,
// non-numeric or non-positive cap leaves the read as the model asked.
export function capReadLimit(input) {
  const cap = Number.parseInt(process.env[READ_MAX_LINES_ENV] || "", 10);
  if (!Number.isInteger(cap) || cap <= 0) return;
  if (!input || typeof input !== "object") return;
  const asked = input.limit;
  if (typeof asked === "number" && Number.isFinite(asked) && asked > 0 && asked <= cap) return;
  input.limit = cap;
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

// WRITE_COMMANDS mark a shell call as a write (#2190): a call that runs one
// of these is never counted against the exploration budget.
const WRITE_COMMANDS = new Set(["tee", "mkdir", "cp", "mv"]);

// MARKER_COMMANDS only print or move the shell (#2190): a shell call whose
// every command is one of these is phase-marker output, not exploration.
const MARKER_COMMANDS = new Set(["printf", "echo", "cd", "true", ":"]);

// LAYOUT_PATH_SUBST matches `$(nightgauge layout path <class> [name])`, the
// way a stage names per-clone state it reads (ADR-024 § 7). shellExplorationPaths
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

// shellWritesFile reports whether command clearly writes a file: a redirect
// to a path other than /dev/null (a heredoc into a file included), a tee,
// mkdir, cp or mv, or a `nightgauge layout write|append` (ADR-024 § 7).
function shellWritesFile(command, segments) {
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

// shellSegments splits command into its commands' words, skipping leading
// VAR=value assignments.
function shellSegments(command) {
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

// shellExplorationPaths returns the path-like arguments of a shell call that
// counts as exploration, or null when it is clearly not exploration (#2190):
// a write, or only phase-marker output. Anything uncertain (command
// substitution, loops, jq, python -c, ...) counts.
function shellExplorationPaths(raw) {
  if (typeof raw !== "string" || raw.trim() === "") return null;
  const command = raw.replace(LAYOUT_PATH_SUBST, LAYOUT_PATH_TARGET);
  const segments = shellSegments(command);
  if (segments.length === 0) return null;
  if (shellWritesFile(command, segments)) return null;
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
      return [args.path];
    case "grep":
    case "glob":
      return [typeof args.path === "string" && args.path !== "" ? args.path : "."];
    case "shell":
      return shellExplorationPaths(args.command);
    default:
      return null;
  }
}

// enforceExplorationBudget counts exploration calls against the stage's
// budget (#2188) and, once the budget is spent, refuses further ones with
// an error telling the model to write the plan. A call whose every target
// is a skill file, .nightgauge/ or per-clone context is exempt. Writes, edits and any
// non-read command are never counted or refused.
export function enforceExplorationBudget(ctx, arg) {
  const budget = Number.parseInt(process.env[EXPLORATION_BUDGET_ENV] || "", 10);
  if (!Number.isInteger(budget) || budget <= 0) return;
  if (!arg) return;
  const targets = explorationTarget(arg.tool, toolInput(arg));
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

// toolInput returns a 2.x hook argument's tool arguments object, or {} when
// it has none, so a malformed argument is gated as one with no arguments.
function toolInput(arg) {
  return arg && arg.input && typeof arg.input === "object" ? arg.input : {};
}

// requirePath returns a file tool's input.path, and blocks the call closed
// when it is missing, empty or not a string: the verbs this module runs treat
// an empty file_path as nothing to check (external-directory-gate allows it),
// so gating an empty path would let a call through under an argument key the
// gate does not read.
function requirePath(tool, args) {
  if (typeof args.path === "string" && args.path !== "") return args.path;
  throw new Error(
    `${WORKFLOW_MARKER} ${tool} names no input.path, the only file argument OpenCode 2.x's ${tool} takes; the call is blocked closed`
  );
}

export async function toolExecuteBefore(ctx, arg) {
  if (!arg) return;
  const tool = arg.tool;
  if (tool === "subagent") {
    throw new Error(
      `${SUBAGENT_MARKER} subagent sessions are denied: a subagent's steps and tokens never reach the stage's turn, token and cost budgets while it runs (ADR-022 amendment 2026-10-05, #1805)`
    );
  }

  // Object.hasOwn reads unambiguously as "is this id ON the table", and stays
  // correct even if the table's null-prototype construction ever changes.
  if (typeof tool !== "string" || !Object.hasOwn(TOOL_CLASSIFICATION, tool)) {
    throw new Error(`${UNKNOWN_TOOL_MARKER} ${tool}`);
  }
  const kind = TOOL_CLASSIFICATION[tool];
  enforceExplorationBudget(ctx, arg);
  if (kind === "passthrough" || kind === "subagent") return;

  const cwd = gateCwd(ctx);
  const args = toolInput(arg);

  if (kind === "blocked") {
    throw new Error(
      `${WORKFLOW_MARKER} ${tool} has no verified Claude-shaped payload mapping on OpenCode 2.x (file it); a tool call this plugin cannot safely gate is blocked closed`
    );
  }

  if (kind === "file") {
    const filePath = requirePath(tool, args);
    const toolName = tool === "edit" ? "Edit" : "Write";
    const payload = {
      tool_name: toolName,
      cwd,
      tool_input: { file_path: filePath },
    };
    runGateVerb(["hook", "workflow-gate"], payload, cwd, WORKFLOW_MARKER);
    enforceCaseFoldedDeny(tool, filePath, cwd, [
      ...SECRET_DENY_PATTERNS,
      ...PROJECT_CONFIG_DENY_PATTERNS,
    ]);
    return;
  }

  if (kind === "shell") {
    // workflow-gate, careful-gate (#1635), stage-gate, in hooks.json's
    // PreToolUse:Bash order — first deny wins.
    // An empty command is nothing to check for every verb below, so a call
    // whose input names no string command is blocked closed, as requirePath
    // does for file tools: a shell argument under another key must never be
    // gated as an empty command and allowed.
    if (typeof args.command !== "string" || args.command === "") {
      throw new Error(
        `${WORKFLOW_MARKER} ${tool} names no input.command, the only command argument OpenCode 2.x's ${tool} takes; the call is blocked closed`
      );
    }
    const command = args.command;
    const payload = { tool_name: "Bash", cwd, tool_input: { command } };
    runGateVerb(["hook", "workflow-gate"], payload, cwd, WORKFLOW_MARKER);
    runGateVerb(["hook", "careful-gate"], payload, cwd, MARKER);
    runGateVerb(["hook", "stage-gate"], payload, cwd, STAGE_MARKER);
    return;
  }

  if (kind === "read") {
    const filePath = requirePath(tool, args);
    const payload = {
      tool_name: "Read",
      cwd,
      tool_input: { file_path: filePath },
    };
    runGateVerb(["hook", "external-directory-gate"], payload, cwd, EXTERNAL_DIRECTORY_MARKER);
    enforceCaseFoldedDeny(tool, filePath, cwd, SECRET_DENY_PATTERNS);
    capReadLimit(arg.input);
    return;
  }

  // Every kind TOOL_CLASSIFICATION can hold is handled above; reaching here
  // means the table maps this id to a kind this function does not recognise.
  // Fail closed rather than fall through to an implicit allow.
  throw new Error(
    `${UNKNOWN_TOOL_MARKER} ${tool} (unrecognised classification kind ${JSON.stringify(kind)})`
  );
}
