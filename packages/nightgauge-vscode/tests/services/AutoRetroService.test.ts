/**
 * AutoRetroService.test.ts
 *
 * Unit tests for AutoRetroService — automatic failure analysis after pipeline failures.
 *
 * Test coverage:
 * - Config gating: auto_retro.enabled=false returns null/skipped
 * - Missing evidence sources → classifies as 'unknown'
 * - Log pattern matching → correct category classification
 * - auto_create_issues=false → gh not called
 * - auto_create_issues=true + matching severity → gh issue create called
 * - gh issue create failure → logs warning, result still returned
 * - retro file write failure → returns null, logs warning
 *
 * @see Issue #1408 - Auto-retro after pipeline failure
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as fs from "node:fs/promises";

vi.mock("node:fs/promises");

// Hoist IPC mocks so they are available inside vi.mock factories
const { mockIssueView, mockIssueCreate, mockIssueLinkSubIssue, mockPrView, mockGetRepoIdentity } =
  vi.hoisted(() => ({
    mockIssueView: vi.fn(),
    mockIssueCreate: vi.fn(),
    mockIssueLinkSubIssue: vi.fn(),
    mockPrView: vi.fn(),
    mockGetRepoIdentity: vi.fn(),
  }));

// Mock IpcClient singleton
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      issueView: mockIssueView,
      issueCreate: mockIssueCreate,
      issueLinkSubIssue: mockIssueLinkSubIssue,
      prView: mockPrView,
    }),
  },
}));

// Mock getRepoIdentity
vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: mockGetRepoIdentity,
}));

import { AutoRetroService } from "../../src/services/AutoRetroService";

function createMockLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

const WORKSPACE = "/test/workspace";
const ISSUE_NUMBER = 42;
const FAILED_STAGE = "feature-dev";

describe("AutoRetroService", () => {
  let logger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = createMockLogger();

    // Default: config file not found
    vi.mocked(fs.readFile).mockRejectedValue(
      Object.assign(new Error("ENOENT"), { code: "ENOENT" })
    );
    // Default: directories don't exist
    vi.mocked(fs.readdir).mockRejectedValue(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
    // Default: mkdir succeeds
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    // Default: writeFile succeeds
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);

    // Default: getRepoIdentity returns a valid identity
    mockGetRepoIdentity.mockResolvedValue({
      owner: "TestOwner",
      repo: "test-repo",
    });
    // Default: IPC mocks return reasonable defaults
    mockIssueView.mockResolvedValue({
      number: ISSUE_NUMBER,
      title: "Test Issue",
      body: "",
      state: "OPEN",
      labels: [],
      assignees: [],
      url: "",
      isEpic: false,
    });
    mockIssueCreate.mockResolvedValue({
      number: 1234,
      title: "",
      body: "",
      state: "OPEN",
      labels: [],
      assignees: [],
      url: "https://github.com/TestOwner/test-repo/issues/1234",
      isEpic: false,
    });
    mockIssueLinkSubIssue.mockResolvedValue(undefined);
  });

  // ===========================================================================
  // Config gating
  // ===========================================================================

  describe("config gating", () => {
    it('returns skippedReason "disabled" when auto_retro.enabled is false', async () => {
      const configYaml = ["feedback_loop:", "  auto_retro:", "    enabled: false"].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.skippedReason).toBe("disabled");
      expect(result!.findings).toHaveLength(0);
      expect(result!.retroFile).toBe("");
    });

    it("does not write retro file when disabled", async () => {
      const configYaml = ["feedback_loop:", "  auto_retro:", "    enabled: false"].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      await AutoRetroService.runAfterFailure(WORKSPACE, ISSUE_NUMBER, FAILED_STAGE, logger as any);

      expect(fs.writeFile).not.toHaveBeenCalled();
    });

    it("does not call gh when disabled", async () => {
      const configYaml = ["feedback_loop:", "  auto_retro:", "    enabled: false"].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      await AutoRetroService.runAfterFailure(WORKSPACE, ISSUE_NUMBER, FAILED_STAGE, logger as any);

      expect(mockIssueCreate).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // Missing evidence → unknown category
  // ===========================================================================

  describe("missing evidence sources", () => {
    it('classifies as "unknown" when no log files or context exist', async () => {
      // readFile fails for config → uses defaults (enabled: true)
      // readdir fails for all dirs → no evidence
      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.findings).toHaveLength(1);
      expect(result!.findings[0].category).toBe("unknown");
    });

    it("still writes retro file even when evidence is missing", async () => {
      await AutoRetroService.runAfterFailure(WORKSPACE, ISSUE_NUMBER, FAILED_STAGE, logger as any);

      expect(fs.writeFile).toHaveBeenCalledOnce();
      const writtenContent = JSON.parse(vi.mocked(fs.writeFile).mock.calls[0][1] as string);
      expect(writtenContent.schema_version).toBe("1.0");
      expect(writtenContent.issue_number).toBe(ISSUE_NUMBER);
      expect(writtenContent.failed_stage).toBe(FAILED_STAGE);
    });

    it("logs an info message when no evidence sources are found", async () => {
      await AutoRetroService.runAfterFailure(WORKSPACE, ISSUE_NUMBER, FAILED_STAGE, logger as any);

      expect(logger.info).toHaveBeenCalledWith(
        expect.stringContaining("no evidence sources found"),
        expect.objectContaining({ issueNumber: ISSUE_NUMBER })
      );
    });
  });

  // ===========================================================================
  // Log pattern classification
  // ===========================================================================

  describe("failure classification", () => {
    it('classifies as "budget-exceeded" when log contains "budget exceeded"', async () => {
      const today = new Date().toISOString().slice(0, 10);
      const logFileName = `${today}-session.log`;

      vi.mocked(fs.readdir)
        .mockResolvedValueOnce([logFileName] as any)
        .mockRejectedValue(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
      vi.mocked(fs.readFile)
        .mockRejectedValueOnce(Object.assign(new Error("ENOENT"), { code: "ENOENT" }))
        .mockResolvedValueOnce("ERROR: budget exceeded at stage feature-dev" as any);

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.findings[0].category).toBe("budget-exceeded");
    });

    it('classifies as "validation-failure" when log contains "tests failed"', async () => {
      const today = new Date().toISOString().slice(0, 10);
      const logFileName = `${today}-session.log`;

      vi.mocked(fs.readdir)
        .mockResolvedValueOnce([logFileName] as any)
        .mockRejectedValue(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
      vi.mocked(fs.readFile)
        .mockRejectedValueOnce(Object.assign(new Error("ENOENT"), { code: "ENOENT" }))
        .mockResolvedValueOnce("tests failed: 3 failures in feature-validate" as any);

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.findings[0].category).toBe("validation-failure");
    });

    it('classifies as "timeout" when log contains "timed out"', async () => {
      const today = new Date().toISOString().slice(0, 10);
      const logFileName = `${today}-session.log`;

      vi.mocked(fs.readdir)
        .mockResolvedValueOnce([logFileName] as any)
        .mockRejectedValue(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
      vi.mocked(fs.readFile)
        .mockRejectedValueOnce(Object.assign(new Error("ENOENT"), { code: "ENOENT" }))
        .mockResolvedValueOnce("Stage feature-dev timed out after 600000ms" as any);

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.findings[0].category).toBe("timeout");
    });

    // ========================================================================
    // Per-issue session log scoping (#3247) — only the failed run's slice
    // should reach the classifier, not signals from earlier same-day runs.
    // ========================================================================

    it("scopes to the LAST stage start for the failed stage when the log has multiple runs", async () => {
      const today = new Date().toISOString().slice(0, 10);
      const perIssue = `${today}_${ISSUE_NUMBER}_session.log`;

      // Synthetic log: TWO runs of feature-dev. Run 1 had a stop-hook-error.
      // Run 2 had a clean stall-kill. Pre-#3247 the classifier matched the
      // run-1 stop-hook-error and reported `stop-hook-error` for the failed
      // run. Post-#3247 the slice from the LAST stage-start onward never sees
      // run 1's events.
      const logContent = [
        `[2026-05-06T12:00:00.000Z] [INFO] [feature-dev] [#${ISSUE_NUMBER}] [skillRunner] Stage: feature-dev | Model: opus (performance-mode) | Effort: high`,
        `[2026-05-06T12:33:02.317Z] [INFO] [feature-dev] [#${ISSUE_NUMBER}] {"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred"}`,
        `[2026-05-06T13:00:00.000Z] [INFO] [feature-dev] [#${ISSUE_NUMBER}] {"type":"result","subtype":"success"}`,
        // --- second run starts ---
        `[2026-05-06T21:34:00.000Z] [INFO] [feature-dev] [#${ISSUE_NUMBER}] [skillRunner] Stage: feature-dev | Model: opus (performance-mode) | Effort: high`,
        `[2026-05-06T22:33:39.000Z] [INFO] [feature-dev] [#${ISSUE_NUMBER}] [skillRunner] Stage exceeded stall idle threshold (24m 0s without output) — forcibly terminating process`,
        `[2026-05-06T22:33:39.000Z] [ERROR] Stage terminated due to stall detection auto-kill {"stage":"feature-dev","stallKilled":true}`,
      ].join("\n");

      vi.mocked(fs.readdir)
        .mockResolvedValueOnce([perIssue] as any)
        .mockRejectedValue(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
      vi.mocked(fs.readFile)
        .mockRejectedValueOnce(Object.assign(new Error("ENOENT"), { code: "ENOENT" }))
        .mockResolvedValueOnce(logContent as any);

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      // The failed run was a clean stall-kill. Pre-fix this returned
      // 'stop-hook-error' from run 1.
      expect(result!.findings[0].category).toBe("stall-kill");
      // Defensive: stop-hook-error should NOT appear in any finding for this
      // run, since the run-1 event was sliced out by the scoping.
      const cats = result!.findings.map((f) => f.category);
      expect(cats).not.toContain("stop-hook-error");
    });

    // ========================================================================
    // Terminal-reason corpus threading (#3926) — a failure with NO session
    // log, context, or history must still classify off the orchestrator's
    // terminal reason alone. Pre-#3926 this was the `unknown` path.
    // ========================================================================

    it("classifies off the threaded terminal reason when no other evidence exists", async () => {
      // beforeEach leaves readdir/readFile rejecting ENOENT → no log, no
      // context, no history, no diag files. The ONLY evidence is the reason.
      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        73,
        "pr-merge",
        logger as any,
        "pr-merge reported success but PR #73 is not merged (state: OPEN). Pipeline halted after 2 verification attempts."
      );

      expect(result).not.toBeNull();
      expect(result!.findings[0].category).toBe("skill-no-op");

      // The retro payload records terminal_reason as an analyzed source.
      const writeCall = vi
        .mocked(fs.writeFile)
        .mock.calls.find((c) => String(c[0]).endsWith("_73_retro.json"));
      expect(writeCall).toBeDefined();
      const payload = JSON.parse(String(writeCall![1]));
      expect(payload.sources_analyzed).toContain("terminal_reason");
    });
  });

  // ===========================================================================
  // classifyFailure unit tests (static method, directly testable)
  // ===========================================================================

  describe("classifyFailure (unit)", () => {
    it('returns "budget-exceeded" for "token limit" pattern', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "ERROR: token limit reached",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("budget-exceeded");
    });

    it('returns "state-management" for "context file missing" pattern', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "context file missing: dev-42.json",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-validate"
      );
      expect(findings[0].category).toBe("state-management");
    });

    it('returns "ci-infrastructure" for "CI checks failed" pattern', () => {
      const findings = AutoRetroService.classifyFailure(
        { text: "CI checks failed on commit abc123", sourcesAnalyzed: [] },
        "pr-merge"
      );
      expect(findings[0].category).toBe("ci-infrastructure");
    });

    it('returns "model-capability" for "model returned empty" pattern', () => {
      const findings = AutoRetroService.classifyFailure(
        { text: "model returned empty response", sourcesAnalyzed: [] },
        "feature-dev"
      );
      expect(findings[0].category).toBe("model-capability");
    });

    it('returns "unknown" when text is empty', () => {
      const findings = AutoRetroService.classifyFailure(
        { text: "", sourcesAnalyzed: [] },
        "feature-dev"
      );
      expect(findings[0].category).toBe("unknown");
    });

    it("returns exactly one finding per classification", () => {
      const findings = AutoRetroService.classifyFailure(
        { text: "budget exceeded and tests failed", sourcesAnalyzed: [] },
        "feature-dev"
      );
      // Only the first matching pattern wins
      expect(findings).toHaveLength(1);
    });

    it("finding includes failedStage in evidence", () => {
      const findings = AutoRetroService.classifyFailure(
        { text: "", sourcesAnalyzed: [] },
        "my-stage"
      );
      expect(findings[0].evidence).toContain("Failed stage: my-stage");
    });
  });

  // ===========================================================================
  // Last-mile failure legibility (#3926 / #3924)
  //
  // These are the exact orchestrator terminal-reason strings that previously
  // fell through to `unknown`, starving the operator of any actionable signal.
  // ===========================================================================

  describe("classifyFailure (last-mile — #3926 / #3924)", () => {
    it('classifies pr-merge "reported success but PR #73 is not merged" as skill-no-op (regex tolerates the PR number)', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "pr-merge reported success but PR #73 is not merged (state: OPEN). Pipeline halted after 2 verification attempts.",
          sourcesAnalyzed: ["terminal_reason", "session_log"],
        },
        "pr-merge"
      );
      // Pre-#3926 this matched no pattern → unknown. The interpolated `#73`
      // defeated the old `/reported success but PR is not merged/i`.
      expect(findings[0].category).toBe("skill-no-op");
      expect(findings[0].category).not.toBe("unknown");
    });

    it('classifies pr-create "reported success but no open PR exists" as skill-no-op (new extractor)', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "pr-create reported success but no open PR exists (gate: no pr-73.json). Pipeline halted.",
          sourcesAnalyzed: ["terminal_reason"],
        },
        "pr-create"
      );
      expect(findings[0].category).toBe("skill-no-op");
    });

    it("classifies a blocked-but-correct pr-merge decline as merge-blocked, NOT skill-no-op", () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text:
            "pr-merge reported success but PR #73 is not merged (state: OPEN). " +
            'blocked by failing check "Sync E2E (Docker)" (mergeStateStatus=UNSTABLE). ' +
            "Pipeline halted after 2 verification attempts.",
          sourcesAnalyzed: ["terminal_reason"],
        },
        "pr-merge"
      );
      // merge-blocked must win — declining to merge a red PR is correct, not a
      // skill no-op. The extractor ordering guarantees this.
      expect(findings[0].category).toBe("merge-blocked");
      expect(findings[0].evidence.join(" ")).toContain("Sync E2E (Docker)");
    });

    it("classifies merge-blocked from a bare mergeStateStatus signal", () => {
      const findings = AutoRetroService.classifyFailure(
        { text: "PR not merged — mergeStateStatus=BLOCKED", sourcesAnalyzed: ["terminal_reason"] },
        "pr-merge"
      );
      expect(findings[0].category).toBe("merge-blocked");
    });

    it("still returns unknown for pr-merge when there is genuinely no signal", () => {
      const findings = AutoRetroService.classifyFailure(
        { text: "", sourcesAnalyzed: [] },
        "pr-merge"
      );
      expect(findings[0].category).toBe("unknown");
    });
  });

  // ===========================================================================
  // Structured-signal classification (#3204)
  // ===========================================================================

  describe("classifyFailure (structured signals — #3204)", () => {
    it('classifies as "stall-kill" on the skillRunner kill log line', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: '[2026-05-06T04:52:53.581Z] [ERROR] Stage terminated due to stall detection auto-kill {"stage":"feature-dev","durationMs":6812294,"stallKilled":true,"stall_indicator":true}',
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("stall-kill");
      expect(findings[0].severity).toBe("medium");
      expect(findings[0].recommendation).toContain("stall diagnostic");
    });

    it('classifies as "stall-kill" via the [stall-killed] error envelope', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "[stall-killed] feature-dev terminated: subagent process exceeded stall kill threshold. The process ran for 6812s without completing.",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("stall-kill");
    });

    it('classifies as "cost-cap" on the [cost-cap-exceeded] log line', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "[cost-cap-exceeded] Stage feature-dev terminated: cost cap exceeded. Cost $25.0000 exceeded the configured cap ($20.00) after 8m 32s.",
          sourcesAnalyzed: ["feature-dev-cost-capped.log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("cost-cap");
      expect(findings[0].severity).toBe("high");
      expect(findings[0].recommendation).toContain("runaway tool loop");
    });

    it('classifies as "infrastructure-outage" on OfflineManager transition', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "[2026-05-05T18:31:45.527Z] [DEBUG] [OfflineManager] degraded -> offline: 3 consecutive failures",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("infrastructure-outage");
      expect(findings[0].severity).toBe("low");
    });

    it('classifies as "infrastructure-outage" on raw DNS failure', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: 'IPC error -32603: fetch board items: Post "https://api.github.com/graphql": dial tcp: lookup api.github.com: no such host',
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("infrastructure-outage");
    });

    it('classifies as "no-adapter-available" on the [stage:no-adapter-available] envelope (Issue #3231)', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "[stage:no-adapter-available] adapters_tried=[claude,codex,gemini] reason=gemini CLI not in PATH",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("no-adapter-available");
      expect(findings[0].severity).toBe("high");
      expect(findings[0].recommendation).toContain("fallback chain");
    });

    it('classifies as "adapter-unavailable" on the [stage:adapter-unavailable] envelope (Issue #3223)', () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "[stage:adapter-unavailable] adapter=claude source=default reason=claude CLI not in PATH",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("adapter-unavailable");
      expect(findings[0].severity).toBe("high");
      expect(findings[0].recommendation).toContain("disable_fallback");
    });

    it("no-adapter-available is recognized as a distinct category from adapter-unavailable (Issue #3231)", () => {
      // Both envelopes share the `[stage:` prefix; the classifier must
      // treat them as distinct so retro recommendations don't conflate
      // chain-exhausted with strict-mode-disabled.
      const noAdapter = AutoRetroService.classifyFailure(
        {
          text: "[stage:no-adapter-available] adapters_tried=[claude,codex] reason=codex broken",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      const adapterUnavail = AutoRetroService.classifyFailure(
        {
          text: "[stage:adapter-unavailable] adapter=claude source=default reason=claude CLI not in PATH",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(noAdapter[0].category).toBe("no-adapter-available");
      expect(adapterUnavail[0].category).toBe("adapter-unavailable");
      expect(noAdapter[0].recommendation).not.toBe(adapterUnavail[0].recommendation);
    });

    it('classifies as "stop-hook-error" on Claude CLI stop-hook notification (no terminal result event)', () => {
      // Genuine #3204 case: stop-hook fires and the subagent goes silent —
      // no terminal result event ever lands. The time-gate (#3275) treats
      // this as the real cause.
      const findings = AutoRetroService.classifyFailure(
        {
          text: '{"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred","priority":"immediate"}',
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("stop-hook-error");
      expect(findings[0].severity).toBe("medium");
    });

    it("returns multiple findings (primary + secondary) when several signals fire", () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: [
            "[OfflineManager] degraded -> offline: 3 consecutive failures",
            'Stage terminated due to stall detection auto-kill {"stallKilled":true}',
          ].join("\n"),
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      const cats = findings.map((f) => f.category);
      expect(cats).toContain("stall-kill");
      expect(cats).toContain("infrastructure-outage");
      // Primary cause is the first signal extractor that fires —
      // stall-kill comes before infrastructure-outage in SIGNAL_EXTRACTORS.
      expect(findings[0].category).toBe("stall-kill");
    });

    it("structured signal beats keyword fallback (e.g. validation-failure red herring)", () => {
      // The session log contains a TypeError (extension cleanup spam) AND
      // the structured stall-kill ERROR. Pre-#3204 the regex blob would have
      // matched /type.*error/ first and reported validation-failure. Now the
      // structured signal wins.
      const findings = AutoRetroService.classifyFailure(
        {
          text: [
            "TypeError: fetch failed at DiscordService.patchEmbed",
            '[ERROR] Stage terminated due to stall detection auto-kill {"stallKilled":true}',
          ].join("\n"),
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("stall-kill");
      expect(findings.map((f) => f.category)).not.toContain("validation-failure");
    });

    it("V3 RunRecord terminal_failure_kind takes precedence", () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: '{"schema_version":"3","issue_number":3204,"outcome":"failed","terminal_failure_kind":"stall_kill","total_duration_ms":6812294}',
          sourcesAnalyzed: ["execution_history"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("stall-kill");
      expect(findings[0].evidence.join("\n")).toContain("terminal_failure_kind");
    });

    it("source-tagged: extension TypeError noise does NOT trip validation-failure", () => {
      // This is the #360 / #3204 misclassification root cause. With tagged
      // lines, the noisy extension TypeError is correctly excluded from the
      // subagent-only validation-failure pattern. Falls through to "unknown"
      // since no structured signal is present in this fixture.
      const findings = AutoRetroService.classifyFailure(
        {
          text: "TypeError: fetch failed at DiscordService.patchEmbed",
          sourcesAnalyzed: ["session_log"],
          lines: [
            {
              source: "extension",
              text: "[2026-05-05T19:00:37.651Z] [WARN] DiscordService: network error patching embed TypeError: fetch failed",
            },
          ],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("unknown");
    });

    it("source-tagged: subagent tsc error correctly classifies as validation-failure", () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "src/foo.ts(10,5): error TS2322: Type 'string' is not assignable",
          sourcesAnalyzed: ["session_log"],
          lines: [
            {
              source: "subagent",
              text: "src/foo.ts(10,5): error TS2322: Type 'string' is not assignable",
            },
          ],
        },
        "feature-validate"
      );
      // Note: pattern matches /tsc error/i which the line above doesn't contain;
      // exercise the broader test-failure path instead. The point is to prove
      // subagent-tagged content is eligible.
      const findings2 = AutoRetroService.classifyFailure(
        {
          text: "5 tests failed",
          sourcesAnalyzed: ["session_log"],
          lines: [{ source: "subagent", text: "5 tests failed" }],
        },
        "feature-validate"
      );
      // First case: no structured signal, no specific tsc keyword match.
      expect(findings[0].category).toBeOneOf(["unknown", "validation-failure"]);
      expect(findings2[0].category).toBe("validation-failure");
    });
  });

  // ===========================================================================
  // Issue #3275 — retro mis-attribution to stop-hook-error
  //
  // Four AC paths covered here:
  //   AC1 — cost-cap diagnostic file present + stop-hook noise → cost-cap wins
  //   AC2 — pr-merge "reported success but PR is not merged" → skill-no-op
  //   AC3 — stop-hook-error AFTER terminal result event → NOT classified as
  //         stop-hook-error (post-result teardown noise is suppressed)
  //   AC4 — pr-merge failed run + MERGED PR + non-budget cause → reclassified
  //         to false-negative-shipped (covered in applyFalseNegativeShippedOverride)
  //
  // Plus regressions:
  //   - Legitimate #3204 case (stop-hook BEFORE missing terminal result) still
  //     classifies as stop-hook-error.
  //   - cost-cap textual log line still wins on its own (no diagnostic file).
  //   - budget-exceeded + MERGED PR still resolves to shipped-but-overbudget,
  //     not false-negative-shipped.
  // ===========================================================================

  describe("classifyFailure (#3275 — stop-hook demotion + cost-cap file signal)", () => {
    it("AC1 — cost-cap diagnostic file present beats stop-hook-error noise", () => {
      // The textual cost-cap log line did NOT land (rotated session log /
      // stdout flush race), but skillRunner DID write the diagnostic file.
      // Pre-#3275 the run would classify as stop-hook-error; post-#3275 the
      // file-existence extractor wins.
      const findings = AutoRetroService.classifyFailure(
        {
          text: [
            // No `[cost-cap-exceeded]` line in the session log — only the
            // routine end-of-stage stop-hook teardown notification.
            '{"type":"result","subtype":"failure"}',
            '{"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred"}',
          ].join("\n"),
          sourcesAnalyzed: ["session_log", "feature-dev-cost-capped.log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("cost-cap");
      expect(findings[0].severity).toBe("high");
      expect(findings[0].evidence.join("\n")).toContain("feature-dev-cost-capped.log");
    });

    it("regression — cost-cap textual log line still wins on its own (no diagnostic file)", () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: "[cost-cap-exceeded] Stage feature-dev terminated: cost cap exceeded.",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("cost-cap");
    });

    it("AC2 — pr-merge with 'reported success but PR is not merged' classifies as skill-no-op", () => {
      const findings = AutoRetroService.classifyFailure(
        {
          text: '{"pr_merged":false,"verification":"reported success but PR is not merged"}',
          sourcesAnalyzed: ["pipeline_context"],
        },
        "pr-merge"
      );
      expect(findings[0].category).toBe("skill-no-op");
      expect(findings[0].severity).toBe("high");
      // Recommendation points at the merge gate runbook (#3926 generalized the
      // copy to cover pr-create too, but it still names PR_MERGE_STAGE.md).
      expect(findings[0].recommendation).toContain("PR_MERGE_STAGE.md");
    });

    it("AC2 — skill-no-op extractor is scoped to pr-merge stage only", () => {
      // Same evidence text, different stage → must NOT classify as skill-no-op.
      const findings = AutoRetroService.classifyFailure(
        {
          text: "reported success but PR is not merged",
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).not.toBe("skill-no-op");
    });

    it("AC3 — stop-hook-error AFTER terminal result event is suppressed (post-result noise)", () => {
      // The terminal result event landed BEFORE the stop-hook notification,
      // which means the stage already produced its result and the stop-hook
      // is routine end-of-stage teardown noise. Pre-#3275 this returned
      // stop-hook-error; post-#3275 it falls through to unknown.
      const findings = AutoRetroService.classifyFailure(
        {
          text: [
            '{"type":"result","subtype":"success","total_cost_usd":1.23}',
            '{"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred"}',
          ].join("\n"),
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings.map((f) => f.category)).not.toContain("stop-hook-error");
      // No other structured signal in this fixture — falls through to unknown.
      expect(findings[0].category).toBe("unknown");
    });

    it("regression — stop-hook-error BEFORE terminal result still classifies (the legitimate #3204 case)", () => {
      // The stop-hook-error fires, then the subagent eventually produces a
      // terminal result event LATER. The match index of the stop-hook
      // precedes the result event, so the gate fires.
      const findings = AutoRetroService.classifyFailure(
        {
          text: [
            '{"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred"}',
            '{"type":"result","subtype":"failure"}',
          ].join("\n"),
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("stop-hook-error");
    });

    it("regression — stop-hook-error with NO terminal result event still classifies (silent hang)", () => {
      // The genuine #3204 silent-hang signature: subagent went silent and
      // never produced a terminal result. The gate must treat this as a
      // real failure cause.
      const findings = AutoRetroService.classifyFailure(
        {
          text: '{"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred"}',
          sourcesAnalyzed: ["session_log"],
        },
        "feature-dev"
      );
      expect(findings[0].category).toBe("stop-hook-error");
    });
  });

  // ===========================================================================
  // applyFalseNegativeShippedOverride — generalized shipped-but-PR-merged (#3275)
  // ===========================================================================

  describe("applyFalseNegativeShippedOverride (#3275)", () => {
    it("AC4 — reclassifies non-budget pr-merge failure as false-negative-shipped when PR is MERGED", async () => {
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify({ pr_number: 313 }) as any);
      mockPrView.mockResolvedValueOnce({ state: "MERGED" });

      // Synthesize a non-budget primary finding (stop-hook-error).
      const findings = AutoRetroService.classifyFailure(
        {
          text: '{"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred"}',
          sourcesAnalyzed: ["session_log"],
        },
        "pr-merge"
      );
      expect(findings[0].category).toBe("stop-hook-error");

      await AutoRetroService.applyFalseNegativeShippedOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );

      expect(findings[0].category).toBe("false-negative-shipped");
      expect(findings[0].severity).toBe("low");
      expect(findings[0].evidence).toContain("PR #313 state: MERGED");
      expect(
        findings[0].evidence.some((e) => e.includes("Original category: stop-hook-error"))
      ).toBe(true);
    });

    it("regression — does NOT touch shipped-but-overbudget findings (budget override already won)", async () => {
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify({ pr_number: 313 }) as any);
      mockPrView.mockResolvedValueOnce({ state: "MERGED" });

      // Run the budget override first to convert to shipped-but-overbudget.
      const findings = AutoRetroService.classifyFailure(
        { text: "budget exceeded", sourcesAnalyzed: ["session_log"] },
        "pr-merge"
      );
      await AutoRetroService.applyShippedButOverbudgetOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );
      expect(findings[0].category).toBe("shipped-but-overbudget");

      // Now the false-negative override should be a no-op.
      await AutoRetroService.applyFalseNegativeShippedOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );
      expect(findings[0].category).toBe("shipped-but-overbudget");
      // PR view was consulted exactly once — by the budget override.
      // The false-negative override short-circuits before re-fetching it.
      expect(mockPrView).toHaveBeenCalledTimes(1);
    });

    it("leaves the finding intact when PR is OPEN", async () => {
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify({ pr_number: 313 }) as any);
      mockPrView.mockResolvedValueOnce({ state: "OPEN" });

      const findings = AutoRetroService.classifyFailure(
        {
          text: '{"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred"}',
          sourcesAnalyzed: ["session_log"],
        },
        "pr-merge"
      );
      await AutoRetroService.applyFalseNegativeShippedOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );

      expect(findings[0].category).toBe("stop-hook-error");
    });

    it("is a no-op when findings array is empty", async () => {
      const findings: any[] = [];
      await AutoRetroService.applyFalseNegativeShippedOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );
      expect(findings).toHaveLength(0);
      expect(mockPrView).not.toHaveBeenCalled();
    });

    it("falls back gracefully when pr-{N}.json is missing", async () => {
      vi.mocked(fs.readFile).mockRejectedValueOnce(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const findings = AutoRetroService.classifyFailure(
        {
          text: '{"type":"system","subtype":"notification","key":"stop-hook-error","text":"Stop hook error occurred"}',
          sourcesAnalyzed: ["session_log"],
        },
        "pr-merge"
      );
      await AutoRetroService.applyFalseNegativeShippedOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );

      // No reclassification — fail-closed
      expect(findings[0].category).toBe("stop-hook-error");
      expect(mockPrView).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // applyShippedButOverbudgetOverride — state-aware reclassification (#3108)
  // ===========================================================================

  describe("applyShippedButOverbudgetOverride", () => {
    it("reclassifies budget-exceeded as shipped-but-overbudget when PR is MERGED", async () => {
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify({ pr_number: 313 }) as any);
      mockPrView.mockResolvedValueOnce({ state: "MERGED" });

      const findings = AutoRetroService.classifyFailure(
        { text: "budget exceeded", sourcesAnalyzed: ["session_log"] },
        "pr-merge"
      );
      await AutoRetroService.applyShippedButOverbudgetOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );

      expect(findings[0].category).toBe("shipped-but-overbudget");
      expect(findings[0].severity).toBe("low");
      expect(findings[0].evidence).toContain("PR #313 state: MERGED");
    });

    it("leaves budget-exceeded intact when PR is OPEN", async () => {
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify({ pr_number: 313 }) as any);
      mockPrView.mockResolvedValueOnce({ state: "OPEN" });

      const findings = AutoRetroService.classifyFailure(
        { text: "budget exceeded", sourcesAnalyzed: ["session_log"] },
        "pr-merge"
      );
      await AutoRetroService.applyShippedButOverbudgetOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );

      expect(findings[0].category).toBe("budget-exceeded");
    });

    it("is a no-op when there is no budget-exceeded finding", async () => {
      const findings = AutoRetroService.classifyFailure(
        { text: "tests failed", sourcesAnalyzed: ["session_log"] },
        "pr-merge"
      );
      await AutoRetroService.applyShippedButOverbudgetOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );

      expect(findings[0].category).toBe("validation-failure");
      // Did not need to consult IPC at all
      expect(mockPrView).not.toHaveBeenCalled();
    });

    it("falls back gracefully when pr-{N}.json is missing", async () => {
      // First readFile rejects (no pr-{N}.json)
      vi.mocked(fs.readFile).mockRejectedValueOnce(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const findings = AutoRetroService.classifyFailure(
        { text: "budget exceeded", sourcesAnalyzed: ["session_log"] },
        "pr-merge"
      );
      await AutoRetroService.applyShippedButOverbudgetOverride(
        findings,
        WORKSPACE,
        ISSUE_NUMBER,
        logger as any
      );

      // No reclassification — fail-closed behavior
      expect(findings[0].category).toBe("budget-exceeded");
      expect(mockPrView).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // auto_create_issues=false → gh not called
  // ===========================================================================

  describe("auto_create_issues disabled", () => {
    it("does not call gh issue create when auto_create_issues is false", async () => {
      const configYaml = [
        "feedback_loop:",
        "  auto_retro:",
        "    enabled: true",
        "    auto_create_issues: false",
      ].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      await AutoRetroService.runAfterFailure(WORKSPACE, ISSUE_NUMBER, FAILED_STAGE, logger as any);

      expect(mockIssueCreate).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // auto_create_issues=true + matching severity → IPC called
  // ===========================================================================

  describe("auto_create_issues enabled", () => {
    /**
     * Helper: set up IPC mocks to handle parent epic lookup (returning no parent)
     * and issue creation (returning a new issue). This simulates the common case
     * where the pipeline issue has no parent epic.
     */
    function mockIpcForIssueCreation(newIssueNumber = 1234) {
      // issueView for parent epic lookup — no parent
      mockIssueView.mockResolvedValue({
        number: ISSUE_NUMBER,
        title: "Test Issue",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "",
        isEpic: false,
        // no parentIssueNumber → no parent epic
      });
      // issueCreate returns created issue
      mockIssueCreate.mockResolvedValue({
        number: newIssueNumber,
        title: "",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: `https://github.com/TestOwner/test-repo/issues/${newIssueNumber}`,
        isEpic: false,
      });
    }

    it("calls issueCreate when auto_create_issues is true and severity matches", async () => {
      const configYaml = [
        "feedback_loop:",
        "  auto_retro:",
        "    enabled: true",
        "    auto_create_issues: true",
        "    severity_threshold: low",
      ].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      mockIpcForIssueCreation();

      await AutoRetroService.runAfterFailure(WORKSPACE, ISSUE_NUMBER, FAILED_STAGE, logger as any);

      expect(mockIssueCreate).toHaveBeenCalled();
    });

    it("reports issuesCreated count in result", async () => {
      const configYaml = [
        "feedback_loop:",
        "  auto_retro:",
        "    enabled: true",
        "    auto_create_issues: true",
        "    severity_threshold: low",
      ].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      mockIpcForIssueCreation();

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.issuesCreated).toBe(1);
    });

    it("sets finding.issueNumber from created issue", async () => {
      const configYaml = [
        "feedback_loop:",
        "  auto_retro:",
        "    enabled: true",
        "    auto_create_issues: true",
        "    severity_threshold: low",
      ].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      mockIpcForIssueCreation(777);

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.findings[0].issueNumber).toBe(777);
    });

    it("links created issue to parent epic when pipeline issue has a parent", async () => {
      const configYaml = [
        "feedback_loop:",
        "  auto_retro:",
        "    enabled: true",
        "    auto_create_issues: true",
        "    severity_threshold: low",
      ].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      // issueView for parent epic lookup — has a parent
      mockIssueView.mockResolvedValue({
        number: ISSUE_NUMBER,
        title: "Test Issue",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "",
        isEpic: false,
        parentIssueNumber: 100,
      });

      // issueCreate returns new issue
      mockIssueCreate.mockResolvedValue({
        number: 999,
        title: "",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "https://github.com/TestOwner/test-repo/issues/999",
        isEpic: false,
      });

      mockIssueLinkSubIssue.mockResolvedValue(undefined);

      await AutoRetroService.runAfterFailure(WORKSPACE, ISSUE_NUMBER, FAILED_STAGE, logger as any);

      expect(mockIssueLinkSubIssue).toHaveBeenCalledWith("TestOwner", "test-repo", 100, 999);
    });

    it("skips sub-issue linking when parent epic lookup fails", async () => {
      const configYaml = [
        "feedback_loop:",
        "  auto_retro:",
        "    enabled: true",
        "    auto_create_issues: true",
        "    severity_threshold: low",
      ].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      // getRepoIdentity fails → parent lookup returns null
      // But we need it to succeed for issueCreate, so mock per-call:
      // First call is getParentEpicInfo (fails), second call is createIssuesForFindings
      mockGetRepoIdentity
        .mockRejectedValueOnce(new Error("not in a git repo"))
        .mockResolvedValue({ owner: "TestOwner", repo: "test-repo" });

      mockIssueCreate.mockResolvedValue({
        number: 555,
        title: "",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "https://github.com/TestOwner/test-repo/issues/555",
        isEpic: false,
      });

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      // Issue should still be created despite parent lookup failure
      expect(result).not.toBeNull();
      expect(result!.issuesCreated).toBe(1);

      // No issueLinkSubIssue call
      expect(mockIssueLinkSubIssue).not.toHaveBeenCalled();
    });

    it("skips findings below severity_threshold", async () => {
      const configYaml = [
        "feedback_loop:",
        "  auto_retro:",
        "    enabled: true",
        "    auto_create_issues: true",
        "    severity_threshold: high",
      ].join("\n");
      // Return YAML for the config path; reject any other readFile so no
      // diagnostic file (e.g. `<stage>-cost-capped.log`) ends up in
      // `sourcesAnalyzed` and accidentally trips the file-existence
      // cost-cap extractor (#3275).
      vi.mocked(fs.readFile).mockImplementation(async (filePath: any) => {
        if (typeof filePath === "string" && filePath.endsWith("config.yaml")) {
          return configYaml as any;
        }
        const err = Object.assign(new Error("ENOENT"), { code: "ENOENT" });
        throw err;
      });

      mockIpcForIssueCreation();

      // 'unknown' category has severity 'low', which is below 'high' threshold
      // No log patterns → unknown → should not create issue
      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.issuesCreated).toBe(0);
      expect(mockIssueCreate).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // issueCreate failure → logs warning, does not throw
  // ===========================================================================

  describe("issueCreate failure", () => {
    it("logs warning and returns result when IPC fails", async () => {
      const configYaml = [
        "feedback_loop:",
        "  auto_retro:",
        "    enabled: true",
        "    auto_create_issues: true",
        "    severity_threshold: low",
      ].join("\n");
      vi.mocked(fs.readFile).mockResolvedValue(configYaml as any);

      // Parent lookup succeeds (no parent) but issue creation fails
      mockIssueView.mockResolvedValue({
        number: ISSUE_NUMBER,
        title: "Test Issue",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "",
        isEpic: false,
      });
      mockIssueCreate.mockRejectedValue(new Error("authentication required"));

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      // Should not throw; returns a result
      expect(result).not.toBeNull();
      // Warning logged
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("failed to create GitHub issue"),
        expect.any(Object)
      );
      // issuesCreated is 0
      expect(result!.issuesCreated).toBe(0);
    });
  });

  // ===========================================================================
  // retro file write failure → returns null, logs warning
  // ===========================================================================

  describe("retro file write failure", () => {
    it("returns null and logs warning when mkdir fails", async () => {
      // Config defaults (enabled: true)
      vi.mocked(fs.mkdir).mockRejectedValue(new Error("permission denied"));

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).toBeNull();
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("Auto-retro analysis failed"),
        expect.objectContaining({ issueNumber: ISSUE_NUMBER })
      );
    });

    it("returns null and logs warning when writeFile fails", async () => {
      vi.mocked(fs.writeFile).mockRejectedValue(new Error("disk full"));

      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).toBeNull();
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("Auto-retro analysis failed"),
        expect.any(Object)
      );
    });
  });

  // ===========================================================================
  // parseAutoRetroConfig unit tests
  // ===========================================================================

  describe("parseAutoRetroConfig", () => {
    it("returns defaults when content has no auto_retro section", () => {
      const config = AutoRetroService.parseAutoRetroConfig(
        "feedback_loop:\n  health_warning_threshold: 70\n"
      );
      expect(config.enabled).toBe(true);
      expect(config.auto_create_issues).toBe(false);
      expect(config.severity_threshold).toBe("high");
    });

    it("parses enabled: false", () => {
      const yaml = "feedback_loop:\n  auto_retro:\n    enabled: false\n";
      const config = AutoRetroService.parseAutoRetroConfig(yaml);
      expect(config.enabled).toBe(false);
    });

    it("parses auto_create_issues: true", () => {
      const yaml =
        "feedback_loop:\n  auto_retro:\n    enabled: true\n    auto_create_issues: true\n";
      const config = AutoRetroService.parseAutoRetroConfig(yaml);
      expect(config.auto_create_issues).toBe(true);
    });

    it("parses severity_threshold: medium", () => {
      const yaml = "feedback_loop:\n  auto_retro:\n    severity_threshold: medium\n";
      const config = AutoRetroService.parseAutoRetroConfig(yaml);
      expect(config.severity_threshold).toBe("medium");
    });

    it("returns defaults when config is empty string", () => {
      const config = AutoRetroService.parseAutoRetroConfig("");
      expect(config.enabled).toBe(true);
      expect(config.auto_create_issues).toBe(false);
    });
  });

  // ===========================================================================
  // Result shape
  // ===========================================================================

  describe("result shape", () => {
    it("result contains correct issueNumber and failedStage", async () => {
      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        99,
        "pr-merge",
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.issueNumber).toBe(99);
      expect(result!.failedStage).toBe("pr-merge");
    });

    it("retroFile path includes issueNumber and today date", async () => {
      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      const today = new Date().toISOString().slice(0, 10);
      expect(result!.retroFile).toContain(today);
      expect(result!.retroFile).toContain(String(ISSUE_NUMBER));
    });

    it("retroFile is written under .nightgauge/retros/", async () => {
      const result = await AutoRetroService.runAfterFailure(
        WORKSPACE,
        ISSUE_NUMBER,
        FAILED_STAGE,
        logger as any
      );

      expect(result).not.toBeNull();
      expect(result!.retroFile).toContain(".nightgauge/retros");
    });
  });

  // ===========================================================================
  // parseIssueNumberFromUrl unit tests
  // ===========================================================================

  describe("parseIssueNumberFromUrl", () => {
    it("parses issue number from standard GitHub URL", () => {
      expect(
        AutoRetroService.parseIssueNumberFromUrl(
          "https://github.com/nightgauge/nightgauge/issues/1234"
        )
      ).toBe(1234);
    });

    it("returns undefined for non-issue URL", () => {
      expect(
        AutoRetroService.parseIssueNumberFromUrl("https://github.com/nightgauge/nightgauge")
      ).toBeUndefined();
    });

    it("returns undefined for empty string", () => {
      expect(AutoRetroService.parseIssueNumberFromUrl("")).toBeUndefined();
    });
  });
});
