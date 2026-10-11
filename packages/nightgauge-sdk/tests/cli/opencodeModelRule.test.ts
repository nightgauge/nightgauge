/**
 * The opencode adapter's pre-spawn model check and the model registry's
 * parser hold one rule (#1707): the adapter delegates to
 * `isValidOpenCodeModel`, so a model one accepts the other cannot refuse.
 */
import { describe, expect, it } from "vitest";
import { validateOpenCodeModel } from "../../src/cli/adapters/OpenCodeAdapter.js";
import { isValidOpenCodeModel, parseOpenCodeModel } from "../../src/eval/modelRegistry.js";

const CASES: Array<[model: string, valid: boolean]> = [
  ["anthropic/claude-sonnet-5", true],
  ["lmstudio/qwen/qwen3.8-27b", true],
  ["my-endpoint/llama-3.3-70b", true],
  ["LMStudio/qwen", false],
  ["lm.studio/qwen", false],
  ["lm_studio/qwen", false],
  ["-anthropic/claude", false],
  ["anthropic/-claude", false],
  ["anthropic/", false],
  ["/claude", false],
  ["claude-sonnet-5", false],
  ["sonnet", false],
  ["", false],
  [" anthropic/claude", false],
  ["anthropic/claude sonnet", false],
  ["anthropic/claude\u0085", false],
  ["anthropic/claude ", false],
  ["anthropic/claude\u007f", false],
];

describe("opencode model rule", () => {
  it.each(CASES)("%j valid=%s in the registry and the adapter alike", (model, valid) => {
    expect(isValidOpenCodeModel(model)).toBe(valid);
    if (valid) {
      expect(() => validateOpenCodeModel(model)).not.toThrow();
      expect(parseOpenCodeModel(model).bareId).not.toBe("");
    } else {
      expect(() => validateOpenCodeModel(model)).toThrow(/not valid for the opencode adapter/);
      expect(parseOpenCodeModel(model).bareId).toBe("");
    }
  });
});
