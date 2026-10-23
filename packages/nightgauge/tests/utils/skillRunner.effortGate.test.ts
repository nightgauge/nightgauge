/**
 * skillRunner.effortGate.test.ts (#569)
 *
 * `preflightAdapterEffort` — the dispatch-site wrapper the grok/codex env
 * sections call BEFORE spawn. It turns a registry effort-gate rejection into
 * the same `[stage:effort-unsupported]`-classified Error shape the Claude
 * branch's effort preflight reports through the failure envelope, so a
 * provider-global effort the resolved model does not declare fails the stage
 * closed instead of reaching the CLI (#532's failure signature).
 */
import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }] },
  window: {
    terminals: [],
    createTerminal: vi.fn(() => ({ show: vi.fn(), sendText: vi.fn() })),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  extensions: { getExtension: vi.fn(() => null) },
}));

import { preflightAdapterEffort } from "../../src/utils/skillRunner";

describe("preflightAdapterEffort — pre-spawn adapter effort gate (#569)", () => {
  it("fails closed with the classified envelope on the #532 repro", () => {
    // Provider-global xhigh against grok-4.5, whose ladder tops out at high.
    const { error, warning } = preflightAdapterEffort("grok", "xhigh", "grok-4.5", "feature-dev");
    expect(warning).toBeUndefined();
    expect(error).toBeInstanceOf(Error);
    // Classified marker + model + requested effort + declared ladder.
    expect(error?.message).toContain("[stage:effort-unsupported]");
    expect(error?.message).toContain("adapter=grok");
    expect(error?.message).toContain("model=grok-4.5");
    expect(error?.message).toContain("effort=xhigh");
    expect(error?.message).toContain("supported=low,medium,high");
  });

  it("returns neither error nor warning for a declared rung", () => {
    const result = preflightAdapterEffort("grok", "high", "grok-4.5", "feature-dev");
    expect(result.error).toBeUndefined();
    expect(result.warning).toBeUndefined();
  });

  it("returns a warning (no error) for an unknown model — fail open (#336)", () => {
    const result = preflightAdapterEffort("grok", "xhigh", "some-local-model", "feature-dev");
    expect(result.error).toBeUndefined();
    expect(result.warning).toContain("no registry descriptor");
  });

  it("no explicit effort → clean pass", () => {
    const result = preflightAdapterEffort("grok", undefined, "grok-4.5", "feature-dev");
    expect(result.error).toBeUndefined();
    expect(result.warning).toBeUndefined();
  });

  it("gates codex dispatches through the same registry authority", () => {
    const { error } = preflightAdapterEffort("codex", "xhigh", "gpt-5.4", "feature-dev");
    expect(error?.message).toContain("[stage:effort-unsupported]");
    expect(error?.message).toContain("adapter=codex");
    // gpt-5.6-sol declares xhigh — passes.
    expect(
      preflightAdapterEffort("codex", "xhigh", "gpt-5.6-sol", "feature-dev").error
    ).toBeUndefined();
  });
});
