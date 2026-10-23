/**
 * What the chat panel (#1571) says about a profile and about a failure.
 *
 * Vocabulary (ADR-017 Decision 3): the execution adapter is the provider and
 * the performance mode is the mode. They are different axes, so the provider
 * label is drawn only from the adapter display-name table and the mode only
 * from `PERFORMANCE_MODES`: `frontier` is a mode and never appears where a
 * provider name belongs. A mode or effort the panel does not recognize is
 * shown as received, never mapped onto a known value.
 *
 * Failures (the "silently queued looks identical to ran" class): each kind
 * renders differently, so workspace offline, not entitled, quota exhausted,
 * turn failed and stream disconnected are never one spinner.
 */

import { PERFORMANCE_MODES } from "../../utils/modeProfiles";
import type { WireExecutionProfile } from "./conversationWire";

/**
 * Display names of the execution adapters, the same table as the Go
 * registry's `adapterDisplayName` (internal/execution/adapters/registry.go);
 * a test pins the two together.
 */
export const ADAPTER_DISPLAY_NAMES: Readonly<Record<string, string>> = {
  "claude-headless": "Claude Headless",
  "claude-sdk": "Claude SDK",
  codex: "Codex",
  gemini: "Gemini Headless",
  "gemini-sdk": "Gemini SDK",
  copilot: "GitHub Copilot",
  grok: "Grok",
  opencode: "OpenCode",
};

const EFFORTS = ["low", "medium", "high", "xhigh", "max"] as const;

/** One field of a displayed profile: the text, and whether the panel recognized the value. */
export interface ProfileField {
  text: string;
  recognized: boolean;
}

export interface ProfileDisplay {
  provider: ProfileField;
  mode: ProfileField;
  effort: ProfileField;
}

/**
 * The provider, mode and effort to show for a profile. The wire's own
 * `adapter_display_name` is never used: the provider is looked up by adapter
 * id in {@link ADAPTER_DISPLAY_NAMES}, and an id the table does not hold is
 * shown as unrecognized, never as the raw value, so a mode in the provider
 * slot cannot read as a provider.
 */
export function describeProfile(profile: WireExecutionProfile): ProfileDisplay {
  const providerName = Object.prototype.hasOwnProperty.call(ADAPTER_DISPLAY_NAMES, profile.adapter)
    ? ADAPTER_DISPLAY_NAMES[profile.adapter]
    : undefined;
  const modeKnown = (PERFORMANCE_MODES as readonly string[]).includes(profile.performance_mode);
  const effortKnown = (EFFORTS as readonly string[]).includes(profile.effort);
  return {
    provider: providerName
      ? { text: providerName, recognized: true }
      : { text: "Unrecognized provider", recognized: false },
    mode: { text: profile.performance_mode, recognized: modeKnown },
    effort:
      profile.effort === ""
        ? { text: "model default", recognized: true }
        : { text: profile.effort, recognized: effortKnown },
  };
}

/** The failure states the panel distinguishes. */
export type FailureKind =
  | "signed_out"
  | "offline"
  | "not_entitled"
  | "not_permitted"
  | "quota_exhausted"
  | "rate_limited"
  | "turn_failed"
  | "stream_disconnected"
  | "choose_target"
  | "transcript_full"
  | "queue_full"
  | "invalid"
  | "not_found"
  | "network"
  | "error";

export interface FailureView {
  kind: FailureKind;
  title: string;
  detail: string;
}

const TITLES: Record<FailureKind, string> = {
  signed_out: "Signed out",
  offline: "Workspace offline",
  not_entitled: "Not included in your plan",
  not_permitted: "Your role cannot do this",
  quota_exhausted: "Allowance used up",
  rate_limited: "Too many messages",
  turn_failed: "Turn failed",
  stream_disconnected: "Stream disconnected",
  choose_target: "Choose a workspace",
  transcript_full: "Conversation full",
  queue_full: "Too many turns waiting",
  invalid: "Message refused",
  not_found: "Conversation not available",
  network: "Hosted service unreachable",
  error: "Request failed",
};

export function failureView(kind: FailureKind, detail: string): FailureView {
  return { kind, title: TITLES[kind], detail };
}

const OFFLINE_CODES = new Set([
  "TARGET_OFFLINE",
  "TARGET_NO_HOST",
  "TARGET_CANNOT_HOST",
  "TARGET_PROFILE_CONFLICT",
  "NO_ELIGIBLE_WORKSPACE",
]);

/** Classify a refused hosted-service request by its HTTP status and error code. */
export function classifyRefusal(status: number, code: string | undefined): FailureKind {
  if (status === 401) return "signed_out";
  if (status === 404) return "not_found";
  if (code === "LICENSE_TIER_EXCEEDED") return "not_entitled";
  if (status === 403) return "not_permitted";
  if (code === "TURN_ALLOWANCE_EXHAUSTED" || code === "HOST_CAP_REACHED") return "quota_exhausted";
  if (status === 429 || code === "RATE_LIMITER_UNAVAILABLE") return "rate_limited";
  if (code !== undefined && OFFLINE_CODES.has(code)) return "offline";
  if (code === "TARGET_AMBIGUOUS" || code === "PREVIOUS_TARGET_UNAVAILABLE") return "choose_target";
  if (code === "TRANSCRIPT_FULL") return "transcript_full";
  if (code === "TURN_QUEUE_FULL") return "queue_full";
  if (status === 400) return "invalid";
  return "error";
}

const TURN_FAILURE_TEXT: Record<string, string> = {
  timeout: "The turn timed out.",
  token_ceiling: "The turn used more tokens than its ceiling allows.",
  chunk_limit: "The reply was longer than a turn may stream.",
  refused: "The host refused the turn.",
  secret_detected: "The reply was stopped because it looked like it contained a credential.",
  poster_unauthorized: "The poster lost access before the turn started.",
  cancel_unconfirmed: "The host did not confirm the cancel in time.",
  host_error: "The host hit an error running the turn.",
};

/**
 * The failure a terminal `failed` turn renders. A turn that never reached a
 * host because its workspace went away is the offline state, not a failed
 * turn; every other reason is a failed turn, with the host's detail shown as
 * received when there is one.
 */
export function turnFailureView(reason: string | null, detail: string | null): FailureView {
  if (reason === "target_unavailable" || reason === "agent_lost") {
    return failureView(
      "offline",
      reason === "agent_lost"
        ? "The workspace went offline while answering."
        : "No machine could answer for the workspace before the turn started."
    );
  }
  const base =
    (reason && TURN_FAILURE_TEXT[reason]) ?? (reason ? `Reason: ${reason}.` : "The turn failed.");
  return failureView("turn_failed", detail ? `${base} ${detail}` : base);
}
