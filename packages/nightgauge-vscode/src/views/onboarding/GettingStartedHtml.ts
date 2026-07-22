/**
 * GettingStartedHtml — pure renderer for the first-run onboarding webview
 * (Issue #4155).
 *
 * Walks a new user through the three steps that matter: initialize the repo →
 * claim an issue → watch a pipeline run produce a pull request. The local
 * product is free and needs no account or sign-in, so onboarding never asks
 * for one. Kept framework-free and side-effect-free (no `vscode` import) so it
 * can be unit-tested without a VSCode host, mirroring AdapterDoctorHtml.ts.
 */

/** Webview hardening inputs — strict CSP + a per-render script nonce. */
export interface GettingStartedHtmlSecurity {
  cspSource: string;
  nonce: string;
}

interface StepConfig {
  action: string;
  title: string;
  body: string;
  buttonLabel: string;
}

const STEPS: StepConfig[] = [
  {
    action: "init",
    title: "1. Initialize this repository",
    body: "Creates <code>.nightgauge/config.yaml</code>, standard labels, and links your GitHub Project board. Nothing is written until you click this.",
    buttonLabel: "Initialize Repository",
  },
  {
    action: "pickup",
    title: "2. Claim an issue",
    body: "Picks the highest-priority Ready issue (or one you choose), creates a branch, and extracts its acceptance criteria.",
    buttonLabel: "Pick Up Issue",
  },
  {
    action: "docs",
    title: "3. Watch the pipeline run",
    body: "The pipeline moves through <code>feature-planning</code> → <code>feature-dev</code> → <code>feature-validate</code> → <code>pr-create</code> → <code>pr-merge</code> automatically, pausing only for your plan approval and a manual test checklist. Read the full walkthrough in the docs.",
    buttonLabel: "Open Full Docs",
  },
];

function esc(value: unknown): string {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function renderStep(step: StepConfig, index: number): string {
  return `
  <div class="step">
    <h2>${esc(step.title)}</h2>
    <p>${step.body}</p>
    <button id="step-${index}" type="button" data-action="${esc(step.action)}">${esc(step.buttonLabel)}</button>
  </div>`;
}

/**
 * Render the full Getting Started webview HTML. Pure: identical input →
 * identical output.
 */
export function renderGettingStartedHtml(security: GettingStartedHtmlSecurity): string {
  const csp = `default-src 'none'; style-src ${security.cspSource} 'unsafe-inline'; script-src 'nonce-${security.nonce}';`;

  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="${csp}" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Getting Started with Nightgauge</title>
  <style>
    body {
      font-family: var(--vscode-font-family);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      padding: 1.5rem;
      max-width: 720px;
    }
    h1 { font-size: 1.5rem; margin-top: 0; }
    p.intro { color: var(--vscode-descriptionForeground); }
    .step {
      border: 1px solid var(--vscode-widget-border);
      border-radius: 4px;
      padding: 1rem 1.25rem;
      margin: 1rem 0;
    }
    .step h2 { font-size: 1.05rem; margin: 0 0 0.4rem; }
    .step p { margin: 0 0 0.75rem; }
    code { font-family: var(--vscode-editor-font-family); }
    button {
      background: var(--vscode-button-background); color: var(--vscode-button-foreground);
      border: none; padding: 0.4rem 0.9rem; cursor: pointer; border-radius: 2px; font: inherit;
    }
    button:hover { background: var(--vscode-button-hoverBackground); }
    .footer { margin-top: 1.5rem; color: var(--vscode-descriptionForeground); font-size: 0.85rem; }
  </style>
</head>
<body>
  <h1>Welcome to Nightgauge 🚀</h1>
  <p class="intro">
    Nightgauge turns a GitHub issue into a reviewed pull request by running
    an AI agent through a documentation-first pipeline with enforced quality
    gates. Here's how to see it work end to end.
  </p>

  ${STEPS.map(renderStep).join("\n")}

  <p class="footer">
    Reopen this walkthrough any time from the Command Palette:
    <strong>Nightgauge: Show Getting Started</strong>.
  </p>

  <script nonce="${security.nonce}">
    const vscode = acquireVsCodeApi();
    document.querySelectorAll('button[data-action]').forEach((btn) => {
      btn.addEventListener('click', () => {
        vscode.postMessage({ type: 'action', action: btn.getAttribute('data-action') });
      });
    });
  </script>
</body>
</html>`;
}
