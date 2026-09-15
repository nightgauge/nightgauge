// edit.js gives an OpenCode stage the same PostToolUse:Edit|Write coverage
// Claude Code's hooks.json gives a stage (#1642): format-on-save, a
// version-consistency check, and a test-quality warning, run in hooks.json's
// own order — format (30 s), check-version (10 s), test-quality (5 s) —
// against a Claude-shaped `{tool_name, tool_input:{file_path,...}}` payload
// on stdin, the same envelope every Claude Code hook verb already reads.
//
// Loaded OPTIONALLY by nightgauge.js's own `tool.execute.after` registration
// (its `optionalHooks`): a missing or throwing edit.js degrades that hook to
// a no-op, never breaks plugin load the way gates.js's mandatory import does
// (nightgauge.js's own header comment). This module is a warning surface,
// never a gate — nothing it does may fail, delay past its own timeout, or
// change the tool call's result beyond appending text to it.
//
// Observed against opencode 1.18.30 (a scripted, offline probe against the
// pinned binary — a logging plugin loaded in place of this one, driven
// through the #1618 stub's `tool-edit-stop` and a temporary `write-then-stop`
// fixture, no live model and no network egress; the probe is quoted in
// #1642's own PR description, not merely asserted from reading):
//
//   - `tool.execute.after`'s `input` carries `{tool, sessionID, callID,
//     args}`, where `args` IS the tool's own camelCase arguments —
//     `{filePath, oldString, newString}` for `edit`, `{filePath, content}`
//     for `write`. This is NOT where `tool.execute.before` puts them
//     (gates.js reads `output.args` there): the two hooks disagree on
//     which side of the call carries the arguments, and this module reads
//     `input.args` accordingly.
//   - `output` carries `{title, output, metadata}`. Mutating `output.output`
//     IN PLACE is what the running session actually sends back to the model
//     on its next turn — confirmed directly: the probe's own appended
//     marker text showed up, byte for byte, in the very next
//     chat-completions request's tool-result message content, not merely in
//     the hook's own view of `output`.
import { spawnSync } from "node:child_process";

// Claude-shaped hooks.json timeouts
// (claude-plugins/nightgauge/hooks/hooks.json, PostToolUse:Edit|Write), in
// registration order. Exported so a test can pin the exact numbers without
// waiting out a real spawn timeout to observe them.
export const FORMAT_TIMEOUT_MS = 30000;
export const CHECK_VERSION_TIMEOUT_MS = 10000;
export const TEST_QUALITY_TIMEOUT_MS = 5000;

// MAX_APPEND_BYTES bounds the total warning text this hook ever appends to
// output.output — the issue's own "capped at 2 KB total", applied to every
// verb's stderr joined together, not to each verb individually.
const MAX_APPEND_BYTES = 2048;

// resolveCwd mirrors gates.js's gateCwd / session.js's resolveCwd: the run's
// worktree, exactly what the Claude Code hook process sees.
function resolveCwd(ctx) {
  return (ctx && (ctx.directory || ctx.worktree)) || process.cwd();
}

// resolveNightgaugeBin mirrors session.js's own helper: NIGHTGAUGE_BIN only
// when it is an absolute path. Unlike gates.js's gate verbs, a missing or
// relative NIGHTGAUGE_BIN here is not a fail-closed condition — this module
// never blocks a tool call — it is simply "nothing to spawn".
function resolveNightgaugeBin() {
  const bin = process.env.NIGHTGAUGE_BIN;
  if (!bin || bin[0] !== "/") return null;
  return bin;
}

// isPathContained mirrors internal/hooks/format.go's ValidateFilePath: an
// absolute path, or one with a literal ".." path segment, is refused before
// anything is ever spawned (AC3: "a path outside the worktree... spawns no
// formatter and adds no warning" — the security notes' "the plugin
// additionally skips any path outside the worktree without spawning" half;
// the Go verb re-validates independently, the other half). This checks path
// SEGMENTS ("..", not merely the substring anywhere in the path), which is
// narrower than ValidateFilePath's own `strings.Contains(cleaned, "..")` —
// deliberately: a real traversal (`../../x.go`) is refused by both, and the
// rare filename that only CONTAINS ".." as a substring without it being a
// path segment stays refused by the Go verb regardless (evaluateFormatIn
// fails closed there too), so nothing here ever double-runs or crashes on
// that edge case, only skips a client-side spawn it would not have needed.
function isPathContained(filePath) {
  if (typeof filePath !== "string" || filePath === "") return false;
  if (filePath.startsWith("/")) return false;
  return !filePath.split("/").includes("..");
}

// formatterEnabled reads the per-run OPENCODE_CONFIG_CONTENT this opencode
// process was itself spawned with
// (internal/execution/adapters/opencode_config.go's PrepareOpenCodeRun sets
// it in the same env map BuildCommand hands the child process that carries
// NIGHTGAUGE_BIN) and returns its `formatter` key (ADR-022 § 12). A missing
// or unparseable value defaults to true — opencode's own formatter is
// assumed on, so this hook stays out of its way — matching
// adapters.buildOpenCodeConfigJSON's own default
// (`orDefault(settings.Formatter, true)`): an unreadable config and a config
// that actually set `formatter: true` are indistinguishable here, and both
// must resolve the same way so a parse gap never causes a double format.
function formatterEnabled() {
  const raw = process.env.OPENCODE_CONFIG_CONTENT;
  if (!raw) return true;
  try {
    const cfg = JSON.parse(raw);
    if (typeof cfg.formatter === "boolean") return cfg.formatter;
  } catch {
    // Falls through to the default below.
  }
  return true;
}

// runQualityVerb spawns `nightgauge hook <verb>` with payload on stdin and
// returns its trimmed stderr, or "" on ANY failure: no NIGHTGAUGE_BIN, a
// spawn error, a timeout, or a non-zero exit. Every one of format,
// check-version and test-quality is documented to "always exit 0"
// (test-quality.sh's own contract, ported byte for byte by the Go verb this
// issue adds); a non-zero exit here means something is already broken, and
// this hook's own contract is "never fail the tool" regardless — so a
// broken verb degrades to "nothing to append", exactly like a missing one.
function runQualityVerb(bin, args, payload, cwd, timeoutMs) {
  if (!bin) return "";
  let result;
  try {
    result = spawnSync(bin, args, {
      input: JSON.stringify(payload),
      cwd,
      timeout: timeoutMs,
      shell: false,
      encoding: "utf8",
    });
  } catch {
    return "";
  }
  if (!result || result.error || result.signal || result.status !== 0) return "";
  return (result.stderr || "").trim();
}

// appendWarnings joins the non-empty chunks with a blank line, builds the
// full addition (a leading separator newline when output.output already has
// content, none otherwise), and truncates THAT to MAX_APPEND_BYTES UTF-8
// bytes (never splitting a multi-byte character — Buffer#toString("utf8")
// drops a trailing incomplete sequence rather than emitting a replacement
// character for it) before appending it — so output.output never grows by
// more than MAX_APPEND_BYTES regardless of whether a separator was needed.
// Mutating output IN PLACE is what the probe above confirmed actually
// reaches the model's next turn; a caller with no warnings to add (every
// chunk empty) leaves output untouched.
function appendWarnings(output, chunks) {
  const text = chunks.filter((c) => c !== "").join("\n\n");
  if (text === "" || !output || typeof output !== "object") return;
  const existing = typeof output.output === "string" ? output.output : "";
  const addition = existing === "" ? text : "\n" + text;
  const truncated = Buffer.from(addition, "utf8").subarray(0, MAX_APPEND_BYTES).toString("utf8");
  output.output = existing + truncated;
}

// toolExecuteAfter is edit.js's one export, wired from nightgauge.js's own
// `tool.execute.after` registration for EVERY tool call opencode makes, not
// only edit/write — so the first check below is the tool-id filter itself
// (issue's own scope: "Other mutating tool ids stay a follow-up (Claude
// parity is Edit|Write)"). Never subscribes to, or otherwise reacts to,
// `file.edited`: a formatter write must not re-trigger these hooks, and
// nightgauge.js has no `file.edited` registration for this module to hook
// into in the first place.
export async function toolExecuteAfter(ctx, input, output) {
  try {
    if (!input || (input.tool !== "edit" && input.tool !== "write")) return;

    const args = (input && input.args) || {};
    const filePath = typeof args.filePath === "string" ? args.filePath : "";
    if (!isPathContained(filePath)) return;

    const cwd = resolveCwd(ctx);
    const bin = resolveNightgaugeBin();
    const toolName = input.tool === "edit" ? "Edit" : "Write";
    const toolInput = { file_path: filePath };
    if (typeof args.content === "string") toolInput.content = args.content;
    if (typeof args.newString === "string") toolInput.new_string = args.newString;
    const payload = { tool_name: toolName, cwd, tool_input: toolInput };

    const formatStderr = formatterEnabled()
      ? ""
      : runQualityVerb(bin, ["hook", "format"], payload, cwd, FORMAT_TIMEOUT_MS);
    const checkVersionStderr = runQualityVerb(
      bin,
      ["hook", "check-version"],
      payload,
      cwd,
      CHECK_VERSION_TIMEOUT_MS
    );
    const testQualityStderr = runQualityVerb(
      bin,
      ["hook", "test-quality"],
      payload,
      cwd,
      TEST_QUALITY_TIMEOUT_MS
    );

    appendWarnings(output, [formatStderr, checkVersionStderr, testQualityStderr]);
  } catch {
    // toolExecuteAfter must never throw: a warning check is not a gate, and
    // this module's whole contract is "never fail the tool".
  }
}
