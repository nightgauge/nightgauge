/**
 * concurrentPipelineManager.haltPolicy.test.ts
 *
 * The queue-halt policy is a KIND-SET decision, not a text ladder (#306).
 *
 * `haltQueueOnSlotFailure` used to carry four private ladders of regexes —
 * isTransientNetworkFailureText, isEnvironmentalFailure, isApiOverloaded,
 * isStallKill — each with a "Match strings mirror Go's ClassifyTerminalKind —
 * keep aligned" comment and nothing checking the claim. They were the fourth,
 * fifth, sixth and seventh copies of terminal-kind matching in the codebase, and
 * the manifest's own note admitted they could still drift.
 *
 * They now resolve the kind through the canonical table and test membership, so
 * what is left in that file is only POLICY: which kinds skip the halt. This
 * suite pins the policy the way the corpus pins classification — against real
 * failure text, with the kind coming from the table.
 *
 * `haltQueueOnSlotFailure` is a private method on a class with a large
 * constructor surface, so rather than instantiating it this suite asserts the
 * decision the policy is made of: kind ∈ set. If the source stops asking the
 * table for the kind, the parity fence over that file (#257) and the greps
 * below fail.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { classifyTerminalKind, signalTerminalKind } from "@nightgauge/sdk";
import {
  ConcurrentPipelineManager,
  resolveHaltKind,
} from "../../src/services/ConcurrentPipelineManager";
import { fakeCloneLayout } from "../helpers/cloneLayout";

vi.mock("vscode", () => ({
  EventEmitter: class {
    event = () => ({ dispose: () => {} });
    fire() {}
    dispose() {}
  },
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test-repo" } }] },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: { executeCommand: vi.fn().mockResolvedValue(undefined) },
}));
vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: async (issueNumber: number, branch: string) => ({
        path: `/test-repo/.worktrees/issue-${issueNumber}`,
        branch,
        issueNumber,
        exists: true,
      }),
      cleanup: async () => undefined,
      cleanupOrphans: async () => 0,
      listActive: async () => [],
      getRepoRoot: () => "/test-repo",
      getWorktreePath: (n: number) => `/test-repo/.worktrees/issue-${n}`,
    };
  }),
}));
vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: () => ({ maxConcurrent: 1 }),
}));
vi.mock("../../src/utils/inReviewGuard", () => ({
  moveToInReviewIfPRBacked: async () => "skipped",
}));
// No git and no forge: a real `gh` skips the halt once #3487 itself is closed.
vi.mock("child_process", async (importActual) => {
  const answer = (...args: unknown[]) => {
    const done = args[args.length - 1] as (err: null, out: object) => void;
    done(null, { stdout: "", stderr: "" });
  };
  return { ...(await importActual<object>()), exec: answer, execFile: answer };
});

const postFailureComment = vi.hoisted(() => vi.fn());
vi.mock("../../src/utils/failureComment", () => ({
  postFailureComment,
  ARCHITECTURE_APPROVAL_REQUIRED_MARKER: "ARCHITECTURE APPROVAL REQUIRED",
  BLOCKED_DEPENDENCY_MARKER: "[blocked-dependency]",
  HOST_SLEPT_MARKER: "[host-slept]",
}));

const autonomousPauseRepo = vi.hoisted(() => vi.fn());
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      gitComposeBranchName: async (n: number) => ({ name: `fix/${n}-no-op` }),
      autonomousStatus: async () => ({ status: "running" }),
      autonomousPause: async () => undefined,
      autonomousPauseRepo,
    }),
  },
}));

const CPM_PATH = path.resolve(__dirname, "../../src/services/ConcurrentPipelineManager.ts");
const source = readFileSync(CPM_PATH, "utf-8");

/** The failure texts each skip-branch exists for, as producers actually emit them. */
const SKIP_CASES: { text: string; kind: string; branch: string }[] = [
  {
    text: "API Error: The socket connection was closed unexpectedly",
    kind: "api_connection_lost",
    branch: "transient network blip",
  },
  {
    // #3238 — the Claude CLI's own lines from a mid-stage network outage.
    text: "API Error: Can't reach the API server — check your internet or DNS (ENOTFOUND)",
    kind: "api_connection_lost",
    branch: "transient network blip",
  },
  {
    text: "API Error: Connection refused — a firewall or proxy may be blocking it (ECONNREFUSED)",
    kind: "api_connection_lost",
    branch: "transient network blip",
  },
  {
    text: "[pipeline-start-failure] github-network-outage: api.github.com unreachable",
    kind: "github_network_outage",
    branch: "transient network blip",
  },
  {
    text: "API Error: Stream idle timeout - partial response received",
    kind: "stream_idle_timeout",
    branch: "environmental",
  },
  {
    text: "Stage [rate-limit-quota-exhausted] idle 2m 14s after rate_limit_event",
    kind: "rate_limit_quota_exhausted",
    branch: "environmental",
  },
  {
    text: "network unavailable: extended github connectivity loss",
    kind: "network_unavailable",
    branch: "environmental",
  },
  {
    // #1169 — the pipeline-start auth gate's own wording, verbatim from
    // HeadlessOrchestrator. Go routes this kind as retryable infra with an
    // explicit "no pause"; this layer must not override that.
    text:
      "[adapter-auth-failed] Auth pre-flight failed — adapter not authenticated. " +
      "Pipeline halted before AI stages (zero tokens spent).",
    kind: "adapter_auth_failed",
    branch: "environmental",
  },
  { text: "API Error: Overloaded", kind: "api_overloaded", branch: "529 overload" },
  {
    // #1391 — GitHub's own secondary-limit wording, verbatim from `gh`. Go
    // routes this kind with the transient-infra branch and says "no pause" in
    // as many words; this layer must not override that. Before the kind
    // existed the text classified as subagent_crash and therefore HALTED the
    // queue on a throttle that clears in minutes.
    text:
      "exit 1: gh: You have exceeded a secondary rate limit for the GitHub API\n" +
      "Please wait a few minutes before you try again",
    kind: "github_rate_limited",
    branch: "environmental",
  },
  {
    text: "[stall-killed] stage exceeded stall idle threshold",
    kind: "stall_kill",
    branch: "transient stall",
  },
  {
    text: "[runaway-ceiling-exceeded] runaway cost ceiling exceeded",
    kind: "stall_kill",
    branch: "transient stall",
  },
  {
    text: "ARCHITECTURE APPROVAL REQUIRED: a human must approve this decision",
    kind: "architecture_approval_required",
    branch: "approval pause",
  },
  // WIDENED vs the pre-#306 regexes, deliberately. main's isStallKill matched
  // `[stall-killed]`/`stall idle threshold`/`exceeded stage_hard_cap` and none
  // of these, so a zombie-run guard halted the queue; the table routes them
  // to stall_kill, which is the recovery they already got from the Go side.
  // (A second zombie-run fixture sat here until #470 retired its table clause —
  // #427 had already deleted that marker's only producer.)
  {
    text: "[stage-no-output-timeout] feature-dev produced no session output",
    kind: "stall_kill",
    branch: "transient stall (widened: #252 first-output watchdog)",
  },
  {
    text: "exit 1: stage stopped after the hard cap",
    kind: "stall_kill",
    branch: "transient stall (widened: bare `hard cap`, main required `exceeded stage_hard_cap`)",
  },
  // #1753 — the three parked kinds (#1631). Go holds the issue for an
  // operator and does not pause the fleet; the extension must not halt the
  // rest of the repository's queue over one parked issue.
  {
    text:
      "AI_APICallError: This model's maximum context length is 32768 tokens. " +
      "However, your messages resulted in 40000 tokens.",
    kind: "context_window_exceeded",
    branch: "parked",
  },
  {
    text: "exit 1: [adapter-permission-rejected] tool=read",
    kind: "adapter_permission_rejected",
    branch: "parked",
  },
  {
    text:
      'dispatch refused for adapter "opencode": adapter_incompatible: opencode 1.2.0 is ' +
      "below the floor 1.18.0. Install opencode at or above 1.18.0.",
    kind: "adapter_incompatible",
    branch: "parked",
  },
  // #101 — a project-declared environment check failed in Phase 0.
  {
    text: "exit 1: [environment-halt] local-stack: exit code 7, want 0 — remedy: Run ./scripts/dev-up.sh",
    kind: "environment_halt",
    branch: "parked",
  },
  // #2637 — feature-dev's gate found a handoff older than the stage it judged.
  {
    text: "exit 1: [dev-context-stale] dev-2637.json predates the stage",
    kind: "dev_context_stale",
    branch: "parked",
  },
];

/** Failures that MUST reach the halt — the bugs the halt exists to surface. */
const HALT_CASES: { text: string; kind: string }[] = [
  { text: "exit 1: schema validation failed for plan.json", kind: "validation_error" },
  { text: "exit 137: killed by signal SIGKILL", kind: "subagent_crash" },
  {
    text: "[validation-failed] feature-validate wrote validation_status=failed",
    kind: "validation_failed",
  },
  { text: "[commit-orphaned] HEAD is not on the expected feature branch", kind: "commit_orphaned" },
  // NARROWED vs the pre-#306 regexes, deliberately. main's isStallKill fired on
  // any text containing stall wording, so a cost-cap kill that also mentioned
  // the stall skipped the halt with a toast. The table gives cost-cap-exceeded
  // precedence, budget_exceeded is in neither skip set, and a run that
  // deliberately spent its cap SHOULD stop the queue rather than be retried
  // into spending it again. No producer composes this shape today
  // (SkillRunner composes cost-cap text without stall wording), which is why it
  // is listed here rather than fixed.
  {
    text: "[cost-cap-exceeded] stage feature-dev [stall-killed] terminated",
    kind: "budget_exceeded",
  },
];

describe("queue-halt policy (ConcurrentPipelineManager)", () => {
  it("routes every skip-branch's real failure text to the kind that branch tests for", () => {
    for (const c of SKIP_CASES) {
      expect(classifyTerminalKind(c.text), `${c.branch}: ${c.text}`).toBe(c.kind);
    }
  });

  it("leaves real defects classified as kinds no skip-branch names", () => {
    const skipKinds = new Set(SKIP_CASES.map((c) => c.kind));
    for (const c of HALT_CASES) {
      expect(classifyTerminalKind(c.text), c.text).toBe(c.kind);
      expect(skipKinds.has(c.kind), `${c.kind} must reach the halt`).toBe(false);
    }
  });

  it("declares each skip-branch's kinds as a set, not as a matcher ladder", () => {
    // The point of #306 in this file: no branch may go back to deriving a
    // terminal kind from raw text. `classifyTerminalKind` is the only sanctioned
    // route, and these two sets are the only local vocabulary.
    for (const kind of [
      "stream_idle_timeout",
      "rate_limit_quota_exhausted",
      "network_unavailable",
      "adapter_auth_failed",
      "github_rate_limited",
    ]) {
      expect(source).toContain(`"${kind}"`);
    }
    expect(source).toContain("HALT_SKIP_ENVIRONMENTAL");
    expect(source).toContain("HALT_SKIP_TRANSIENT_STALL");
    expect(source).toContain("const haltKind = resolveHaltKind(haltErrMsg, hostSleptVerdict);");
    expect(source).not.toContain("classifyTerminalKind(haltErrMsg)");
  });

  it("exempts the parked kinds from the halt, as not_pipeline_actionable is (#1753, #101)", () => {
    // A park holds ONE issue for an operator; Go schedules no retry and does
    // not pause the fleet (docs/FAILURE_TAXONOMY.md, "The parked kinds."). The
    // extension used to halt the whole repository queue on it.
    const parked = setLiteral(source, "HALT_SKIP_PARKED");
    expect(parked).toEqual(
      [
        "adapter_incompatible",
        "adapter_permission_rejected",
        "adapter_provider_no_route",
        "context_window_exceeded",
        "dev_context_stale",
        "environment_halt",
        "gate_plugin_missing",
        "review_cap_reached",
        "needs_decomposition",
        "resume_no_op",
      ].sort()
    );
    for (const c of SKIP_CASES.filter((c) => c.branch === "parked")) {
      expect(parked, c.text).toContain(classifyTerminalKind(c.text));
    }
    const body = methodBody(source, "private async haltQueueOnSlotFailure(");
    expect(body).toMatch(/HALT_SKIP_PARKED\.has\(haltKind\)/);
  });

  it("does not halt on a plan that lists no files (#3354)", () => {
    // The feature-planning gate fails an empty plan with this reason; the
    // scheduler wraps gate reasons into the slot's failure text.
    const text =
      "Stage feature-planning gate failed: [not-pipeline-actionable] plan lists no files to create or modify";
    expect(classifyTerminalKind(text)).toBe("not_pipeline_actionable");
    expect(resolveHaltKind(text, false)).toBe("not_pipeline_actionable");
    const body = methodBody(source, "private async haltQueueOnSlotFailure(");
    expect(body).toContain('if (haltKind === "not_pipeline_actionable") {');
  });

  it("keeps exactly one deliberate raw-text condition, in any form, and says why", () => {
    // A bare Anthropic "session/usage limit" with no model named is a shape the
    // RECORD does not classify — Go returns "" for it — so the halt branch
    // cannot be expressed as a kind. It is an addition to the table's answer,
    // not a duplicate of it.
    //
    // The previous version of this guard grepped for the regex FORM
    // (`/…/i.test(haltErrMsg)`) and therefore passed for
    // `haltErrMsg.toLowerCase().includes("[baseline-ci]")` — executed, green.
    // So it now scans the whole method body for BOTH: every regex literal, and
    // every method called on the raw text.
    const body = methodBody(source, "private async haltQueueOnSlotFailure(");

    // String and template literals are blanked first: a log message containing
    // a slash otherwise reads as a regex and makes this assertion noise.
    const scannable = body.replace(/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`/g, '""');
    // Branches are mutually exclusive (escape starts with \, class starts with
    // [, the rest excludes /, \, [ and newline) so the scan is linear — the
    // overlapping form flagged as js/redos backtracked exponentially on
    // bracket-heavy source.
    const regexes =
      scannable.match(/\/(?![/*])(?:\\.|\[(?:\\.|[^\]\\\n])*\]|[^/\\[\n])+\/[gimsuy]*/g) ?? [];
    expect(
      regexes,
      "a new regex appeared in haltQueueOnSlotFailure — resolve the kind through the canonical " +
        "table instead, or document the addition here"
    ).toEqual(["/\\b(?:session|usage)\\s+limit\\b/i"]);

    const textOps = [...body.matchAll(/haltErrMsg\s*\.\s*(\w+)/g)].map((m) => m[1]);
    expect(
      [...new Set(textOps)].sort(),
      "haltQueueOnSlotFailure called a string method on the raw failure text other than slice(). " +
        "Any of includes/match/test/startsWith/toLowerCase is a matcher the table cannot see."
    ).toEqual(["slice"]);

    // And the two halves of the story stay tied: the RECORD says nothing for
    // this wording, while the REACTION does — via the declared signal
    // extension, which is why the halt policy is not the only thing keeping it
    // alive any more.
    expect(classifyTerminalKind("You've hit your usage limit · resets 10am")).toBeUndefined();
    expect(signalTerminalKind("You've hit your usage limit · resets 10am")).toBe(
      "rate_limit_quota_exhausted"
    );
  });
});

/** The sorted string members of a `const NAME ... = new Set([...])` literal, comments stripped. */
function setLiteral(src: string, name: string): string[] {
  const code = src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/[^\n]*/g, "");
  const at = code.indexOf(`const ${name}`);
  expect(at, `${name} is gone from ConcurrentPipelineManager`).toBeGreaterThan(0);
  const open = code.indexOf("new Set([", at);
  const close = code.indexOf("]", open);
  expect(close, `${name} literal did not parse`).toBeGreaterThan(open);
  return [...code.slice(open, close).matchAll(/"([a-z0-9_]+)"/g)].map((m) => m[1]).sort();
}

/** The brace-balanced body of a method, comments stripped. */
function methodBody(src: string, decl: string): string {
  const start = src.indexOf(decl);
  expect(start, `${decl} is gone — this guard now checks nothing`).toBeGreaterThan(0);
  let depth = 0;
  for (let i = src.indexOf("{", start); i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) {
      return src
        .slice(start, i + 1)
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .replace(/\/\/[^\n]*/g, "");
    }
  }
  throw new Error(`${decl} body is unbalanced`);
}

describe("host_slept keys on the structured verdict, not echoed text (#3273)", () => {
  const defect = "[host-slept] echoed by a stage, but: TypeError: x is not a function";

  it("does not classify echoed [host-slept] text as host_slept without the verdict", () => {
    const kind = resolveHaltKind(defect, false);
    expect(kind).not.toBe("host_slept");
    expect(resolveHaltKind("[host-slept]", false)).toBeUndefined();
  });

  it("returns host_slept when the scheduler booked the verdict", () => {
    expect(resolveHaltKind("anything", true)).toBe("host_slept");
  });

  it("reads the verdict for the failed stage from slot state", () => {
    expect(source).toContain("stage_host_slept?.[");
    expect(source).toContain('if (haltKind === "host_slept")');
  });
});

describe("a partial-delivery no-op is read from its booking, never from text (#3487)", () => {
  const repo = "nightgauge/nightgauge";
  const booking = "[not-pipeline-actionable] wrote no changes; unmet on the merged diff: handles ";

  beforeEach(() => {
    vi.clearAllMocks();
    fakeCloneLayout("/test-repo");
  });

  /**
   * Settle one real slot on `result`, through its whole finally, at concurrency
   * 1 with one more item pending behind it.
   */
  async function settle(result: Record<string, unknown>) {
    const item = (issueNumber: number) => [
      { issueNumber, title: "queued", position: 1, status: "pending", repoName: repo },
    ];
    const clear = vi.fn().mockResolvedValue(undefined);
    const drain = vi.fn().mockResolvedValue([]);
    const started: number[] = [];
    const queue = {
      dequeueIndependent: vi
        .fn()
        .mockResolvedValueOnce(item(3487))
        .mockResolvedValueOnce(item(3488))
        .mockResolvedValue([]),
      updateActiveSlots: async () => undefined,
      drainBlockedSuccessors: drain,
      enqueue: async () => null,
      clear,
      getQueue: async () => ({ items: [], status: "idle" }),
    };
    let finish: (value: unknown) => void = () => {};
    const run = new Promise((resolve) => (finish = resolve));
    const getState = vi.fn().mockResolvedValue(null);
    const factory = () => ({
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setRepoOverride: vi.fn(),
        setUnattended: vi.fn(),
        resolveRunRepoSlug: async () => repo,
        runPipeline: (issueNumber: number) => {
          started.push(issueNumber);
          // The pending item, once started, just runs.
          return issueNumber === 3487 ? run : new Promise(() => {});
        },
        stop: vi.fn(),
        dispose: vi.fn(),
      },
      stateService: {
        onStateChanged: () => ({ dispose: vi.fn() }),
        onPhaseStart: () => ({ dispose: vi.fn() }),
        onPhaseComplete: () => ({ dispose: vi.fn() }),
        onUnifiedTokenUsage: () => ({ dispose: vi.fn() }),
        getState,
        beginRun: vi.fn(),
        endRun: vi.fn(),
        getRunId: () => null,
        initEmpty: vi.fn(),
        setMeta: vi.fn(),
        dispose: vi.fn(),
      },
    });
    const logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      queue as never,
      factory as never,
      logger as never,
      { maxConcurrent: 1 }
    );
    const onSlotFailed = vi.fn();
    manager.setCallbacks({ onSlotFailed });

    await manager.fillSlots();
    finish({
      success: false,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      failedStage: "feature-dev",
      totalDurationMs: 20_000,
      ...result,
    });
    await manager.settleForTest(3487);
    // Long enough for a refill to start: the negative cases assert there is none.
    await new Promise((resolve) => setTimeout(resolve, 100));
    return { clear, drain, started, kind: onSlotFailed.mock.calls[0][5] };
  }

  it("fills the freed slot at concurrency 1, and leaves the successors queued", async () => {
    const { started, drain, clear } = await settle({
      partialNoOp: {},
      terminalKind: "not_pipeline_actionable",
      error: new Error(`${booking}x`),
    });
    await vi.waitFor(() => expect(started).toEqual([3487, 3488]));
    expect(drain).not.toHaveBeenCalled();
    expect(clear).not.toHaveBeenCalled();
  });

  it.each([
    ["a real failure", { error: new Error("stage exited 1") }],
    [
      "a publication verdict",
      {
        terminalKind: "not_pipeline_actionable",
        error: new Error("[not-pipeline-actionable] publication-boundary verdict"),
      },
    ],
  ])("treats %s as before: drains, and does not refill", async (_name, result) => {
    const { started, drain } = await settle(result);
    expect(drain).toHaveBeenCalledTimes(1);
    expect(started).toEqual([3487]);
  });

  it("gives onSlotFailed the structured kind of a booking", async () => {
    const { kind } = await settle({
      partialNoOp: {},
      terminalKind: "not_pipeline_actionable",
      error: new Error(`${booking}x`),
    });
    expect(kind).toBe("not_pipeline_actionable");
  });

  it("gives onSlotFailed no kind for a publication verdict", async () => {
    const { kind } = await settle({
      terminalKind: "not_pipeline_actionable",
      error: new Error("[not-pipeline-actionable] publication-boundary verdict"),
    });
    expect(kind).toBeUndefined();
  });

  // The table's own markers: `[merge-conflict]` is merge_conflict's only one.
  it.each(["merge_conflict [merge-conflict]", "pr_merge_unmerged"])(
    "does not halt on a booking that quotes %s; the same text unflagged still halts",
    async (quoted) => {
      const error = new Error(`${booking}${quoted}`);
      const booked = await settle({ partialNoOp: {}, error });
      expect(booked.clear).not.toHaveBeenCalled();
      expect(autonomousPauseRepo).not.toHaveBeenCalled();

      await settle({ error });
      expect(autonomousPauseRepo).toHaveBeenCalledTimes(1);
    }
  );
});
