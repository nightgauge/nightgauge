/**
 * Mock factories for GitHub API responses
 *
 * Provides mock data for testing GitHub GraphQL queries and issue fetching.
 */

import type { ReadyIssue, BlockingIssue } from "../../src/services/ProjectBoardService";

/**
 * Create a mock ReadyIssue with optional overrides
 */
export function createMockReadyIssue(overrides?: Partial<ReadyIssue>): ReadyIssue {
  return {
    number: 110,
    title: "Add Ready items list view",
    labels: ["type:feature", "priority:medium", "size:M"],
    priority: "P2",
    size: "M",
    url: "https://github.com/nightgauge/nightgauge/issues/110",
    status: "Ready",
    epicRef: undefined,
    ...overrides,
  };
}

/**
 * Create a mock epic issue
 */
export function createMockEpicIssue(overrides?: Partial<ReadyIssue>): ReadyIssue {
  return createMockReadyIssue({
    number: 100,
    title: "User Authentication Epic",
    labels: ["type:epic", "priority:high"],
    priority: "P1",
    size: "XL",
    url: "https://github.com/nightgauge/nightgauge/issues/100",
    ...overrides,
  });
}

/**
 * Create a mock sub-issue that references an epic
 */
export function createMockSubIssue(
  epicNumber: number,
  overrides?: Partial<ReadyIssue>
): ReadyIssue {
  return createMockReadyIssue({
    number: 110,
    title: "Implement login form",
    labels: ["type:feature", "priority:medium", "size:M"],
    epicRef: epicNumber,
    ...overrides,
  });
}

/**
 * Create a mock BlockingIssue with optional overrides
 */
export function createMockBlockingIssue(overrides?: Partial<BlockingIssue>): BlockingIssue {
  return {
    number: 100,
    title: "Foundation for feature",
    url: "https://github.com/nightgauge/nightgauge/issues/100",
    state: "OPEN",
    ...overrides,
  };
}

/**
 * Create a mock ReadyIssue with blocking dependencies
 */
export function createMockBlockedIssue(
  blockingIssues: BlockingIssue[] = [createMockBlockingIssue()]
): ReadyIssue {
  return createMockReadyIssue({
    number: 120,
    title: "Feature that depends on #100",
    blockedBy: blockingIssues,
  });
}

/**
 * Mock GraphQL response for project items query
 */
export function createMockGraphQLResponse(issues: ReadyIssue[], status: string = "Ready") {
  return {
    data: {
      organization: {
        projectV2: {
          items: {
            nodes: issues.map((issue) => ({
              content: {
                number: issue.number,
                title: issue.title,
                labels: {
                  nodes: issue.labels.map((name) => ({ name })),
                },
                url: issue.url,
                blockedBy: issue.blockedBy
                  ? {
                      nodes: issue.blockedBy.map((b) => ({
                        number: b.number,
                        title: b.title,
                        url: b.url,
                        state: b.state,
                      })),
                    }
                  : undefined,
                blocking: issue.blocks
                  ? {
                      nodes: issue.blocks.map((b) => ({
                        number: b.number,
                        title: b.title,
                        url: b.url,
                        state: b.state,
                      })),
                    }
                  : undefined,
              },
              fieldValueByName: {
                name: status,
              },
            })),
          },
        },
      },
    },
  };
}

/**
 * Mock GraphQL error response
 */
export function createMockGraphQLError(message: string = "GraphQL error") {
  return {
    errors: [{ message }],
  };
}

/**
 * Mock GraphQL dependency response (blockedBy/blocking)
 */
export function createMockDependencyResponse(
  blockedByIssues: BlockingIssue[] = [],
  blockingIssues: BlockingIssue[] = []
) {
  return {
    data: {
      repository: {
        issue: {
          blockedBy: {
            nodes: blockedByIssues.map((issue) => ({
              number: issue.number,
              title: issue.title,
              url: issue.url,
              state: issue.state,
            })),
          },
          blocking: {
            nodes: blockingIssues.map((issue) => ({
              number: issue.number,
              title: issue.title,
              url: issue.url,
              state: issue.state,
            })),
          },
        },
      },
    },
  };
}

// ============================================================================
// Field Mapping Mock Factories
// ============================================================================

import {
  mapPriorityLabel,
  mapSizeLabel,
  extractPriorityLabel,
  extractSizeLabel,
  type PriorityValue,
  type SizeValue,
} from "../../src/utils/projectFieldMapping";

/**
 * Create a mock issue with labels and auto-mapped priority/size
 *
 * Uses the mapping functions to automatically set priority and size
 * based on the provided labels, ensuring consistency between labels
 * and field values in tests.
 *
 * @param overrides - Issue overrides (labels will be used for mapping)
 * @returns ReadyIssue with consistent label-to-field mappings
 *
 * @example
 * // Priority and size auto-mapped from labels
 * createMockIssueWithMappedFields({
 *   number: 42,
 *   labels: ['type:feature', 'priority:high', 'size:M']
 * })
 * // Returns: { ...defaults, priority: 'P1', size: 'M', labels: [...] }
 */
export function createMockIssueWithMappedFields(overrides?: Partial<ReadyIssue>): ReadyIssue {
  const defaultLabels = ["type:feature", "priority:medium", "size:M"];
  const labels = overrides?.labels ?? defaultLabels;

  // Extract and map priority from labels
  const priorityLabel = extractPriorityLabel(labels);
  const priority = priorityLabel ? (mapPriorityLabel(priorityLabel) as PriorityValue) : null;

  // Extract and map size from labels
  const sizeLabel = extractSizeLabel(labels);
  const size = sizeLabel ? (mapSizeLabel(sizeLabel) as SizeValue) : null;

  return createMockReadyIssue({
    priority: priority || null,
    size: size || null,
    ...overrides,
    labels, // Ensure labels override is preserved
  });
}

/**
 * Create mock issues with all priority levels for sorting tests
 *
 * @returns Array of 4 issues with P0, P1, P2, and null priority
 */
export function createMockIssuesWithAllPriorities(): ReadyIssue[] {
  return [
    createMockIssueWithMappedFields({
      number: 1,
      labels: ["type:feature", "priority:critical"],
    }),
    createMockIssueWithMappedFields({
      number: 2,
      labels: ["type:feature", "priority:high"],
    }),
    createMockIssueWithMappedFields({
      number: 3,
      labels: ["type:feature", "priority:medium"],
    }),
    createMockIssueWithMappedFields({
      number: 4,
      labels: ["type:feature"], // No priority label
    }),
  ];
}

/**
 * Create mock issues with all size levels for sorting tests
 *
 * @returns Array of 5 issues with XS, S, M, L, XL sizes
 */
export function createMockIssuesWithAllSizes(): ReadyIssue[] {
  return [
    createMockIssueWithMappedFields({
      number: 1,
      labels: ["type:feature", "size:XS"],
    }),
    createMockIssueWithMappedFields({
      number: 2,
      labels: ["type:feature", "size:S"],
    }),
    createMockIssueWithMappedFields({
      number: 3,
      labels: ["type:feature", "size:M"],
    }),
    createMockIssueWithMappedFields({
      number: 4,
      labels: ["type:feature", "size:L"],
    }),
    createMockIssueWithMappedFields({
      number: 5,
      labels: ["type:feature", "size:XL"],
    }),
  ];
}

/**
 * Create a mock GraphQL response with pagination support
 *
 * @param issues - Array of issues to include in this page
 * @param options - Pagination options
 * @returns Mock GraphQL response with pagination info
 */
export function createMockGraphQLResponseWithPagination(
  issues: ReadyIssue[],
  options: {
    status?: string;
    hasNextPage?: boolean;
    endCursor?: string;
  } = {}
) {
  const { status = "Ready", hasNextPage = false, endCursor = "" } = options;

  return {
    data: {
      organization: {
        projectV2: {
          items: {
            pageInfo: {
              hasNextPage,
              endCursor,
            },
            nodes: issues.map((issue) => ({
              content: {
                number: issue.number,
                title: issue.title,
                labels: {
                  nodes: issue.labels.map((name) => ({ name })),
                },
                url: issue.url,
                blockedBy: issue.blockedBy
                  ? {
                      nodes: issue.blockedBy.map((b) => ({
                        number: b.number,
                        title: b.title,
                        url: b.url,
                        state: b.state,
                      })),
                    }
                  : { nodes: [] },
                blocking: issue.blocks
                  ? {
                      nodes: issue.blocks.map((b) => ({
                        number: b.number,
                        title: b.title,
                        url: b.url,
                        state: b.state,
                      })),
                    }
                  : { nodes: [] },
              },
              fieldValueByName: {
                name: status,
              },
            })),
          },
        },
      },
    },
  };
}
