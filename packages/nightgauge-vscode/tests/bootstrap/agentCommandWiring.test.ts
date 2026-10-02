/**
 * agentCommandWiring.test.ts
 *
 * Pins the bootstrap wiring the platform agent commands depend on (#2334,
 * #2335, #2337), the way adapterUsageServiceWired.test.ts pins its wiring: by
 * reading bootstrap/services.ts, which is impractical to instantiate in a
 * unit test. The behaviour behind each line is tested where it lives:
 * AgentCommandDispatcher.test.ts (the relay subscription, against the
 * daemon's own event fixture), RunVerbCommandHandler.test.ts and
 * pauseUi.test.ts.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const servicesSource = readFileSync(
  path.resolve(__dirname, "../../src/bootstrap/services.ts"),
  "utf-8"
);
const extensionSource = readFileSync(path.resolve(__dirname, "../../src/extension.ts"), "utf-8");

describe("platform agent command wiring in bootstrap/services.ts", () => {
  it("subscribes the dispatcher to the commands the daemon relays, and disposes it", () => {
    expect(servicesSource).toContain(
      "context.subscriptions.push(subscribeToDaemonRelay(ipcClient, agentCommandDispatcher))"
    );
  });

  it("feeds the window's own command stream to the same dispatcher", () => {
    expect(servicesSource).toMatch(
      /new AgentCommandStreamService\([^)]*agentCommandDispatcher,\s*\(\) => void throttleSync\.refresh\(\)\s*\)/
    );
  });

  // #2337: the window reads its own workspace's throttle (by the manifest's
  // slug) into per-workspace state, and only while the platform session is
  // authenticated. The behaviour is tested in WorkspaceThrottleSync.test.ts.
  it("follows the workspace throttle through the session, the stream and throttle commands", () => {
    expect(servicesSource).toMatch(
      /new WorkspaceThrottleState\(concurrentPipelineManager, context\.workspaceState, logger\)/
    );
    expect(servicesSource).toMatch(
      /new AgentCommandDispatcher\([\s\S]*?new ThrottleCommandHandler\(throttleSync, ipcClient, logger\)/
    );
    // Subscribed before the session is restored, so the restored session's
    // event reaches it; nothing restores the throttle unconditionally.
    expect(servicesSource).toMatch(
      /workspaceThrottleSync\.followSession\(sessionManager\)\);\s*\}\s*void sessionManager\.restore\(\);/
    );
    expect(servicesSource).not.toMatch(/[Tt]hrottle\w*\.restore\(/);
  });

  // Registration no longer carries the throttle: a reload reuses the stored
  // registration exactly as before #2337, so a failed re-registration can
  // never leave the window without its heartbeat and command stream.
  it("leaves the registration path free of the workspace throttle", () => {
    expect(extensionSource).not.toMatch(/applyRegistrationThrottle|workspaceThrottleState/);
    expect(extensionSource).toContain(
      "if (storedAgentId && registeredReposSig === currentReposSig) {"
    );
    expect(extensionSource).toMatch(
      /onWorkspaceConfigReloaded\.event\(\(\) => \{\s*void services!\.workspaceThrottleSync\?\.refresh\(\);/
    );
  });

  it("gives the run-verb handler the window's pause UI", () => {
    expect(servicesSource).toMatch(
      /new RunVerbCommandHandler\([\s\S]*?createRemotePauseUi\(statusBar, \(runId\) => pipelineManager\.remoteRunState\(runId\)\),/
    );
  });

  // #2357: a verb no window holds is refused through the machine's ledger,
  // which follows this window's held runs from the start (its current set,
  // and the queue read once and on every reconnect) and is marked closed
  // when the window goes.
  it("gives the run-verb handler the machine's remote-run ledger, kept current", () => {
    expect(servicesSource).toMatch(
      /new RunVerbCommandHandler\([\s\S]*?remoteRunLedger \? \{ ledger: remoteRunLedger \} : undefined\s*\)/
    );
    expect(servicesSource).toContain(
      "pipelineManager.onHeldRemoteRunsChanged((runIds) => void remoteRunLedger.publish(runIds))"
    );
    expect(servicesSource).toContain(
      "void remoteRunLedger.publish(pipelineManager.heldRemoteRunIds());"
    );
    expect(servicesSource).toMatch(
      /ipcClient\.onDidChangeStatus\(\(connected\) => \{\s*if \(connected\) void pipelineManager\.syncQueuedRemoteRuns\(\);/
    );
    expect(servicesSource).toContain(
      "if (remoteRunLedger) context.subscriptions.push(remoteRunLedger);"
    );
  });

  // #2339: the paused-snapshot scan offers a platform run a reload ended to
  // the window's holds, which claim it in the ledger, so one window of the
  // clone holds it; the behaviour is tested in reloadInterruptedHolds.test.ts.
  it("hands the paused runs a reload ended to the window's exclusive holds", () => {
    expect(servicesSource).toContain("const interrupted = reloadInterruptedRemoteRun(runtime);");
    expect(servicesSource).toContain(
      "const reloadInterruptedHolds = new ReloadInterruptedRunHolds(remoteRunLedger);"
    );
    expect(servicesSource).toMatch(
      /await reloadInterruptedHolds\.found\(interrupted, async \(\) => \{\s*await fs\.unlink\(filePath\)/
    );
    expect(servicesSource).toContain(
      "if (interrupted) await reloadInterruptedHolds.resumed(interrupted.remoteRunId);"
    );
    expect(servicesSource).toContain("reloadInterruptedHolds.attach(concurrentPipelineManager);");
  });
});
