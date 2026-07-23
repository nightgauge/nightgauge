/**
 * Unit tests for the Adapter Doctor command's pure orchestration helpers
 * (Issue #4031): merging Go binary/version/MCP facts with SDK auth verdicts,
 * per-stage status derivation, the Go `doctor --adapters --json` spawn wrapper,
 * and the full report builder with injected dependencies.
 */

import { describe, it, expect } from "vitest";
import { EventEmitter } from "events";
import type { spawn } from "child_process";
import type { AdapterPreflightAggregateResult } from "@nightgauge/sdk";
import {
  mergeAdapterRows,
  finalizeStageRows,
  collectDistinctSdkAdapters,
  runGoDoctorAdapters,
  buildAdapterDoctorReport,
  EXECUTABLE_STAGES,
  type GoAdapterHealth,
  type StageRouting,
  type BuildReportDeps,
} from "../../src/commands/adapterDoctor";

function authResult(
  results: AdapterPreflightAggregateResult["results"],
  failures: AdapterPreflightAggregateResult["failures"] = []
): AdapterPreflightAggregateResult {
  return { ok: failures.length === 0, results, failures };
}

function makeFakeSpawn(
  stdout: string,
  opts: { stderr?: string; error?: Error } = {}
): typeof spawn {
  return ((..._args: unknown[]) => {
    const proc = new EventEmitter() as EventEmitter & {
      stdout: EventEmitter;
      stderr: EventEmitter;
    };
    proc.stdout = new EventEmitter();
    proc.stderr = new EventEmitter();
    setImmediate(() => {
      if (opts.error) {
        proc.emit("error", opts.error);
        return;
      }
      if (stdout) proc.stdout.emit("data", Buffer.from(stdout));
      if (opts.stderr) proc.stderr.emit("data", Buffer.from(opts.stderr));
      proc.emit("close", 0);
    });
    return proc;
  }) as unknown as typeof spawn;
}

const codexHealthy: GoAdapterHealth = {
  adapter: "codex",
  kind: "cli",
  binary: "codex",
  installed: true,
  path: "/usr/local/bin/codex",
  version: "0.112.0",
  version_ok: true,
  min_version: "0.111.0",
  mcp: { config_path: "/home/u/.codex/config.toml", config_present: true, managed_block: true },
  ok: true,
};

describe("mergeAdapterRows (#4031)", () => {
  it("marks an adapter ready only when installed + version_ok + authenticated", () => {
    const rows = mergeAdapterRows(
      ["codex"],
      [codexHealthy],
      authResult({ codex: { ok: true } }),
      true
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].ok).toBe(true);
    expect(rows[0].installed).toBe(true);
    expect(rows[0].authOk).toBe(true);
    expect(rows[0].mcp?.managedBlock).toBe(true);
    expect(rows[0].remediations).toEqual([]);
  });

  it("collects remediation from both the Go (version) and SDK (auth) layers", () => {
    const stale: GoAdapterHealth = {
      ...codexHealthy,
      version: "0.110.0",
      version_ok: false,
      ok: false,
      remediation: "Update codex to >= 0.111.0 (current 0.110.0).",
    };
    const rows = mergeAdapterRows(
      ["codex"],
      [stale],
      authResult({ codex: { ok: false, reason: "not logged in" } }, [
        { adapter: "codex", reason: "not logged in", suggestedFix: "Run `codex login`." },
      ]),
      true
    );
    expect(rows[0].ok).toBe(false);
    expect(rows[0].versionOk).toBe(false);
    expect(rows[0].remediations).toContain("Update codex to >= 0.111.0 (current 0.110.0).");
    expect(rows[0].remediations).toContain("Run `codex login`.");
  });

  it("falls back to auth-only readiness when the Go binary facts are unavailable", () => {
    const rows = mergeAdapterRows(
      ["codex"],
      [], // no Go facts
      authResult({ codex: { ok: true } }),
      false
    );
    // installed unknown (rendered as unknown), but auth passed → ready.
    expect(rows[0].installed).toBe(false);
    expect(rows[0].ok).toBe(true);
  });

  it("treats a missing auth result as not authenticated", () => {
    const rows = mergeAdapterRows(["gemini"], [], authResult({}), false);
    expect(rows[0].authOk).toBe(false);
    expect(rows[0].ok).toBe(false);
  });

  it("handles binaryResolved=true but no matching Go row (version defaults true, ok gated on install)", () => {
    // Go ran but returned no row for the requested adapter (or echoed a different
    // name): installed must be false, versionOk defaults true, ok stays false.
    const rows = mergeAdapterRows(["codex"], [], authResult({ codex: { ok: true } }), true);
    expect(rows[0].installed).toBe(false);
    expect(rows[0].versionOk).toBe(true);
    expect(rows[0].ok).toBe(false); // not installed ⇒ not ready even with auth ok
  });

  it("drops Go rows the extension did not request (rows track the requested set)", () => {
    const rows = mergeAdapterRows(
      ["codex"],
      [codexHealthy, { ...codexHealthy, adapter: "gemini", binary: "gemini" }],
      authResult({ codex: { ok: true } }),
      true
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].sdkAdapter).toBe("codex");
  });

  it("does NOT mark a local HTTP adapter ready in the binary-missing fallback", () => {
    // ollama/lm-studio validateAuth is a no-op (authOk always true), so the
    // auth-only fallback must not claim them ready when Go facts are absent.
    const rows = mergeAdapterRows(["ollama"], [], authResult({ ollama: { ok: true } }), false);
    expect(rows[0].authOk).toBe(true);
    expect(rows[0].ok).toBe(false);
    expect(rows[0].remediations.join(" ")).toMatch(/Cannot verify readiness/);
  });
});

describe("finalizeStageRows (#4031)", () => {
  const routing: StageRouting[] = [
    {
      stage: "feature-dev",
      adapter: "codex",
      sdkAdapter: "codex",
      source: "stage-config",
      model: "opus",
    },
    {
      stage: "pr-merge",
      adapter: "claude",
      sdkAdapter: "claude-headless",
      source: "default",
      model: "(auto / router)",
    },
  ];

  it("derives ok/error status from the merged adapter rows and resolves codex models", () => {
    const rows = mergeAdapterRows(
      ["codex", "claude-headless"],
      [codexHealthy],
      authResult({ codex: { ok: true }, "claude-headless": { ok: false, reason: "no auth" } }, [
        { adapter: "claude-headless", reason: "no auth", suggestedFix: "Run `claude auth login`." },
      ]),
      true
    );
    const stages = finalizeStageRows(routing, rows, true);
    const codexStage = stages.find((s) => s.stage === "feature-dev")!;
    const claudeStage = stages.find((s) => s.stage === "pr-merge")!;
    expect(codexStage.status).toBe("ok");
    expect(codexStage.codexModel).toBe("gpt-5.6-sol");
    expect(claudeStage.status).toBe("error");
    expect(claudeStage.codexModel).toBeUndefined();
  });

  it("emits 'warn' when binary/version failed but auth passed", () => {
    // installed=false/versionOk=false but authOk=true, binaryResolved=true ⇒ warn
    const rows = mergeAdapterRows(
      ["codex"],
      [{ ...codexHealthy, installed: false, version_ok: false, ok: false }],
      authResult({ codex: { ok: true } }),
      true
    );
    const stages = finalizeStageRows(
      [{ stage: "feature-dev", adapter: "codex", sdkAdapter: "codex", source: "x", model: "opus" }],
      rows,
      true
    );
    expect(stages[0].status).toBe("warn");
  });

  it("emits 'unknown' when a stage routes to an adapter with no merged row", () => {
    const stages = finalizeStageRows(
      [
        {
          stage: "feature-dev",
          adapter: "gemini",
          sdkAdapter: "gemini",
          source: "x",
          model: "sonnet",
        },
      ],
      [], // no rows
      true
    );
    expect(stages[0].status).toBe("unknown");
  });
});

describe("collectDistinctSdkAdapters (#4031)", () => {
  it("dedupes per-stage adapters and includes the global default", () => {
    const routing: StageRouting[] = [
      { stage: "feature-dev", adapter: "codex", sdkAdapter: "codex", source: "x", model: "opus" },
      { stage: "pr-merge", adapter: "codex", sdkAdapter: "codex", source: "x", model: "opus" },
    ];
    expect(collectDistinctSdkAdapters(routing, "claude-headless")).toEqual([
      "codex",
      "claude-headless",
    ]);
  });
});

describe("runGoDoctorAdapters (#4031)", () => {
  it("parses the adapter section from doctor --json", async () => {
    const json = JSON.stringify({ v: 1, adapters: [codexHealthy] });
    const out = await runGoDoctorAdapters(
      "/bin/ib",
      ["codex"],
      "/repo",
      process.env,
      makeFakeSpawn(json)
    );
    expect(out).toHaveLength(1);
    expect(out[0].adapter).toBe("codex");
  });

  it("rejects when stdout is not valid JSON", async () => {
    await expect(
      runGoDoctorAdapters("/bin/ib", ["codex"], "/repo", process.env, makeFakeSpawn("not json"))
    ).rejects.toThrow(/could not parse/);
  });

  it("rejects when the process fails to spawn", async () => {
    await expect(
      runGoDoctorAdapters(
        "/bin/ib",
        ["codex"],
        "/repo",
        process.env,
        makeFakeSpawn("", { error: new Error("ENOENT") })
      )
    ).rejects.toThrow(/failed to spawn/);
  });
});

describe("buildAdapterDoctorReport (#4031)", () => {
  const codexRouting = (): StageRouting[] => [
    {
      stage: "feature-dev",
      adapter: "codex",
      sdkAdapter: "codex",
      source: "stage-config",
      model: "opus",
    },
    {
      stage: "pr-merge",
      adapter: "codex",
      sdkAdapter: "codex",
      source: "stage-config",
      model: "sonnet",
    },
  ];

  it("composes a report from injected resolvers/probes (binary present)", async () => {
    const deps: BuildReportDeps = {
      workspaceRoot: "/repo",
      env: {},
      resolveBinary: async () => "/bin/ib",
      runGoAdapters: async () => [codexHealthy],
      runAuth: async () => authResult({ codex: { ok: true } }),
      globalAdapter: () => "codex",
      now: () => "TEST-TIME",
      resolveRouting: codexRouting,
    };
    const report = await buildAdapterDoctorReport(deps);
    expect(report.binaryResolved).toBe(true);
    expect(report.generatedAt).toBe("TEST-TIME");
    expect(report.rows.map((r) => r.sdkAdapter)).toEqual(["codex"]);
    expect(report.rows[0].ok).toBe(true);
    expect(report.stages).toHaveLength(2);
    expect(report.stages.every((s) => s.status === "ok")).toBe(true);
    expect(report.notes).toEqual([]);
  });

  it("notes the missing binary and still reports auth", async () => {
    const deps: BuildReportDeps = {
      workspaceRoot: "/repo",
      env: {},
      resolveBinary: async () => null,
      runGoAdapters: async () => {
        throw new Error("should not be called");
      },
      runAuth: async () => authResult({ codex: { ok: true } }),
      globalAdapter: () => "codex",
      now: () => "T",
      resolveRouting: codexRouting,
    };
    const report = await buildAdapterDoctorReport(deps);
    expect(report.binaryResolved).toBe(false);
    expect(report.notes.join(" ")).toMatch(/Go binary not found/);
    expect(report.rows[0].authOk).toBe(true);
    expect(report.rows[0].ok).toBe(true); // auth-only fallback
  });

  it("resolves the codex model PER STAGE (same adapter, different tiers)", async () => {
    const report = await buildAdapterDoctorReport({
      workspaceRoot: "/repo",
      env: {},
      resolveBinary: async () => "/bin/ib",
      runGoAdapters: async () => [codexHealthy],
      runAuth: async () => authResult({ codex: { ok: true } }),
      globalAdapter: () => "codex",
      now: () => "T",
      resolveRouting: codexRouting, // feature-dev=opus, pr-merge=sonnet
    });
    const dev = report.stages.find((s) => s.stage === "feature-dev")!;
    const merge = report.stages.find((s) => s.stage === "pr-merge")!;
    expect(dev.codexModel).toBe("gpt-5.6-sol");
    expect(merge.codexModel).toBe("gpt-5.6-terra");
    expect(dev.codexModel).not.toBe(merge.codexModel); // not cached once per adapter
  });

  it("feeds the deduped distinct adapter list to both probes", async () => {
    let goArgs: string[] = [];
    let authArgs: string[] = [];
    await buildAdapterDoctorReport({
      workspaceRoot: "/repo",
      env: {},
      resolveBinary: async () => "/bin/ib",
      runGoAdapters: async (_b, adapters) => {
        goArgs = adapters;
        return [codexHealthy];
      },
      runAuth: async (adapters) => {
        authArgs = adapters;
        return authResult({ codex: { ok: true } });
      },
      globalAdapter: () => "codex",
      now: () => "T",
      resolveRouting: codexRouting, // two codex stages → one distinct adapter
    });
    expect(goArgs).toEqual(["codex"]);
    expect(authArgs).toEqual(["codex"]);
  });

  it("notes a binary-resolved-but-probe-threw failure and falls back to auth", async () => {
    const report = await buildAdapterDoctorReport({
      workspaceRoot: "/repo",
      env: {},
      resolveBinary: async () => "/bin/ib", // binary found...
      runGoAdapters: async () => {
        throw new Error("could not parse doctor --json output"); // ...but probe throws
      },
      runAuth: async () => authResult({ codex: { ok: true } }),
      globalAdapter: () => "codex",
      now: () => "T",
      resolveRouting: codexRouting,
    });
    expect(report.binaryResolved).toBe(false);
    expect(report.notes.join(" ")).toMatch(/probe failed/);
    expect(report.rows[0].ok).toBe(true); // auth-only fallback (codex is CLI)
  });

  it("notes an auth-probe rejection without crashing", async () => {
    const report = await buildAdapterDoctorReport({
      workspaceRoot: "/repo",
      env: {},
      resolveBinary: async () => "/bin/ib",
      runGoAdapters: async () => [codexHealthy],
      runAuth: async () => {
        throw new Error("auth runner exploded");
      },
      globalAdapter: () => "codex",
      now: () => "T",
      resolveRouting: codexRouting,
    });
    expect(report.notes.join(" ")).toMatch(/Auth probe failed/);
    expect(report.rows[0].authOk).toBe(false);
  });

  it("excludes the pipeline-start / pipeline-finish boundary sentinels", () => {
    expect(EXECUTABLE_STAGES).not.toContain("pipeline-start");
    expect(EXECUTABLE_STAGES).not.toContain("pipeline-finish");
    expect(EXECUTABLE_STAGES).toContain("feature-dev");
    expect(EXECUTABLE_STAGES).toHaveLength(6);
  });
});
