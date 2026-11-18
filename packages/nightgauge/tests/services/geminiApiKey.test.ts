import * as fs from "fs";
import * as path from "path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  geminiApiKeyEnv,
  hasGeminiApiKey,
  loadGeminiApiKey,
  resetGeminiApiKeyForTest,
} from "../../src/services/geminiApiKey";

describe("geminiApiKey", () => {
  const saved = {
    GEMINI_API_KEY: process.env.GEMINI_API_KEY,
    GOOGLE_API_KEY: process.env.GOOGLE_API_KEY,
  };
  beforeEach(() => {
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
    resetGeminiApiKeyForTest();
  });
  afterEach(() => {
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
    resetGeminiApiKeyForTest();
  });

  it("loads the stored key without putting it in process.env", async () => {
    await loadGeminiApiKey({ getApiKey: async () => "stored-gemini-key" });
    expect(process.env.GEMINI_API_KEY).toBeUndefined();
    expect(hasGeminiApiKey()).toBe(true);
    expect(geminiApiKeyEnv()).toEqual({ GEMINI_API_KEY: "stored-gemini-key" });
  });

  it("leaves an operator's exported key in charge", async () => {
    process.env.GEMINI_API_KEY = "exported";
    await loadGeminiApiKey({ getApiKey: async () => "stored-gemini-key" });
    expect(geminiApiKeyEnv()).toEqual({});
    expect(process.env.GEMINI_API_KEY).toBe("exported");
  });

  it("reports no key when neither store has one", async () => {
    await loadGeminiApiKey({ getApiKey: async () => undefined });
    expect(hasGeminiApiKey()).toBe(false);
    expect(geminiApiKeyEnv()).toEqual({});
  });

  it("no extension source writes a Gemini key into process.env", () => {
    const offenders: string[] = [];
    const walk = (dir: string): void => {
      for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory()) walk(p);
        else if (p.endsWith(".ts")) {
          const src = fs.readFileSync(p, "utf8");
          if (/process\.env(\.GEMINI_API_KEY|\[["']GEMINI_API_KEY["']\])\s*=(?!=)/.test(src)) {
            offenders.push(p);
          }
        }
      }
    };
    walk(path.resolve(__dirname, "../../src"));
    expect(offenders).toEqual([]);
  });
});
