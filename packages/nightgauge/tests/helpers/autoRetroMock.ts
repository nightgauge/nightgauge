/**
 * Shared AutoRetroService mock (#2893).
 *
 * HeadlessOrchestrator starts `AutoRetroService.runAfterFailure` without
 * awaiting it. Run for real in a test, its retro write lands in the pinned
 * clone layout after the test ends and races the teardown in tests/setup.ts
 * (ENOTEMPTY). tests/setup.ts applies this mock to every test file; suites
 * that exercise the real service opt back in with vi.unmock.
 */
import { vi } from "vitest";
import type * as AutoRetroModule from "../../src/services/AutoRetroService";

type Module = typeof AutoRetroModule;

export async function mockAutoRetroModule(importOriginal: () => Promise<unknown>): Promise<Module> {
  const actual = (await importOriginal()) as Module;
  // A subclass keeps every other static (terminalKindCoverage, …) real.
  class MockedAutoRetroService extends actual.AutoRetroService {}
  MockedAutoRetroService.runAfterFailure = vi.fn(async () => null);
  return { ...actual, AutoRetroService: MockedAutoRetroService };
}
