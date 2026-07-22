/**
 * Cloud master-switch gating (feat/free-local-cloud-off).
 *
 * Product decision: the entire LOCAL product is free. Cloud / server-backed
 * features are not offered yet — hidden, not paywalled — behind the single
 * reversible switch `nightgauge.cloud.enabled` (default false), which
 * drives the `nightgauge.cloudEnabled` context key.
 *
 * These assertions pin:
 *  - the config default stays OFF (so the free-local path is the default),
 *  - every cloud/account command is hidden behind `nightgauge.cloudEnabled`
 *    in the command palette,
 *  - local commands are NOT hidden (the free product stays fully reachable),
 *  - telemetry stays opt-in (default false) — a guardrail so a future change
 *    cannot silently flip it on.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { MANIFEST_CONTRIBUTES } from "../../src/manifest";

interface MenuEntry {
  command: string;
  when?: string;
}

const CLOUD_CONTEXT_KEY = "nightgauge.cloudEnabled";

/** Cloud / account commands that require the hosted platform or an account. */
const CLOUD_COMMANDS = [
  "nightgauge.signIn",
  "nightgauge.signOut",
  "nightgauge.signInWithGitHub",
  "nightgauge.manageSubscription",
  "nightgauge.activateLicense",
  "nightgauge.startTrial",
  "nightgauge.openUpgradeUrl",
  "nightgauge.openManageSubscription",
  "nightgauge.openSubscriptionUrl",
  "nightgauge.showMachineBinding",
  "nightgauge.editTeamConfig",
];

/**
 * Local commands that must stay reachable with cloud off. These render local
 * workspace data (dashboards read `.nightgauge` / pipeline state) or drive
 * local pipeline execution — none require an account.
 */
const LOCAL_COMMANDS_THAT_MUST_STAY_VISIBLE = [
  "nightgauge.showDashboard",
  "nightgauge.rescrubDashboardHistory",
  "nightgauge.showBrownfieldDashboard",
  "nightgauge.openKnowledgeValueDashboard",
  "nightgauge.addEpicToPipeline",
  "nightgauge.setConcurrentSlots",
  "nightgauge.repo.setMaxConcurrent",
  "nightgauge.pickupIssue",
  "nightgauge.runStage",
  "nightgauge.showGettingStarted",
];

const commandPalette = (MANIFEST_CONTRIBUTES.menus.commandPalette ?? []) as MenuEntry[];

function paletteEntry(command: string): MenuEntry | undefined {
  return commandPalette.find((m) => m.command === command);
}

function readConfigProperty(key: string): Record<string, unknown> {
  const pkgPath = resolve(__dirname, "../../package.json");
  const pkg = JSON.parse(readFileSync(pkgPath, "utf-8"));
  const cfg = pkg.contributes.configuration;
  const blocks = Array.isArray(cfg) ? cfg : [cfg];
  for (const blk of blocks) {
    const prop = blk?.properties?.[key];
    if (prop) return prop;
  }
  throw new Error(`${key} not found in package.json contributions`);
}

describe("cloud master switch — config", () => {
  it("nightgauge.cloud.enabled exists and defaults to false", () => {
    const prop = readConfigProperty("nightgauge.cloud.enabled");
    expect(prop.type).toBe("boolean");
    expect(prop.default).toBe(false);
  });
});

describe("cloud master switch — command palette gating", () => {
  it("hides every cloud/account command behind nightgauge.cloudEnabled", () => {
    for (const command of CLOUD_COMMANDS) {
      const entry = paletteEntry(command);
      expect(entry, `expected commandPalette entry for ${command}`).toBeDefined();
      expect(entry?.when).toBe(CLOUD_CONTEXT_KEY);
    }
  });

  it("only gates on the cloudEnabled context key (consistent wiring)", () => {
    // Every commandPalette when-clause must reference exactly the context key
    // that bootstrap/services.ts sets from `nightgauge.cloud.enabled`.
    for (const entry of commandPalette) {
      expect(entry.when).toBe(CLOUD_CONTEXT_KEY);
    }
  });

  it("does NOT hide local commands (free product stays fully reachable)", () => {
    for (const command of LOCAL_COMMANDS_THAT_MUST_STAY_VISIBLE) {
      const entry = paletteEntry(command);
      // Either no palette entry at all (default = always visible) or, if one
      // exists, it must not be gated on the cloud switch.
      if (entry?.when) {
        expect(entry.when).not.toContain(CLOUD_CONTEXT_KEY);
      }
    }
  });

  it("gates exactly the known cloud commands (no accidental over/under-reach)", () => {
    const gated = commandPalette
      .filter((m) => m.when === CLOUD_CONTEXT_KEY)
      .map((m) => m.command)
      .sort();
    expect(gated).toEqual([...CLOUD_COMMANDS].sort());
  });
});

describe("telemetry stays opt-in (guardrail)", () => {
  it("nightgauge.telemetry.enabled defaults to false", () => {
    const prop = readConfigProperty("nightgauge.telemetry.enabled");
    expect(prop.type).toBe("boolean");
    expect(prop.default).toBe(false);
  });
});
