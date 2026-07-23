import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createAdapterQueryFunction } from "../../cli/adapterQuery.js";
import { defaultRegistry } from "../../cli/adapters/AdapterRegistry.js";
import { validateCodexStagePostconditions } from "../../cli/commands/stage.js";
import { rewriteStageSkillPaths } from "../../orchestrator/StageExecutor.js";

describe("Codex stage contract", () => {
  const temporaryDirectories: string[] = [];

  afterEach(async () => {
    vi.restoreAllMocks();
    await Promise.all(
      temporaryDirectories.splice(0).map((directory) =>
        rm(directory, { recursive: true, force: true })
      )
    );
  });

  it("passes stage and cwd into the selected adapter query factory", async () => {
    const query = vi.fn();
    const adapter = defaultRegistry.get("codex");
    const createQueryFunction = vi.spyOn(adapter, "createQueryFunction").mockResolvedValue(query);

    await expect(
      createAdapterQueryFunction("codex", {
        stage: "feature-planning",
        cwd: "/consumer/worktree",
      })
    ).resolves.toBe(query);

    expect(createQueryFunction).toHaveBeenCalledWith({
      stage: "feature-planning",
      cwd: "/consumer/worktree",
    });
  });

  it("preserves the final Codex response when the handoff artifact is missing", async () => {
    const cwd = await mkdtemp(path.join(tmpdir(), "nightgauge-codex-contract-"));
    temporaryDirectories.push(cwd);

    await expect(
      validateCodexStagePostconditions({
        stage: "feature-planning",
        issueNumber: 211,
        cwd,
        finalResponse: "I completed the analysis but did not write the requested file.",
      })
    ).rejects.toThrow(
      /planning-211\.json.*Codex final response: "I completed the analysis but did not write the requested file\."/
    );
  });

  it("resolves packaged progressive-disclosure paths outside the consumer repository", () => {
    const skillDirectory = "/extension/dist/skills/nightgauge-feature-planning";
    const rewritten = rewriteStageSkillPaths(
      [
        "Read `skills/nightgauge-feature-planning/_includes/plan.md` now.",
        "See `skills/_shared/PREFLIGHT.md`.",
      ].join("\n"),
      "feature-planning",
      skillDirectory
    );

    expect(rewritten).toContain(
      "/extension/dist/skills/nightgauge-feature-planning/_includes/plan.md"
    );
    expect(rewritten).toContain("/extension/dist/skills/_shared/PREFLIGHT.md");
    expect(rewritten).not.toContain("`skills/nightgauge-feature-planning/");
  });
});
