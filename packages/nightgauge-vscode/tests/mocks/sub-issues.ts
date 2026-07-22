import { SubIssue } from "../../src/utils/subIssueProgress";

/**
 * Interface representing a parent issue with sub-issues
 */
export interface ParentIssue {
  number: number;
  title: string;
  subIssues: SubIssue[];
}

/**
 * Creates a mock SubIssue for testing purposes.
 *
 * @param overrides - Partial SubIssue properties to override defaults
 * @returns A SubIssue with default or overridden values
 *
 * @example
 * // Create a closed sub-issue
 * const closedIssue = createMockSubIssue({ number: 5, state: 'CLOSED' });
 *
 * // Create an open sub-issue with default state
 * const openIssue = createMockSubIssue({ number: 10 });
 */
export function createMockSubIssue(overrides?: Partial<SubIssue>): SubIssue {
  return {
    number: 1,
    state: "OPEN",
    ...overrides,
  };
}

/**
 * Creates a mock parent issue with the specified number of open and closed sub-issues.
 * Sub-issue numbers are assigned sequentially starting from 1.
 *
 * @param openCount - Number of open sub-issues to create
 * @param closedCount - Number of closed sub-issues to create
 * @returns A ParentIssue with the specified sub-issues
 *
 * @example
 * // Create parent with 3 open and 2 closed sub-issues
 * const parent = createMockParentWithSubIssues(3, 2);
 * // parent.subIssues = [
 * //   { number: 1, state: 'OPEN' },
 * //   { number: 2, state: 'OPEN' },
 * //   { number: 3, state: 'OPEN' },
 * //   { number: 4, state: 'CLOSED' },
 * //   { number: 5, state: 'CLOSED' }
 * // ]
 */
export function createMockParentWithSubIssues(openCount: number, closedCount: number): ParentIssue {
  const subIssues: SubIssue[] = [];
  let issueNumber = 1;

  // Create open sub-issues
  for (let i = 0; i < openCount; i++) {
    subIssues.push(createMockSubIssue({ number: issueNumber++, state: "OPEN" }));
  }

  // Create closed sub-issues
  for (let i = 0; i < closedCount; i++) {
    subIssues.push(createMockSubIssue({ number: issueNumber++, state: "CLOSED" }));
  }

  return {
    number: 100,
    title: "Mock Parent Issue",
    subIssues,
  };
}
