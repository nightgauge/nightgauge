/**
 * callSource (#360) — threads a real caller label through the GitHub-API log
 * line so a refresh storm names its origin instead of `src=unknown`, and always
 * restores the prior label so callers don't leak.
 */
import { describe, it, expect, afterEach } from "vitest";
import {
  withCallSource,
  getActiveCallSource,
  setActiveCallSource,
} from "../../src/services/callSource";

describe("callSource (#360)", () => {
  afterEach(() => setActiveCallSource(undefined));

  it("sets the source for the duration of fn and restores it after", async () => {
    let inside: string | undefined;
    await withCallSource("repositories:acme", async () => {
      inside = getActiveCallSource();
    });
    expect(inside).toBe("repositories:acme");
    expect(getActiveCallSource()).toBeUndefined();
  });

  it("restores the PREVIOUS source (nested calls don't clobber the outer label)", async () => {
    await withCallSource("outer", async () => {
      expect(getActiveCallSource()).toBe("outer");
      await withCallSource("inner", async () => {
        expect(getActiveCallSource()).toBe("inner");
      });
      expect(getActiveCallSource()).toBe("outer");
    });
    expect(getActiveCallSource()).toBeUndefined();
  });

  it("restores the source even when fn throws", async () => {
    setActiveCallSource("baseline");
    await expect(
      withCallSource("board-tab:ready", async () => {
        throw new Error("boom");
      })
    ).rejects.toThrow("boom");
    expect(getActiveCallSource()).toBe("baseline");
  });

  it("returns fn's resolved value", async () => {
    const result = await withCallSource("x", async () => 42);
    expect(result).toBe(42);
  });
});
