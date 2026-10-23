/**
 * Tests for the pure Getting Started onboarding activation gate (#4155).
 */

import { describe, it, expect } from "vitest";
import { shouldAutoShowGettingStarted } from "../../../src/views/onboarding/onboardingGate";

describe("shouldAutoShowGettingStarted", () => {
  it("returns true when the repo is not initialized and the panel has never auto-shown", () => {
    expect(shouldAutoShowGettingStarted({ repoInitialized: false, alreadyShown: false })).toBe(
      true
    );
  });

  it("returns false when the repo is already initialized, even if never shown", () => {
    expect(shouldAutoShowGettingStarted({ repoInitialized: true, alreadyShown: false })).toBe(
      false
    );
  });

  it("returns false when the panel has already auto-shown, even if the repo is uninitialized", () => {
    expect(shouldAutoShowGettingStarted({ repoInitialized: false, alreadyShown: true })).toBe(
      false
    );
  });

  it("returns false when both the repo is initialized and the panel has already shown", () => {
    expect(shouldAutoShowGettingStarted({ repoInitialized: true, alreadyShown: true })).toBe(false);
  });
});
