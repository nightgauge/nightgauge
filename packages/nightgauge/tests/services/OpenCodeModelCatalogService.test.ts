import { beforeEach, describe, expect, it, vi } from "vitest";
import { OpenCodeModelCatalogService } from "../../src/services/OpenCodeModelCatalogService";

let execFileHandler:
  | ((
      file: string,
      args: string[],
      options: Record<string, unknown>
    ) => Promise<{ stdout?: string; stderr?: string }>)
  | null = null;
let lastExecFileCall: { file: string; args: string[]; options: Record<string, unknown> } | null =
  null;

vi.mock("child_process", () => ({
  execFile: vi.fn((...callArgs: unknown[]) => {
    const file = callArgs[0] as string;
    const args = callArgs[1] as string[];
    const options = (callArgs.find(
      (arg) => typeof arg === "object" && arg !== null && !Array.isArray(arg)
    ) ?? {}) as Record<string, unknown>;
    const callback = callArgs.find((arg) => typeof arg === "function") as
      ((error: Error | null, result?: { stdout: string; stderr: string }) => void) | undefined;

    lastExecFileCall = { file, args, options };
    const handler = execFileHandler ?? (async () => ({ stdout: "", stderr: "" }));
    handler(file, args, options)
      .then((result) =>
        callback?.(null, { stdout: result.stdout ?? "", stderr: result.stderr ?? "" })
      )
      .catch((error) => callback?.(error));
  }),
}));

describe("OpenCodeModelCatalogService", () => {
  beforeEach(() => {
    execFileHandler = null;
    lastExecFileCall = null;
  });

  it("spawns `opencode models` via execFile with a 10s timeout and the fetch/autoupdate env flags", async () => {
    execFileHandler = async () => ({ stdout: "lmstudio/qwen/qwen3.8-27b\n" });

    const service = new OpenCodeModelCatalogService();
    await service.listModels();

    expect(lastExecFileCall).not.toBeNull();
    expect(lastExecFileCall!.file).toBe("opencode");
    expect(lastExecFileCall!.args).toEqual(["models"]);
    expect(lastExecFileCall!.options.timeout).toBe(10000);
    expect(lastExecFileCall!.options.env).toMatchObject({
      OPENCODE_DISABLE_MODELS_FETCH: "1",
      OPENCODE_DISABLE_AUTOUPDATE: "1",
    });
  });

  it("keeps only provider/model lines, de-duplicates, and caps at 500", async () => {
    const lines = Array.from({ length: 600 }, (_, i) => `lmstudio/model-${i}`);
    execFileHandler = async () => ({
      stdout: [...lines, ...lines.slice(0, 10), "--auto", "x y", "", "not-a-model"].join("\n"),
    });

    const service = new OpenCodeModelCatalogService();
    const entries = await service.listModels();

    expect(entries).toHaveLength(500);
    expect(entries.every((e) => e.selectable)).toBe(true);
    expect(entries.some((e) => e.id === "--auto")).toBe(false);
    expect(entries.some((e) => e.id === "x y")).toBe(false);
  });

  it("puts the configured model first, marked (Configured)", async () => {
    execFileHandler = async () => ({
      stdout: "anthropic/claude-sonnet-5\nlmstudio/qwen/qwen3.8-27b\n",
    });

    const service = new OpenCodeModelCatalogService();
    const entries = await service.listModels("lmstudio/qwen/qwen3.8-27b");

    expect(entries[0]).toEqual({
      id: "lmstudio/qwen/qwen3.8-27b",
      label: "lmstudio/qwen/qwen3.8-27b (Configured)",
      selectable: true,
    });
    expect(entries.filter((e) => e.id === "lmstudio/qwen/qwen3.8-27b")).toHaveLength(1);
  });

  it("returns only the configured model plus a non-selectable notice on execFile error", async () => {
    execFileHandler = async () => {
      throw new Error("spawn opencode ENOENT");
    };

    const service = new OpenCodeModelCatalogService();
    const entries = await service.listModels("lmstudio/qwen/qwen3.8-27b");

    expect(entries).toEqual([
      {
        id: "lmstudio/qwen/qwen3.8-27b",
        label: "lmstudio/qwen/qwen3.8-27b (Configured)",
        selectable: true,
      },
      expect.objectContaining({ id: "", selectable: false }),
    ]);
  });

  it("returns only a non-selectable notice on timeout with no configured model", async () => {
    execFileHandler = async () => {
      const error = new Error("Command timed out") as Error & { killed?: boolean };
      error.killed = true;
      throw error;
    };

    const service = new OpenCodeModelCatalogService();
    const entries = await service.listModels();

    expect(entries).toEqual([expect.objectContaining({ id: "", selectable: false })]);
  });

  it("never throws when the CLI output has no valid model lines", async () => {
    execFileHandler = async () => ({ stdout: "no models available\n--flag\n" });

    const service = new OpenCodeModelCatalogService();
    await expect(service.listModels()).resolves.toEqual([
      expect.objectContaining({ id: "", selectable: false }),
    ]);
  });
});
