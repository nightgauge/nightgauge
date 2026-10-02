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

  it("gives the run-verb handler the window's pause UI", () => {
    expect(servicesSource).toMatch(
      /new RunVerbCommandHandler\([\s\S]*?createRemotePauseUi\(statusBar, \(runId\) => pipelineManager\.remoteRunState\(runId\)\)\s*\)/
    );
  });
});
