import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const vscodeRoot = resolve(import.meta.dirname, "../..");
const repoRoot = resolve(vscodeRoot, "../..");

describe("Claude Agent SDK distribution boundary", () => {
  it("keeps the Agent SDK external to Marketplace bundles", () => {
    const pkg = JSON.parse(readFileSync(resolve(vscodeRoot, "package.json"), "utf8")) as {
      scripts: Record<string, string>;
    };

    expect(pkg.scripts["build:bundle"]).toContain("--external:@anthropic-ai/claude-agent-sdk");
  });

  it("exposes the Agent SDK only as an optional SDK peer", () => {
    const pkg = JSON.parse(
      readFileSync(resolve(repoRoot, "packages/nightgauge-sdk/package.json"), "utf8")
    ) as {
      dependencies?: Record<string, string>;
      peerDependencies?: Record<string, string>;
      peerDependenciesMeta?: Record<string, { optional?: boolean }>;
    };

    expect(pkg.dependencies).not.toHaveProperty("@anthropic-ai/claude-agent-sdk");
    expect(pkg.peerDependencies).toHaveProperty("@anthropic-ai/claude-agent-sdk");
    expect(pkg.peerDependenciesMeta?.["@anthropic-ai/claude-agent-sdk"]?.optional).toBe(true);
  });

  it("does not claim the optional Agent SDK as redistributed software", () => {
    const notices = readFileSync(resolve(vscodeRoot, "THIRD_PARTY_NOTICES"), "utf8");
    expect(notices).not.toContain("@anthropic-ai/claude-agent-sdk");
  });
});
