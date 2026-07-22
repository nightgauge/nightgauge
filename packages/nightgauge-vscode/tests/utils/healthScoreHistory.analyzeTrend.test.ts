/**
 * Tests for `HealthScoreHistoryReader.analyzeTrend()` message wording.
 *
 * The chart on the Overview tab can show a wider range (7d, 30d, 90d) than
 * the comparison window the message describes (3d, 7d, 14d respectively).
 * The message wording must therefore make the *comparison* explicit
 * ("vs prior N days") rather than describing a single contiguous window
 * ("over last N days"), which previously misled users when the two windows
 * disagreed.
 */
import { describe, it, expect } from "vitest";
import { HealthScoreHistoryReader, type TrendChartDay } from "../../src/utils/healthScoreHistory";

function bucket(date: string, avgScore: number, count = 1): TrendChartDay {
  return { date, avgScore, count };
}

describe("HealthScoreHistoryReader.analyzeTrend — message clarity", () => {
  it("reports an improvement using comparison wording (not single-window wording)", () => {
    // Recent 3 days avg 90, prior 3 days avg 60 → +50%.
    const data: TrendChartDay[] = [
      bucket("2026-05-17", 90),
      bucket("2026-05-16", 90),
      bucket("2026-05-15", 90),
      bucket("2026-05-14", 60),
      bucket("2026-05-13", 60),
      bucket("2026-05-12", 60),
    ];
    const result = HealthScoreHistoryReader.analyzeTrend(data, 3);

    expect(result.direction).toBe("improving");
    expect(result.percentChange).toBe(50);
    expect(result.message).toBe("Health improved 50% vs prior 3 days");
    // Critically, the old "over last 3 days" wording is gone.
    expect(result.message).not.toContain("over last");
  });

  it("reports a decline using comparison wording", () => {
    const data: TrendChartDay[] = [
      bucket("2026-05-17", 50),
      bucket("2026-05-16", 50),
      bucket("2026-05-15", 50),
      bucket("2026-05-14", 100),
      bucket("2026-05-13", 100),
      bucket("2026-05-12", 100),
    ];
    const result = HealthScoreHistoryReader.analyzeTrend(data, 3);

    expect(result.direction).toBe("declining");
    expect(result.percentChange).toBe(-50);
    expect(result.message).toBe("Health declined 50% vs prior 3 days");
  });

  it("reports stable with comparison wording when change is within ±2%", () => {
    const data: TrendChartDay[] = [
      bucket("2026-05-17", 80),
      bucket("2026-05-16", 80),
      bucket("2026-05-15", 80),
      bucket("2026-05-14", 80),
      bucket("2026-05-13", 80),
      bucket("2026-05-12", 80),
    ];
    const result = HealthScoreHistoryReader.analyzeTrend(data, 3);

    expect(result.direction).toBe("stable");
    expect(result.message).toBe("Health stable vs prior 3 days");
  });

  it("uses hour wording for the 24h comparison bucket size", () => {
    const data: TrendChartDay[] = Array.from({ length: 24 }, (_, i) =>
      bucket(`2026-05-17T${String(23 - i).padStart(2, "0")}`, i < 12 ? 90 : 60)
    );
    const result = HealthScoreHistoryReader.analyzeTrend(data, 12);

    expect(result.message).toMatch(/^Health improved \d+% vs prior 12 hours$/);
  });

  it("handles a 1-day window with singular grammar", () => {
    const data: TrendChartDay[] = [bucket("2026-05-17", 90), bucket("2026-05-16", 60)];
    const result = HealthScoreHistoryReader.analyzeTrend(data, 1);

    expect(result.message).toBe("Health improved 50% vs prior 1 day");
  });
});
