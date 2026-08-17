/**
 * skillRunner.grokEffortEnv.test.ts (Issue #635)
 *
 * Since #606 the grok child process gets its effort from one of two
 * channels: the operator's `NIGHTGAUGE_GROK_EFFORT`, or — absent that — the
 * dispatch envelope's resolved wire effort (`effortOverride`, #581). That
 * precedence is implemented in `skillRunner.ts` (`grokOperatorEffort` and the
 * `grokEnv.NIGHTGAUGE_GROK_EFFORT` assignment) but nothing previously
 * asserted the resulting CHILD SPAWN ENVIRONMENT.
 *
 * These tests exercise the REAL composed dispatch path
 * (`runStageSkillHeadless`) through the same `child_process.spawn` seam
 * `skillRunner.adapterPerfMode.test.ts` uses — they do not reimplement the
 * precedence rule (see #498 on that anti-pattern). `modelOverride` +
 * `effortOverride` pin `modelDecision.effort` to a known value (the
 * `if (modelOverride)` branch in `runStageSkillHeadless` sets
 * `effort: effortOverride ?? resolveStageEffort(...)` verbatim when
 * `effortOverride` is on the EFFORT_LEVELS ladder — see #581), so "the wire
 * effort" is a concrete, controlled string rather than whatever the local
 * resolution chain happens to produce.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";
import * as fs from "fs";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(() => ({ show: vi.fn(), sendText: vi.fn() })),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  extensions: {
    getExtension: vi.fn(() => null),
  },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
  writeFileSync: vi.fn(),
  mkdirSync: vi.fn(),
}));

vi.mock("child_process", async () => {
  // Since #79 the extension composes no skill text of its own: it shells out
  // to `nightgauge skill render`. Answer that one call with the shared
  // envelope stub; every other execFileSync caller keeps an empty result.
  const { isSkillRenderCall, skillRenderStdout } = await import("../helpers/skillRender");
  return {
    spawn: vi.fn(),
    execFileSync: vi.fn((_cmd: string, args: string[]) =>
      isSkillRenderCall(args) ? skillRenderStdout(args) : ""
    ),
    execFile: vi.fn(
      (
        _cmd: string,
        _args: string[],
        _opts: unknown,
        cb: (e: Error | null, s: string, t: string) => void
      ) => {
        cb(new Error("no children"), "", "");
      }
    ),
  };
});

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: false,
  })),
  logDeprecationWarning: vi.fn(),
}));

// Partial mock: keep everything else (including resolveModel's dependency
// chain, which these tests never reach — modelOverride + effortOverride skip
// it) real; only the getters that would otherwise touch disk/CLI state need
// stubbing for a clean grok dispatch.
vi.mock("../../src/utils/incrediConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/incrediConfig");
  return {
    ...actual,
    getAuthProvider: vi.fn(() => "max"),
    getPerformanceMode: vi.fn(() => "elevated"),
  };
});

vi.mock("../../src/services/RepositoryContextLoader", () => ({
  RepositoryContextLoader: {
    getInstance: vi.fn(() => ({
      getCurrentRepository: vi.fn().mockReturnValue(null),
      getWorkingDirectory: vi.fn().mockReturnValue("/test/workspace"),
    })),
  },
}));

import { runStageSkillHeadless } from "../../src/utils/skillRunner";

const MOCK_SKILL_CONTENT = `---
name: test-skill
allowed-tools: Read Write Edit
---
# Test Skill

Test content.
`;

function createMockChildProcess(): ChildProcess {
  const proc = new EventEmitter() as ChildProcess;
  proc.stdout = new EventEmitter() as NodeJS.ReadableStream;
  proc.stderr = new EventEmitter() as NodeJS.ReadableStream;
  proc.stdin = {
    write: vi.fn(),
    end: vi.fn(),
    destroyed: false,
  } as unknown as NodeJS.WritableStream;
  proc.kill = vi.fn();
  proc.killed = false;
  return proc;
}

function setExistsForGrok() {
  vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
    const filePath = String(p);
    if (filePath.includes("SKILL.md") || filePath.includes("skills/")) return true;
    if (filePath.includes("sdk-cli.cjs")) return true;
    if (filePath.includes("nightgauge-sdk/dist/cli/index.js")) return true;
    if (
      filePath.endsWith("/node") ||
      filePath.endsWith("/git") ||
      filePath.endsWith("/gh") ||
      filePath.endsWith("/grok")
    ) {
      return true;
    }
    return false;
  });
  vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);
}

function lastSpawnEnv(): Record<string, string> {
  const calls = vi.mocked(spawn).mock.calls;
  expect(calls.length, "spawn was not called").toBeGreaterThan(0);
  const lastCall = calls[calls.length - 1];
  const opts = lastCall[2] as { env?: Record<string, string> };
  return opts?.env ?? {};
}

// A model id with no registry descriptor and no tier-band match, so
// `modelTierBand()` resolves to `undefined` and the grok band-mapping branch
// (`getAdapterModelForBand`) is never reached — the fixture stays isolated to
// the effort precedence this suite covers, not grok model-band translation
// (that is #3214's territory).
const FIXTURE_MODEL = "test-fixture-grok-635-model";

const originalEnv = process.env;

beforeEach(() => {
  vi.clearAllMocks();
  process.env = {
    ...originalEnv,
    PATH: "/usr/local/bin:/usr/bin:/bin",
    VITEST: "true",
    // #635: NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE> is Step 1 of
    // resolveStageAdapter — the highest-precedence adapter selector — so the
    // grok dispatch path is reached deterministically regardless of the
    // ui.core.adapter default.
    NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV: "grok",
  };
  delete process.env.NIGHTGAUGE_GROK_EFFORT;
  setExistsForGrok();
  vi.mocked(spawn).mockReturnValue(createMockChildProcess());
});

afterEach(() => {
  process.env = {
    ...originalEnv,
    VITEST: "true",
  };
  vi.restoreAllMocks();
});

describe("grok adapter — child-spawn NIGHTGAUGE_GROK_EFFORT (Issue #635, #606)", () => {
  it("with no operator override, the spawned child env's NIGHTGAUGE_GROK_EFFORT equals the wire effort", () => {
    const WIRE_EFFORT = "high";

    runStageSkillHeadless(
      "feature-dev",
      42,
      {},
      undefined, // issueMetadata
      undefined, // _batchContext
      undefined, // skipToPhase
      FIXTURE_MODEL, // modelOverride
      undefined, // pauseAutoRouting
      undefined, // pinnedWorkspaceRoot
      undefined, // modelOverrideSource
      undefined, // injectedSkillContent
      undefined, // autonomousMode
      undefined, // warnThresholdUsd
      undefined, // targetRepoOverride
      undefined, // runId
      WIRE_EFFORT // effortOverride — the resolved wire effort (#581)
    );

    expect(lastSpawnEnv().NIGHTGAUGE_GROK_EFFORT).toBe(WIRE_EFFORT);
  });

  it("with an operator override set, it wins over the envelope at the branch level (deliberately distinct values)", () => {
    // Deliberately distinct from the wire effort below: if the precedence
    // branch were swapped (envelope wins instead of override), this would
    // assert "low" and get "high" — failing loudly instead of passing by
    // coincidence of equal values.
    const OPERATOR_EFFORT = "low";
    const WIRE_EFFORT = "high";
    process.env.NIGHTGAUGE_GROK_EFFORT = OPERATOR_EFFORT;

    runStageSkillHeadless(
      "feature-dev",
      42,
      {},
      undefined, // issueMetadata
      undefined, // _batchContext
      undefined, // skipToPhase
      FIXTURE_MODEL, // modelOverride
      undefined, // pauseAutoRouting
      undefined, // pinnedWorkspaceRoot
      undefined, // modelOverrideSource
      undefined, // injectedSkillContent
      undefined, // autonomousMode
      undefined, // warnThresholdUsd
      undefined, // targetRepoOverride
      undefined, // runId
      WIRE_EFFORT // effortOverride — the resolved wire effort (#581)
    );

    expect(lastSpawnEnv().NIGHTGAUGE_GROK_EFFORT).toBe(OPERATOR_EFFORT);
    expect(lastSpawnEnv().NIGHTGAUGE_GROK_EFFORT).not.toBe(WIRE_EFFORT);
  });
});
