import { describe, it, expect, vi, beforeEach } from "vitest";
import { runAdapterAuthPreflight } from "../../../src/cli/adapters/runAdapterAuthPreflight.js";
import { resetAuthPreflightCache } from "../../../src/cli/adapters/authPreflightCache.js";
import { AdapterError } from "../../../src/cli/adapters/errors.js";
import type { ICliAdapter, NightgaugeAdapter } from "../../../src/cli/adapters/ICliAdapter.js";

function fakeAdapter(
  name: NightgaugeAdapter,
  validateImpl: ICliAdapter["validateAuth"]
): ICliAdapter {
  return {
    name,
    displayName: name,
    cliCommand: name,
    validateAuth: validateImpl,
    createQueryFunction: vi.fn() as unknown as ICliAdapter["createQueryFunction"],
    getDefaultArgs: () => [],
    getOrchestrationCapability: () => "sdk-fanout" as const,
    requiresDirectApiKey: () => false,
  };
}

function buildRegistry(adapters: ICliAdapter[]) {
  const map = new Map(adapters.map((a) => [a.name, a]));
  return {
    get(name: NightgaugeAdapter): ICliAdapter {
      const a = map.get(name);
      if (!a) throw new Error(`Unknown adapter '${name}'`);
      return a;
    },
    has(name: NightgaugeAdapter): boolean {
      return map.has(name);
    },
  };
}

describe("runAdapterAuthPreflight", () => {
  // The preflight now dedups probes through a process-wide cache / single-flight
  // (Issue #312). These cases reuse adapter names with no cwd (same cache key),
  // so isolate each with a fresh cache — otherwise a cached OK from an earlier
  // case would suppress a later case's expected probe/failure.
  beforeEach(() => {
    resetAuthPreflightCache();
  });

  it("returns ok=true with empty results when adapter list is empty", async () => {
    const result = await runAdapterAuthPreflight([], { registry: buildRegistry([]) });

    expect(result).toEqual({ ok: true, results: {}, failures: [] });
  });

  it("dedupes the input list — runs each distinct adapter exactly once", async () => {
    const claudeProbe = vi.fn(async () => "passed" as const);
    const geminiProbe = vi.fn(async () => "passed" as const);
    const registry = buildRegistry([
      fakeAdapter("claude-sdk", claudeProbe),
      fakeAdapter("gemini-sdk", geminiProbe),
    ]);

    const result = await runAdapterAuthPreflight(["claude-sdk", "claude-sdk", "gemini-sdk"], {
      registry,
    });

    expect(claudeProbe).toHaveBeenCalledTimes(1);
    expect(geminiProbe).toHaveBeenCalledTimes(1);
    expect(result.ok).toBe(true);
    expect(Object.keys(result.results)).toHaveLength(2);
  });

  it("runs probes in parallel — wall clock is max(t1, t2), not t1 + t2", async () => {
    vi.useFakeTimers();
    try {
      const probeWith = (delayMs: number) =>
        vi.fn(
          () =>
            new Promise<"passed">((resolve) => {
              setTimeout(() => resolve("passed"), delayMs);
            })
        );
      const slow1 = probeWith(2_000);
      const slow2 = probeWith(3_000);
      const registry = buildRegistry([
        fakeAdapter("claude-sdk", slow1),
        fakeAdapter("gemini-sdk", slow2),
      ]);

      let resolved = false;
      const promise = runAdapterAuthPreflight(["claude-sdk", "gemini-sdk"], {
        registry,
        timeoutMs: 10_000,
      }).then((r) => {
        resolved = true;
        return r;
      });

      // After 2.5s the first probe is done but the second is still pending —
      // sequential execution would have only just started the second probe.
      await vi.advanceTimersByTimeAsync(2_500);
      expect(resolved).toBe(false);

      // After advancing to t=3001 (well under t1+t2=5_000) both probes resolve.
      await vi.advanceTimersByTimeAsync(600);
      const result = await promise;

      expect(result.ok).toBe(true);
      expect(slow1).toHaveBeenCalledTimes(1);
      expect(slow2).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("aggregates: any single failure flips ok to false", async () => {
    const registry = buildRegistry([
      fakeAdapter("claude-sdk", async () => "passed"),
      fakeAdapter("codex", async () => {
        throw new AdapterError("not authed", "AUTH_MISSING", "Codex");
      }),
    ]);

    const result = await runAdapterAuthPreflight(["claude-sdk", "codex"], {
      registry,
    });

    expect(result.ok).toBe(false);
    expect(result.failures).toHaveLength(1);
    expect(result.failures[0].adapter).toBe("codex");
    expect(result.results["claude-sdk"]).toEqual({ ok: true });
  });

  it("populates suggestedFix for every known adapter", async () => {
    const known: NightgaugeAdapter[] = [
      "claude-sdk",
      "claude-headless",
      "codex",
      "gemini",
      "gemini-sdk",
      "lm-studio",
      "ollama",
      "copilot",
    ];
    const adapters = known.map((n) =>
      fakeAdapter(n, async () => {
        throw new AdapterError("missing", "AUTH_MISSING", n);
      })
    );
    const registry = buildRegistry(adapters);

    const result = await runAdapterAuthPreflight(known, { registry });

    expect(result.ok).toBe(false);
    expect(result.failures).toHaveLength(known.length);
    for (const f of result.failures) {
      expect(f.suggestedFix).toBeTruthy();
      expect(f.suggestedFix.length).toBeGreaterThan(10);
    }
  });

  // #1615 / ADR-022 § 17: hosted providers behind OpenCode authenticate with
  // their own API-key variable, anthropic/* only with ANTHROPIC_API_KEY. The
  // hint must never steer anyone to an interactive provider login.
  it("gives opencode an install, local-provider and API-key hint with no login", async () => {
    const registry = buildRegistry([
      fakeAdapter("opencode", async () => {
        throw new AdapterError("missing", "AUTH_MISSING", "opencode");
      }),
    ]);

    const result = await runAdapterAuthPreflight(["opencode"], { registry });

    expect(result.failures).toHaveLength(1);
    const fix = result.failures[0].suggestedFix;
    expect(fix).toContain("opencode-ai");
    expect(fix).toContain("http://127.0.0.1:1234");
    expect(fix).toContain("http://localhost:11434");
    expect(fix).toContain("ANTHROPIC_API_KEY");
    expect(fix).toContain("OPENAI_API_KEY");
    expect(fix).not.toMatch(/providers login|auth login/);
    // anthropic/* dispatches on ANTHROPIC_API_KEY (ADR-022 § 17), so the hint
    // must not tell the operator it is refused.
    expect(fix).not.toMatch(/refused/);
    // Only loopback hosts: no LAN or remote model host in a hint.
    for (const url of fix.match(/https?:\/\/[^\s),]+/g) ?? []) {
      expect(new URL(url).hostname).toMatch(/^(127\.0\.0\.1|localhost)$/);
    }
  });

  it("all-pass aggregate yields ok=true and zero failures", async () => {
    const registry = buildRegistry([
      fakeAdapter("claude-sdk", async () => "passed"),
      fakeAdapter("gemini-sdk", async () => "passed"),
    ]);

    const result = await runAdapterAuthPreflight(["claude-sdk", "gemini-sdk"], {
      registry,
    });

    expect(result.ok).toBe(true);
    expect(result.failures).toHaveLength(0);
    expect(result.results["claude-sdk"]).toEqual({ ok: true });
    expect(result.results["gemini-sdk"]).toEqual({ ok: true });
  });
});
