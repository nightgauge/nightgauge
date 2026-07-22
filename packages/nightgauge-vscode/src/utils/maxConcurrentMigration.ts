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
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import type { Logger } from "./logger";

const STATE_KEY = "nightgauge.maxConcurrentMigrationCompleted";

/**
 * Read both `pipeline.max_concurrent` and `autonomous.max_concurrent` from
 * the merged config tier (project + local) and prompt the user to consolidate
 * when they disagree. The chosen value is written to `pipeline.max_concurrent`
 * in the same tier the original `autonomous.max_concurrent` lived in, and the
 * legacy key is removed.
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

  const yaml = new IncrediYamlService(workspaceRoot);
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
 * Write the resolved value to `pipeline.max_concurrent` in whichever tier
 * the legacy key currently lives in (local-first, then project) and remove
 * the legacy key from that tier.
 */
async function consolidateInto(
  yaml: IncrediYamlService,
  projectCfg: Record<string, unknown> | null,
  localCfg: Record<string, unknown> | null,
  value: number
): Promise<void> {
  const localAutonomous = pickMaxConcurrent(
    (localCfg?.autonomous as { max_concurrent?: unknown } | undefined)?.max_concurrent
  );
  if (localAutonomous !== undefined) {
    await writeTier(yaml, localCfg, value, /* tier */ "local");
    return;
  }
  await writeTier(yaml, projectCfg, value, /* tier */ "project");
}

/**
 * Remove `autonomous.max_concurrent` from whichever tier holds it, leaving
 * pipeline.max_concurrent untouched.
 */
async function dropAutonomousMaxConcurrent(
  yaml: IncrediYamlService,
  projectCfg: Record<string, unknown> | null,
  localCfg: Record<string, unknown> | null
): Promise<void> {
  const localAutonomous = pickMaxConcurrent(
    (localCfg?.autonomous as { max_concurrent?: unknown } | undefined)?.max_concurrent
  );
  if (localAutonomous !== undefined) {
    await writeTier(yaml, localCfg, /* keepValue */ undefined, "local");
    return;
  }
  await writeTier(yaml, projectCfg, /* keepValue */ undefined, "project");
}

async function writeTier(
  yaml: IncrediYamlService,
  cfg: Record<string, unknown> | null,
  keepValue: number | undefined,
  tier: "project" | "local"
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

  if (tier === "local") {
    await yaml.writeLocal(next as Parameters<typeof yaml.writeLocal>[0]);
  } else {
    await yaml.write(next as Parameters<typeof yaml.write>[0], "project");
  }
}
