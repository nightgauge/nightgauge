/**
 * The chat panel's page (#1571).
 *
 * Everything the page shows comes from other people and from a model, so the
 * page builds its DOM with `textContent` only: no `innerHTML`, no markdown
 * renderer, no links built from message text. Markup in a message is shown
 * as the characters it is. The content security policy allows only this
 * page's own nonce-bearing script and style, and no network access at all;
 * the extension host makes every request.
 *
 * Chunks of a streamed reply are appended as they arrive (`delta`), so a
 * reply renders incrementally rather than when the turn ends.
 */

export interface ConversationHtmlSecurity {
  cspSource: string;
  nonce: string;
}

function attr(value: string): string {
  return value.replace(/[^A-Za-z0-9+/=_-]/g, "");
}

export function renderConversationHtml(security: ConversationHtmlSecurity): string {
  const nonce = attr(security.nonce);
  const csp = [
    "default-src 'none'",
    `style-src 'nonce-${nonce}'`,
    `script-src 'nonce-${nonce}'`,
    "img-src 'none'",
    "connect-src 'none'",
    "form-action 'none'",
    "base-uri 'none'",
  ].join("; ");
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="${csp}" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Nightgauge Conversation</title>
  <style nonce="${nonce}">${STYLE}</style>
</head>
<body>
  <header id="header">
    <span id="team"></span>
    <button id="switch-team" type="button" hidden>Switch team</button>
    <button id="refresh" type="button">Refresh</button>
  </header>
  <div id="banners"></div>
  <div id="layout">
    <nav id="sidebar">
      <section id="new-conversation">
        <button id="create" type="button">New conversation</button>
        <label id="private-choice"><input id="private" type="checkbox" /> Private</label>
        <p id="private-detail" class="detail"></p>
      </section>
      <ul id="conversations"></ul>
    </nav>
    <main id="main">
      <h2 id="title"></h2>
      <div id="transcript" aria-live="polite"></div>
      <div id="profile"></div>
      <div id="candidates"></div>
      <form id="composer">
        <label for="target">Workspace</label>
        <select id="target"></select>
        <textarea id="content" rows="3" placeholder="Message the workspace"></textarea>
        <div id="composer-note" class="detail"></div>
        <button id="send" type="submit">Send</button>
      </form>
    </main>
  </div>
  <script nonce="${nonce}">${SCRIPT}</script>
</body>
</html>`;
}

const STYLE = `
body { font-family: var(--vscode-font-family); font-size: var(--vscode-font-size);
  color: var(--vscode-foreground); background: var(--vscode-editor-background); margin: 0; padding: 8px; }
header { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
#team { font-weight: 600; flex: 1; }
#layout { display: flex; gap: 12px; }
#sidebar { width: 220px; flex-shrink: 0; }
#main { flex: 1; min-width: 0; }
#conversations { list-style: none; padding: 0; margin: 8px 0; }
#conversations li { padding: 4px 6px; cursor: pointer; border-radius: 3px; }
#conversations li.active { background: var(--vscode-list-activeSelectionBackground);
  color: var(--vscode-list-activeSelectionForeground); }
.badge { font-size: 0.8em; padding: 0 4px; margin-left: 4px; border-radius: 3px;
  border: 1px solid var(--vscode-badge-background); }
.detail { font-size: 0.85em; opacity: 0.8; margin: 4px 0; }
#transcript { border: 1px solid var(--vscode-panel-border); padding: 8px; max-height: 60vh;
  overflow-y: auto; }
.entry { margin: 6px 0; padding: 6px; border-radius: 3px; }
.entry.user { background: var(--vscode-editor-inactiveSelectionBackground); }
.author { font-weight: 600; }
.text { white-space: pre-wrap; word-break: break-word; margin-top: 2px; }
.meta { font-size: 0.85em; opacity: 0.75; }
.state { font-size: 0.85em; font-style: italic; }
.status { border-left: 4px solid; padding: 6px 8px; margin: 4px 0; }
.status .heading { font-weight: 600; }
.status[data-kind="offline"] { border-color: var(--vscode-editorWarning-foreground); }
.status[data-kind="not_entitled"] { border-color: var(--vscode-charts-purple); }
.status[data-kind="not_permitted"] { border-color: var(--vscode-charts-purple); }
.status[data-kind="quota_exhausted"] { border-color: var(--vscode-charts-orange); }
.status[data-kind="rate_limited"] { border-color: var(--vscode-charts-orange); }
.status[data-kind="turn_failed"] { border-color: var(--vscode-errorForeground); }
.status[data-kind="stream_disconnected"] { border-color: var(--vscode-charts-blue); }
.status[data-kind="signed_out"] { border-color: var(--vscode-charts-blue); }
.reference { margin-top: 4px; }
#profile { margin: 6px 0; }
.unrecognized { text-decoration: underline dotted; }
#composer { display: flex; flex-direction: column; gap: 4px; }
textarea { font-family: inherit; }
`;

/**
 * The page script. Every `vscode.postMessage` names a literal type so the
 * message-handler check can match it to the panel's `handleMessage`.
 */
const SCRIPT = `
(function () {
  const vscode = acquireVsCodeApi();
  let model = null;
  const $ = (id) => document.getElementById(id);

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function statusNode(view) {
    const box = el("div", "status");
    box.dataset.kind = view.kind;
    box.appendChild(el("div", "heading", view.title));
    if (view.detail) box.appendChild(el("div", "body", view.detail));
    return box;
  }

  const STATE_TEXT = {
    queued: "Waiting for the thread's earlier turn",
    dispatched: "Sent to the host, waiting for it to start",
    running: "Answering",
    completed: "",
    cancelled: "Cancelled",
    failed: ""
  };

  function profileText(p) {
    const parts = [];
    const field = (label, f) => {
      const span = el("span", f.recognized ? "" : "unrecognized", label + ": " + f.text +
        (f.recognized ? "" : " (not recognized)"));
      parts.push(span);
    };
    field("Provider", p.provider);
    field("Mode", p.mode);
    field("Effort", p.effort);
    return parts;
  }

  function renderBanners() {
    const banners = $("banners");
    banners.replaceChildren();
    if (!model) return;
    if (model.status) banners.appendChild(statusNode(model.status));
    if (model.stream === "reconnecting" || model.stream === "disconnected") {
      banners.appendChild(statusNode({
        kind: "stream_disconnected",
        title: "Stream disconnected",
        detail: model.stream === "reconnecting"
          ? "Reconnecting. Replies resume from the last frame received."
          : "Not receiving updates for this conversation."
      }));
    }
    if (model.notice) banners.appendChild(el("div", "detail notice", model.notice));
    if (!model.signedIn) {
      const b = el("button", "", "Sign in");
      b.type = "button";
      b.addEventListener("click", () => vscode.postMessage({ type: "signIn" }));
      banners.appendChild(b);
    }
  }

  function renderSidebar() {
    $("team").textContent = model.team ? "Team: " + model.team.name + " (" + model.team.role + ")" : "";
    $("switch-team").hidden = !model.canSwitchTeam;
    $("create").disabled = !model.canCreate;
    $("private").disabled = !model.canCreate;
    $("private-detail").textContent = model.privateDetail;
    const list = $("conversations");
    list.replaceChildren();
    for (const c of model.conversations) {
      const li = el("li", c.active ? "active" : "", c.title);
      li.dataset.conversationId = c.id;
      if (c.privateBadge) li.appendChild(el("span", "badge", "Private"));
      li.addEventListener("click", () => {
        vscode.postMessage({ type: "selectConversation", conversationId: c.id });
      });
      list.appendChild(li);
    }
  }

  function replyNode(e) {
    const box = el("div", "entry reply");
    box.dataset.turn = e.turnId;
    box.dataset.state = e.state;
    const head = el("div", "meta", "Workspace reply" + (e.target ? " from " + e.target : "") +
      (e.host ? " on " + e.host : ""));
    box.appendChild(head);
    const text = el("div", "text reply-text", e.redacted ? "[redacted]" : e.text);
    box.appendChild(text);
    const stateText = STATE_TEXT[e.state] !== undefined ? STATE_TEXT[e.state] : e.state;
    if (stateText) box.appendChild(el("div", "state", stateText + (e.inFlight ? "..." : "")));
    if (e.failure) box.appendChild(statusNode(e.failure));
    if (e.profile) {
      const line = el("div", "meta profile-used");
      line.appendChild(el("span", "", "Ran with "));
      for (const part of profileText(e.profile)) { line.appendChild(part); line.appendChild(el("span", "", " · ")); }
      box.appendChild(line);
    }
    for (const r of e.references) {
      const ref = el("div", "reference");
      ref.dataset.kind = r.kind;
      ref.appendChild(el("span", "", r.label));
      if (r.kind === "card") {
        if (r.actionable) {
          const act = el("button", "act", "Act on card");
          act.type = "button";
          act.addEventListener("click", () => vscode.postMessage({ type: "actOnCard", cardId: r.objectId }));
          ref.appendChild(act);
        } else {
          ref.appendChild(el("span", "detail", " (read only)"));
        }
      }
      box.appendChild(ref);
    }
    for (const a of e.actions) box.appendChild(el("div", "meta", a));
    if (e.inFlight && model.canPost) {
      const cancel = el("button", "cancel", "Cancel");
      cancel.type = "button";
      cancel.addEventListener("click", () => vscode.postMessage({ type: "cancel", turnId: e.turnId }));
      box.appendChild(cancel);
    }
    return box;
  }

  function renderTranscript() {
    const t = $("transcript");
    const atBottom = t.scrollTop + t.clientHeight >= t.scrollHeight - 4;
    t.replaceChildren();
    $("title").textContent = model.active ? model.active.title : "No conversation open";
    if (model.active && model.active.privateBadge) $("title").appendChild(el("span", "badge", "Private"));
    if (!model.active) return;
    for (const e of model.active.entries) {
      if (e.kind === "user") {
        const box = el("div", "entry user");
        box.dataset.messageId = e.id;
        box.dataset.author = e.authorAccountId || "";
        const head = el("div", "meta");
        head.appendChild(el("span", "author", e.author));
        if (e.target) head.appendChild(el("span", "", " to " + e.target));
        box.appendChild(head);
        box.appendChild(el("div", "text", e.redacted ? "[redacted]" : e.text));
        t.appendChild(box);
      } else {
        t.appendChild(replyNode(e));
      }
    }
    if (atBottom) t.scrollTop = t.scrollHeight;
  }

  function renderComposer() {
    const p = $("profile");
    p.replaceChildren();
    if (model.profile) {
      p.appendChild(el("span", "", "Next turn: "));
      for (const part of profileText(model.profile)) { p.appendChild(part); p.appendChild(el("span", "", " · ")); }
      p.appendChild(el("span", "meta", "on " + model.profile.workspace + (model.profile.host ? ", " + model.profile.host : "")));
    } else if (model.profileNote) {
      p.appendChild(el("span", "detail", model.profileNote));
    }
    const select = $("target");
    const current = model.selectedTarget || "";
    select.replaceChildren();
    const auto = el("option", "", "The thread's target" + (model.serverTarget ? " (" + model.serverTarget + ")" : ""));
    auto.value = "";
    select.appendChild(auto);
    for (const t of model.targets) {
      const o = el("option", "", t.label + (t.eligible ? "" : " (" + (t.reason || "unavailable") + ")"));
      o.value = t.id;
      o.disabled = !t.eligible;
      select.appendChild(o);
    }
    select.value = current;
    const c = $("candidates");
    c.replaceChildren();
    if (model.candidates && model.candidates.length) {
      c.appendChild(el("div", "detail", "Choose the workspace that should answer, then send again:"));
      for (const cand of model.candidates) {
        const b = el("button", "candidate", cand.label);
        b.type = "button";
        b.addEventListener("click", () => vscode.postMessage({ type: "selectTarget", targetWorkspaceId: cand.id }));
        c.appendChild(b);
      }
    }
    const enabled = !!model.active && model.canPost && !model.posting;
    $("content").disabled = !enabled;
    $("send").disabled = !enabled;
    select.disabled = !enabled;
    $("composer-note").textContent = model.active && !model.canPost
      ? "Your role can read this conversation but not post to it." : "";
  }

  function render() {
    if (!model) return;
    renderBanners();
    renderSidebar();
    renderTranscript();
    renderComposer();
  }

  function appendDelta(m) {
    for (const node of document.querySelectorAll(".entry.reply")) {
      if (node.dataset.turn === m.turnId) {
        const text = node.querySelector(".reply-text");
        if (text) text.appendChild(document.createTextNode(m.text));
        const t = $("transcript");
        t.scrollTop = t.scrollHeight;
        return;
      }
    }
  }

  window.addEventListener("message", (event) => {
    const m = event.data;
    if (!m || typeof m !== "object") return;
    if (m.type === "state") { model = m.model; render(); }
    else if (m.type === "delta") appendDelta(m);
    else if (m.type === "sent") $("content").value = "";
  });

  $("refresh").addEventListener("click", () => vscode.postMessage({ type: "refresh" }));
  $("switch-team").addEventListener("click", () => vscode.postMessage({ type: "switchTeam" }));
  $("create").addEventListener("click", () => {
    const wantPrivate = $("private").checked;
    // The choice never carries over: it is off again for the next conversation.
    $("private").checked = false;
    vscode.postMessage({ type: "newConversation", private: wantPrivate });
  });
  $("target").addEventListener("change", () => {
    const v = $("target").value;
    vscode.postMessage({ type: "selectTarget", targetWorkspaceId: v === "" ? null : v });
  });
  $("composer").addEventListener("submit", (event) => {
    event.preventDefault();
    const content = $("content").value;
    if (!content.trim()) return;
    const v = $("target").value;
    vscode.postMessage({ type: "send", content: content, targetWorkspaceId: v === "" ? null : v });
  });
  vscode.postMessage({ type: "ready" });
})();
`;
