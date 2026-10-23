/**
 * Doctor status bar item (ADR-025): the verdict as text, a click opens the
 * panel, and background scans run at most every ten minutes without the
 * adapter probe.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DoctorRunResult, DoctorSummary } from "../../../src/services/IpcClientBase";

vi.mock("vscode", () => {
  class EventEmitter<T> {
    private listeners = new Set<(v: T) => void>();
    event = (listener: (v: T) => void) => {
      this.listeners.add(listener);
      return { dispose: () => this.listeners.delete(listener) };
    };
    fire(v: T) {
      for (const l of [...this.listeners]) l(v);
    }
    dispose() {
      this.listeners.clear();
    }
  }
  return {
    EventEmitter,
    StatusBarAlignment: { Left: 1, Right: 2 },
    ThemeColor: class ThemeColor {
      constructor(public id: string) {}
    },
    window: {
      createStatusBarItem: vi.fn(() => ({
        text: "",
        tooltip: "",
        show: vi.fn(),
        hide: vi.fn(),
        dispose: vi.fn(),
      })),
    },
  };
});

import {
  BACKGROUND_INTERVAL_MS,
  DoctorStatusBarItem,
  doctorStatusText,
  RUN_DOCTOR_COMMAND,
} from "../../../src/views/doctor/DoctorStatusBarItem";
import { DoctorService } from "../../../src/views/doctor/DoctorService";

const summary = (blocker: number, warning: number): DoctorSummary => ({
  blocker,
  warning,
  housekeeping: 3,
  info: 1,
});

function result(s: DoctorSummary): DoctorRunResult {
  const findings = [
    ...Array.from({ length: s.blocker }, (_, i) => ({ severity: "blocker", fp: `b${i}` })),
    ...Array.from({ length: s.warning }, (_, i) => ({ severity: "warning", fp: `w${i}` })),
  ].map(({ severity, fp }) => ({
    code: "NGD000",
    check: "check",
    severity: severity as "blocker" | "warning",
    title: "t",
    cause: "c",
    evidence: {},
    docs: "",
    fingerprint: fp.padStart(16, "0"),
    remedies: [],
  }));
  return {
    v: 2,
    findings,
    summary: s,
    healthy: s.blocker === 0,
    exit_code: s.blocker ? 2 : s.warning ? 1 : 0,
    failed_checks: [],
    errors: [],
    warnings: [],
    install_instructions: "",
  };
}

describe("doctorStatusText", () => {
  it("reads healthy with no blocker or warning (housekeeping and info do not count)", () => {
    expect(doctorStatusText(summary(0, 0))).toBe("Nightgauge: ✓ healthy");
  });

  it("counts warnings", () => {
    expect(doctorStatusText(summary(0, 2))).toBe("Nightgauge: 2 warnings");
    expect(doctorStatusText(summary(0, 1))).toBe("Nightgauge: 1 warning");
  });

  it("counts blockers, which win over warnings", () => {
    expect(doctorStatusText(summary(1, 2))).toBe("Nightgauge: 1 blocker");
    expect(doctorStatusText(summary(3, 0))).toBe("Nightgauge: 3 blockers");
  });
});

describe("DoctorStatusBarItem", () => {
  let now = 1_000_000;
  let items: DoctorStatusBarItem[] = [];

  beforeEach(() => {
    vi.useFakeTimers();
    now = 1_000_000;
    items = [];
  });

  afterEach(() => {
    for (const i of items) i.dispose();
    vi.useRealTimers();
  });

  function setup(results: DoctorRunResult[]) {
    const ipc = {
      doctorRun: vi.fn(async () => results.shift() ?? result(summary(0, 0))),
      doctorApplyRemedy: vi.fn(),
      doctorRecheck: vi.fn(),
      on: vi.fn(() => ({ dispose: vi.fn() })),
    };
    const service = new DoctorService(
      ipc as never,
      () => ["codex"],
      () => now
    );
    const item = new DoctorStatusBarItem(service, {
      enabled: () => true,
      firstScanDelayMs: 1000,
      tickMs: 60_000,
    });
    items.push(item);
    return { ipc, service, item };
  }

  it("opens the Doctor panel on click and renders the first background scan as text", async () => {
    const { ipc, item } = setup([result(summary(1, 0))]);
    expect(item.item.command).toBe(RUN_DOCTOR_COMMAND);
    await vi.advanceTimersByTimeAsync(1000);
    // A background scan never runs the opt-in adapter probe.
    expect(ipc.doctorRun).toHaveBeenCalledWith(undefined, undefined, undefined);
    expect(item.item.text).toBe("Nightgauge: 1 blocker");
    expect(item.item.accessibilityInformation?.label).toContain("1 blocker");
  });

  it("follows a scan the panel ran, including after a fix", async () => {
    const { service, item } = setup([result(summary(0, 2))]);
    await service.run({ probeAdapters: true });
    expect(item.item.text).toBe("Nightgauge: 2 warnings");
  });

  it("scans in the background at most every ten minutes", async () => {
    const { ipc } = setup([]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(ipc.doctorRun).toHaveBeenCalledTimes(1);
    // Ticks inside the ten minutes do not scan.
    now += BACKGROUND_INTERVAL_MS - 60_000;
    await vi.advanceTimersByTimeAsync(9 * 60_000);
    expect(ipc.doctorRun).toHaveBeenCalledTimes(1);
    now += 60_000;
    await vi.advanceTimersByTimeAsync(60_000);
    expect(ipc.doctorRun).toHaveBeenCalledTimes(2);
  });
});
