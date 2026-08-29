/**
 * Marketing screenshot capture — regenerable product imagery.
 *
 *   npm run marketing:screenshots            # everything
 *   npm run marketing:screenshots -- cards   # only the Discord/Slack cards
 *   npm run marketing:screenshots -- dashboard
 *
 * Output: docs/images/marketing/*.png (+ the Discord/Slack payload JSON that
 * each card was rendered from, so a reader can diff the wire format too).
 *
 * What is real and what is a frame is documented in
 * docs/images/marketing/README.md. Short version: the dashboard content and
 * both notification payloads come from production code fed with a real run
 * (issue #338 of a private Flutter app); the VS Code window and the chat-client chrome
 * are faithful frames, because neither VS Code nor Discord can be rendered
 * headless from a script.
 */

import { chromium } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { REPO_NAME } from "./run-data";

const here = dirname(fileURLToPath(import.meta.url));
const PKG = resolve(here, "..", "..");
const REPO = resolve(PKG, "..", "..");
const OUT = resolve(REPO, "docs", "images", "marketing");
const SCALE = 2;

function dataUri(path: string, mime: string): string {
  return `data:${mime};base64,${readFileSync(path).toString("base64")}`;
}

async function shot(browser: any, html: string, out: string, width: number, height?: number) {
  const ctx = await browser.newContext({
    viewport: { width, height: height ?? 900 },
    deviceScaleFactor: SCALE,
    colorScheme: "dark",
  });
  const page = await ctx.newPage();
  await page.setContent(html, { waitUntil: "load" });
  await page.waitForTimeout(600);
  if (height) {
    await page.screenshot({ path: out, type: "png" });
  } else {
    const el = page.locator("body > *").first();
    await el.screenshot({ path: out, type: "png" });
  }
  await ctx.close();
  console.log(`✓ ${out.replace(REPO + "/", "")}`);
}

async function main() {
  const only = new Set(process.argv.slice(2));
  const want = (k: string) => only.size === 0 || only.has(k);
  mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch();
  const icon = dataUri(resolve(PKG, "resources", "nightgauge-icon.png"), "image/png");
  const activityIcon = dataUri(
    resolve(PKG, "resources", "nightgauge-activity-bar.svg"),
    "image/svg+xml"
  );

  if (want("cards")) {
    const cards = await import("./cards.js");
    const embed = cards.buildDiscordEmbed();
    const slack = cards.buildSlackAttachment();
    writeFileSync(
      resolve(OUT, "notification-discord.json"),
      JSON.stringify({ embeds: [embed] }, null, 2)
    );
    writeFileSync(
      resolve(OUT, "notification-slack.json"),
      JSON.stringify({ text: slack.fallback, attachments: [slack] }, null, 2)
    );
    await shot(
      browser,
      cards.renderDiscordHtml(embed, icon),
      resolve(OUT, "notification-discord.png"),
      1180
    );
    await shot(
      browser,
      cards.renderSlackHtml(slack, icon),
      resolve(OUT, "notification-slack.png"),
      1180
    );
  }

  if (want("dashboard")) {
    const { renderDashboardTab } = await import("./dashboard.js");
    const { renderFrame, themeWebview } = await import("./vscode-frame.js");
    const tabs: Array<[string, string]> = [
      ["overview", "extension-dashboard-overview"],
      ["analytics", "extension-dashboard-analytics"],
      ["history", "extension-dashboard-history"],
    ];
    for (const [tab, name] of tabs) {
      const webview = themeWebview(renderDashboardTab(tab)).replace(
        "<head>",
        `<head><script>window.acquireVsCodeApi=function(){var s={activeTab:${JSON.stringify(tab)}};return{postMessage:function(){},setState:function(v){s=v;return v;},getState:function(){return s;}}};</script>`
      );
      const html = renderFrame({
        title: `Nightgauge Dashboard — ${REPO_NAME} — Visual Studio Code`,
        tabLabel: "Nightgauge Dashboard",
        workspaceName: REPO_NAME,
        activityIcon,
        webviewHtml: webview,
      });
      await shot(browser, html, resolve(OUT, `${name}.png`), 1440, 900);
    }
  }

  await browser.close();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
