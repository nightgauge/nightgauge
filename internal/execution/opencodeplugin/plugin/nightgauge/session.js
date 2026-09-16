// session.js gives an OpenCode stage the same session-lifecycle coverage
// Claude Code's hooks give a stage (#1641): compaction context re-injection,
// post-compaction auto-continue suppression, idle stop-verification,
// skill-usage telemetry and a rate-limited permission-ask notification. Every
// export here is OPTIONAL from nightgauge.js's point of view
// (optionalHooks): a missing or throwing function here degrades that one
// hook to a no-op, never breaks plugin load the way gates.js's mandatory
// import does — this module observes and records, it is never a gate.
//
// Every spawn below is bounded — by SPAWN_TIMEOUT_MS here, or, for the one
// verb spawned on a terminal event and deliberately let go of
// (runHookDetached), by that verb's own Go-side bound — and its result is never
// allowed to change what the tool call, permission decision or model turn
// does: a hung, missing or failing verb degrades to "nothing recorded",
// never a throw. Model-authored content never reaches a spawned verb's argv;
// every payload travels as stdin JSON, exactly as gates.js's careful-gate
// already does.
//
// runHook is async (node:child_process spawn, not spawnSync) on purpose
// (#1641 fixed forward): opencode is a single-threaded event loop process,
// and spawnSync blocks that ENTIRE process — every other session's tool
// calls, timers and bus events — for as long as the child runs, up to
// SPAWN_TIMEOUT_MS. An async spawn lets opencode keep servicing everything
// else while a hook verb runs; the bound, the argv/stdin contract and the
// fail-open "not ok" result are unchanged.
//
// There are two spawn helpers, for two genuinely different lifetimes
// (#1810):
//
//   - runHook, for a verb whose result this process will still be alive to
//     see. Its promise is either awaited (sessionCompacting's
//     inject-context call, the one case where opencode is itself waiting on
//     the output before it can proceed) or left unawaited and only
//     .catch()-guarded (notifyOnce, toolExecuteBefore's skill-usage call).
//     Its SPAWN_TIMEOUT_MS timer, which lives in THIS process, is what
//     bounds the child.
//
//   - runHookDetached, for a verb spawned on a TERMINAL event, where this
//     process may be gone before the child has even started work. Measured
//     against the pinned 1.18.30 binary (#1810): opencode does NOT await the
//     promise a plugin's `event` hook returns, and a one-shot `opencode run`
//     exits within ~10 ms of publishing session.idle — a continuation
//     scheduled 20 ms out never runs at all, and an `await` inside the event
//     hook never resumes. Nothing this process schedules can therefore
//     record a session.idle verb's result, and no timer of this process's
//     can bound it either. So the child is unref'd (this process never waits
//     on it), it writes its OWN result line into the run's events file, and
//     it enforces its OWN bound. See event()'s session.idle branch.
import fs from "node:fs";
import path from "node:path";
import { spawn } from "node:child_process";

const SPAWN_TIMEOUT_MS = 5000;

// EVENTS_MAX_BYTES mirrors opencodeplugin.eventsMaxBytes (events.go): once
// the events file reaches this size, one "truncated" line is appended and
// every further write this process makes is skipped.
const EVENTS_MAX_BYTES = 1024 * 1024; // 1 MiB

// PERMISSION_NOTIFY_INTERVAL_MS is the per-session notify throttle: at most
// one desktop notification per session per 60 s, however many times
// permission.ask fires in that window.
const PERMISSION_NOTIFY_INTERVAL_MS = 60000;

// INJECT_CONTEXT_MAX_BYTES bounds what `hook inject-context` may add to a
// compaction prompt (technical notes: "append its JSON (≤4 KB)").
const INJECT_CONTEXT_MAX_BYTES = 4096;

// eventsTruncated is module-level (not per-call) state: Node/Bun evaluates
// this module once per opencode process, so the flag correctly survives
// across every session and hook call that process ever makes, and a second
// "truncated" line is never written once the first one is.
let eventsTruncated = false;

// lastNotifyAt tracks, per sessionID, the epoch ms of this process's last
// notify spawn — the whole of the 60 s per-session throttle.
const lastNotifyAt = new Map();

// childSessionIDs is every session id this process has ever observed with a
// non-empty session.info.parentID, learned from session.created/
// session.updated events (1.18.30's Session.Info carries an optional
// parentID: observed directly on the bundled client bundle's own
// `session.updated` reducer and `experimental.session.list({roots: ...})`
// call). Module-level for the same reason as eventsTruncated/lastNotifyAt: it
// must survive across every hook call this one opencode process makes.
// gates.js denies the `task` tool unconditionally (ADR-022, AC9), so no
// child session is known to reach any hook this plugin registers today —
// this set is expected to stay empty in production until that denial lifts,
// and every appendEvent call below still reads it rather than hard-coding
// false, so child reporting is correct the day it does.
const childSessionIDs = new Set();

// SKILL_ID_RE bounds what toolExecuteBefore ever treats as a skill id: opaque
// identifiers only (the "skill" tool's own schema field), never a sentence.
// A model-authored value that fails this (an object, or free text) is never
// recorded verbatim — see toolExecuteBefore's own comment.
const SKILL_ID_RE = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;

// isChildSession reports whether sessionID was ever seen with a non-empty
// parentID.
function isChildSession(sessionID) {
  return !!sessionID && childSessionIDs.has(sessionID);
}

// trackSessionParentage records session.created/session.updated's own
// parentage, when present, so later events for the same sessionID can be
// tagged as a child session rather than always reporting false.
function trackSessionParentage(props) {
  const info = props && props.info;
  if (info && info.id && info.parentID) {
    childSessionIDs.add(info.id);
  }
}

function resolveCwd(ctx) {
  return (ctx && (ctx.directory || ctx.worktree)) || process.cwd();
}

// resolveNightgaugeBin returns NIGHTGAUGE_BIN only when it is an absolute
// path, the same closed-by-default rule gates.js's careful-gate uses: a
// relative or unset value means nothing here is ever spawned.
function resolveNightgaugeBin() {
  const bin = process.env.NIGHTGAUGE_BIN;
  if (!bin || bin[0] !== "/") return null;
  return bin;
}

// runHook spawns bin with args, feeding stdinPayload (a string, or null for
// no stdin) and resolves {ok, stdout} — ok is false on any spawn error,
// signal, timeout or non-zero exit, in which case stdout is always "". Every
// caller below treats a not-ok result as "nothing to record", never as a
// reason to throw: this module is telemetry, not a gate.
//
// The child is spawned detached (POSIX: its own process group, pgid ==
// child.pid) so that on timeout the WHOLE group is killed, not just the
// direct child: `hook notify`'s own osascript/notify-send grandchild (or any
// verb that shells out further) would otherwise survive its parent's SIGKILL
// and keep running past SPAWN_TIMEOUT_MS. Killing `-child.pid` reaches the
// group; killing child.pid alone would not.
function runHook(bin, args, cwd, stdinPayload) {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(bin, args, {
        cwd,
        stdio: ["pipe", "pipe", "ignore"],
        shell: false,
        detached: true,
      });
    } catch {
      resolve({ ok: false, stdout: "" });
      return;
    }

    let stdout = "";
    let timedOut = false;
    let settled = false;

    const finish = (ok) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ ok, stdout: ok ? stdout.trim() : "" });
    };

    // killGroup reaps the child and every grandchild it spawned: SIGKILL to
    // -pid targets the process group detached:true created, not just the
    // one pid. If the group kill itself fails (e.g. the child already
    // exited between the timer firing and this running), falling back to
    // killing the child directly is still best-effort cleanup, never a
    // throw.
    const killGroup = () => {
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch {
        try {
          child.kill("SIGKILL");
        } catch {
          // best effort: nothing left to reap
        }
      }
    };

    const timer = setTimeout(() => {
      timedOut = true;
      killGroup();
    }, SPAWN_TIMEOUT_MS);

    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    // child.stdout/child.stdin are their own EventEmitters: a pipe error on
    // either (e.g. the child exits or never spawns before this process
    // finishes writing/reading) surfaces as an 'error' event on that stream,
    // not on `child` itself. Node's default behaviour for an unhandled
    // 'error' event is to throw, which crashes this whole opencode process —
    // exactly the EPIPE observed when a child exits (or fails to spawn)
    // before its stdin is read (#1641 fixed forward, second pass). A no-op
    // listener here is enough: the close handler below still resolves this
    // promise from the child's own exit/signal/timeout, so nothing is lost
    // by swallowing the stream-level error itself.
    child.stdin.on("error", () => {});
    child.stdout.on("error", () => {});
    child.on("error", () => finish(false));
    child.on("close", (code, signal) => {
      if (timedOut || signal || code !== 0) {
        finish(false);
      } else {
        finish(true);
      }
    });

    try {
      if (stdinPayload == null) {
        child.stdin.end();
      } else {
        child.stdin.end(stdinPayload, "utf8");
      }
    } catch {
      // A write/end failure on stdin still lets the close handler above
      // resolve this promise from the child's own exit/signal/timeout.
    }
  });
}

// runHookDetached spawns bin with args and immediately lets go of it: its
// own process group (detached), no stdio at all, and unref()'d so this
// process's event loop is never held open by it and it is never killed when
// this process exits. It is for a verb spawned on a TERMINAL event, where
// this process is about to disappear (see runHook's own comment); such a
// verb records its own result and enforces its own bound, because nothing
// here can do either for it.
//
// It returns true when the child was started, and false when it provably was
// not — the ONE failure a caller can still act on synchronously. The
// executable check is deliberately synchronous (accessSync): spawn() reports
// ENOENT/EACCES on a LATER tick as an 'error' event, which on a terminal
// event is a tick this process may never reach, so a caller that needs to
// record "the verb could not run" has to learn it before returning.
function runHookDetached(bin, args, cwd) {
  try {
    fs.accessSync(bin, fs.constants.X_OK);
  } catch {
    return false;
  }
  let child;
  try {
    child = spawn(bin, args, { cwd, stdio: "ignore", shell: false, detached: true });
  } catch {
    return false;
  }
  // Nothing reads this child's outcome, but an unhandled 'error' event would
  // still throw and crash opencode (the #1641 EPIPE lesson): swallow it.
  child.on("error", () => {});
  child.unref();
  return true;
}

// eventsPath mirrors opencodeplugin.EventsPath (events.go) byte for byte: a
// relative or ".."-bearing NIGHTGAUGE_OUTPUT_FILE, or a missing run id,
// disables the file rather than writing elsewhere. Both env vars are the
// same ones every adapter already sets on a dispatch (adapters.RunIDEnvVar,
// opts.OutputFile) — no new plumbing is added for this.
function eventsPath() {
  const outputFile = process.env.NIGHTGAUGE_OUTPUT_FILE;
  const runID = process.env.NIGHTGAUGE_RUN_ID;
  if (!outputFile || !runID) return null;
  if (outputFile[0] !== "/") return null;
  for (const part of outputFile.split("/")) {
    if (part === "..") return null;
  }
  return path.join(path.dirname(outputFile), `opencode-events-${runID}.jsonl`);
}

// appendEvent writes one JSONL line: {v, ts, kind, session_id, child,
// detail}. detail must hold only ids, counts and verdict codes — never
// transcript, summary, tool output or prompt text (the file's own retention
// contract; internal/execution/opencodeplugin/events.go's reader drops any
// line that violates it anyway, as a second line of defence). A write
// failure of any kind is swallowed: telemetry never surfaces to the model.
function appendEvent(kind, sessionID, child, detail) {
  const target = eventsPath();
  if (!target || eventsTruncated) return;
  try {
    let size = 0;
    try {
      size = fs.statSync(target).size;
    } catch {
      size = 0;
    }
    if (size >= EVENTS_MAX_BYTES) {
      eventsTruncated = true;
      try {
        fs.appendFileSync(
          target,
          JSON.stringify({ v: 1, ts: new Date().toISOString(), kind: "truncated" }) + "\n",
          { mode: 0o600 }
        );
      } catch {
        // best effort
      }
      return;
    }
    const line =
      JSON.stringify({
        v: 1,
        ts: new Date().toISOString(),
        kind,
        session_id: sessionID || "",
        child: !!child,
        detail: detail || {},
      }) + "\n";
    fs.appendFileSync(target, line, { mode: 0o600 });
  } catch {
    // best effort
  }
}

// sessionCompacting re-injects this stage's own context (branch, last
// commit, plan progress) into the compaction prompt via the same `hook
// inject-context` verb the Claude Code SessionStart hook runs
// (internal/hooks/context.go). It only ever appends to output.context
// (opencode's own "additional context strings" channel, observed on
// 1.18.30's @opencode-ai/plugin types: experimental.session.compacting's
// output is {context: string[], prompt?: string}) — output.prompt, which
// would replace the default compaction prompt outright, is never touched.
// Any failure (no NIGHTGAUGE_BIN, a non-zero exit, a timeout, output over
// INJECT_CONTEXT_MAX_BYTES, output that is not JSON) is skipped silently:
// compaction proceeds with no added context rather than failing.
export async function sessionCompacting(ctx, input, output) {
  if (!output) return;
  const bin = resolveNightgaugeBin();
  if (!bin) return;
  const cwd = resolveCwd(ctx);
  const { ok, stdout } = await runHook(
    bin,
    ["hook", "inject-context", "--workdir", cwd],
    cwd,
    null
  );
  if (!ok || stdout === "" || stdout.length > INJECT_CONTEXT_MAX_BYTES) return;
  try {
    JSON.parse(stdout); // validate before injecting
  } catch {
    return;
  }
  if (!Array.isArray(output.context)) output.context = [];
  output.context.push(stdout);
}

// compactionAutocontinue always disables the synthetic "Continue if you have
// next steps..." user turn opencode 1.18.30 otherwise adds once a
// compaction succeeds (ADR-022): the session ends at idle instead, and
// unfinished work surfaces through the existing Go stage gates and #1643's
// resume/retry path, never as a silent continuation a $0 local run could
// loop on for hours with every USD guardrail inert. #1625's declared steps
// cap is NOT itself a hard stop underneath this on 1.18.30 — a step at or
// past the cap still executes a tool call rather than ending the turn (only
// an extra assistant nudge message is added); ADR-022's "Session-lifecycle
// events plugin" amendment measures this directly (13 loop steps against a
// declared cap of 8). So this suppression, not the steps cap, is what
// actually bounds a compacted session's runtime. There is nothing to spawn
// here, only a value to set, so this cannot itself hang or fail.
export async function compactionAutocontinue(ctx, input, output) {
  if (output) output.enabled = false;
}

// shouldNotify enforces the per-session, per-60s notify throttle: the first
// call for a session always notifies; every call inside the following 60s
// does not, whatever else it does.
function shouldNotify(sessionID) {
  const now = Date.now();
  const last = lastNotifyAt.get(sessionID) || 0;
  if (now - last < PERMISSION_NOTIFY_INTERVAL_MS) return false;
  lastNotifyAt.set(sessionID, now);
  return true;
}

// permissionAsk is the plugin's `permission.ask` hook contribution, kept for
// forward compatibility only: empirically, opencode 1.18.30 never calls this
// hook at all (0 occurrences of the literal "permission.ask" in the pinned
// binary's own trigger sites; permissions are published only as the bus
// event "permission.asked", handled in event() below). It records the ask in
// the events file and, at most once per session per 60 s, runs `hook notify`
// to alert the operator. It never reads or writes output.status: the
// permission decision is opencode's own to make, and this hook only
// observes it.
export async function permissionAsk(ctx, input) {
  if (!input) return;
  const sessionID = input.sessionID || "";
  appendEvent(
    "permission_ask",
    sessionID,
    isChildSession(sessionID),
    input.type ? { permission_type: input.type } : {}
  );
  notifyOnce(ctx, sessionID, input.type);
}

// notifyOnce runs `hook notify` at most once per sessionID per 60 s (the
// shared throttle both the permission.ask export and event()'s own
// permission.asked handling apply). Fire-and-forget: nothing here reads the
// notify verb's result, so the returned promise is deliberately not
// awaited — awaiting it would only delay this hook's own return without
// opencode gaining anything, since runHook itself is already async and never
// blocks the event loop while the child runs. The trailing .catch keeps a
// spawn error from ever becoming an unhandled promise rejection.
function notifyOnce(ctx, sessionID, permissionType) {
  if (!shouldNotify(sessionID)) return;
  const bin = resolveNightgaugeBin();
  if (!bin) return;
  const cwd = resolveCwd(ctx);
  const message = "Nightgauge: permission requested (" + (permissionType || "unknown") + ")";
  runHook(
    bin,
    ["hook", "notify", "--event", "permission_prompt"],
    cwd,
    JSON.stringify({ message })
  ).catch(() => {});
}

// event handles the session lifecycle events this module cares about:
// session.created/session.updated (track parentage only, never recorded
// directly), session.compacted (a compaction just succeeded: record one
// "compaction" event), session.idle (the session went idle: record one
// "idle" event, then hand a detached `hook stop-verify --emit-event` the job
// of recording its own verdict as a "stop_verify" event, since this process
// will not be alive to do it), and permission.asked — opencode 1.18.30's own bus
// event for a permission prompt; the plugin hook named "permission.ask" is
// never called on this binary (see permissionAsk's own comment), so this is
// the path AC5/AC6 actually run on. Every other event type is ignored. This
// function never sends a message into the session — it has no such
// capability — and never re-prompts the model: it only spawns read-only
// verification/notify verbs and records what they returned.
export async function event(ctx, input) {
  const evt = input && input.event;
  if (!evt || typeof evt.type !== "string") return;
  const props = evt.properties || {};

  if (evt.type === "session.created" || evt.type === "session.updated") {
    trackSessionParentage(props);
    return;
  }

  const sessionID = props.sessionID || "";

  if (evt.type === "session.compacted") {
    appendEvent("compaction", sessionID, isChildSession(sessionID), {});
    return;
  }

  if (evt.type === "session.idle") {
    appendEvent("idle", sessionID, isChildSession(sessionID), {});
    const bin = resolveNightgaugeBin();
    if (!bin) {
      appendEvent("stop_verify", sessionID, isChildSession(sessionID), { verdict: "no_bin" });
      return;
    }
    const cwd = resolveCwd(ctx);
    const child = isChildSession(sessionID);
    // session.idle is TERMINAL for a one-shot `opencode run`: measured
    // against the pinned 1.18.30 binary (#1810), opencode neither awaits the
    // promise this hook returns nor stays alive more than ~10 ms past
    // publishing the event, so an `await` here never resumes and a
    // .then()/.catch() continuation never runs. Either shape loses the
    // verdict silently — which is exactly how #1641's first fix left
    // TestCompactionAutocontinueSuppressionAgainstRealOpenCode reading 0
    // stop_verify events where it wants 1.
    //
    // So the verdict is not recorded from here at all. The `hook
    // stop-verify` child is handed --emit-event and writes its OWN
    // stop_verify line into this same events file (Go side: events.go's
    // AppendRunEvent, honouring the same 1 MiB cap and the same retention
    // contract), from a process that outlives opencode and bounds itself.
    // Nothing in this function waits on it, so no dispatch can be slowed by
    // a slow or hung verb — not by a millisecond.
    //
    // The session id travels as argv, not stdin, because it is opencode's
    // own opaque id and never model-authored; the Go side re-checks its
    // shape before recording it regardless.
    const args = ["hook", "stop-verify", "--workdir", cwd, "--emit-event"];
    if (sessionID) args.push("--session-id", sessionID);
    if (child) args.push("--child");
    if (!runHookDetached(bin, args, cwd)) {
      // The one outcome this process can still determine synchronously, and
      // therefore the one it still owns: the verb could not be started at
      // all, so no child exists to write the line.
      appendEvent("stop_verify", sessionID, child, { verdict: "no_bin" });
    }
    return;
  }

  if (evt.type === "permission.asked") {
    // 1.18.30's permission.asked properties: {id, sessionID, permission,
    // patterns, metadata, always, tool}. Only `permission` (the permission
    // *type*, e.g. "bash") and sessionID are ever recorded: `patterns` and
    // `metadata` carry the model-authored command text the permission asks
    // about (e.g. a literal shell command), which the events file's own
    // retention contract (never transcript or prompt text) forbids.
    const permissionType = typeof props.permission === "string" ? props.permission : "";
    appendEvent(
      "permission_ask",
      sessionID,
      isChildSession(sessionID),
      permissionType ? { permission_type: permissionType } : {}
    );
    notifyOnce(ctx, sessionID, permissionType);
  }
}

// toolExecuteBefore is this module's own tool.execute.before contribution:
// skill-usage telemetry for the native "skill" tool. nightgauge.js's shared
// tool.execute.before handler calls this AFTER gates.js's mandatory
// toolExecuteBefore, so a throw here would still leave the careful gate's
// own decision already applied — but this never throws regardless (fail-
// open, telemetry only: technical notes, "asynchronous and fail-open").
// Every other tool is untouched.
//
// tool.execute.before fires on the model's raw arguments, before opencode's
// own skill tool decodes its schema — so a confused or adversarial model can
// put anything there, including nested objects or free-text sentences (see
// the events file's own retention contract: no transcript or prompt text,
// ever). `name` is read first (the skill tool's own schema field on 1.18.30;
// `skill`/`id` are read only as a fallback for an older or divergent
// shape), and is accepted only when it is a string matching SKILL_ID_RE — an
// opaque identifier, never a sentence. Anything else records
// {skill_invalid:true} instead of the raw value, and never reaches `hook
// skill-usage`'s stdin either.
export async function toolExecuteBefore(ctx, input, output) {
  if (!input || input.tool !== "skill") return;
  const args = (output && output.args) || {};
  const sessionID = input.sessionID || "";
  const child = isChildSession(sessionID);
  const raw = args.name !== undefined ? args.name : args.skill !== undefined ? args.skill : args.id;
  if (raw === undefined || raw === null || raw === "") return;

  if (typeof raw !== "string" || !SKILL_ID_RE.test(raw)) {
    appendEvent("skill", sessionID, child, { skill_invalid: true });
    return;
  }
  const skill = raw;

  appendEvent("skill", sessionID, child, { skill });

  const bin = resolveNightgaugeBin();
  if (!bin) return;
  const cwd = resolveCwd(ctx);
  const payload = JSON.stringify({ tool_name: "Skill", cwd, tool_input: { skill } });
  // Fire-and-forget, same reasoning as notifyOnce: nothing here reads the
  // skill-usage verb's result, so its promise is not awaited.
  runHook(bin, ["hook", "skill-usage"], cwd, payload).catch(() => {});
}
