/**
 * One-time migration that consolidates the legacy `autonomous.max_concurrent`
 * key into the unified `pipeline.max_concurrent` source of truth.
 *
 * Triggered on extension activation. Runs at most once per workspace per
 * resolved decision (the chosen value is recorded under
 * `globalState["nightgauge.maxConcurrentMigrationDoneAt"]` so the user
 * isn't re-prompted on every reload). Safe to invoke unconditionally — it
 * fast-paths out when both keys agree, when only one is set, or when the
 * user has previously been migrated.
 *
 * @see Issue #3195
 */
import * as vscode from "vscode";
import { NightgaugeYamlService } from "../views/settings/NightgaugeYamlService";
import type { Logger } from "./logger";

const STATE_KEY = "nightgauge.maxConcurrentMigrationCompleted";

/**
 * Read both `pipeline.max_concurrent` and `autonomous.max_concurrent` from
 * the merged config tier (project + local) and prompt the user to consolidate
 * when they disagree. The chosen value is written to `pipeline.max_concurrent`
 * in the LOCAL tier — an activation-time migration never rewrites the
 * committed `.nightgauge/config.yaml` (#1516) — and the legacy key is removed
 * from the local tier.
 *
 * Returns `true` if a migration was performed, `false` otherwise.
 */
export async function runMaxConcurrentMigration(
  context: vscode.ExtensionContext,
  workspaceRoot: string,
  logger: Logger
): Promise<boolean> {
  // Skip if the user has already been migrated for this workspace.
  if (context.globalState.get<boolean>(STATE_KEY) === true) {
    return false;
  }

  const yaml = new NightgaugeYamlService(workspaceRoot);
  try {
    const projectRead = await yaml.read();
    const localRead = await yaml.readLocal();

    const projectCfg = (projectRead.success ? projectRead.config : null) ?? null;
    const localCfg = (localRead.success ? localRead.config : null) ?? null;

    // Resolve effective values across both tiers — local wins on conflict
    // (mirrors the merge precedence used elsewhere).
    const effectivePipeline =
      pickMaxConcurrent(localCfg?.pipeline?.max_concurrent) ??
      pickMaxConcurrent(projectCfg?.pipeline?.max_concurrent);
    const effectiveAutonomous =
      pickMaxConcurrent(localCfg?.autonomous?.max_concurrent) ??
      pickMaxConcurrent(projectCfg?.autonomous?.max_concurrent);

    // Nothing to migrate — autonomous key isn't set anywhere.
    if (effectiveAutonomous === undefined) {
      await context.globalState.update(STATE_KEY, true);
      return false;
    }

    // Both set and agree — silently drop the legacy key.
    if (effectivePipeline !== undefined && effectivePipeline === effectiveAutonomous) {
      await dropAutonomousMaxConcurrent(yaml, projectCfg, localCfg);
      logger.info("[max-concurrent-migration] removed redundant autonomous.max_concurrent", {
        value: effectiveAutonomous,
      });
      await context.globalState.update(STATE_KEY, true);
      return true;
    }

    // Only autonomous is set — promote it without prompting (no semantic change).
    if (effectivePipeline === undefined) {
      await consolidateInto(yaml, projectCfg, localCfg, effectiveAutonomous);
      logger.info(
        "[max-concurrent-migration] promoted autonomous.max_concurrent → pipeline.max_concurrent",
        {
          value: effectiveAutonomous,
        }
      );
      vscode.window.showInformationMessage(
        `Nightgauge: moved \`autonomous.max_concurrent: ${effectiveAutonomous}\` to \`pipeline.max_concurrent\` (legacy key deprecated).`
      );
      await context.globalState.update(STATE_KEY, true);
      return true;
    }

    // Both set and disagree — prompt the user.
    const choice = await vscode.window.showWarningMessage(
      `Nightgauge: \`pipeline.max_concurrent\` (${effectivePipeline}) and \`autonomous.max_concurrent\` (${effectiveAutonomous}) disagree. ` +
        `These are now unified — pipeline.max_concurrent is the source of truth for both drag-to-pipeline and autonomous mode. Which should we keep?`,
      { modal: true },
      `Use ${effectivePipeline} (pipeline)`,
      `Use ${effectiveAutonomous} (autonomous)`,
      "Decide later"
    );

    if (!choice || choice === "Decide later") {
      // Don't mark as done — re-prompt on next activation so the user can't
      // forget. The deprecation log line still fires from
      // getConcurrentPipelineConfig() so the user sees a steady reminder.
      return false;
    }

    const keepValue =
      choice === `Use ${effectivePipeline} (pipeline)` ? effectivePipeline : effectiveAutonomous;
    await consolidateInto(yaml, projectCfg, localCfg, keepValue);
    logger.info("[max-concurrent-migration] resolved divergence", {
      pipeline: effectivePipeline,
      autonomous: effectiveAutonomous,
      kept: keepValue,
    });
    await context.globalState.update(STATE_KEY, true);
    return true;
  } catch (err) {
    // Migration is best-effort — never block activation.
    logger.warn("[max-concurrent-migration] failed", {
      error: err instanceof Error ? err.message : String(err),
    });
    return false;
  } finally {
    yaml.dispose();
  }
}

function pickMaxConcurrent(raw: unknown): number | undefined {
  if (typeof raw !== "number" || !Number.isFinite(raw)) return undefined;
  const n = Math.round(raw);
  if (n < 1 || n > 10) return undefined;
  return n;
}

/**
 * Write the resolved value to `pipeline.max_concurrent` in the LOCAL tier and
 * drop the legacy key from the local tier.
 *
 * This migration runs on extension activation, which is not an explicit user
 * action naming the committed team file — so it must not write
 * `.nightgauge/config.yaml`, however small the diff (#1516). When the legacy
 * key lives only in the team file it stays there; the local tier's
 * `pipeline.max_concurrent` outranks it in the merge chain, so behaviour is
 * already correct, and removing the deprecated key from a committed file is a
 * reviewed PR, not a startup side effect.
 */
async function consolidateInto(
  yaml: NightgaugeYamlService,
  _projectCfg: Record<string, unknown> | null,
  localCfg: Record<string, unknown> | null,
  value: number
): Promise<void> {
  await writeTier(yaml, localCfg, value);
}

/**
 * Remove `autonomous.max_concurrent` from the local tier when it holds it,
 * leaving pipeline.max_concurrent untouched. The committed team file is never
 * rewritten from activation (#1516).
 */
async function dropAutonomousMaxConcurrent(
  yaml: NightgaugeYamlService,
  _projectCfg: Record<string, unknown> | null,
  localCfg: Record<string, unknown> | null
): Promise<void> {
  const localAutonomous = pickMaxConcurrent(
    (localCfg?.autonomous as { max_concurrent?: unknown } | undefined)?.max_concurrent
  );
  if (localAutonomous === undefined) {
    // The redundant key lives only in the committed team file. Leave it —
    // an activation-time migration never edits `.nightgauge/config.yaml`
    // (#1516).
    return;
  }
  await writeTier(yaml, localCfg, /* keepValue */ undefined);
}

async function writeTier(
  yaml: NightgaugeYamlService,
  cfg: Record<string, unknown> | null,
  keepValue: number | undefined
): Promise<void> {
  const next: Record<string, unknown> = { ...(cfg ?? {}) };

  // Update pipeline.max_concurrent (only when we have a value to write).
  if (keepValue !== undefined) {
    const pipeline = { ...((next.pipeline as Record<string, unknown> | undefined) ?? {}) };
    pipeline.max_concurrent = keepValue;
    next.pipeline = pipeline;
  }

  // Always strip the deprecated key from this tier.
  if (next.autonomous && typeof next.autonomous === "object") {
    const autonomous = { ...(next.autonomous as Record<string, unknown>) };
    delete autonomous.max_concurrent;
    if (Object.keys(autonomous).length === 0) {
      delete next.autonomous;
    } else {
      next.autonomous = autonomous;
    }
  }

  await yaml.writeLocal(next as Parameters<typeof yaml.writeLocal>[0]);
}
