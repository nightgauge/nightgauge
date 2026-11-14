import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  LAUNCH_ATTEMPTS,
  classifyWindowOutcome,
  copyWindowLogs,
  diagnosticsDir,
  loadAverageLine,
  shouldRemoveScratch,
  shouldRetryLaunch,
} from "./windowDiagnostics";

const temps: string[] = [];
function tmp(): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "window-diagnostics-test-"));
  temps.push(dir);
  return dir;
}
afterEach(() => {
  for (const dir of temps.splice(0)) fs.rmSync(dir, { recursive: true, force: true });
});

describe("loadAverageLine", () => {
  it("prints the 1/5/15 minute load and the core count", () => {
    expect(
      loadAverageLine(
        () => [12.345, 8, 4.5],
        () => 10
      )
    ).toBe("load avg 12.35 8.00 4.50 (1/5/15 min) on 10 cores");
  });

  it("reads the real machine by default", () => {
    expect(loadAverageLine()).toMatch(/^load avg \d+\.\d\d \d+\.\d\d \d+\.\d\d .* on \d+ cores$/);
  });
});

describe("classifyWindowOutcome", () => {
  it("is a launch fault whenever the transcript is missing, error or not", () => {
    expect(
      classifyWindowOutcome({ transcriptExists: false, runTestsError: new Error("Canceled") })
    ).toBe("launch-fault");
    // A window can die before the module loads and still exit 0.
    expect(classifyWindowOutcome({ transcriptExists: false, runTestsError: undefined })).toBe(
      "launch-fault"
    );
  });

  it("is a failed test when the module ran and runTests rejected", () => {
    expect(
      classifyWindowOutcome({ transcriptExists: true, runTestsError: new Error("Failed") })
    ).toBe("test-failed");
  });

  it("passes only with a transcript and no rejection", () => {
    expect(classifyWindowOutcome({ transcriptExists: true, runTestsError: undefined })).toBe(
      "passed"
    );
  });
});

describe("shouldRetryLaunch", () => {
  it("retries a launch fault once, then stops", () => {
    expect(LAUNCH_ATTEMPTS).toBe(2);
    expect(shouldRetryLaunch("launch-fault", 1)).toBe(true);
    expect(shouldRetryLaunch("launch-fault", LAUNCH_ATTEMPTS)).toBe(false);
  });

  it("never retries a failed test or a pass, on any attempt", () => {
    for (let attempt = 1; attempt <= LAUNCH_ATTEMPTS; attempt++) {
      expect(shouldRetryLaunch("test-failed", attempt)).toBe(false);
      expect(shouldRetryLaunch("passed", attempt)).toBe(false);
    }
  });
});

describe("diagnosticsDir", () => {
  it("lands beside the ci-local step log when the gate exports it", () => {
    expect(
      diagnosticsDir(
        { CI_LOCAL_STEP_LOG: "/repo/.ci-local-logs/VSCode-host-smoke-tests.log" },
        "/tmp/scratch"
      )
    ).toBe("/repo/.ci-local-logs/VSCode-host-smoke-tests.vscode-logs");
  });

  it("falls back to scratch outside the gate", () => {
    expect(diagnosticsDir({}, "/tmp/scratch")).toBe(path.join("/tmp/scratch", "vscode-logs"));
    expect(diagnosticsDir({ CI_LOCAL_STEP_LOG: "" }, "/tmp/scratch")).toBe(
      path.join("/tmp/scratch", "vscode-logs")
    );
  });
});

describe("copyWindowLogs", () => {
  it("copies the user-data logs tree under the window label", () => {
    const userData = tmp();
    const nested = path.join(userData, "logs", "20261113T170000", "window1", "exthost");
    fs.mkdirSync(nested, { recursive: true });
    fs.writeFileSync(path.join(nested, "exthost.log"), "Canceled\n");
    const dest = tmp();

    const copied = copyWindowLogs(userData, "demo-attempt-1", dest);

    expect(copied).toBe(path.join(dest, "demo-attempt-1"));
    expect(
      fs.readFileSync(
        path.join(dest, "demo-attempt-1", "20261113T170000", "window1", "exthost", "exthost.log"),
        "utf8"
      )
    ).toBe("Canceled\n");
  });

  it("is a no-op when VS Code wrote no logs", () => {
    const dest = tmp();
    expect(copyWindowLogs(tmp(), "main-attempt-1", dest)).toBeUndefined();
    expect(fs.readdirSync(dest)).toEqual([]);
  });
});

describe("shouldRemoveScratch", () => {
  it("removes scratch after a pass and keeps it after any failure", () => {
    expect(shouldRemoveScratch(0)).toBe(true);
    expect(shouldRemoveScratch(1)).toBe(false);
  });
});
