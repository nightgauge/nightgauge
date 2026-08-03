/**
 * QueueSectionTreeItem count tests — Issue #264
 *
 * The header count answers "how much work is WAITING?". An item that has been
 * dispatched is marked `processing` IN PLACE rather than spliced out
 * (#232/#246), so the queue still holds a record of it — deliberately, because
 * removing it on dispatch is what once made the queue report `idle` while a
 * pipeline was running. The presentation defect was counting that record as
 * queued, which reads as double-dispatch to an operator.
 */

import { describe, it, expect } from "vitest";
import { QueueSectionTreeItem } from "../../../src/views/items/QueueSectionTreeItem";
import { createMockQueueItem } from "../../mocks/queue";

describe("QueueSectionTreeItem count (#264)", () => {
  it("does not count a running item as queued", () => {
    const section = new QueueSectionTreeItem();

    // The observed live state: one item, executing, with an open PR.
    section.setItems([createMockQueueItem({ issueNumber: 702, status: "processing" })]);

    expect(section.description).not.toBe("(1)");
    expect(section.description).toBe("(0 queued, 1 running)");
  });

  it("keeps the running item visible in the tree", () => {
    const section = new QueueSectionTreeItem();
    section.setItems([createMockQueueItem({ issueNumber: 702, status: "processing" })]);

    // Hiding it would re-create the #232 blindness the current model exists
    // to fix — the count is the defect, not the item's presence.
    expect(section.getChildren()).toHaveLength(1);
    expect(section.getItemCount()).toBe(1);
  });

  it("splits the count when work is both waiting and running", () => {
    const section = new QueueSectionTreeItem();
    section.setItems([
      createMockQueueItem({ issueNumber: 1, status: "processing" }),
      createMockQueueItem({ issueNumber: 2, status: "pending" }),
      createMockQueueItem({ issueNumber: 3, status: "pending" }),
    ]);

    expect(section.description).toBe("(2 queued, 1 running)");
  });

  it("renders a plain count when nothing is running", () => {
    const section = new QueueSectionTreeItem();
    section.setItems([
      createMockQueueItem({ issueNumber: 1, status: "pending" }),
      createMockQueueItem({ issueNumber: 2, status: "ready" }),
    ]);

    expect(section.description).toBe("(2)");
  });

  it("counts a paused item as waiting, not running", () => {
    const section = new QueueSectionTreeItem();
    section.setItems([
      createMockQueueItem({ issueNumber: 1, status: "paused" }),
      createMockQueueItem({ issueNumber: 2, status: "pending" }),
    ]);

    // Paused work is still waiting for a slot — it is not executing.
    expect(section.description).toBe("(2)");
  });

  it("tooltip reports waiting and running separately", () => {
    const section = new QueueSectionTreeItem();
    section.setItems([
      createMockQueueItem({ issueNumber: 1, status: "processing" }),
      createMockQueueItem({ issueNumber: 2, status: "pending" }),
    ]);

    const tooltip = String((section.tooltip as { value?: string })?.value ?? section.tooltip);
    expect(tooltip).toContain("**1** issue(s) waiting");
    expect(tooltip).toContain("**1** currently running");
  });

  it("clear() resets both counts", () => {
    const section = new QueueSectionTreeItem();
    section.setItems([createMockQueueItem({ issueNumber: 1, status: "processing" })]);
    section.clear();

    expect(section.description).toBe("(0)");
    expect(section.getChildren()).toHaveLength(0);
  });
});
