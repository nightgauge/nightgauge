/**
 * The stage environment's credential allowlist (ADR-023 Decision 7), for the
 * agent stages this extension spawns itself (#3406).
 *
 * The TS twin of `StageWithholdsCredentialEnv` in
 * internal/execution/adapters/stage_env.go. A stage keeps the host's toolchain,
 * proxy and TLS variables; a variable whose NAME marks it as a credential
 * reaches it only when it is a forge token or cloud platform variable every
 * stage gets, the adapter's harness declares it, or the operator lists it in
 * NIGHTGAUGE_STAGE_ENV_ALLOW. Every model service key the OpenCode catalog
 * names is withheld from a harness that does not declare it.
 *
 * The data is generated from the Go source (stageCredentialEnv.generated.ts),
 * and tests/utils/stageCredentialEnv.test.ts checks this rule against the Go
 * rule's recorded decisions (internal/execution/adapters/testdata/
 * stage_env_decisions.json), so the two cannot drift apart. The decision reads
 * only names, never values, so nothing it withholds can be logged.
 */

import {
  ADAPTER_CREDENTIAL_ENV,
  CREDENTIAL_ANY_SEGMENTS,
  CREDENTIAL_FINAL_SEGMENTS,
  STAGE_BASE_CREDENTIAL_ENV,
  STAGE_CATALOG_ENV,
  STAGE_ENV_ALLOW_VAR,
  STAGE_PLATFORM_ENV,
} from "./stageCredentialEnv.generated";

export { STAGE_ENV_ALLOW_VAR };

const BASE = new Set([...STAGE_BASE_CREDENTIAL_ENV, ...STAGE_PLATFORM_ENV]);
const CATALOG = new Set(STAGE_CATALOG_ENV);
const FINAL_SEGMENTS = new Set(CREDENTIAL_FINAL_SEGMENTS);
const ANY_SEGMENTS = new Set(CREDENTIAL_ANY_SEGMENTS);

/** Whether a variable's name marks it as a credential (Go: credentialEnvName). */
export function credentialEnvName(key: string): boolean {
  const segs = key.toUpperCase().split("_");
  if (FINAL_SEGMENTS.has(segs[segs.length - 1])) return true;
  return segs.some((s) => ANY_SEGMENTS.has(s));
}

/**
 * Whether an inherited variable named `key` must not reach a stage run by
 * `adapter`. `extraAllow` is the operator's NIGHTGAUGE_STAGE_ENV_ALLOW list.
 */
export function stageWithholdsCredentialEnv(
  adapter: string,
  key: string,
  extraAllow: readonly string[] = []
): boolean {
  if (BASE.has(key) || extraAllow.includes(key)) return false;
  const decl = Object.prototype.hasOwnProperty.call(ADAPTER_CREDENTIAL_ENV, adapter)
    ? ADAPTER_CREDENTIAL_ENV[adapter]
    : undefined;
  if (decl) {
    if (decl.names.includes(key)) return false;
    if (decl.prefixes.some((p) => key.startsWith(p))) return false;
  }
  if (CATALOG.has(key)) {
    // The OpenCode adapter decides these per dispatch
    // (reconcileOpenCodeSpawnEnv); every other adapter declared its own above.
    return !decl?.catalogOwn;
  }
  return credentialEnvName(key);
}

/** The operator's NIGHTGAUGE_STAGE_ENV_ALLOW list, trimmed, empty entries dropped. */
export function stageEnvAllow(hostEnv: NodeJS.ProcessEnv = process.env): string[] {
  return (hostEnv[STAGE_ENV_ALLOW_VAR] ?? "")
    .split(",")
    .map((n) => n.trim())
    .filter((n) => n !== "");
}

/**
 * The names in `env` a stage run by `adapter` must not inherit. The opt-in list
 * is read from `hostEnv`, the extension's own environment, never from a
 * repository.
 */
export function stageWithheldCredentialNames(
  adapter: string,
  env: NodeJS.ProcessEnv,
  hostEnv: NodeJS.ProcessEnv = process.env
): string[] {
  const extra = stageEnvAllow(hostEnv);
  return Object.keys(env).filter((k) => stageWithholdsCredentialEnv(adapter, k, extra));
}

/**
 * A copy of `env` without the credentials a stage run by `adapter` must not
 * inherit. Variables Nightgauge sets for the stage are spread after this base,
 * so they arrive whatever their name, as the Go manager's composeStageEnv adds
 * the adapter's own variables after the withhold.
 */
export function withoutWithheldStageCredentials(
  adapter: string,
  env: NodeJS.ProcessEnv,
  hostEnv: NodeJS.ProcessEnv = process.env
): NodeJS.ProcessEnv {
  const withheld = new Set(stageWithheldCredentialNames(adapter, env, hostEnv));
  const out: NodeJS.ProcessEnv = {};
  for (const [k, v] of Object.entries(env)) {
    if (!withheld.has(k)) out[k] = v;
  }
  return out;
}
