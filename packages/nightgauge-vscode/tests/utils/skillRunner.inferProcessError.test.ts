import { describe, it, expect } from "vitest";
import { inferProcessError } from "../../src/utils/skillRunner";

describe("inferProcessError", () => {
  it("returns undefined when success=true", () => {
    expect(inferProcessError(true, "", "", 0)).toBeUndefined();
  });

  it("uses stderr tail when stderr is present", () => {
    const err = inferProcessError(false, "prologue\nline-1\nline-2\nfatal: boom", "", 1);
    expect(err?.message).toBe("line-1\nline-2\nfatal: boom");
  });

  // Issue #3406: the synthetic skillRunner kill marker (rate-limit-quota-
  // exhausted, stall-killed, cost-cap-exceeded, etc.) MUST flow through
  // stderr so that downstream classification (the bootstrap regex match
  // and the Go autonomous scheduler's terminalFailureKind routing) can
  // recognize it. Pre-fix the marker only landed in the diagnostic log
  // file, never in the buffer that inferProcessError reads, so result.
  // error.message was just "Pipeline failed at <stage>" and the agent's
  // 1h environmental-failure backoff never engaged.
  it("preserves the rate-limit-quota-exhausted kill marker when subprocess stderr is empty (#3406)", () => {
    // Real-shape kill marker as written by skillRunner at the rate-limit-
    // quota-exhausted kill site. Subprocess wrote nothing; the synthetic
    // marker is the only thing in the stderr buffer.
    const stderr =
      "[skillRunner] Stage [rate-limit-quota-exhausted] idle 56m 0s after rate_limit_event with overage rejected (five_hour bucket; resetsAt=1778367000) — forcibly terminating process after 70m 30s (idle for 56m 16s).\n";
    const err = inferProcessError(false, stderr, "", 143 /* SIGTERM */);
    expect(err).toBeDefined();
    expect(err!.message).toContain("[rate-limit-quota-exhausted]");
    // Bootstrap's terminalFailureKind regex (services.ts) must match this
    // exact substring or the Go-side exemption path won't engage.
    expect(/rate-limit-quota-exhausted/i.test(err!.message)).toBe(true);
  });

  it("preserves the generic stall-kill marker (and other markers) the same way", () => {
    const stderr =
      "[skillRunner] Stage exceeded stall idle threshold (1200s without output) — forcibly terminating process after 25m 0s (idle for 20m 0s).\n";
    const err = inferProcessError(false, stderr, "", 143);
    expect(err).toBeDefined();
    expect(err!.message).toContain("stall idle threshold");
  });

  describe("stream-json result envelope", () => {
    it("prefers terminal {type:result,is_error:true,result:...}", () => {
      const stdout = [
        '{"type":"assistant","message":{"content":[{"type":"text","text":"thinking..."}]}}',
        '{"type":"user","message":{"content":[{"type":"tool_result","content":"file contents here"}]},"tool_use_result":{"type":"text","file":{"filePath":"/a/b/c.ts","content":"...huge file body..."}}}',
        '{"type":"result","subtype":"error_max_turns","is_error":true,"result":"Max turns reached before completion","duration_ms":60000}',
      ].join("\n");
      const err = inferProcessError(false, "", stdout, 1);
      expect(err?.message).toBe("error_max_turns: Max turns reached before completion");
    });

    it("returns undefined when result envelope reports is_error=false", () => {
      const stdout =
        '{"type":"result","subtype":"success","is_error":false,"result":"done","duration_ms":1000}';
      const err = inferProcessError(false, "", stdout, 0);
      // No real error — process exited non-zero but the protocol succeeded.
      expect(err).toBeUndefined();
    });

    it("does not regurgitate tool_use_result payloads as the error message", () => {
      // Regression for the 'ect(healthy).toHaveBeenCalled();' bug — the tail of stdout
      // was a user/tool_result wrapper containing a Read result, and that entire JSON
      // was stringified into an Error message. We now skip those wrappers.
      const stdout = [
        '{"type":"user","message":{"content":[{"type":"tool_result","content":"describe(\\"x\\", ()=>{});"}]},"tool_use_result":{"type":"text","file":{"filePath":"/x/y/z.test.ts","content":"describe(\\"x\\", ()=>{});","numLines":1,"startLine":1,"totalLines":1}}}',
        '{"type":"assistant","message":{"content":[{"type":"text","text":"continuing..."}]}}',
      ].join("\n");
      const err = inferProcessError(false, "", stdout, 1);
      expect(err?.message).not.toContain("tool_use_result");
      expect(err?.message).not.toContain("filePath");
      expect(err?.message).not.toContain("describe(");
    });
  });

  describe("Codex adapter structured JSON", () => {
    it("uses parsed.message when present", () => {
      const stdout = '{"level":"error","message":"Codex authentication required"}';
      const err = inferProcessError(false, "", stdout, 1);
      expect(err?.message).toBe("Codex authentication required");
    });

    it("uses parsed.error when .message is absent", () => {
      const stdout = '{"error":"Codex CLI failed: ENOENT"}';
      const err = inferProcessError(false, "", stdout, 1);
      expect(err?.message).toBe("Codex CLI failed: ENOENT");
    });
  });

  describe("plain-text fallback", () => {
    it("returns the last non-empty line when stdout has no JSON", () => {
      const stdout = "step 1\nstep 2\nsomething failed";
      const err = inferProcessError(false, "", stdout, 1);
      expect(err?.message).toBe("step 1\nstep 2\nsomething failed");
    });

    it("falls back to exit-code message when stdout and stderr are empty", () => {
      const err = inferProcessError(false, "", "", 137);
      expect(err?.message).toBe("Process exited with code 137");
    });
  });
});
