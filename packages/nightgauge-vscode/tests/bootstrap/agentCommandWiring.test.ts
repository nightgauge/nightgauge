/**
 * agentCommandWiring.test.ts
 *
 * Pins the bootstrap wiring the platform agent commands depend on (#2334,
 * #2335), the way adapterUsageServiceWired.test.ts pins its wiring: by
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
      /new AgentCommandStreamService\([^)]*agentCommandDispatcher\s*\)/
    );
  });

  // #2337: the throttle state is restored at activation, and the dispatcher
  // hands `throttle` commands to a handler that applies it.
  it("restores the workspace throttle and gives the dispatcher a throttle handler", () => {
    expect(servicesSource).toMatch(
      /new WorkspaceThrottleState\(\s*concurrentPipelineManager,\s*context\.globalState,\s*logger\s*\);\s*workspaceThrottleState\.restore\(\);/
    );
    expect(servicesSource).toMatch(
      /new AgentCommandDispatcher\([\s\S]*?new ThrottleCommandHandler\(workspaceThrottleState, ipcClient, logger\)/
    );
  });

  it("applies the registration's throttle after both registrations, and re-registers to refresh a kept one", () => {
    expect(extensionSource.match(/await applyRegistrationThrottle\(/g)?.length).toBe(2);
    expect(extensionSource).toMatch(
      /storedAgentId &&\s*registeredReposSig === currentReposSig &&\s*!services!\.workspaceThrottleState\?\.hasPersisted\(\)/
    );
  });

  it("gives the run-verb handler the window's pause UI", () => {
    expect(servicesSource).toMatch(
      /new RunVerbCommandHandler\([\s\S]*?createRemotePauseUi\(statusBar, \(runId\) => pipelineManager\.remoteRunState\(runId\)\)\s*\)/
    );
  });
});
