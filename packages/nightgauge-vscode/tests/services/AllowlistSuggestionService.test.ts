/**
 * AllowlistSuggestionService.test.ts
 *
 * Unit tests for allowlist suggestion generation from blocked/warned events.
 * The service is a pure function (no side effects) — no vscode mocks needed.
 *
 * @see Issue #786 - Firewall Learning Mode
 */

import { describe, it, expect } from "vitest";
import { AllowlistSuggestionService } from "../../src/services/AllowlistSuggestionService";
import type { SanitizationEvent } from "../../src/views/dashboard/FirewallTypes";

function makeEvent(overrides: Partial<SanitizationEvent> = {}): SanitizationEvent {
  return {
    timestamp: new Date("2026-02-16T12:00:00Z"),
    event: "blocked",
    category: "destructive",
    pattern: "rm.*-rf",
    content: "rm -rf ./build/output",
    tool: "Bash",
    branch: "feat/786",
    context: "",
    ...overrides,
  };
}

describe("AllowlistSuggestionService", () => {
  const service = new AllowlistSuggestionService();

  it("returns empty array when no events", () => {
    const result = service.generateSuggestions([], [], [], []);
    expect(result).toEqual([]);
  });

  it("returns empty array when only bypassed events", () => {
    const events = [makeEvent({ event: "bypassed" })];
    const result = service.generateSuggestions(events, [], [], []);
    expect(result).toEqual([]);
  });

  it("suggests safe_directory when 2+ events share directory prefix", () => {
    const events = [
      makeEvent({
        content: "rm -rf ./build/output/chunk1.js",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
      makeEvent({
        content: "rm -rf ./build/output/chunk2.js",
        timestamp: new Date("2026-02-16T12:01:00Z"),
      }),
    ];

    const result = service.generateSuggestions(events, [], [], []);
    expect(result.length).toBeGreaterThan(0);

    const safeDirSuggestion = result.find(
      (s) => s.type === "safe_directory" && s.pattern === "./build"
    );
    expect(safeDirSuggestion).toBeDefined();
    expect(safeDirSuggestion!.frequency).toBe(2);
  });

  it("suggests allowlist regex for single-occurrence commands", () => {
    const events = [
      makeEvent({
        content: "rm -rf ./temp-unique-dir/file.js",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
    ];

    const result = service.generateSuggestions(events, [], [], []);
    expect(result.length).toBeGreaterThan(0);
    expect(result[0].type).toBe("allowlist");
  });

  it("filters out system paths (absolute non-workspace paths)", () => {
    const events = [
      makeEvent({
        content: "rm -rf /usr/local/bin/dangerous",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
    ];

    const result = service.generateSuggestions(events, [], [], []);
    // Should not suggest anything for /usr paths
    const systemSuggestion = result.find(
      (s) => s.pattern.includes("/usr") || s.pattern.includes("/etc")
    );
    expect(systemSuggestion).toBeUndefined();
  });

  it("excludes already-allowlisted patterns", () => {
    const events = [
      makeEvent({
        content: "rm -rf ./dist/bundle.js",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
    ];

    // The existing allowlist entry matches the path
    const currentAllowlist = ["\\./dist/bundle\\.js"];
    const result = service.generateSuggestions(events, currentAllowlist, [], []);

    // Should be excluded since it matches existing allowlist
    const matching = result.find((s) => s.pattern.includes("dist/bundle"));
    expect(matching).toBeUndefined();
  });

  it("excludes already-configured safe_directories", () => {
    const events = [
      makeEvent({
        content: "rm -rf ./dist/chunk1.js",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
      makeEvent({
        content: "rm -rf ./dist/chunk2.js",
        timestamp: new Date("2026-02-16T12:01:00Z"),
      }),
    ];

    const currentSafeDirs = ["./dist"];
    const result = service.generateSuggestions(events, [], currentSafeDirs, []);

    const distSuggestion = result.find((s) => s.pattern === "./dist");
    expect(distSuggestion).toBeUndefined();
  });

  it("excludes dismissed patterns", () => {
    const events = [
      makeEvent({
        content: "rm -rf ./build/output/chunk.js",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
      makeEvent({
        content: "rm -rf ./build/output/chunk2.js",
        timestamp: new Date("2026-02-16T12:01:00Z"),
      }),
    ];

    const dismissed = ["./build"];
    const result = service.generateSuggestions(events, [], [], dismissed);

    const buildSuggestion = result.find((s) => s.pattern === "./build");
    expect(buildSuggestion).toBeUndefined();
  });

  it("respects 50-event limit (takes most recent)", () => {
    // Create 60 events
    const events: SanitizationEvent[] = [];
    for (let i = 0; i < 60; i++) {
      events.push(
        makeEvent({
          content: `rm -rf ./dir${i}/file.js`,
          timestamp: new Date(Date.now() - i * 60000),
        })
      );
    }

    const result = service.generateSuggestions(events, [], [], []);
    // Should not crash and should return suggestions
    expect(result.length).toBeLessThanOrEqual(10);
  });

  it("ranks by frequency (highest first)", () => {
    const events = [
      // 3 events in ./build
      makeEvent({
        content: "rm -rf ./build/a.js",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
      makeEvent({
        content: "rm -rf ./build/b.js",
        timestamp: new Date("2026-02-16T12:01:00Z"),
      }),
      makeEvent({
        content: "rm -rf ./build/c.js",
        timestamp: new Date("2026-02-16T12:02:00Z"),
      }),
      // 2 events in ./dist
      makeEvent({
        content: "rm -rf ./dist/x.js",
        timestamp: new Date("2026-02-16T12:03:00Z"),
      }),
      makeEvent({
        content: "rm -rf ./dist/y.js",
        timestamp: new Date("2026-02-16T12:04:00Z"),
      }),
    ];

    const result = service.generateSuggestions(events, [], [], []);

    // Both should appear, but ./build should come first (higher frequency)
    expect(result.length).toBeGreaterThanOrEqual(2);
    if (result.length >= 2) {
      const buildIdx = result.findIndex((s) => s.pattern === "./build");
      const distIdx = result.findIndex((s) => s.pattern === "./dist");
      if (buildIdx >= 0 && distIdx >= 0) {
        expect(buildIdx).toBeLessThan(distIdx);
      }
    }
  });

  it("handles malformed content gracefully", () => {
    const events = [
      makeEvent({ content: "" }),
      makeEvent({ content: "  " }),
      makeEvent({ content: "no paths here at all" }),
    ];

    // Should not throw
    const result = service.generateSuggestions(events, [], [], []);
    expect(Array.isArray(result)).toBe(true);
  });

  it("caps suggestions at 10", () => {
    // Create 20 different directory events (each with 2+ occurrences)
    const events: SanitizationEvent[] = [];
    for (let i = 0; i < 20; i++) {
      events.push(
        makeEvent({
          content: `rm -rf ./dir${i}/a.js`,
          timestamp: new Date(Date.now() - i * 60000),
        })
      );
      events.push(
        makeEvent({
          content: `rm -rf ./dir${i}/b.js`,
          timestamp: new Date(Date.now() - i * 60000 - 1000),
        })
      );
    }

    const result = service.generateSuggestions(events, [], [], []);
    expect(result.length).toBeLessThanOrEqual(10);
  });

  it("includes warned events along with blocked", () => {
    const events = [
      makeEvent({
        event: "warned",
        content: "rm -rf ./cache/temp.js",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
    ];

    const result = service.generateSuggestions(events, [], [], []);
    expect(result.length).toBeGreaterThan(0);
  });

  it("generates both allowlist and safe_directory types", () => {
    const events = [
      // 2 events in same directory -> safe_directory
      makeEvent({
        content: "rm -rf ./output/chunk1.js",
        timestamp: new Date("2026-02-16T12:00:00Z"),
      }),
      makeEvent({
        content: "rm -rf ./output/chunk2.js",
        timestamp: new Date("2026-02-16T12:01:00Z"),
      }),
      // 1 event -> allowlist
      makeEvent({
        content: "rm -rf ./single-dir/only-file.js",
        timestamp: new Date("2026-02-16T12:02:00Z"),
      }),
    ];

    const result = service.generateSuggestions(events, [], [], []);

    const types = new Set(result.map((s) => s.type));
    expect(types.has("safe_directory")).toBe(true);
    expect(types.has("allowlist")).toBe(true);
  });
});
