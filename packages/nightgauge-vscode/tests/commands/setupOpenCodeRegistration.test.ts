/**
 * nightgauge.setupOpenCode (#1671): contributed with its palette title and
 * registered to OpenCodeSetupService.showSetupPrompt(). A contribution without
 * the registration fails at runtime with "command not found".
 */

import { describe, it, expect } from "vitest";
import * as fs from "fs";
import * as path from "path";

const PKG = path.resolve(__dirname, "..", "..");

describe("nightgauge.setupOpenCode", () => {
  it("is contributed in package.json with the palette title", () => {
    const pkg = JSON.parse(fs.readFileSync(path.join(PKG, "package.json"), "utf8"));
    const cmd = pkg.contributes.commands.find(
      (c: { command: string }) => c.command === "nightgauge.setupOpenCode"
    );
    expect(cmd?.title).toBe("Nightgauge: Setup OpenCode Skills");
  });

  it("is registered and calls OpenCodeSetupService.showSetupPrompt()", () => {
    const src = fs.readFileSync(path.join(PKG, "src", "commands", "register-all.ts"), "utf8");
    expect(src).toMatch(
      /registerCommand\(\s*"nightgauge\.setupOpenCode",\s*async \(\) => \{\s*await openCodeSetupService\.showSetupPrompt\(\);/
    );
    expect(src).toMatch(/\n\s+setupOpenCodeCommand,\n/);
  });

  it("is constructed at activation and passed through to registration", () => {
    const services = fs.readFileSync(path.join(PKG, "src", "bootstrap", "services.ts"), "utf8");
    expect(services).toContain("new OpenCodeSetupService(context)");
    const ext = fs.readFileSync(path.join(PKG, "src", "extension.ts"), "utf8");
    expect(ext.match(/openCodeSetupService,/g)?.length).toBe(2);
  });
});
