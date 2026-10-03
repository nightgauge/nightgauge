/**
 * auditConfigMachineTier.test.ts
 *
 * Audit emission follows `platform.enabled` (ADR-021). That switch is the
 * machine's opt-in to the hosted service, so only the machine-tier
 * config.yaml can set it: a repository's committed `.nightgauge/config.yaml`
 * must not turn audit emission on for whoever opens the repository. The Go
 * daemon strips the platform block from repository tiers (#1049);
 * ConfigBridge.getPlatform and getAuditConfig follow the same rule.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { getAuditConfig, machineTierPlatformEnabled } from "../../src/utils/nightgaugeConfig";

describe("audit emission reads platform.enabled from the machine tier only", () => {
  let configHome: string;
  let workspace: string;
  const saved = {
    home: process.env.NIGHTGAUGE_CONFIG_HOME,
    audit: process.env.NIGHTGAUGE_AUDIT_ENABLED,
  };

  beforeEach(() => {
    configHome = fs.mkdtempSync(path.join(os.tmpdir(), "ng-audit-home-"));
    workspace = fs.mkdtempSync(path.join(os.tmpdir(), "ng-audit-ws-"));
    fs.mkdirSync(path.join(workspace, ".nightgauge"));
    process.env.NIGHTGAUGE_CONFIG_HOME = configHome;
    delete process.env.NIGHTGAUGE_AUDIT_ENABLED;
  });

  afterEach(() => {
    if (saved.home === undefined) delete process.env.NIGHTGAUGE_CONFIG_HOME;
    else process.env.NIGHTGAUGE_CONFIG_HOME = saved.home;
    if (saved.audit !== undefined) process.env.NIGHTGAUGE_AUDIT_ENABLED = saved.audit;
    fs.rmSync(configHome, { recursive: true, force: true });
    fs.rmSync(workspace, { recursive: true, force: true });
  });

  function writeProject(text: string): void {
    fs.writeFileSync(path.join(workspace, ".nightgauge", "config.yaml"), text);
  }
  function writeMachine(text: string): void {
    fs.writeFileSync(path.join(configHome, "config.yaml"), text);
  }

  it("ignores platform.enabled: true in the repository's project config", () => {
    writeProject("platform:\n  enabled: true\naudit:\n  batch_size: 10\n");
    expect(machineTierPlatformEnabled()).toBe(false);
    expect(getAuditConfig(workspace).enabled).toBe(false);
  });

  it("follows platform.enabled: true in the machine-tier config", () => {
    writeProject("audit:\n  batch_size: 10\n");
    writeMachine("platform:\n  enabled: true # cloud on\n");
    expect(machineTierPlatformEnabled()).toBe(true);
    const cfg = getAuditConfig(workspace);
    expect(cfg.enabled).toBe(true);
    expect(cfg.batchSize).toBe(10);
  });

  // platform.telemetry.enabled is not platform.enabled: the nested key must
  // neither turn audit emission on nor, written first, hide the real switch.
  it.each([
    [
      "a nested telemetry.enabled: true alone",
      "platform:\n  telemetry:\n    enabled: true\n",
      false,
    ],
    [
      "a nested telemetry.enabled: true before enabled: false",
      "platform:\n  telemetry:\n    enabled: true\n  enabled: false\n",
      false,
    ],
    [
      "a nested telemetry.enabled: false before enabled: true",
      "platform:\n  telemetry:\n    enabled: false\n  enabled: true\n",
      true,
    ],
    ["enabled under another block", "audit:\n  enabled: true\nplatform:\n  api_url: x\n", false],
  ])("reads only platform.enabled itself: %s", (_name, machine, want) => {
    writeProject("audit:\n  batch_size: 10\n");
    writeMachine(machine);
    expect(machineTierPlatformEnabled()).toBe(want);
    expect(getAuditConfig(workspace).enabled).toBe(want);
  });

  it("stays off when the machine tier says false, whatever the project says", () => {
    writeProject("platform:\n  enabled: true\n");
    writeMachine("platform:\n  enabled: false\n");
    expect(getAuditConfig(workspace).enabled).toBe(false);
  });
});
