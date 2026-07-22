import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  LmStudioService,
  normalizeManagementBaseUrl,
  normalizeOpenAiBaseUrl,
} from "../../src/services/LmStudioService";

let execFileHandler:
  ((file: string, args: string[]) => Promise<{ stdout?: string; stderr?: string }>) | null = null;

vi.mock("child_process", () => ({
  execFile: vi.fn((...args: any[]) => {
    const file = args[0] as string;
    const cliArgs = args[1] as string[];
    const callback = args.find((arg: unknown) => typeof arg === "function") as
      ((error: Error | null, result?: { stdout: string; stderr: string }) => void) | undefined;
    const handler = execFileHandler ?? (async () => ({ stdout: "", stderr: "" }));
    handler(file, cliArgs)
      .then((result) =>
        callback?.(null, { stdout: result.stdout ?? "", stderr: result.stderr ?? "" })
      )
      .catch((error) => callback?.(error));
  }),
}));

describe("LmStudioService", () => {
  beforeEach(() => {
    execFileHandler = null;
    vi.stubGlobal("fetch", vi.fn());
  });

  it("normalizes OpenAI-compatible base URLs to the management API base", () => {
    expect(normalizeManagementBaseUrl("http://localhost:1234/v1")).toBe(
      "http://localhost:1234/api/v1"
    );
    expect(normalizeManagementBaseUrl("http://localhost:1234/api/v1")).toBe(
      "http://localhost:1234/api/v1"
    );
    expect(normalizeOpenAiBaseUrl("http://localhost:1234")).toBe("http://localhost:1234/v1");
    expect(normalizeOpenAiBaseUrl("http://localhost:1234/api/v1")).toBe("http://localhost:1234/v1");
  });

  it("lists models from the CLI and marks loaded models first", async () => {
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    fetchMock.mockResolvedValue({
      ok: true,
      status: 200,
      statusText: "OK",
      json: async () => ({
        models: [
          {
            key: "a-model",
            max_context_length: 65536,
            loaded_instances: [{ config: { context_length: 32768 } }],
          },
          {
            key: "b-model",
            max_context_length: 131072,
            loaded_instances: [],
          },
        ],
      }),
    } as Response);

    execFileHandler = async (_file, args) => {
      if (args[0] === "ls") {
        return {
          stdout: JSON.stringify([{ id: "b-model" }, { id: "a-model" }]),
        };
      }
      if (args[0] === "ps") {
        return {
          stdout: JSON.stringify([{ id: "a-model" }]),
        };
      }
      throw new Error(`unexpected args: ${args.join(" ")}`);
    };

    const service = new LmStudioService();
    const models = await service.listModels("http://localhost:1234/v1", "lm-studio");

    expect(models).toEqual([
      {
        id: "a-model",
        loaded: true,
        maxContextLength: 65536,
        currentContextLength: 32768,
      },
      { id: "b-model", loaded: false, maxContextLength: 131072, currentContextLength: undefined },
    ]);
  });

  it("starts the server through the lms CLI", async () => {
    const calls: Array<{ file: string; args: string[] }> = [];
    execFileHandler = async (file, args) => {
      calls.push({ file, args });
      if (args[0] === "server" && args[1] === "status") {
        return {
          stdout: calls.length === 1 ? "The server is not running." : "The server is running.",
        };
      }
      return { stdout: "started" };
    };

    const service = new LmStudioService();
    await service.startServer();

    expect(calls).toEqual([
      { file: "lms", args: ["server", "status"] },
      { file: "lms", args: ["server", "start"] },
      { file: "lms", args: ["server", "status"] },
    ]);
  });

  it("treats the server as ready when the HTTP endpoint is reachable even if CLI status says not running", async () => {
    const calls: Array<{ file: string; args: string[] }> = [];
    execFileHandler = async (file, args) => {
      calls.push({ file, args });
      if (args[0] === "server" && args[1] === "status") {
        return { stdout: "The server is not running." };
      }
      throw new Error(`unexpected args: ${args.join(" ")}`);
    };

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    fetchMock.mockResolvedValue({
      ok: true,
      status: 200,
      statusText: "OK",
      json: async () => ({ data: [] }),
    } as Response);

    const service = new LmStudioService();
    await service.startServer("http://127.0.0.1:1234/v1", "lm-studio");

    expect(calls).toEqual([{ file: "lms", args: ["server", "status"] }]);
    expect(fetchMock).toHaveBeenCalledWith("http://127.0.0.1:1234/v1/models", {
      headers: { Authorization: "Bearer lm-studio" },
    });
  });

  it("waits for HTTP readiness after starting the server", async () => {
    const calls: Array<{ file: string; args: string[] }> = [];
    execFileHandler = async (file, args) => {
      calls.push({ file, args });
      if (args[0] === "server" && args[1] === "status") {
        return { stdout: "The server is not running." };
      }
      if (args[0] === "server" && args[1] === "start") {
        return { stdout: "started" };
      }
      throw new Error(`unexpected args: ${args.join(" ")}`);
    };

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    fetchMock
      .mockRejectedValueOnce(new Error("connect ECONNREFUSED"))
      .mockRejectedValueOnce(new Error("connect ECONNREFUSED"))
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: "OK",
        json: async () => ({ data: [] }),
      } as Response);

    const service = new LmStudioService();
    await service.startServer("http://127.0.0.1:1234/v1", "lm-studio");

    expect(calls[0]).toEqual({ file: "lms", args: ["server", "status"] });
    expect(calls[1]).toEqual({ file: "lms", args: ["server", "start"] });
    expect(
      calls.filter((call) => call.args[0] === "server" && call.args[1] === "status").length
    ).toBeGreaterThanOrEqual(2);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("falls back to the HTTP model list when CLI listing fails", async () => {
    execFileHandler = async () => {
      throw new Error("cli unavailable");
    };
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    fetchMock.mockResolvedValue({
      ok: true,
      status: 200,
      statusText: "OK",
      json: async () => ({
        data: [
          { id: "b-model", loaded: false },
          { id: "a-model", loaded: true },
        ],
      }),
    } as Response);

    const service = new LmStudioService();
    const models = await service.listModels("http://localhost:1234/v1", "lm-studio");

    expect(models).toEqual([
      {
        id: "a-model",
        loaded: true,
        maxContextLength: undefined,
        currentContextLength: undefined,
      },
      {
        id: "b-model",
        loaded: false,
        maxContextLength: undefined,
        currentContextLength: undefined,
      },
    ]);
    expect(fetchMock).toHaveBeenCalledWith("http://localhost:1234/api/v1/models", {
      headers: { Authorization: "Bearer lm-studio" },
    });
  });

  it("falls back to the OpenAI model list when the management API is unavailable", async () => {
    execFileHandler = async () => {
      throw new Error("cli unavailable");
    };
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    fetchMock
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        statusText: "Not Found",
      } as Response)
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: "OK",
        json: async () => ({
          data: [{ id: "openai/gpt-oss-20b" }],
        }),
      } as Response);

    const service = new LmStudioService();
    const models = await service.listModels("http://127.0.0.1:1234/v1", "lm-studio");

    expect(models).toEqual([
      {
        id: "openai/gpt-oss-20b",
        loaded: true,
        maxContextLength: undefined,
        currentContextLength: undefined,
      },
    ]);
    expect(fetchMock).toHaveBeenNthCalledWith(1, "http://127.0.0.1:1234/api/v1/models", {
      headers: { Authorization: "Bearer lm-studio" },
    });
    expect(fetchMock).toHaveBeenNthCalledWith(2, "http://127.0.0.1:1234/v1/models", {
      headers: { Authorization: "Bearer lm-studio" },
    });
  });

  it("loads a model with context length through the lms CLI", async () => {
    const calls: Array<{ file: string; args: string[] }> = [];
    execFileHandler = async (file, args) => {
      calls.push({ file, args });
      return { stdout: "loaded" };
    };

    const service = new LmStudioService();
    await service.loadModel("openai/gpt-oss-20b", 65536);

    expect(calls).toEqual([
      {
        file: "lms",
        args: ["load", "openai/gpt-oss-20b", "--context-length=65536"],
      },
    ]);
  });
});
