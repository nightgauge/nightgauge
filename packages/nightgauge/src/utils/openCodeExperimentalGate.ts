/**
 * The OpenCode enable switch — the same env var name the Go gate and the SDK
 * resolver read (`EXPERIMENTAL_OPENCODE_ENV_VAR` in
 * packages/nightgauge-sdk/src/cli/adapter.ts). Only the exact value `1` opens
 * it, read from the process environment only, so a committed config file can
 * never turn it on.
 *
 * Shared by every adapter-selection surface (Switch Adapter quick pick,
 * Settings panel dropdowns) so the gate reads identically everywhere
 * (Issue #1628).
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md § The enable gate
 */
export const OPENCODE_ENABLE_SWITCH_ENV_VAR = "NIGHTGAUGE_EXPERIMENTAL_OPENCODE";

export function isOpenCodeSwitchOn(): boolean {
  return process.env[OPENCODE_ENABLE_SWITCH_ENV_VAR] === "1";
}

/**
 * The message shown wherever a surface refuses to persist `adapter: opencode`
 * because the switch is off — writing it now would silently produce a
 * pipeline that refuses to run.
 */
export function openCodeGateMessage(): string {
  return (
    `OpenCode is experimental and does not dispatch by default. Set ` +
    `${OPENCODE_ENABLE_SWITCH_ENV_VAR}=1 in the environment that runs Nightgauge to enable ` +
    `it, then switch adapters again.`
  );
}
