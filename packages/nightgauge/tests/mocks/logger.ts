/**
 * A `Logger` double with every public method spied.
 *
 * Three test files each built `{info, warn, error, debug}` and passed it where
 * a `Logger` is required. `Logger` also has `show`, `clear`, `getChannel` and
 * `dispose`, so a service that logs to the channel or disposes its logger
 * would have thrown in exactly the tests meant to cover it (#499).
 *
 * The cast is deliberate and lives here rather than at each call site.
 * `Logger` is a CLASS with private fields (`channel`, `ownsChannel`,
 * `prefix`, `formatMessage`), and TypeScript treats private members
 * nominally — no object literal can ever structurally satisfy it, however
 * complete. Casting once, from a double that covers the whole public surface,
 * is honest; casting sixteen times from a quarter of it was not.
 */
import { vi } from "vitest";
import type { Logger } from "../../src/utils/logger";

type Spied<K extends keyof Logger> = Logger[K] extends (...a: infer A) => infer R
  ? ReturnType<typeof vi.fn<(...a: A) => R>>
  : never;

export type MockLogger = Logger & {
  debug: Spied<"debug">;
  info: Spied<"info">;
  warn: Spied<"warn">;
  error: Spied<"error">;
  show: Spied<"show">;
  clear: Spied<"clear">;
  getChannel: Spied<"getChannel">;
  dispose: Spied<"dispose">;
};

export function makeMockLogger(): MockLogger {
  return {
    debug: vi.fn(),
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    show: vi.fn(),
    clear: vi.fn(),
    getChannel: vi.fn(),
    dispose: vi.fn(),
  } as unknown as MockLogger;
}
