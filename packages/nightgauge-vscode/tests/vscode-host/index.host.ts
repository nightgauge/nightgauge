/**
 * Entry point loaded by VSCode as `--extensionTestsPath`.
 *
 * `*.host.ts` is this tier's naming convention and marks a file as the
 * bundle root for a VSCode-host run — the same role `*.test.ts` plays for
 * vitest and `*.playwright.ts` for Playwright.
 * `scripts/check-test-runner-coverage.sh` enforces that a `*.host.ts` file
 * lives where the tier's esbuild step can find it, and that every
 * `*.suite.ts` beside it is actually imported below. A suite file nobody
 * imports is bundled by nothing and runs under no runner — the exact orphan
 * shape #732 and #744 were about, reproduced one tier later.
 *
 * The observation layer is installed at module scope, before `run()`, so the
 * patches are in place no matter how early the host decides to activate the
 * extension.
 */

import * as fs from "node:fs";
import { installObservers, allOutputLines, processFaults } from "./observe.js";
import { runRegisteredCases, registeredCaseCount, registeredSuiteNames } from "./harness.js";
import { unexpectedFaults } from "./known-issues.js";

// Importing a suite registers its cases; order here is execution order.
import "./suites/activation.suite.js";
import "./suites/commands.suite.js";
import "./suites/treeviews.suite.js";
import "./suites/webviews.suite.js";

// Module scope, not inside run(): the suite bodies above only *register*
// cases, so nothing has touched the VSCode API yet, and the patches are in
// place before anything can activate the extension.
installObservers();

/** Suites that must each contribute at least one case, or the run is a lie. */
const REQUIRED_SUITES = [
  "activation",
  "commands",
  "tree views (empty workspace)",
  "tree views (populated workspace)",
  "webviews",
];

export async function run(): Promise<void> {
  const lines: string[] = [];
  const log = (line: string): void => {
    lines.push(line);
    // This IS the test reporter; the extension host forwards stdout to the launcher.
    console.log(line);
  };

  log("VSCode host smoke tier");

  const declaredSuites = registeredSuiteNames();
  const missingSuites = REQUIRED_SUITES.filter((name) => !declaredSuites.includes(name));
  if (missingSuites.length > 0) {
    throw new Error(
      `Suite(s) registered no cases: ${missingSuites.join(", ")}. A tier that runs fewer ` +
        `suites than it claims reads as green while covering nothing.`
    );
  }
  if (registeredCaseCount() === 0) {
    throw new Error("Zero cases registered — refusing to report success.");
  }

  const results = await runRegisteredCases(log);

  const passed = results.filter((entry) => entry.status === "pass");
  const failed = results.filter((entry) => entry.status === "fail");
  const skipped = results.filter((entry) => entry.status === "skip");

  log("");
  log(`  ${passed.length} passed, ${failed.length} failed, ${skipped.length} skipped`);

  if (skipped.length > 0) {
    log("");
    log("  Skipped (each names the product gap that makes the surface unreachable):");
    for (const entry of skipped) {
      log(`    - ${entry.suite} > ${entry.name}: ${entry.detail}`);
    }
  }

  // Faults are re-checked at the end, not only in the activation suite: a
  // rejection thrown by a panel's message handler or a provider's background
  // refresh lands here rather than inside the case that triggered it.
  const faults = unexpectedFaults(processFaults());
  const problems: string[] = [];

  for (const entry of failed) {
    problems.push(`FAILED  ${entry.suite} > ${entry.name}\n${indent(entry.detail ?? "")}`);
  }
  for (const fault of faults) {
    problems.push(`${fault.kind.toUpperCase()}\n${indent(fault.detail)}`);
  }

  writeTranscript(lines);

  if (problems.length > 0) {
    log("");
    for (const problem of problems) {
      log(problem);
    }
    throw new Error(
      `VSCode host smoke tier: ${failed.length} failing case(s), ${faults.length} process fault(s).`
    );
  }
}

function indent(text: string): string {
  return text
    .split("\n")
    .map((line) => `    ${line}`)
    .join("\n");
}

/**
 * Persist the transcript so the launcher can prove the in-host module
 * actually executed. A VSCode window that dies before loading this module
 * can still exit 0; the launcher treats a missing transcript as a failure
 * rather than a pass.
 */
function writeTranscript(lines: string[]): void {
  const target = process.env.NIGHTGAUGE_HOST_TRANSCRIPT;
  if (!target) {
    return;
  }
  const outputLines = allOutputLines().map((entry) => `[${entry.channel}] ${entry.line}`);
  fs.writeFileSync(
    target,
    `${lines.join("\n")}\n\n--- output channels ---\n${outputLines.join("\n")}\n`,
    "utf8"
  );
}
