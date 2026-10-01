/**
 * The execution profile a workspace advertises on agent registration and
 * every heartbeat (#1567): what it would run a turn with — the adapter, the
 * performance mode and the default effort, each with the layer that produced
 * it.
 *
 * The extension resolves NONE of it. The Go binary does, over the
 * `agent.executionProfile` IPC method, through the resolvers the pipeline
 * dispatches with; this module only carries the result to the wire. A second
 * TypeScript resolution here is exactly the drift the issue forbids.
 *
 * The field set is pinned against Go's `platform.ExecutionProfile`
 * (internal/platform/execution_profile.go) by executionProfileFields.test.ts.
 */
export interface ExecutionProfile {
  adapter: string;
  adapter_display_name: string;
  adapter_source: string;
  performance_mode: string;
  performance_mode_source: string;
  /** "" when nothing names an effort: the model's declared default applies. */
  effort: string;
  effort_source: string;
}

/** Every key of ExecutionProfile, in Go's declaration order. */
export const EXECUTION_PROFILE_FIELDS: ReadonlyArray<keyof ExecutionProfile> = [
  "adapter",
  "adapter_display_name",
  "adapter_source",
  "performance_mode",
  "performance_mode_source",
  "effort",
  "effort_source",
];

/** The capabilities every extension agent advertises. */
export const BASE_AGENT_CAPABILITIES: readonly string[] = ["headless", "interactive"];

/** Advertised only when the workspace's adapter can host a conversational turn. */
export const CONVERSATION_CAPABILITY = "conversation";

/** The profile and conversation viability, as Go resolved them. */
export interface ResolvedExecutionProfile {
  profile: ExecutionProfile;
  conversation: boolean;
}

/** The slice of IpcClient this module needs. */
export interface ExecutionProfileSource {
  agentExecutionProfile(): Promise<{ profile: unknown; conversation: unknown }>;
}

/**
 * Copies exactly the allowlisted fields, each a string, or returns null. The
 * Go side already validated the values; this keeps anything the result might
 * grow later off the wire until both sides agree on it.
 */
export function toWireProfile(raw: unknown): ExecutionProfile | null {
  if (raw === null || typeof raw !== "object") return null;
  const src = raw as Record<string, unknown>;
  const out: Partial<Record<keyof ExecutionProfile, string>> = {};
  for (const field of EXECUTION_PROFILE_FIELDS) {
    const value = src[field];
    if (typeof value !== "string") return null;
    out[field] = value;
  }
  return out as ExecutionProfile;
}

/**
 * Asks Go for the current profile. Called on every registration and every
 * heartbeat, so a changed adapter, mode or effort reaches the platform within
 * one beat with no file watcher and no restart. Null — advertise no profile —
 * when there is no daemon or it cannot resolve one; a profile must never cost
 * the agent its registration or its presence.
 */
export async function resolveExecutionProfile(
  source: ExecutionProfileSource | null | undefined
): Promise<ResolvedExecutionProfile | null> {
  if (!source) return null;
  try {
    const result = await source.agentExecutionProfile();
    const profile = toWireProfile(result?.profile);
    if (!profile) return null;
    return { profile, conversation: result.conversation === true };
  } catch {
    return null;
  }
}

/** The capability list for a registration, given the resolved profile. */
export function agentCapabilities(resolved: ResolvedExecutionProfile | null): string[] {
  const caps = [...BASE_AGENT_CAPABILITIES];
  if (resolved?.conversation === true) caps.push(CONVERSATION_CAPABILITY);
  return caps;
}
