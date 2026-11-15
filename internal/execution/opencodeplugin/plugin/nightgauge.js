// nightgauge.js is the Nightgauge OpenCode plugin's entry module (#1635),
// ported to the OpenCode 2.x plugin definition (#3302). It is embedded in the
// Go binary (internal/execution/opencodeplugin/plugin.go) and written into the
// run's per-run plugin directory at spawn, never installed from npm or Bun.
// OpenCode 2.x loads a plugin only from a directory (a file entry logs "must
// be a directory" and runs ungated), so the per-run config's `plugins` array
// names that directory, and OpenCode loads its index.js, which re-exports this
// module's definition.
//
// On 2.x a plugin is `export default { id, setup }`. `setup` registers every
// hook imperatively through the services on its context, and a hook map it
// returns registers nothing (spike #3197 § 4), so setup returns nothing.
//
// Every registration below fired on the local `opencode v2.2.7`, observed by
// a throwaway probe plugin that logged each hook argument and every
// ctx.event.subscribe() event during stub-provider runs that edited a file,
// read one, spawned a `subagent`, asked a permission, and compacted
// (#3302 feature-dev, 2026-11-15). A name registered on 2.x that OpenCode
// does not know is not an error and never fires, so nothing here is
// registered on a guess:
//
//   - ctx.command has no `hook` method on 2.x, so the 1.x
//     command.execute.before sanitize-prompt gate has no attachment point.
//   - ctx.permission.hook names tried (ask, asked, before, request, ...)
//     never fired during a permission ask; the ask is observed as the bus
//     event permission.asked instead (session.js).
//   - No hook decides whether a session continues after compaction (the 1.x
//     experimental.compaction.autocontinue). ctx.session.hook names tried
//     besides "prompt" and "compaction" never fired.
//
// gates.js is MANDATORY and imported statically, so a broken or missing
// gates.js fails this module's own import. OpenCode then runs with the
// Nightgauge plugin not loaded, no handshake sentinel is written, and the
// manager's handshake check fails the stage closed as adapter_incompatible.
// session.js and edit.js are optional: a missing or broken one is a no-op
// for every hook it would have supplied, never a load failure.
import fs from "node:fs";
import { toolExecuteBefore } from "./nightgauge/gates.js";

// NIGHTGAUGE_PLUGIN_VERSION MUST match opencodeplugin.PluginVersion
// (plugin.go) byte for byte: TestPluginVersionAndHooksMatchGo compares them by
// reading this file's source text. It stays unexported: OpenCode 2.x's loader
// expects the module's default export to be the plugin definition, and these
// constants are not part of it.
const NIGHTGAUGE_PLUGIN_VERSION = "2";

// NIGHTGAUGE_HOOK_NAMES are every registration setup makes, in order, as
// "<service>:<name>": the handshake sentinel's "hooks" field, compared with
// opencodeplugin.HookNames (plugin.go).
const NIGHTGAUGE_HOOK_NAMES = [
  "tool:execute.before",
  "tool:execute.after",
  "session:compaction",
  "event:subscribe",
];

// optionalModule loads a sibling module that may be absent or broken. Either
// way it is a no-op for every hook it would have supplied.
async function optionalModule(specifier) {
  try {
    const mod = await import(specifier);
    return mod && typeof mod === "object" ? mod : {};
  } catch {
    return {};
  }
}

// writeHandshakeSentinel writes the handshake sentinel the manager verifies
// (opencodeplugin.VerifyLoaded/VerifyNotLate): the run's own nonce and this
// plugin's version, at the path the Go side exported as
// NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL. Outside a Nightgauge run (no sentinel
// path or nonce exported) this is a no-op.
function writeHandshakeSentinel() {
  const sentinelPath = process.env.NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL;
  const nonce = process.env.NIGHTGAUGE_OPENCODE_PLUGIN_NONCE;
  if (!sentinelPath || !nonce) return;
  const sentinel = {
    nonce,
    plugin_version: NIGHTGAUGE_PLUGIN_VERSION,
    hooks: NIGHTGAUGE_HOOK_NAMES,
  };
  fs.writeFileSync(sentinelPath, JSON.stringify(sentinel), { mode: 0o600 });
}

// consumeEvents drains an event subscription in the background. The stream
// ends when OpenCode shuts the location down; a handler that throws loses that
// one event, never the subscription.
async function consumeEvents(ctx, stream, handle) {
  try {
    for await (const evt of stream) {
      try {
        await handle(ctx, evt);
      } catch {
        // session.js observes and records; it is never a gate.
      }
    }
  } catch {
    // The subscription itself failed: nothing further to observe.
  }
}

async function setup(ctx) {
  const session = await optionalModule("./nightgauge/session.js");
  const edit = await optionalModule("./nightgauge/edit.js");

  // Observed on 2.2.7: awaited before the tool runs, in the main session and
  // in a subagent's session (with that session's sessionID and agent), with
  // one argument {tool, sessionID, agent, messageID, id, input}. A throw
  // blocks the tool, and the thrown message is the tool's error result the
  // model reads (stream: tool_use status "error"). Setting a field on
  // arg.input changes what the tool runs with (read's `limit`).
  ctx.tool.hook("execute.before", async (arg) => {
    await toolExecuteBefore(ctx, arg);
    if (typeof session.toolExecuteBefore === "function") {
      await session.toolExecuteBefore(ctx, arg);
    }
  });

  // Observed on 2.2.7: the before-hook's argument plus `status` and `result`
  // (or `error`). A text part pushed onto arg.result.content reaches the
  // tool's output the model reads, and fields set on arg.result.metadata reach
  // the stream's tool_use metadata.
  ctx.tool.hook("execute.after", async (arg) => {
    if (typeof edit.toolExecuteAfter === "function") {
      await edit.toolExecuteAfter(ctx, arg);
    }
  });

  // Observed on 2.2.7: fires before the compaction request with
  // {sessionID, agent, model, system, messages, tools, options}. A text part
  // pushed onto arg.system reached the compaction request (+6016 bytes for a
  // 6000-character part, measured at the stub provider).
  ctx.session.hook("compaction", async (arg) => {
    if (typeof session.sessionCompaction === "function") {
      await session.sessionCompaction(ctx, arg);
    }
  });

  // Observed on 2.2.7: subscribe({types}) returns an async iterable of
  // {id, created, type, location, data}. The types session.js reads, each
  // seen on a probe run, are listed in session.js's SUBSCRIBED_EVENT_TYPES.
  if (typeof session.event === "function") {
    const types = Array.isArray(session.SUBSCRIBED_EVENT_TYPES)
      ? session.SUBSCRIBED_EVENT_TYPES
      : undefined;
    consumeEvents(ctx, ctx.event.subscribe({ types }), session.event);
  }

  writeHandshakeSentinel();
}

export default { id: "nightgauge", setup };
