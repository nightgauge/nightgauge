/**
 * The chat panel's view model (#1571): everything the webview renders, as
 * plain strings and flags. The webview renders it with text nodes only, so
 * nothing here is ever markup.
 */

import type { FailureView, ProfileDisplay } from "../../services/conversation/conversationDisplay";

export interface ConversationListItem {
  id: string;
  title: string;
  /** Shown only when the service reports the thread private. */
  privateBadge: boolean;
  active: boolean;
}

export interface ReferenceView {
  kind: "card" | "issue" | "run";
  objectId: string;
  label: string;
  /** A card the viewer may act on: rendered from the reference frame, never from model text. */
  actionable: boolean;
}

export type TranscriptEntryView =
  | {
      kind: "user";
      id: string;
      turnId: string;
      author: string;
      authorAccountId: string | null;
      text: string;
      redacted: boolean;
      target: string;
    }
  | {
      kind: "reply";
      turnId: string;
      host: string;
      text: string;
      redacted: boolean;
      state: string;
      inFlight: boolean;
      failure: FailureView | null;
      profile: ProfileDisplay | null;
      target: string;
      references: ReferenceView[];
      actions: string[];
    };

export interface TargetOption {
  id: string;
  label: string;
  eligible: boolean;
  reason: string | null;
}

export interface ChatViewModel {
  signedIn: boolean;
  loading: boolean;
  team: { name: string; role: string } | null;
  canSwitchTeam: boolean;
  canPost: boolean;
  canCreate: boolean;
  conversations: ConversationListItem[];
  active: {
    id: string;
    title: string;
    privateBadge: boolean;
    entries: TranscriptEntryView[];
    inFlightTurnId: string | null;
  } | null;
  /** The profile the next turn will use, as the hosted picker reports it. */
  profile: (ProfileDisplay & { host: string; workspace: string }) | null;
  profileNote: string | null;
  targets: TargetOption[];
  /** The workspace the user picked in the composer; null means the thread's own target. */
  selectedTarget: string | null;
  /** The target the server reports for an untargeted post. */
  serverTarget: string | null;
  /** Workspaces a refused untargeted post offered. */
  candidates: { id: string; label: string }[] | null;
  status: FailureView | null;
  stream: "idle" | "connected" | "reconnecting" | "disconnected";
  notice: string | null;
  posting: boolean;
  privateDetail: string;
}
