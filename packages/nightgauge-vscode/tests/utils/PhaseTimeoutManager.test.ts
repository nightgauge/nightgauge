/**
 * Tests for PhaseTimeoutManager
 *
 * Verifies phase timeout monitoring and stale detection behavior:
 * - classifyPhase keyword matching
 * - Stale detection fires after configured inactivity
 * - Activity resets prevent premature stale detection
 * - Hard timeout fires at the configured limit
 * - Phase completion cancels pending timers
 * - Disabled mode suppresses all events
 * - Multiple phase transitions clear previous timers
 *
 * @see Issue #1187 - Add pipeline phase cancel/timeout support
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("vscode", () => ({
  EventEmitter: class MockEventEmitter {
    private listeners: Array<(...args: any[]) => void> = [];
    event = (listener: (...args: any[]) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          this.listeners = this.listeners.filter((l) => l !== listener);
        },
      };
    };
    fire = (data: any) => {
      this.listeners.forEach((l) => l(data));
    };
    dispose = () => {
      this.listeners = [];
    };
  },
}));

vi.mock("@nightgauge/sdk", () => ({}));

type PipelineStage = string;

import {
  classifyPhase,
  PhaseTimeoutManager,
  DEFAULT_PHASE_TIMEOUT_CONFIG,
  type PhaseTimeoutConfig,
  type PhaseTimeoutEvent,
  type PhaseStaleEvent,
} from "../../src/utils/PhaseTimeoutManager";

describe("classifyPhase", () => {
  it('should classify "read-planning-context" as context', () => {
    expect(classifyPhase("read-planning-context")).toBe("context");
  });

  it('should classify "implementation" as implementation', () => {
    expect(classifyPhase("implementation")).toBe("implementation");
  });

  it('should classify "testing" as testing', () => {
    expect(classifyPhase("testing")).toBe("testing");
  });

  it('should classify "write-dev-context" as context_write', () => {
    expect(classifyPhase("write-dev-context")).toBe("context_write");
  });

  it('should classify "quality-review" as implementation (default fallback)', () => {
    expect(classifyPhase("quality-review")).toBe("implementation");
  });

  it('should classify phase names containing "context" as context', () => {
    expect(classifyPhase("load-context")).toBe("context");
  });

  it('should classify phase names containing "test" as testing', () => {
    expect(classifyPhase("run-tests")).toBe("testing");
  });

  it('should classify phase names containing "write" as context_write', () => {
    expect(classifyPhase("write-output")).toBe("context_write");
  });

  it("should fall back to implementation for unknown phase names", () => {
    expect(classifyPhase("unknown-phase")).toBe("implementation");
    expect(classifyPhase("some-random-step")).toBe("implementation");
  });
});

describe("DEFAULT_PHASE_TIMEOUT_CONFIG", () => {
  it("should be enabled by default", () => {
    expect(DEFAULT_PHASE_TIMEOUT_CONFIG.enabled).toBe(true);
  });

  it("should have stale_detection_ms defined", () => {
    expect(typeof DEFAULT_PHASE_TIMEOUT_CONFIG.stale_detection_ms).toBe("number");
    expect(DEFAULT_PHASE_TIMEOUT_CONFIG.stale_detection_ms).toBeGreaterThan(0);
  });

  it("should have max_auto_retries defined", () => {
    expect(typeof DEFAULT_PHASE_TIMEOUT_CONFIG.max_auto_retries).toBe("number");
    expect(DEFAULT_PHASE_TIMEOUT_CONFIG.max_auto_retries).toBeGreaterThanOrEqual(0);
  });

  it("should have positive timeout defaults for all phase types", () => {
    const { defaults } = DEFAULT_PHASE_TIMEOUT_CONFIG;
    expect(defaults.context).toBeGreaterThan(0);
    expect(defaults.implementation).toBeGreaterThan(0);
    expect(defaults.testing).toBeGreaterThan(0);
    expect(defaults.context_write).toBeGreaterThan(0);
  });
});

describe("PhaseTimeoutManager — stale detection", () => {
  const STALE_MS = 5000;
  const TIMEOUT_MS = 30000;

  let manager: PhaseTimeoutManager;
  const config: Partial<PhaseTimeoutConfig> = {
    enabled: true,
    stale_detection_ms: STALE_MS,
    max_auto_retries: 1,
    defaults: {
      context: TIMEOUT_MS,
      implementation: TIMEOUT_MS,
      testing: TIMEOUT_MS,
      context_write: TIMEOUT_MS,
    },
    per_stage: {},
  };

  beforeEach(() => {
    vi.useFakeTimers();
    manager = new PhaseTimeoutManager(config);
  });

  afterEach(() => {
    manager.dispose();
    vi.useRealTimers();
  });

  it("should fire onPhaseStale after stale_detection_ms of inactivity", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.startPhase(stage, "implementation");

    expect(staleEvents).toHaveLength(0);

    vi.advanceTimersByTime(STALE_MS);

    expect(staleEvents).toHaveLength(1);
    expect(staleEvents[0].stage).toBe(stage);
    expect(staleEvents[0].phaseName).toBe("implementation");
    expect(staleEvents[0].phaseType).toBe("implementation");
    expect(staleEvents[0].inactivityMs).toBeGreaterThanOrEqual(STALE_MS);
  });

  it("should not fire onPhaseStale before stale_detection_ms elapses", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.startPhase(stage, "implementation");

    vi.advanceTimersByTime(STALE_MS - 1);

    expect(staleEvents).toHaveLength(0);
  });

  it("should include correct phaseName and phaseType in stale event", () => {
    const stage: PipelineStage = "issue-pickup";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.startPhase(stage, "read-planning-context");

    vi.advanceTimersByTime(STALE_MS);

    expect(staleEvents[0].phaseName).toBe("read-planning-context");
    expect(staleEvents[0].phaseType).toBe("context");
  });
});

describe("PhaseTimeoutManager — activity resets", () => {
  const STALE_MS = 5000;
  const TIMEOUT_MS = 60000;

  let manager: PhaseTimeoutManager;
  const config: Partial<PhaseTimeoutConfig> = {
    enabled: true,
    stale_detection_ms: STALE_MS,
    max_auto_retries: 1,
    defaults: {
      context: TIMEOUT_MS,
      implementation: TIMEOUT_MS,
      testing: TIMEOUT_MS,
      context_write: TIMEOUT_MS,
    },
    per_stage: {},
  };

  beforeEach(() => {
    vi.useFakeTimers();
    manager = new PhaseTimeoutManager(config);
  });

  afterEach(() => {
    manager.dispose();
    vi.useRealTimers();
  });

  it("should not fire stale when activity resets the timer before stale_detection_ms", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.startPhase(stage, "implementation");

    // Advance partially — not yet stale
    vi.advanceTimersByTime(STALE_MS - 1000);
    expect(staleEvents).toHaveLength(0);

    // Reset activity — stale timer restarts
    manager.resetActivityTimer();

    // Advance past the original stale threshold from start, but not from reset
    vi.advanceTimersByTime(STALE_MS - 1000);
    expect(staleEvents).toHaveLength(0);
  });

  it("should fire stale after stale_detection_ms from last activity reset", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.startPhase(stage, "implementation");

    // Advance partially, reset activity
    vi.advanceTimersByTime(STALE_MS - 1000);
    manager.resetActivityTimer();

    // Advance stale_detection_ms from the reset point
    vi.advanceTimersByTime(STALE_MS);

    expect(staleEvents).toHaveLength(1);
  });

  it("should not fire stale when multiple resets occur", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.startPhase(stage, "implementation");

    // Simulate repeated activity that keeps resetting the timer
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(STALE_MS - 500);
      manager.resetActivityTimer();
    }

    // Stale should not have fired yet
    expect(staleEvents).toHaveLength(0);
  });
});

describe("PhaseTimeoutManager — hard timeout", () => {
  const STALE_MS = 5000;
  const TIMEOUT_MS = 20000;

  let manager: PhaseTimeoutManager;
  const config: Partial<PhaseTimeoutConfig> = {
    enabled: true,
    stale_detection_ms: STALE_MS,
    max_auto_retries: 1,
    defaults: {
      context: TIMEOUT_MS,
      implementation: TIMEOUT_MS,
      testing: TIMEOUT_MS,
      context_write: TIMEOUT_MS,
    },
    per_stage: {},
  };

  beforeEach(() => {
    vi.useFakeTimers();
    manager = new PhaseTimeoutManager(config);
  });

  afterEach(() => {
    manager.dispose();
    vi.useRealTimers();
  });

  it("should fire onPhaseTimeout after the hard timeout elapses", () => {
    const stage: PipelineStage = "feature-dev";
    const timeoutEvents: PhaseTimeoutEvent[] = [];

    manager.onPhaseTimeout((event) => timeoutEvents.push(event));
    manager.startPhase(stage, "implementation");

    vi.advanceTimersByTime(TIMEOUT_MS);

    expect(timeoutEvents).toHaveLength(1);
    expect(timeoutEvents[0].stage).toBe(stage);
    expect(timeoutEvents[0].phaseName).toBe("implementation");
    expect(timeoutEvents[0].phaseType).toBe("implementation");
    expect(timeoutEvents[0].elapsedMs).toBeGreaterThanOrEqual(TIMEOUT_MS);
  });

  it("should not fire onPhaseTimeout before the hard timeout elapses", () => {
    const stage: PipelineStage = "feature-dev";
    const timeoutEvents: PhaseTimeoutEvent[] = [];

    manager.onPhaseTimeout((event) => timeoutEvents.push(event));
    manager.startPhase(stage, "implementation");

    vi.advanceTimersByTime(TIMEOUT_MS - 1);

    expect(timeoutEvents).toHaveLength(0);
  });

  it("should fire onPhaseTimeout even when activity resets stale timer", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];
    const timeoutEvents: PhaseTimeoutEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.onPhaseTimeout((event) => timeoutEvents.push(event));
    manager.startPhase(stage, "implementation");

    // Keep resetting activity (prevents stale from firing)
    vi.advanceTimersByTime(STALE_MS - 500);
    manager.resetActivityTimer();
    vi.advanceTimersByTime(STALE_MS - 500);
    manager.resetActivityTimer();

    // Advance to the full hard timeout from phase start
    vi.advanceTimersByTime(TIMEOUT_MS);

    // Hard timeout fires regardless of activity resets
    expect(timeoutEvents).toHaveLength(1);
  });
});

describe("PhaseTimeoutManager — phase completion cancels timers", () => {
  const STALE_MS = 5000;
  const TIMEOUT_MS = 30000;

  let manager: PhaseTimeoutManager;
  const config: Partial<PhaseTimeoutConfig> = {
    enabled: true,
    stale_detection_ms: STALE_MS,
    max_auto_retries: 1,
    defaults: {
      context: TIMEOUT_MS,
      implementation: TIMEOUT_MS,
      testing: TIMEOUT_MS,
      context_write: TIMEOUT_MS,
    },
    per_stage: {},
  };

  beforeEach(() => {
    vi.useFakeTimers();
    manager = new PhaseTimeoutManager(config);
  });

  afterEach(() => {
    manager.dispose();
    vi.useRealTimers();
  });

  it("should not fire stale after phase is completed", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];
    const timeoutEvents: PhaseTimeoutEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.onPhaseTimeout((event) => timeoutEvents.push(event));

    manager.startPhase(stage, "implementation");
    manager.completePhase(stage, "implementation");

    // Advance well past both stale and hard timeout thresholds
    vi.advanceTimersByTime(TIMEOUT_MS + STALE_MS);

    expect(staleEvents).toHaveLength(0);
    expect(timeoutEvents).toHaveLength(0);
  });

  it("should not fire timeout after phase is completed", () => {
    const stage: PipelineStage = "feature-validate";
    const timeoutEvents: PhaseTimeoutEvent[] = [];

    manager.onPhaseTimeout((event) => timeoutEvents.push(event));

    manager.startPhase(stage, "testing");

    // Complete phase before timeout fires
    vi.advanceTimersByTime(TIMEOUT_MS / 2);
    manager.completePhase(stage, "testing");

    // Advance past the full timeout
    vi.advanceTimersByTime(TIMEOUT_MS);

    expect(timeoutEvents).toHaveLength(0);
  });

  it("should allow starting a new phase after completing the previous one", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));

    manager.startPhase(stage, "implementation");
    manager.completePhase(stage, "implementation");

    // Start a new phase — should work without errors
    manager.startPhase(stage, "testing");

    vi.advanceTimersByTime(STALE_MS);

    // Only the new phase's stale should fire
    expect(staleEvents).toHaveLength(1);
    expect(staleEvents[0].phaseName).toBe("testing");
  });
});

describe("PhaseTimeoutManager — disabled mode", () => {
  let manager: PhaseTimeoutManager;

  beforeEach(() => {
    vi.useFakeTimers();
    manager = new PhaseTimeoutManager({ enabled: false });
  });

  afterEach(() => {
    manager.dispose();
    vi.useRealTimers();
  });

  it("should not fire stale event when disabled", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.startPhase(stage, "implementation");

    vi.advanceTimersByTime(999999);

    expect(staleEvents).toHaveLength(0);
  });

  it("should not fire timeout event when disabled", () => {
    const stage: PipelineStage = "feature-dev";
    const timeoutEvents: PhaseTimeoutEvent[] = [];

    manager.onPhaseTimeout((event) => timeoutEvents.push(event));
    manager.startPhase(stage, "implementation");

    vi.advanceTimersByTime(999999);

    expect(timeoutEvents).toHaveLength(0);
  });

  it("should not throw when calling methods while disabled", () => {
    const stage: PipelineStage = "feature-dev";

    expect(() => {
      manager.startPhase(stage, "implementation");
      manager.resetActivityTimer();
      manager.completePhase(stage, "implementation");
    }).not.toThrow();
  });
});

describe("PhaseTimeoutManager — multiple phases", () => {
  const STALE_MS = 5000;
  const TIMEOUT_MS = 30000;

  let manager: PhaseTimeoutManager;
  const config: Partial<PhaseTimeoutConfig> = {
    enabled: true,
    stale_detection_ms: STALE_MS,
    max_auto_retries: 1,
    defaults: {
      context: TIMEOUT_MS,
      implementation: TIMEOUT_MS,
      testing: TIMEOUT_MS,
      context_write: TIMEOUT_MS,
    },
    per_stage: {},
  };

  beforeEach(() => {
    vi.useFakeTimers();
    manager = new PhaseTimeoutManager(config);
  });

  afterEach(() => {
    manager.dispose();
    vi.useRealTimers();
  });

  it("should clear old timers when starting a new phase on the same stage", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));

    // Start first phase, then immediately start another on the same stage
    manager.startPhase(stage, "implementation");
    manager.startPhase(stage, "testing");

    // Advance just past stale threshold
    vi.advanceTimersByTime(STALE_MS);

    // Only one stale event should fire (for 'testing', not 'implementation')
    expect(staleEvents).toHaveLength(1);
    expect(staleEvents[0].phaseName).toBe("testing");
  });

  it("should track phases independently across different stages", () => {
    const stage1: PipelineStage = "feature-dev";
    const stage2: PipelineStage = "feature-validate";
    const staleEvents: PhaseStaleEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));

    manager.startPhase(stage1, "implementation");
    manager.startPhase(stage2, "testing");

    // Complete stage1's phase before stale fires
    vi.advanceTimersByTime(STALE_MS / 2);
    manager.completePhase(stage1, "implementation");

    // Advance past stale threshold
    vi.advanceTimersByTime(STALE_MS);

    // Only stage2 should have fired stale (stage1 was completed)
    expect(staleEvents).toHaveLength(1);
    expect(staleEvents[0].stage).toBe(stage2);
    expect(staleEvents[0].phaseName).toBe("testing");
  });

  it("should not fire events for a completed phase after starting a new one", () => {
    const stage: PipelineStage = "feature-dev";
    const staleEvents: PhaseStaleEvent[] = [];
    const timeoutEvents: PhaseTimeoutEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.onPhaseTimeout((event) => timeoutEvents.push(event));

    manager.startPhase(stage, "read-planning-context");
    manager.completePhase(stage, "read-planning-context");
    manager.startPhase(stage, "write-dev-context");

    vi.advanceTimersByTime(STALE_MS);

    // Only the new phase's stale event fires — not the completed one
    expect(staleEvents).toHaveLength(1);
    expect(staleEvents[0].phaseName).toBe("write-dev-context");
    expect(staleEvents[0].phaseType).toBe("context_write");
  });
});

describe("PhaseTimeoutManager — dispose", () => {
  const STALE_MS = 5000;
  const TIMEOUT_MS = 30000;

  const config: Partial<PhaseTimeoutConfig> = {
    enabled: true,
    stale_detection_ms: STALE_MS,
    max_auto_retries: 1,
    defaults: {
      context: TIMEOUT_MS,
      implementation: TIMEOUT_MS,
      testing: TIMEOUT_MS,
      context_write: TIMEOUT_MS,
    },
    per_stage: {},
  };

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should cancel all pending timers on dispose", () => {
    const manager = new PhaseTimeoutManager(config);
    const staleEvents: PhaseStaleEvent[] = [];
    const timeoutEvents: PhaseTimeoutEvent[] = [];

    manager.onPhaseStale((event) => staleEvents.push(event));
    manager.onPhaseTimeout((event) => timeoutEvents.push(event));

    manager.startPhase("feature-dev", "implementation");
    manager.dispose();

    vi.advanceTimersByTime(TIMEOUT_MS + STALE_MS);

    expect(staleEvents).toHaveLength(0);
    expect(timeoutEvents).toHaveLength(0);
  });

  it("should not throw when disposed multiple times", () => {
    const manager = new PhaseTimeoutManager(config);

    expect(() => {
      manager.dispose();
      manager.dispose();
      manager.dispose();
    }).not.toThrow();
  });
});
