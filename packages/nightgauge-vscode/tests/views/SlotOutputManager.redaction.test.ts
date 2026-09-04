/**
 * SlotOutputManager.redaction.test.ts — #1335.
 *
 * THE INCIDENT. During a clean-install CI run the `feature-dev` agent echoed
 * the raw value of GH_TOKEN. The value arrived inside a Bash **tool_result**,
 * SlotOutputManager wrote it verbatim to the slot's output channel AND handed
 * it verbatim to `onOutput`, and #1330 copies that channel into the run's
 * evidence artifact — so a live `github_pat_` credential was published in a
 * public-repo workflow artifact.
 *
 * THE SHAPE THAT MATTERS. Redaction happens at the SINK, not per stream-json
 * block. By the time text reaches these methods it has already been flattened
 * out of assistant text, tool_use input, tool_result content and stderr alike,
 * so one redaction covers every block by construction. A per-block sanitizer
 * has to enumerate the shapes, and the shape nobody enumerated is exactly how
 * this leaked — which is why the cases below feed the block shapes through the
 * same sink and assert on the sink's two outputs.
 *
 * BOTH OUTPUTS ARE ASSERTED. The channel is what a user sees; `onOutput` is
 * what the evidence artifact reads. It was the artifact that leaked, so a fix
 * that only cleaned the channel would have left the incident intact.
 *
 * Every credential below is synthetic.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

const appendLine = vi.fn();
vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn((name: string) => ({
      appendLine,
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
      name,
    })),
  },
}));

import { SlotOutputManager } from "../../src/views/SlotOutputManager";

const ISSUE = 1335;

/** Synthetic secrets — shaped like the real thing, valid nowhere. */
const FAKE = {
  githubPat: "github_pat_11ABCDEFG0aaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbCCCCCCCCCCC",
  ghu: "ghu_16CharsMinimumAAAAAAAAAAAAAAAAAAAA",
  anthropic: "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
};

function setup() {
  const onOutput = vi.fn();
  const manager = new SlotOutputManager();
  // setCallbacks, NOT a constructor argument. The first version of this file
  // passed `{ onOutput }` to the constructor, which takes none — a type error
  // vitest transpiles straight past, leaving `callbacks` as `{}` so onOutput
  // was never invoked and every assertion about the evidence-artifact half
  // passed vacuously. That half is the one that leaked.
  manager.setCallbacks({ onOutput });
  manager.createSlotChannel(0, ISSUE, "Leak regression");
  appendLine.mockClear();
  return { manager, onOutput };
}

/** Fails loudly if the callback never fired — the assertions below read it. */
function callbackText(onOutput: ReturnType<typeof vi.fn>): string {
  if (onOutput.mock.calls.length === 0) {
    throw new Error(
      "onOutput was never called — the evidence-artifact half of the sink is untested, " +
        "which is exactly how the first version of this suite passed vacuously"
    );
  }
  return onOutput.mock.calls.map((c) => String(c[2])).join("\n");
}

/** Everything the sink wrote, from both of its outputs. */
function written(onOutput: ReturnType<typeof vi.fn>): string {
  return [...appendLine.mock.calls.map((c) => String(c[0])), callbackText(onOutput)].join("\n");
}

describe("SlotOutputManager redacts secrets before they reach the channel (#1335)", () => {
  beforeEach(() => vi.clearAllMocks());

  it("redacts a github_pat_ value carried inside a tool_result — the incident verbatim", () => {
    const { manager, onOutput } = setup();

    // The shape that leaked: a Bash tool result echoing the environment.
    manager.appendOutput(
      ISSUE,
      `{"type":"tool_result","content":[{"type":"text","text":"GH_TOKEN=${FAKE.githubPat}\\nHOME=/root"}]}`
    );

    const out = written(onOutput);
    expect(out, "the token survived into the channel or the evidence callback").not.toContain(
      FAKE.githubPat
    );
    expect(out).toContain("[REDACTED");
  });

  it("redacts the same value on every block shape the stream can carry", () => {
    // Per-block sanitizing is what failed: the block nobody enumerated is the
    // one that leaks. Sink-level redaction makes the block shape irrelevant,
    // and this pins that property rather than the individual shapes.
    for (const block of [
      `assistant text: here is the token ${FAKE.githubPat}`,
      `{"type":"tool_use","input":{"command":"curl -H 'Authorization: token ${FAKE.githubPat}'"}}`,
      `{"type":"tool_result","content":"${FAKE.githubPat}"}`,
      `stderr: remote: Invalid credentials ${FAKE.githubPat}`,
    ]) {
      const { manager, onOutput } = setup();
      manager.appendOutput(ISSUE, block);
      expect(written(onOutput), `leaked from: ${block.slice(0, 40)}`).not.toContain(FAKE.githubPat);
    }
  });

  it("redacts on the error path too", () => {
    // appendError is a separate method with its own writes; instrumenting only
    // appendOutput would leave stderr — where credentials most often surface —
    // unprotected.
    const { manager, onOutput } = setup();
    manager.appendError(ISSUE, `fatal: authentication failed for ${FAKE.githubPat}`);

    const out = written(onOutput);
    expect(out).not.toContain(FAKE.githubPat);
    expect(appendLine.mock.calls.map((c) => String(c[0])).join("\n")).toContain("[ERROR]");
  });

  it("redacts ghu_ and sk-ant- shapes", () => {
    // ghu_ (user-to-server) was absent from the token prefix list until #1335,
    // and an Anthropic key was being reported as an OpenAI one — which is the
    // name an incident responder reads to know what to rotate.
    const { manager, onOutput } = setup();
    manager.appendOutput(ISSUE, `${FAKE.ghu} and ${FAKE.anthropic}`);

    const out = written(onOutput);
    expect(out).not.toContain(FAKE.ghu);
    expect(out).not.toContain(FAKE.anthropic);
    expect(out).toContain("[REDACTED:ANTHROPIC_KEY]");
  });

  it("redacts the CALLBACK payload, not just the channel", () => {
    // The channel is what a user sees; onOutput is what #1330 copies into the
    // evidence artifact, and it was the ARTIFACT that leaked. Asserted on its
    // own so that redacting only the channel — which the combined assertions
    // above would still pass — goes red here.
    const { manager, onOutput } = setup();
    manager.appendOutput(ISSUE, `tool_result: GH_TOKEN=${FAKE.githubPat}`);

    expect(callbackText(onOutput)).not.toContain(FAKE.githubPat);
    expect(callbackText(onOutput)).toContain("[REDACTED:GH_TOKEN]");
  });

  it("redacts the CALLBACK payload on the error path too", () => {
    const { manager, onOutput } = setup();
    manager.appendError(ISSUE, `fatal: ${FAKE.githubPat}`);

    expect(callbackText(onOutput)).not.toContain(FAKE.githubPat);
  });

  it("leaves ordinary output untouched", () => {
    // A redactor that eats normal output would be turned off.
    const { manager, onOutput } = setup();
    const line = "feature-dev: 4 files changed, 120 insertions(+)";
    manager.appendOutput(ISSUE, line);
    expect(written(onOutput)).toContain(line);
  });
});
