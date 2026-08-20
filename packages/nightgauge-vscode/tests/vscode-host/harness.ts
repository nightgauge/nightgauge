/**
 * A minimal test harness that runs *inside* the VSCode extension host.
 *
 * Why not Mocha (which `@vscode/test-cli` would bring)? Two reasons, in order
 * of weight:
 *
 *  1. Supply chain. `@vscode/test-cli` depends on `mocha`, which currently
 *     pulls `serialize-javascript` (GHSA high, RCE via `RegExp.flags`) and
 *     `diff` (GHSA moderate, ReDoS). `scripts/npm-audit-check.js` fails the
 *     `security` job on any unallowed high, so adopting the conventional
 *     harness would have meant opening an audit exception on day one.
 *     `@vscode/test-electron` on its own audits clean.
 *  2. This tier must fail on things Mocha has no concept of — an unhandled
 *     promise rejection anywhere in the extension host process, an error
 *     written to any output channel during startup — and it must refuse to
 *     report success when it executed zero cases. A small runner expresses
 *     that directly instead of bolting it onto someone else's reporter.
 *
 * The API is deliberately Mocha-shaped (`suite` / `test`) so these files read
 * like every other test in the repository.
 */

export type TestFn = () => void | Promise<void>;

export interface CaseResult {
  suite: string;
  name: string;
  status: "pass" | "fail" | "skip";
  durationMs: number;
  detail?: string;
}

interface RegisteredCase {
  suite: string;
  name: string;
  fn: TestFn;
  skipReason?: string;
}

const registry: RegisteredCase[] = [];
let currentSuite: string | undefined;

/** Declare a group of cases. The body runs immediately, at registration time. */
export function suite(name: string, body: () => void): void {
  if (currentSuite !== undefined) {
    throw new Error(`suite("${name}") nested inside suite("${currentSuite}") — not supported`);
  }
  currentSuite = name;
  try {
    body();
  } finally {
    currentSuite = undefined;
  }
}

/** Declare a case. Must be called from inside a `suite()` body. */
export function test(name: string, fn: TestFn): void {
  if (currentSuite === undefined) {
    throw new Error(`test("${name}") declared outside a suite()`);
  }
  registry.push({ suite: currentSuite, name, fn });
}

/**
 * Declare a case that is knowingly not exercised, with a mandatory reason.
 *
 * A skip here is a claim about the product, not a convenience. It is printed
 * in the summary and, per this tier's contract, must name the bug or the
 * missing affordance that makes the surface unreachable.
 */
export function skippedTest(name: string, reason: string, _fn: TestFn): void {
  if (currentSuite === undefined) {
    throw new Error(`skippedTest("${name}") declared outside a suite()`);
  }
  if (!reason.trim()) {
    throw new Error(`skippedTest("${name}") requires a non-empty reason`);
  }
  registry.push({ suite: currentSuite, name, fn: () => undefined, skipReason: reason });
}

export function registeredCaseCount(): number {
  return registry.length;
}

export function registeredSuiteNames(): string[] {
  return [...new Set(registry.map((entry) => entry.suite))];
}

/**
 * Execute everything registered, in declaration order, serially.
 *
 * Serial by design: these cases open real editor UI in one shared window, so
 * running them concurrently would make "did opening X create a panel"
 * unanswerable.
 */
export async function runRegisteredCases(
  log: (line: string) => void,
  perCaseTimeoutMs = 30_000
): Promise<CaseResult[]> {
  const results: CaseResult[] = [];
  let lastSuite: string | undefined;

  for (const entry of registry) {
    if (entry.suite !== lastSuite) {
      log(`\n  ${entry.suite}`);
      lastSuite = entry.suite;
    }

    if (entry.skipReason) {
      results.push({
        suite: entry.suite,
        name: entry.name,
        status: "skip",
        durationMs: 0,
        detail: entry.skipReason,
      });
      log(`    - ${entry.name}  (SKIP: ${entry.skipReason})`);
      continue;
    }

    const startedAt = Date.now();
    try {
      await withTimeout(entry.fn(), perCaseTimeoutMs, `${entry.suite} > ${entry.name}`);
      const durationMs = Date.now() - startedAt;
      results.push({ suite: entry.suite, name: entry.name, status: "pass", durationMs });
      log(`    PASS ${entry.name} (${durationMs}ms)`);
    } catch (error) {
      const durationMs = Date.now() - startedAt;
      const detail = error instanceof Error ? (error.stack ?? error.message) : String(error);
      results.push({ suite: entry.suite, name: entry.name, status: "fail", durationMs, detail });
      log(`    FAIL ${entry.name} (${durationMs}ms)`);
    }
  }

  return results;
}

async function withTimeout<T>(work: T | Promise<T>, ms: number, label: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      Promise.resolve(work),
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(`timed out after ${ms}ms: ${label}`)), ms);
      }),
    ]);
  } finally {
    if (timer) {
      clearTimeout(timer);
    }
  }
}
