/**
 * Mock factories for ProjectIterationService tests
 *
 * Provides mock data for testing iteration sync and date calculations.
 *
 * @see Issue #132 - Rewrite sync-project-iteration.sh in TypeScript
 */

import type {
  Iteration,
  SyncSuccess,
  SyncSkipped,
  IterationConfig,
  GraphQLProjectItem,
} from "../../src/services/types/iteration";

/**
 * Create a mock Iteration with optional overrides
 */
export function createMockIteration(overrides?: Partial<Iteration>): Iteration {
  return {
    id: "iter_abc123",
    title: "Sprint 5",
    startDate: "2026-02-03",
    duration: 14,
    ...overrides,
  };
}

/**
 * Create a mock current iteration (contains today's date)
 *
 * @param today - Optional date to use as "today" (defaults to current date)
 */
export function createMockCurrentIteration(today?: Date): Iteration {
  const baseDate = today ?? new Date();
  const startDate = new Date(baseDate);
  startDate.setDate(startDate.getDate() - 3); // Started 3 days ago

  return createMockIteration({
    id: "iter_current",
    title: "Current Sprint",
    startDate: startDate.toISOString().split("T")[0],
    duration: 14,
  });
}

/**
 * Create a mock next iteration (starts after today)
 *
 * @param today - Optional date to use as "today" (defaults to current date)
 */
export function createMockNextIteration(today?: Date): Iteration {
  const baseDate = today ?? new Date();
  const startDate = new Date(baseDate);
  startDate.setDate(startDate.getDate() + 7); // Starts in 7 days

  return createMockIteration({
    id: "iter_next",
    title: "Next Sprint",
    startDate: startDate.toISOString().split("T")[0],
    duration: 14,
  });
}

/**
 * Create a mock past iteration (ended before today)
 *
 * @param today - Optional date to use as "today" (defaults to current date)
 */
export function createMockPastIteration(today?: Date): Iteration {
  const baseDate = today ?? new Date();
  const startDate = new Date(baseDate);
  startDate.setDate(startDate.getDate() - 30); // Started 30 days ago

  return createMockIteration({
    id: "iter_past",
    title: "Past Sprint",
    startDate: startDate.toISOString().split("T")[0],
    duration: 14,
  });
}

/**
 * Create a set of mock iterations for testing date resolution
 *
 * @param today - Optional date to use as "today" (defaults to current date)
 */
export function createMockIterationSet(today?: Date): Iteration[] {
  return [
    createMockPastIteration(today),
    createMockCurrentIteration(today),
    createMockNextIteration(today),
  ];
}

/**
 * Create a mock IterationConfig
 */
export function createMockConfig(overrides?: Partial<IterationConfig>): IterationConfig {
  return {
    projectNumber: 10,
    sprintEnabled: true,
    fieldName: "Sprint",
    ...overrides,
  };
}

/**
 * Create a mock SyncSuccess result
 */
export function createMockSyncSuccess(overrides?: Partial<SyncSuccess>): SyncSuccess {
  return {
    success: true,
    issue: 90,
    project: 10,
    item_id: "PVTI_lADOABC123",
    iteration: {
      id: "iter_abc123",
      title: "Sprint 5",
    },
    action: "assigned",
    ...overrides,
  };
}

/**
 * Create a mock SyncSkipped result
 */
export function createMockSyncSkipped(reason: string): SyncSkipped {
  return {
    skipped: true,
    reason,
  };
}

/**
 * Create a mock GraphQL project item
 */
export function createMockProjectItem(overrides?: Partial<GraphQLProjectItem>): GraphQLProjectItem {
  return {
    id: "PVTI_lADOABC123",
    content: {
      number: 90,
      repository: {
        nameWithOwner: "nightgauge/nightgauge",
      },
    },
    ...overrides,
  };
}

/**
 * Create mock gh CLI output for project list
 */
export function createMockProjectListOutput(projectNumber: number): string {
  return JSON.stringify({
    projects: [
      {
        number: projectNumber,
        id: "PVT_kwDOABC123",
        title: "Nightgauge",
      },
    ],
  });
}

/**
 * Create mock gh CLI output for field list
 */
export function createMockFieldListOutput(fieldName: string): string {
  return JSON.stringify({
    fields: [
      {
        id: "PVTF_iteration123",
        name: fieldName,
        type: "ITERATION",
      },
      {
        id: "PVTF_status123",
        name: "Status",
        type: "SINGLE_SELECT",
      },
    ],
  });
}

/**
 * Create mock gh CLI output for iteration GraphQL query
 */
export function createMockIterationQueryOutput(iterations: Iteration[]): string {
  return JSON.stringify({
    data: {
      node: {
        field: {
          id: "PVTF_iteration123",
          configuration: {
            iterations,
          },
        },
      },
    },
  });
}

/**
 * Create mock gh CLI output for project items query (single page)
 */
export function createMockProjectItemsOutput(
  items: GraphQLProjectItem[],
  hasNextPage: boolean = false,
  endCursor: string = ""
): string {
  return JSON.stringify({
    data: {
      node: {
        items: {
          pageInfo: {
            hasNextPage,
            endCursor,
          },
          nodes: items,
        },
      },
    },
  });
}

/**
 * Create mock nightgauge.yaml content
 */
export function createMockIncrediYaml(config: Partial<IterationConfig>): string {
  const lines: string[] = ["project:"];

  if (config.projectNumber !== undefined) {
    lines.push(`  number: ${config.projectNumber}`);
  }

  if (config.sprintEnabled !== undefined || config.fieldName !== undefined) {
    lines.push("  sprint:");
    if (config.sprintEnabled !== undefined) {
      lines.push(`    enabled: ${config.sprintEnabled}`);
    }
    if (config.fieldName !== undefined) {
      lines.push(`    field_name: "${config.fieldName}"`);
    }
  }

  return lines.join("\n");
}
