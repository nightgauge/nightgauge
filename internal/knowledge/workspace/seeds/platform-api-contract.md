# Service API Contract

Use this page as a pointer to the authoritative public API contract shared by
repositories in the workspace.

## Authoritative sources

- API schema or route documentation:
- Shared generated types:
- Compatibility and deprecation policy:
- Public decision records:

## Drift rules

- Generate clients from the authoritative schema where practical.
- Do not hand-roll request or response shapes already owned by a shared package.
- Version incompatible changes and provide a documented migration window.
- Verify the current contract during planning whenever a change crosses a
  repository boundary.

Do not copy private endpoints, credentials, deployment topology, or unreleased
service plans into this knowledge base.
