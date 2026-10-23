/**
 * `Nightgauge: Run Doctor` (ADR-025): opens the Doctor panel, which runs
 * `doctor.run` over IPC and renders every finding.
 *
 * The panel probes the adapters the pipeline would dispatch to: every
 * executable stage's resolved adapter plus the global default, the same set
 * each stage resolves at run time.
 */

import * as vscode from "vscode";
import { PIPELINE_STAGE_ORDER, PHASE_REGISTRY } from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";
import { resolveStageAdapter } from "../utils/resolvers/adapterResolver";
import { getExecutionAdapter } from "../utils/resolvers/modelResolver";
import { toNightgaugeAdapter } from "../services/HeadlessOrchestrator";
import { DoctorPanel } from "../views/doctor/DoctorPanel";
import type { DoctorService } from "../views/doctor/DoctorService";
import { RUN_DOCTOR_COMMAND } from "../views/doctor/DoctorStatusBarItem";

/**
 * The distinct adapters the pipeline resolves to, in stage order, then the
 * global default. Stages come from `PHASE_REGISTRY` (those that run a skill),
 * ordered by `PIPELINE_STAGE_ORDER`.
 */
export function resolveDoctorAdapters(
  workspaceRoot: string | undefined,
  env: NodeJS.ProcessEnv = process.env
): string[] {
  const seen = new Set<string>();
  for (const stage of PIPELINE_STAGE_ORDER.filter((s) => s in PHASE_REGISTRY)) {
    seen.add(toNightgaugeAdapter(resolveStageAdapter(stage, workspaceRoot, env).adapter, env));
  }
  seen.add(toNightgaugeAdapter(getExecutionAdapter(workspaceRoot), env));
  return [...seen];
}

export function registerRunDoctorCommand(
  service: DoctorService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(RUN_DOCTOR_COMMAND, () => {
    logger.info("Doctor panel opened");
    DoctorPanel.show(service);
  });
}
