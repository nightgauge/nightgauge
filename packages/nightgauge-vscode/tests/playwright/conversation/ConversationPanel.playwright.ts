/**
 * The chat panel's page in a browser (#1571): replies render chunk by chunk,
 * untrusted text stays text, each failure renders as its own state, and a
 * viewer sees a card with no act control.
 */

import { expect, test } from "@playwright/test";
import { renderConversationHtml } from "../../../src/views/conversation/ConversationHtml.js";
import type { ChatViewModel } from "../../../src/views/conversation/conversationViewModel.js";
import {
  clearPostedMessages,
  dispatchInboundMessage,
  getPostedMessages,
  loadWebview,
} from "../helpers/webview-loader.js";

const HOSTILE = '<img src=x onerror="window.__pwned=1"><script>window.__pwned=2</script>';

function model(over: Partial<ChatViewModel> = {}): ChatViewModel {
  return {
    signedIn: true,
    loading: false,
    team: { name: "Acme", role: "developer" },
    canSwitchTeam: false,
    canPost: true,
    canCreate: true,
    conversations: [{ id: "c1", title: "Release", privateBadge: false, active: true }],
    active: {
      id: "c1",
      title: "Release",
      privateBadge: false,
      inFlightTurnId: "t1",
      entries: [
        {
          kind: "user",
          id: "m1",
          turnId: "t1",
          author: "alice",
          authorAccountId: "a1",
          text: HOSTILE,
          redacted: false,
          target: "Platform",
        },
        {
          kind: "reply",
          turnId: "t1",
          host: "olivia's machine",
          text: "",
          redacted: false,
          state: "running",
          inFlight: true,
          failure: null,
          profile: null,
          target: "Platform",
          references: [
            {
              kind: "card",
              objectId: "dr_1",
              label: "Needs Attention card dr_1",
              actionable: true,
            },
          ],
          actions: [],
        },
      ],
    },
    profile: {
      provider: { text: "Codex", recognized: true },
      mode: { text: "frontier", recognized: true },
      effort: { text: "high", recognized: true },
      host: "olivia's machine",
      workspace: "Platform",
    },
    profileNote: null,
    targets: [{ id: "w1", label: "Platform", eligible: true, reason: null }],
    selectedTarget: null,
    serverTarget: "Platform",
    candidates: null,
    status: null,
    stream: "connected",
    notice: null,
    posting: false,
    privateDetail: "Only you can read this conversation on the hosted service.",
    ...over,
  };
}

async function open(page: import("@playwright/test").Page, m: ChatViewModel) {
  await loadWebview(page, renderConversationHtml({ cspSource: "", nonce: "testnonce123" }));
  await dispatchInboundMessage(page, { type: "state", model: m });
}

test("appends streamed chunks one at a time before the turn ends", async ({ page }) => {
  await open(page, model());
  const reply = page.locator('.entry.reply[data-turn="t1"] .reply-text');
  await dispatchInboundMessage(page, { type: "delta", turnId: "t1", seq: 1, text: "Two runs " });
  await expect(reply).toHaveText("Two runs ");
  await dispatchInboundMessage(page, {
    type: "delta",
    turnId: "t1",
    seq: 2,
    text: "<b>are</b> going.",
  });
  await expect(reply).toHaveText("Two runs <b>are</b> going.");
  await expect(page.locator(".entry.reply b")).toHaveCount(0);
});

test("shows markup in a message as text and runs none of it", async ({ page }) => {
  await open(page, model());
  await expect(page.locator(".entry.user .text")).toHaveText(HOSTILE);
  await expect(page.locator("#transcript img")).toHaveCount(0);
  await expect(page.locator("#transcript script")).toHaveCount(0);
  expect(
    await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned)
  ).toBeUndefined();
});

test("renders the five failure states differently", async ({ page }) => {
  await open(page, model());
  const kinds = [
    { kind: "offline", title: "Workspace offline", detail: "a" },
    { kind: "not_entitled", title: "Not included in your plan", detail: "b" },
    { kind: "quota_exhausted", title: "Allowance used up", detail: "c" },
    { kind: "turn_failed", title: "Turn failed", detail: "d" },
  ] as const;
  const seen: string[] = [];
  for (const status of kinds) {
    await dispatchInboundMessage(page, { type: "state", model: model({ status }) });
    const banner = page.locator("#banners .status");
    await expect(banner).toHaveAttribute("data-kind", status.kind);
    seen.push(
      `${await banner.getAttribute("data-kind")}|${await banner.locator(".heading").textContent()}`
    );
  }
  await dispatchInboundMessage(page, { type: "state", model: model({ stream: "reconnecting" }) });
  const stream = page.locator("#banners .status");
  await expect(stream).toHaveAttribute("data-kind", "stream_disconnected");
  seen.push(
    `${await stream.getAttribute("data-kind")}|${await stream.locator(".heading").textContent()}`
  );
  expect(new Set(seen).size).toBe(5);
});

test("gives a viewer the card with no act control and a closed composer", async ({ page }) => {
  const viewer = model({ canPost: false, canCreate: false });
  const reply = viewer.active!.entries[1]!;
  if (reply.kind === "reply") reply.references = [{ ...reply.references[0]!, actionable: false }];
  await open(page, viewer);
  await expect(page.locator(".reference")).toContainText("Needs Attention card dr_1");
  await expect(page.locator(".reference .act")).toHaveCount(0);
  await expect(page.locator(".cancel")).toHaveCount(0);
  await expect(page.locator("#content")).toBeDisabled();
});

test("posts an act, a cancel and a send as the panel's own message types", async ({ page }) => {
  await open(page, model());
  await clearPostedMessages(page);
  await page.click(".reference .act");
  await page.click(".entry.reply .cancel");
  await page.fill("#content", "what is blocked?");
  await page.click("#send");
  expect(await getPostedMessages(page)).toEqual([
    { type: "actOnCard", cardId: "dr_1" },
    { type: "cancel", turnId: "t1" },
    { type: "send", content: "what is blocked?", targetWorkspaceId: null },
  ]);
});

test("resets the private choice after each create", async ({ page }) => {
  await open(page, model());
  await clearPostedMessages(page);
  await page.check("#private");
  await page.click("#create");
  await expect(page.locator("#private")).not.toBeChecked();
  await page.click("#create");
  expect(await getPostedMessages(page)).toEqual([
    { type: "newConversation", private: true },
    { type: "newConversation", private: false },
  ]);
  await expect(page.locator("#private-detail")).toHaveText(
    "Only you can read this conversation on the hosted service."
  );
});

test("labels the profile as provider, mode and effort", async ({ page }) => {
  await open(page, model());
  await expect(page.locator("#profile")).toContainText("Provider: Codex");
  await expect(page.locator("#profile")).toContainText("Mode: frontier");
  await expect(page.locator("#profile")).not.toContainText("Provider: frontier");
});
