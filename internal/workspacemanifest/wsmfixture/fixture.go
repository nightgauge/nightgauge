// Package wsmfixture holds the workspace-manifest test fixture shared by the
// splicer's own tests (internal/workspacemanifest) and the CLI's tests
// (cmd/nightgauge/workspace).
//
// It is a normal package rather than a _test.go const because Go cannot share
// test-file identifiers across packages, and duplicating the fixture would let
// the two copies drift — which for this fixture means silently losing coverage
// of the exact formatting features the splicer exists to preserve.
package wsmfixture

// Realistic mirrors the shape of this repository's own manifest: head comment,
// a comment block owned by the FIRST repositories entry, blank-line separators,
// a trailing NOTE block, and a routing section. Every formatting feature here
// is one a marshal-based writer would destroy.
const Realistic = `# Workspace Configuration
#
# Paths are relative to this file's location.

workspace:
  name: "Test Workspace"
  description: "fixture"

repositories:
  # ` + "`project_number`" + ` — explicit repo→project mapping. Without it,
  # defaults caused silent cross-repo misroutes.
  - name: alpha
    path: .
    role: primary
    project_number: 3

  - name: beta
    path: ../beta
    role: primary
    project_number: 4

  - name: gamma
    path: ../gamma
    role: secondary
    project_number: 5

# NOTE: delta is deliberately NOT listed — it carries no project board, and
# project_number has no zero-value guard.

routing:
  default_repository: alpha
  patterns:
    - id: web
      keywords: [angular, web]
      preferred_repo: gamma
`
