// Package gitlab is the GitLab adapter for the forge abstraction defined in
// internal/forge. It exposes IssueService and PRService (Merge Request) CRUD
// against /api/v4/projects/:id/issues and
// /api/v4/projects/:id/merge_requests, plus a thin REST client with
// PRIVATE-TOKEN auth, link-header pagination, and forge-sentinel error
// mapping.
//
// # iid vs id
//
// GitLab exposes two identifiers for every issue and merge request:
//
//   - id  — globally unique across the instance.
//   - iid — internal id, scoped to the project, surfaced in URLs and the UI.
//
// The forge contract says "the user-visible number is what callers query";
// this adapter therefore uses iid as the canonical Number on
// forgetypes.Issue / forgetypes.PullRequest, and records id in NodeID for
// parity with GitHub's GraphQL node ID.
//
// # Out of scope
//
// This adapter intentionally implements only the high-frequency CRUD
// surface required by Wave 2.4 (#3356):
//
//   - Issues: get/list/iterate/create/update/close/reopen
//   - MRs:    get/list/iterate/create/update/merge/close
//
// Sub-issues, CI, rulesets, labels CRUD, and full auth chain return
// forge.ErrUnsupported with a tracking-issue reference. Boards and project
// field mapping landed in #3357 (Status / Iteration / Weight / Health).
// Future waves (#3358, #3359) extend this package; the auth chain (#3354)
// and full CA/proxy HTTP foundation (#3353) replace the inline transport
// here without changing call sites — adapters expose a WithHTTPClient
// option for that purpose.
//
// # Edition detection
//
// EE-only fields (weight, health_status, iteration_id) probe the instance
// at first use via GET /api/v4/license. The result is cached on the client
// via sync.Once. CE deployments fall back to scoped labels for Status /
// Health and to project milestones for iteration semantics.
package gitlab
