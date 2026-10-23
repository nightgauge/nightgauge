/**
 * Tests for the gate-driven outcome classifier (Issue #3267).
 *
 * Exercises HeadlessOrchestrator.classifyOutcomeFromGateResults — the pure,
 * static helper that the in-process classifier and the backfill script
 * (`scripts/backfill-skill-no-op-outcomes.ts`) both call. Avoids spinning
 * up the full HeadlessOrchestrator (which has heavy vscode dependencies).
 */

import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [],
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(),
      onDidCreate: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: {
    executeCommand: vi.fn(),
    registerCommand: vi.fn(),
  },
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  Disposable: { from: vi.fn() },
  Uri: { file: (p: string) => ({ fsPath: p }) },
  ExtensionMode: { Production: 1, Development: 2, Test: 3 },
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";

describe("HeadlessOrchestrator.classifyOutcomeFromGateResults (Issue #3267)", () => {
  it("returns false on empty input", () => {
    expect(HeadlessOrchestrator.classifyOutcomeFromGateResults([])).toBe(false);
  });

  it("returns false when every gate passed (kind=ok)", () => {
    expect(
      HeadlessOrchestrator.classifyOutcomeFromGateResults([
        { passed: true, kind: "ok" },
        { passed: true, kind: "ok" },
      ])
    ).toBe(false);
  });

  it("returns true when ANY gate has kind=no_op", () => {
    expect(
      HeadlessOrchestrator.classifyOutcomeFromGateResults([
        { passed: true, kind: "ok" },
        { passed: false, kind: "no_op" },
      ])
    ).toBe(true);
  });

  it("returns true when issue-pickup gate is no_op", () => {
    expect(
      HeadlessOrchestrator.classifyOutcomeFromGateResults([{ passed: false, kind: "no_op" }])
    ).toBe(true);
  });

  it("returns true when pr-merge gate is no_op", () => {
    expect(
      HeadlessOrchestrator.classifyOutcomeFromGateResults([
        { passed: true, kind: "ok" },
        { passed: true, kind: "ok" },
        { passed: true, kind: "ok" },
        { passed: true, kind: "ok" },
        { passed: true, kind: "ok" },
        { passed: false, kind: "no_op" },
      ])
    ).toBe(true);
  });

  it("returns false when gate failed with kind=fail (not no_op)", () => {
    expect(
      HeadlessOrchestrator.classifyOutcomeFromGateResults([{ passed: false, kind: "fail" }])
    ).toBe(false);
  });

  it("returns false on legacy records without kind discriminator", () => {
    // Pre-#3267 records have no `kind`. The classifier intentionally does
    // NOT regex-match the reason string, so legacy records never produce
    // skill-no-op without an explicit backfill.
    expect(
      HeadlessOrchestrator.classifyOutcomeFromGateResults([
        { passed: false } as { passed: boolean; kind?: string },
      ])
    ).toBe(false);
  });

  it("ignores null/undefined entries defensively", () => {
    expect(
      HeadlessOrchestrator.classifyOutcomeFromGateResults([
        null as unknown as { passed: boolean; kind?: string },
        { passed: false, kind: "no_op" },
      ])
    ).toBe(true);
  });

  it("ignores non-array input", () => {
    expect(
      HeadlessOrchestrator.classifyOutcomeFromGateResults(
        "not an array" as unknown as Array<{ passed: boolean; kind?: string }>
      )
    ).toBe(false);
  });
});
