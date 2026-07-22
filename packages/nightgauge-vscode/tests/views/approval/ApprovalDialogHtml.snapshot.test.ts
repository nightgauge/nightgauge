/**
 * ApprovalDialogHtml.snapshot.test.ts
 *
 * HTML snapshot regression tests for getApprovalDialogHtml().
 * Captures structural HTML output to catch silent regressions
 * in the approval dialog template generator.
 *
 * @see Issue #1242 - Add HTML snapshot regression tests for *Html.ts
 */

import { describe, it, expect } from "vitest";
import { getApprovalDialogHtml } from "../../../src/views/approval/ApprovalDialogHtml";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const mockWebview = { cspSource: "test-csp" } as any;
const mockExtensionUri = {
  fsPath: "/extension",
  toString: () => "/extension",
} as any;

function normalize(html: string): string {
  return html
    .replace(/nonce-[A-Za-z0-9]{32}/g, "nonce-NONCE")
    .replace(/nonce="[A-Za-z0-9]{32}"/g, 'nonce="NONCE"');
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const simplePlanContent = `
# Implementation Plan

## Approach
Add a new PhotoService to handle file uploads.

## Files to Modify
- \`src/services/PhotoService.ts\` (create)
- \`src/routes/photo.ts\` (create)

## Acceptance Criteria
- [ ] Upload endpoint accepts multipart/form-data
- [ ] Files stored in S3 with UUID keys
- [ ] Returns signed URL on success
`;

const longPlanContent = `
# Detailed Implementation Plan for Issue #99

## Background
This is a comprehensive plan covering multiple components and requiring
careful coordination across multiple files and services.

## Approach
Use a layered architecture with clear separation of concerns.

### Layer 1: Data Access
- Repository pattern for all DB operations
- Parameterized queries only
- Transaction support

### Layer 2: Business Logic
- Service classes with single responsibility
- Input validation at service boundaries
- Error types mapped to HTTP status codes

### Layer 3: API
- RESTful endpoints
- Request/response DTOs with Zod validation
- Rate limiting middleware

## Files to Create
- \`src/repositories/UserRepository.ts\`
- \`src/services/UserService.ts\`
- \`src/routes/users.ts\`
- \`tests/repositories/UserRepository.test.ts\`
- \`tests/services/UserService.test.ts\`

## Testing Strategy
Unit tests for each service and repository. Integration tests for routes.
`;

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

describe("getApprovalDialogHtml snapshots (Issue #1242)", () => {
  it("plan content rendered — feature-planning stage", () => {
    const html = getApprovalDialogHtml(
      mockWebview,
      mockExtensionUri,
      "feature-planning" as any,
      42,
      simplePlanContent
    );
    expect(normalize(html)).toMatchSnapshot();
  });

  it("plan content rendered — feature-dev stage", () => {
    const html = getApprovalDialogHtml(
      mockWebview,
      mockExtensionUri,
      "feature-dev" as any,
      99,
      longPlanContent
    );
    expect(normalize(html)).toMatchSnapshot();
  });

  it("auto-accept state — pr-create stage", () => {
    const html = getApprovalDialogHtml(
      mockWebview,
      mockExtensionUri,
      "pr-create" as any,
      1337,
      "# PR Ready\n\nAll checks passed. PR will be created automatically."
    );
    expect(normalize(html)).toMatchSnapshot();
  });
});
