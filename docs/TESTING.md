# Testing Guide

This document explains how to test plugins and configurations in this
repository.

## Testing Philosophy

### Core Principles

1. **Behavior over implementation** - Test what the code does, not how it does
   it
2. **Quality over quantity** - A few well-designed tests beat many trivial ones
3. **Error paths matter** - Test failure scenarios with specific error types
4. **Integration tests for contracts** - Verify API boundaries and data flows

### Test Value Hierarchy

| Value Level | Description                           | Example                                 |
| ----------- | ------------------------------------- | --------------------------------------- |
| **High**    | Tests behavior at API boundaries      | Service method returns expected result  |
| **High**    | Tests error paths with specific types | Invalid input throws ValidationError    |
| **Medium**  | Tests state transitions               | Pipeline stage updates status correctly |
| **Medium**  | Tests integration between components  | Config flows from YAML to service       |
| **Low**     | Tests implementation details          | Mock was called with exact args         |
| **Zero**    | Tests framework behavior or constants | Constant equals its hardcoded value     |

### What to Test

Since this repository contains both configuration/documentation files AND
executable TypeScript code, testing has two focus areas:

**For configuration/documentation files:**

1. **Syntax Validation** - Files are valid Markdown/JSON/YAML
2. **Functional Testing** - Plugins work correctly with AI assistants
3. **Cross-Tool Compatibility** - Configurations work across different AI tools

**For TypeScript code (VSCode extension, SDK):**

1. **Behavior at API boundaries** - Public methods produce expected outputs
2. **Error handling** - Invalid inputs produce appropriate errors
3. **State management** - State transitions happen correctly
4. **Integration** - Components work together as expected

## Automated Validation

### UI Feature Verification (MANDATORY for UI-affecting changes)

Every feature or fix that changes a user-facing surface (dashboard, VSCode
extension views, flutter/web UI) **must** be verified end-to-end in the running
UI with the `verify-ui` skill (Playwright-driven flow check) before it is
declared done — unit and component tests are not sufficient. A page can render
perfectly with no data behind it: the Action Center E1 launch shipped a
dashboard inbox whose platform sync was never wired, and every repo-level
suite stayed green while the deployed page sat empty (#330).

The verification must exercise the real user flow against a running instance
(deployed or locally served against real APIs): navigate, assert visible
state, perform the primary action, and assert the observable consequence —
including cross-surface effects when the feature spans surfaces (e.g. resolve
on the dashboard → card disappears in the extension). Capture screenshots or
traces as evidence in the PR or issue. Operator rule, 2026-07-20.

### Stage Parity Validation

Validate core issue-to-PR stage parity between the Codex/Gemini adapters, Claude
command docs, and shared skill contracts:

```bash
npx -w @nightgauge/sdk vitest run tests/cli/stageParity.test.ts
```

This check fails with stage-specific diagnostics when command routing or stage
contract references drift.

### JSON Validation

Validate JSON files using `jq`:

```bash
# Validate a single file
jq . claude-plugins/nightgauge/.claude-plugin/plugin.json > /dev/null && echo "Valid JSON"

# Validate all JSON files
find . -name "*.json" -exec jq . {} \; > /dev/null 2>&1 && echo "All JSON valid"
```

### Markdown Linting

Use `markdownlint` to check Markdown files:

```bash
# Install
npm install -g markdownlint-cli

# Lint all Markdown files
markdownlint "**/*.md" --ignore node_modules
```

### YAML Validation

Use `yamllint` for YAML files:

```bash
# Install
pip install yamllint

# Validate YAML files
yamllint configs/ standards/
```

## Manual Plugin Testing

### Testing with Claude Code

1. **Install the plugin locally:**

   ```bash
   # Add to settings.json
   {
     "plugins": [
       "/path/to/nightgauge/claude-plugins/nightgauge"
     ]
   }
   ```

2. **Test commands in a sample repository:**

   ```bash
   cd /path/to/test-repo

   # Test smart-setup
   /nightgauge:smart-setup --audit-only

   # Test update-docs
   /nightgauge:update-docs --report-only
   ```

3. **Verify expected outputs:**
   - Audit reports are accurate
   - Generated files are correct
   - No existing files are destroyed (NON-DESTRUCTIVE policy)

### Testing with GitHub Copilot

1. Copy configuration to a test repository:

   ```bash
   cp standards/AGENTS_TEMPLATE.md /test-repo/AGENTS.md
   ```

2. Open the repository in VS Code with Copilot enabled

3. Verify Copilot acknowledges the AGENTS.md in chat

4. Ask Copilot to perform tasks and verify it follows the guidelines

### Testing with Cursor

1. Copy Cursor rules to a test repository:

   ```bash
   mkdir -p /test-repo/.cursor/rules
   cp configs/cursor/*.mdc /test-repo/.cursor/rules/
   ```

2. Open in Cursor IDE

3. Verify the rules are being applied in suggestions

## Test Scenarios

### Smart Setup Command

| Scenario                            | Expected Result                                              |
| ----------------------------------- | ------------------------------------------------------------ |
| Empty repository                    | Creates AGENTS.md, CLAUDE.md, docs/, copilot-instructions.md |
| Repository with existing AGENTS.md  | Reads existing, identifies gaps, asks permission to add      |
| Repository with complete docs       | Reports "No changes needed"                                  |
| "Just audit what's missing" request | Reports status without making changes                        |
| "Skip questions" request            | Uses `[TEAM TO DOCUMENT]` markers                            |

### Update Docs Command

_Part of the `docs` plugin_

| Scenario                            | Expected Result                       |
| ----------------------------------- | ------------------------------------- |
| Docs in sync                        | Reports "Documentation is up to date" |
| Stale references                    | Identifies deprecated terms/patterns  |
| Missing cross-references            | Reports broken internal links         |
| "Auto-fix simple issues" request    | Fixes simple issues automatically     |
| "Just show me what's stale" request | Generates report without changes      |

### Non-Destructive Policy

| Scenario              | Expected Result                          |
| --------------------- | ---------------------------------------- |
| Existing AGENTS.md    | Never overwritten without permission     |
| Existing CLAUDE.md    | Never overwritten without permission     |
| Existing docs/\*.md   | Only additions offered, not replacements |
| User declines changes | Original files remain untouched          |

## CI/CD Validation

The repository includes GitHub Actions workflows for automated validation:

### claude-plugin-validation.yml

Validates:

- JSON syntax in plugin manifests
- Required fields in plugin.json
- Markdown structure in command files

### Viewing CI Results

Check the Actions tab in GitHub for validation results after each push.

## Reporting Issues

If testing reveals issues:

1. Document the scenario that caused the issue
2. Note expected vs. actual behavior
3. Include relevant file contents
4. Create a GitHub issue with the details

## Test Checklist

Before submitting changes:

- [ ] All JSON files pass `jq` validation
- [ ] All Markdown files pass linting
- [ ] Plugin commands tested in Claude Code
- [ ] Non-destructive policy verified
- [ ] Cross-tool compatibility checked (if applicable)
- [ ] CI/CD checks pass

---

## VSCode Extension Testing

The `packages/nightgauge-vscode` package contains TypeScript code that is
tested using Vitest. This section covers testing patterns for the extension.

### Running Tests

> **WARNING: Watch Mode Footgun**
>
> Running bare `vitest` (or `npm run test` if it calls `vitest` without `run`)
> enters **interactive watch mode** and hangs indefinitely — blocking CI, AI
> agents, and any automated pipeline.
>
> | Command                               | Behavior                          |
> | ------------------------------------- | --------------------------------- |
> | `vitest run`                          | ✅ Runs once and exits            |
> | `npx -w nightgauge-vscode vitest run` | ✅ Runs once and exits (monorepo) |
> | `npx -w @nightgauge/sdk vitest run`   | ✅ Runs once and exits (SDK)      |
> | `vitest`                              | ❌ Enters watch mode — **hangs**  |
> | `npm run test` (calls bare `vitest`)  | ❌ Enters watch mode — **hangs**  |
>
> Always use `vitest run` for any non-interactive context.

```bash
cd packages/nightgauge-vscode

# Run tests once (CI-safe)
npx vitest run

# Run tests once via workspace alias
npm run test:run

# Run tests in watch mode (interactive development only)
npm run test

# Run tests with coverage
npm run test:coverage
```

### Test Structure

Tests follow the Arrange/Act/Assert pattern and use Vitest:

```typescript
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

describe("MyModule", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should do something", () => {
    // Arrange
    const input = "test";

    // Act
    const result = myFunction(input);

    // Assert
    expect(result).toBe("expected");
  });
});
```

### Mock Factories

Mock factories are located in `tests/mocks/` and provide consistent test data:

| Mock File           | Purpose                                   |
| ------------------- | ----------------------------------------- |
| `github-api.ts`     | GitHub GraphQL responses, issue mocks     |
| `field-mappings.ts` | Project field option IDs and lookup mocks |

### Project Field Mapping Tests

The project field mapping logic has comprehensive test coverage. This tests the
label-to-field mappings for Priority and Size fields. Status is managed directly
via project board fields (not labels).

#### Test Files

| File                                                 | Coverage                         |
| ---------------------------------------------------- | -------------------------------- |
| `tests/utils/projectFieldMapping.test.ts`            | Pure mapping function unit tests |
| `tests/services/ProjectBoardService.mapping.test.ts` | Integration tests                |

#### Testing Mapping Logic

The mapping functions mirror shell script behavior for Priority and Size:

```typescript
import {
  mapPriorityLabel,
  mapSizeLabel,
  extractPriorityLabel,
  extractSizeLabel,
} from "../../src/utils/projectFieldMapping";

// Forward mapping: label → field value
expect(mapPriorityLabel("priority:high")).toBe("P1");
expect(mapSizeLabel("size:M")).toBe("M");

// Label extraction from arrays
expect(extractPriorityLabel(["type:feature", "priority:high"])).toBe("priority:high");
expect(extractSizeLabel(["type:feature", "size:M"])).toBe("size:M");
```

#### Shell Script Parity Tests

Tests verify TypeScript matches shell script behavior exactly:

```typescript
describe("matches add-to-project.sh map_priority_label()", () => {
  const shellMappings = [
    ["priority:critical", "P0"],
    ["priority:high", "P1"],
    ["priority:medium", "P2"],
    ["priority:low", "P2"],
    ["unknown", ""],
  ];

  shellMappings.forEach(([input, expected]) => {
    it(`map_priority_label("${input}") returns "${expected}"`, () => {
      expect(mapPriorityLabel(input)).toBe(expected);
    });
  });
});
```

#### Using Mock Field Mappings

For tests that need option ID lookups:

```typescript
import {
  MOCK_FIELD_MAPPINGS,
  getMockStatusOptionId,
  getMockPriorityOptionId,
  getMockSizeOptionId,
} from "../mocks/field-mappings";

// Get option IDs
expect(getMockStatusOptionId("Ready")).toBe("opt_ready_id");
expect(getMockPriorityOptionId("P1")).toBe("opt_p1_id");
expect(getMockSizeOptionId("M")).toBe("opt_m_id");
```

#### Creating Mock Issues with Consistent Mappings

Use `createMockIssueWithMappedFields` to auto-map labels to field values:

```typescript
import { createMockIssueWithMappedFields } from "../mocks/github-api";

// Priority and size auto-mapped from labels
const issue = createMockIssueWithMappedFields({
  number: 42,
  labels: ["type:feature", "priority:high", "size:M"],
});

expect(issue.priority).toBe("P1"); // Auto-mapped from priority:high
expect(issue.size).toBe("M"); // Auto-mapped from size:M
```

### Coverage Goals

| Area              | Target | Rationale                                |
| ----------------- | ------ | ---------------------------------------- |
| Mapping functions | >90%   | Pure functions with clear contracts      |
| Critical services | ≥60%   | State management, pipeline orchestration |
| Services          | >80%   | Core business logic                      |
| Overall extension | >70%   | Balanced coverage with quality focus     |

---

## Testing Anti-Patterns (Issue #485 Audit)

Based on a comprehensive audit of 135 test files (~63K lines), these
anti-patterns were identified. **Avoid these patterns in new tests:**

### 1. Implementation Detail Testing

**Problem:** Tests verify internal state or private methods rather than
observable behavior.

```typescript
// ❌ BAD: Testing implementation detail
it("should call internal helper", () => {
  const result = service.process(input);
  expect((service as any)._internalHelper).toHaveBeenCalled();
});

// ✅ GOOD: Testing observable behavior
it("should return processed result", () => {
  const result = service.process(input);
  expect(result.status).toBe("processed");
});
```

### 2. Framework Behavior Testing

**Problem:** Tests verify that mocks were called correctly rather than that
behavior occurred.

```typescript
// ❌ BAD: Testing framework behavior
it("should register command", () => {
  registerCommand(logger);
  expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
    "nightgauge.myCommand",
    expect.any(Function)
  );
});

// ✅ GOOD: Testing command behavior
it("should execute command successfully", async () => {
  const disposable = registerCommand(logger);
  const handler = extractHandler(disposable);
  await handler();
  expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Command executed");
});
```

### 3. Mock Call Count Assertions

**Problem:** Tests focus on how many times a mock was called rather than the
result.

```typescript
// ❌ BAD: Brittle call count assertion
expect(mockService.fetch).toHaveBeenCalledTimes(3);

// ✅ GOOD: Focus on result
const items = await provider.getChildren();
expect(items).toHaveLength(3);
```

### 4. 1:1 Test-to-Function Mapping

**Problem:** Each function has exactly one test that verifies it exists and
returns a value.

```typescript
// ❌ BAD: Trivial 1:1 mapping
describe("getNextStage", () => {
  it("returns issue-pickup after pipeline-start", () => {
    expect(getNextStage("pipeline-start")).toBe("issue-pickup");
  });
  it("returns feature-planning after issue-pickup", () => {
    expect(getNextStage("issue-pickup")).toBe("feature-planning");
  });
  // ... 6 more identical tests
});

// ✅ GOOD: Test the behavioral contract
describe("Pipeline stage progression", () => {
  it("should complete full pipeline from start to finish", () => {
    let stage = "pipeline-start";
    const visited = [stage];
    while (stage) {
      stage = getNextStage(stage);
      if (stage) visited.push(stage);
    }
    expect(visited).toEqual([
      "pipeline-start",
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
      "pipeline-finish",
    ]);
  });
});
```

### 5. Trivial Constant Verification

**Problem:** Tests verify that constants equal their hardcoded values.

```typescript
// ❌ BAD: Testing a constant
it("should have correct prefix", () => {
  expect(ENV_VAR_PREFIX).toBe("NIGHTGAUGE_");
});

// ❌ BAD: Testing defaults match schema
it("has expected default values", () => {
  expect(DEFAULT_SETTINGS.enabled).toBe(true);
  expect(DEFAULT_SETTINGS.volume).toBe(0.5);
});

// ✅ GOOD: Test behavior when defaults are used
it("should use defaults when config is missing", () => {
  mockConfigBridge.getUI.mockReturnValue({});
  const settings = getSettings();
  expect(settings.enabled).toBe(true); // Falls back to default
});
```

### 6. Pass-Through Tests

**Problem:** Tests verify that a function returns what it was given.

```typescript
// ❌ BAD: Testing pass-through
it("should handle zero values", () => {
  const result = parseTokens({ input: 0, output: 0 });
  expect(result.input).toBe(0);
  expect(result.output).toBe(0);
});

// ✅ GOOD: Test meaningful transformation or edge case
it("should handle missing fields with defaults", () => {
  const result = parseTokens({});
  expect(result.input).toBe(0); // Default applied
});
```

---

## Good Testing Patterns

### 1. Behavior Testing at API Boundaries

```typescript
describe("ProjectBoardService", () => {
  it("should return issues sorted by priority", async () => {
    const issues = await service.getIssuesByStatus("Ready");
    const priorities = issues.map((i) => i.priority);
    expect(priorities).toEqual(["P0", "P1", "P1", "P2"]);
  });
});
```

### 2. Error Path Coverage with Specific Types

```typescript
describe("ContextManager", () => {
  it("should throw ContextNotFoundError for missing file", async () => {
    await expect(manager.read("nonexistent")).rejects.toThrow(ContextNotFoundError);
  });

  it("should throw ContextValidationError for invalid schema", async () => {
    await expect(manager.read("invalid")).rejects.toThrow(ContextValidationError);
  });
});
```

### 3. Integration Tests for Service Interactions

```typescript
describe("Config Integration", () => {
  it("should flow config from YAML through merge engine to service", async () => {
    // Arrange
    await writeConfigFile({ pr: { delete_branch: false } });

    // Act
    const service = new PRService();
    await service.initialize();

    // Assert
    expect(service.canAdminMerge()).toBe(true);
  });
});
```

### 4. Table-Driven Tests for Multiple Scenarios

```typescript
describe("mapPriorityLabel", () => {
  const cases = [
    ["priority:critical", "P0"],
    ["priority:high", "P1"],
    ["priority:medium", "P2"],
    ["priority:low", "P2"],
    ["unknown", ""],
  ] as const;

  cases.forEach(([input, expected]) => {
    it(`maps "${input}" to "${expected}"`, () => {
      expect(mapPriorityLabel(input)).toBe(expected);
    });
  });
});
```

### 5. Boundary Condition Testing

```typescript
describe("calculateComplexity", () => {
  it("should handle minimum valid input", () => {
    expect(calculateComplexity({ files: 1, lines: 1 })).toBeGreaterThan(0);
  });

  it("should handle maximum expected input", () => {
    expect(calculateComplexity({ files: 100, lines: 10000 })).toBeLessThan(Infinity);
  });

  it("should reject negative values", () => {
    expect(() => calculateComplexity({ files: -1 })).toThrow();
  });
});
```

---

### Common Testing Patterns

#### Mocking External Commands

```typescript
import { exec } from "child_process";
import { promisify } from "util";

vi.mock("child_process");
const execAsync = promisify(exec);

vi.mocked(execAsync).mockResolvedValue({
  stdout: JSON.stringify({ data: "response" }),
  stderr: "",
} as any);
```

#### Testing with GraphQL Responses

```typescript
import { createMockGraphQLResponseWithPagination } from "../mocks/github-api";

const mockResponse = createMockGraphQLResponseWithPagination(
  [
    createMockReadyIssue({ number: 1, priority: "P0" }),
    createMockReadyIssue({ number: 2, priority: "P1" }),
  ],
  {
    status: "Ready",
    hasNextPage: false,
  }
);

vi.mocked(execAsync).mockResolvedValue({
  stdout: JSON.stringify(mockResponse),
  stderr: "",
} as any);
```

---

## Integration Tests

Integration tests exercise the full HTTP transport layer with real HTTP servers
(no mocked fetch). Two modes are available:

### Mock Server Mode (always runnable, no Docker required)

Uses in-process HTTP servers (`httptest.NewServer` in Go, `http.createServer` in
TypeScript) to simulate platform API responses. These run as part of the normal
test suite.

```bash
make test-platform-integration
```

Or individually:

```bash
# Go — IPC binary ↔ mock platform server
go test ./internal/ipc/... -run TestIPCPlatform -v -count=1

# TypeScript — PlatformApiClient ↔ mock HTTP server
npx -w nightgauge-vscode vitest run tests/integration/authFlowsIntegration.test.ts
```

**What's covered (mock mode):**

| Area               | Go (`ipc_platform_integration_test.go`)     | TypeScript (`authFlowsIntegration.test.ts`) |
| ------------------ | ------------------------------------------- | ------------------------------------------- |
| Health check       | `TestIPCPlatform_HealthCheck_Online`        | `describe('Health check')`                  |
| License validation | `TestIPCPlatform_ValidateLicense_Online`    | `describe('License validation')`            |
| Skill resolution   | `TestIPCPlatform_ResolveSkill_Online`       | `describe('Skill resolution')`              |
| Offline fallback   | `TestIPCPlatform_ValidateLicense_Offline_*` | `describe('Offline fallback')`              |
| GitHub token exch. | —                                           | `describe('GitHub token exchange')`         |
| Device flow        | —                                           | `describe('Device flow')`                   |
| Token refresh      | —                                           | `describe('Token refresh')`                 |
| Connection state   | `TestIPCPlatform_Status_Online`             | `describe('Connection state lifecycle')`    |

### Docker Compose Mode (requires acme-platform)

Runs the same test suites against a real platform instance started via Docker
Compose. Tests guarded by `PLATFORM_TEST_URL` are enabled in this mode.

```bash
make test-e2e-docker
```

Or manually:

```bash
export PLATFORM_TEST_URL=http://localhost:3000
go test ./internal/ipc/... -run TestIPCPlatform -v -count=1
npx -w nightgauge-vscode vitest run tests/integration/authFlowsIntegration.test.ts
```

The `scripts/test-e2e-platform.sh` script automates the full lifecycle: start
Docker Compose services, wait for health, run tests, tear down.

---

## GitLab CE Integration Harness (#3366)

The GitLab CE harness exercises the live-API path of the forge surface (Wave
5-2 of the forge-abstraction epic #3349). It boots a Dockerized GitLab CE
container, seeds deterministic fixtures, and runs the integration test suite
under `tests/integration/` with the `integration` build tag.

### Quick start (local)

```bash
make integration-gitlab
```

That target boots `gitlab/gitlab-ce:17.6.0-ce.0`, waits for `/-/health`,
generates a root PAT via the GitLab OAuth password grant, runs the seeder
binary (`go run ./tests/integration/cmd/seed`), executes the integration test
suite, and tears the container down with `docker compose down -v`.

### Environment variables

| Variable               | Default                 | Purpose                                                                   |
| ---------------------- | ----------------------- | ------------------------------------------------------------------------- |
| `GITLAB_E2E_URL`       | `http://localhost:8929` | Base URL of the running GitLab CE instance. Unset → suite skips entirely. |
| `GITLAB_E2E_PORT`      | `8929`                  | Port for the GitLab container; override to avoid host port conflicts.     |
| `GITLAB_ROOT_PASSWORD` | `nightgauge-test`       | Initial root password baked into the omnibus config.                      |
| `GITLAB_ROOT_TOKEN`    | (generated)             | Root-scope PAT — required by TestMain. Generated from the OAuth grant.    |
| `GITLAB_E2E_OWNER`     | `root`                  | Owner namespace used by skill-smoke scripts.                              |
| `GITLAB_E2E_REPO`      | `nightgauge-ci-test`    | Seeded project path.                                                      |
| `IB_FORGE`             | `gitlab`                | Selects the GitLab adapter for the binary.                                |

### Pinned GitLab CE version

The harness pins to `gitlab/gitlab-ce:17.6.0-ce.0` in
`tests/integration/docker-compose.gitlab.yml`. The CI cache key includes this
version string, so a version bump invalidates the cache on the next run. Update
the pin when:

1. A new minor or major version ships (cadence: approximately quarterly).
2. A GitLab security advisory requires it.
3. The forge adapter exercises an API path only available in newer CE.

After bumping, re-run `make integration-gitlab` locally to confirm the seeder
and tests still pass against the new image.

### Webhook receiver networking

The webhook tests start an `httptest.Server` inside the test binary rather
than a sidecar container (ADR-001 in
`.nightgauge/knowledge/features/3366-.../decisions.md`). The GitLab
container reaches the receiver via:

- **macOS Docker Desktop**: `host.docker.internal:<port>`
- **Linux Docker bridge**: `172.17.0.1:<port>`

The harness picks the right host string at runtime based on `runtime.GOOS`.

### Seeder fixtures

The seeder (`tests/integration/seed/seed.go`, binary at
`tests/integration/cmd/seed/main.go`) is idempotent — each resource is created
only when an existing match by deterministic name is not found. Fixtures
produced on each run:

- One private project: `root/nightgauge-ci-test` (initialized with README,
  default branch `main`)
- A 30-day root PAT named `nightgauge-test` (scopes: `api`,
  `read_repository`, `write_repository`)
- Two labels: `type:bug`, `type:feature`
- One issue board: `nightgauge-board`
- Five fixture issues, alternating label assignment
- One fixture MR on branch `feature/ci-test-mr`

### Known limitations

- **CE only**: the harness intentionally pins to GitLab CE. EE features
  (epics, multi-level group hierarchy, security dashboards) are out of scope
  for #3366.
- **OAuth app**: GitLab CE does not expose application CRUD through the
  documented public REST surface — the seeder skips OAuth app creation and
  documents this in `decisions.md`.
- **Pipeline hooks**: pipeline hook delivery requires GitLab CI to be
  enabled, which adds significant container memory pressure. The webhook
  suite asserts on Push, Merge Request, and Note hooks instead — they
  exercise the same delivery code path.

### Regenerating parity cassettes

The parity cassettes under `cmd/nightgauge/forge/testdata/gitlab-snapshots/`
are recorded from this harness (W5-1 follow-up tracked separately under
#3349). The seeder produces deterministic data so cassette diffs are
reviewable.

---

## Dockerized Mattermost Integration Harness (#3381)

The Mattermost harness exercises both directions of the Mattermost
integration end-to-end: **outbound** (post to an incoming webhook → message
lands in a channel) and **inbound** (a signed / unsigned slash command POST
→ the Go receiver in `internal/notifications/inbound` → dispatcher). It boots
a Dockerized Mattermost team-edition container with a separate Postgres,
seeds deterministic fixtures via the REST API, and runs the suite under
`tests/integration/mattermost/` with the `integration` build tag.

### Quick start (local)

```bash
make integration-mattermost
```

That target boots `mattermost/mattermost-team-edition:9.11.3` plus
`postgres:14-alpine`, waits for `/api/v4/system/ping`, runs the integration
suite (the suite seeds fixtures itself in `TestMain`), and tears the stack
down with `docker compose down -v`. Use `integration-mattermost-up` /
`integration-mattermost-down` to manage the container lifecycle separately.

### Environment variables

| Variable                    | Default                 | Purpose                                                                          |
| --------------------------- | ----------------------- | -------------------------------------------------------------------------------- |
| `MATTERMOST_E2E_URL`        | `http://localhost:8065` | Base URL of the running Mattermost instance. Unset → suite skips entirely.       |
| `MATTERMOST_E2E_PORT`       | `8065`                  | Host port for the Mattermost container; override to avoid host port conflicts.   |
| `MATTERMOST_ADMIN_USER`     | `admin`                 | System-admin username the seeder bootstraps and logs in with.                    |
| `MATTERMOST_ADMIN_PASSWORD` | `Nightgauge-Test-1`     | System-admin password. The first account on an open server becomes system-admin. |

### Pinned Mattermost version

The harness pins to `mattermost/mattermost-team-edition:9.11.3` in
`tests/integration/docker-compose.mattermost.yml`. The CI cache key includes
this version string, so a version bump invalidates the cache on the next run.
Update the pin when:

1. A new minor or major version ships (cadence: approximately quarterly).
2. A Mattermost security advisory requires it.
3. A test exercises an API path only available in a newer release.

After bumping, update the pin in both the compose file and the CI cache key,
then re-run `make integration-mattermost` locally to confirm the seeder and
tests still pass against the new image.

### Seeder fixtures

The seeder (`tests/integration/mattermost-fixtures/fixtures.go`) is
idempotent — each resource is created only when an existing match by
deterministic name is not found, so the suite is safe to re-run without a
teardown. Fixtures produced on each run:

- One open team: `nightgauge-test`
- One public channel: `ci-test-channel`
- One bot user account: `mm-ci-bot` (added to the team)
- One incoming webhook (`ci-incoming`) — the URL is captured for the
  outbound test
- One outgoing webhook (`ci-outgoing`) — the signing token is captured for
  the slash-command tests

On a fresh instance the seeder also creates the system-admin account (the
first account on an open server is granted system-admin) before logging in.

### Test design notes

- **Receiver in-process**: the slash-command tests start the inbound handler
  via `httptest.Server` rather than spawning the binary — no subprocess
  coordination needed (ADR-004).
- **Direct POST**: the signed slash-command test POSTs to the receiver
  directly from the test runner using the fixture-captured token, rather than
  triggering a real Mattermost webhook delivery (ADR-003). A full round-trip
  delivery test is deferred to #3382.
- **Token threading**: the `TokenStore` only populates from config env-refs,
  so the test threads the fixture token through the process env with
  `t.Setenv` and a synthetic `config.Config` — no production-code change to
  expose a direct setter.

### Known limitations

- **Team edition only**: the harness pins to Mattermost team edition.
  Enterprise features are out of scope for #3381.
- **No real inbound delivery**: Mattermost is not driven to deliver an
  outgoing-webhook callback to the receiver — that round-trip is deferred to
  #3382.

---

## Contract Testing Strategy (Issue #1826 Audit)

Issue #1826 audited all ~8,400 tests across the VSCode extension and SDK. The
codebase is overwhelmingly high quality — only ~22 tests were removed as
zero-value. This section codifies the audit criteria so future tests maintain
the same bar.

### What Makes a Test Zero-Value

A test is zero-value if removing it cannot cause a real bug to go undetected.
Four categories were identified:

| Category                  | Example                                                                          | Why It's Worthless                                                                |
| ------------------------- | -------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| **Logging-only**          | `expect(logger.info).toHaveBeenCalledWith('Starting pipeline')`                  | Log messages are implementation details; changing or removing them is never a bug |
| **Compile-time enforced** | `expect(disposable).toBeDefined()` as sole assertion                             | TypeScript enforces return types at compile time; a runtime check adds nothing    |
| **Tautological constant** | `expect(ENV_VAR_PREFIX).toBe('NIGHTGAUGE_')`                                     | A constant checking its own literal value — the test and code are the same string |
| **Self-fulfilling**       | Test body calls `mockLogger.info(...)` then asserts `mockLogger.info` was called | Tests the test, not the code                                                      |

### When `.toBeDefined()` IS Valid

`.toBeDefined()` is a valid assertion when used as a **TypeScript type guard**
before accessing properties with `!`:

```typescript
// ✅ VALID: Type guard enabling further assertions
const item = items.find((i) => i.number === 42);
expect(item).toBeDefined();
expect(item!.status).toBe("Ready"); // Safe because of guard above
```

It is NOT valid as a **sole assertion** that duplicates compile-time guarantees:

```typescript
// ❌ INVALID: TypeScript already enforces this return type
const disposable = registerCommand(logger);
expect(disposable).toBeDefined(); // Only assertion — adds nothing
```

### The Contract Testing Principle

Every test should verify an **observable contract** — something a caller, user,
or downstream system depends on:

- **Return values** from public functions
- **Side effects** visible outside the unit (UI messages, state changes, API
  calls)
- **Error behavior** (thrown errors, error messages shown to users)
- **State transitions** (context keys set, status bar updated)

Tests should NOT verify:

- Which internal helpers were called
- Specific log messages (unless logging IS the feature, e.g., an audit trail)
- That mocks were wired correctly (framework behavior)
- That constants equal their literal values

### Applying the Audit Checklist

When writing or reviewing tests, ask:

1. **If this test were deleted, could a real bug ship?** If no, the test is
   zero-value.
2. **Does the assertion verify something the compiler already guarantees?** If
   yes, remove it.
3. **Does the test call a mock in its own body, then assert the mock was
   called?** If yes, it's self-fulfilling — rewrite to test actual code.
4. **Is the test asserting a log message with no other behavioral assertion?**
   If yes, either add a behavioral assertion or remove the test.

---

## E2E Tests — VSCode Extension

End-to-end tests for the VSCode extension live in
`packages/nightgauge-vscode/tests/e2e/`. They wire real service classes
together with only the outermost boundary (IPC / filesystem) mocked.

### Running E2E Tests

```bash
# Run all E2E tests (from repo root)
npx -w nightgauge-vscode vitest run tests/e2e/

# Run a specific file
npx -w nightgauge-vscode vitest run tests/e2e/pipeline-execution.test.ts
```

### Test Files

| File                             | What it tests                                                                            |
| -------------------------------- | ---------------------------------------------------------------------------------------- |
| `activation-smoke.test.ts`       | Full activation chain: config load → IPC → tree view population, epic grouping, blocking |
| `activation-integration.test.ts` | Service chain: Config load → IPC init → ProjectBoardService → ProjectBoardTreeProvider   |
| `pipeline-execution.test.ts`     | Single-stage & multi-stage context file schema validation; IPC round-trip                |
| `ipc-integration.test.ts`        | IPC request/response lifecycle, event streaming, error handling, handler disposal        |

### Mocking IPC — Key Pattern

`vi.mock()` factories are hoisted before any imports are evaluated. To provide
mock function references inside the factory, use `vi.hoisted()` to create them
before the factory runs:

```typescript
// ✅ Correct: inline mock creation inside vi.hoisted()
const ipcMock = vi.hoisted(() => ({
  mockBoardList: vi.fn().mockResolvedValue([]),
  mockPipelineRun: vi.fn().mockResolvedValue({ success: true }),
  mockOn: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  mockStart: vi.fn().mockResolvedValue(undefined),
  // ...
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: ipcMock.mockBoardList,
      pipelineRun: ipcMock.mockPipelineRun,
      on: ipcMock.mockOn,
      start: ipcMock.mockStart,
    }),
  },
}));

// ❌ Wrong: calling an imported helper inside vi.hoisted()
// const ipcMock = vi.hoisted(() => setupIpcClientMock()); // → ReferenceError
```

Helper factories in `tests/mocks/ipc-client.ts` document the full interface but
**cannot** be called inside `vi.hoisted()`. Use them as reference when adding
new mock methods.

### Context File Schema Validation

Pipeline context files (`.nightgauge/pipeline/*.json`) are validated with
Zod schemas from `@nightgauge/sdk`. Use the helpers in
`tests/helpers/workspaceSetup.ts` to create isolated temp workspaces:

```typescript
import { createTempWorkspace, makeIssueContext } from "../helpers/workspaceSetup";
import { IssueContextSchema } from "@nightgauge/sdk/src/context/schemas/index.js";

const workspace = createTempWorkspace();
workspace.writeContext("issue-42.json", makeIssueContext(42));
const raw = workspace.readContext("issue-42.json");

const result = IssueContextSchema.safeParse(raw);
expect(result.success).toBe(true);

workspace.cleanup(); // always in afterEach
```

Available helpers: `makeIssueContext`, `makePlanningContext`, `makeDevContext`,
`createPipelineWorkspace` (creates all three at once).

### Checklist for Adding New E2E Tests

1. **Choose the right file** — use existing files if the scenario fits; create
   a new `tests/e2e/` file for distinct subsystems.
2. **Create hoisted mocks inline** — never call imported helpers inside
   `vi.hoisted()`.
3. **Use workspace helpers** for context file tests — never write to the real
   `.nightgauge/pipeline/` directory.
4. **Reset mocks in `beforeEach`** — call `mockXxx.mockReset()` to prevent
   test bleed. Don't use `vi.clearAllMocks()` (it clears global setup mocks).
5. **Dispose services in `afterEach`** — call `service.dispose()` and
   `workspace.cleanup()`.
6. **Verify at the boundary** — test what the user or downstream system
   observes (tree items, JSON files, IPC calls), not internal implementation.

---

## SDK Integration Tests

Integration tests validate multi-function workflows across SDK component
boundaries. They differ from unit tests in that they exercise real data flow
(file I/O, event delivery, cumulative state) rather than isolated functions.

### What They Test

- Multi-function workflows exercising SDK API contracts
- Data flow across component boundaries (e.g., write → read round-trips)
- Error handling at integration points (ContextNotFoundError, ContextValidationError)
- Event ordering and async behavior (stage:start before stage:complete)
- Cumulative token tracking across pipeline stages
- Atomic file write behavior for context handoffs

### Location

```
packages/nightgauge-sdk/src/__tests__/integration/
├── workflows/
│   ├── orchestration.integration.test.ts       # PipelineOrchestrator init, runStage(), events
│   ├── context-handoff.integration.test.ts     # ContextManager write/read, error classes
│   ├── token-tracking.integration.test.ts      # TokenTracker record(), getTotalUsage(), edge cases
│   └── event-driven-state.integration.test.ts  # EventBus on/off/once, event ordering
├── helpers/
│   ├── mocks.ts          # SDKResultMessage mock factories
│   ├── workspace.ts      # Temp workspace setup/teardown for real I/O tests
│   └── query-mocks.ts    # Mock SDKQueryFunction implementations
└── fixtures/
    ├── valid-issue-context.json
    ├── valid-planning-context.json
    └── invalid-planning-context.json
```

### How to Run

```bash
# Run integration tests only
npm run -w @nightgauge/sdk test:integration

# Run all SDK tests (unit + integration)
npx -w @nightgauge/sdk vitest run
```

### Writing New Integration Tests

1. **Use temp workspace helpers** — never write to the real `.nightgauge/pipeline/` directory. Use `createTestWorkspace()` from `helpers/workspace.ts`.
2. **Mock external dependencies at boundaries** — use `createSuccessQueryFn()` or `createFailureQueryFn()` from `helpers/query-mocks.ts` to avoid real Claude API calls.
3. **Use real EventBus and TokenTracker** — these components are simple enough that mocking them adds no value and hides real behavior.
4. **Test observable behavior** — verify what downstream systems see (files on disk, events received, cumulative totals), not internal implementation.
5. **Include both happy and error paths** — every workflow test must cover at least one error scenario.
6. **Keep runtime under 60s** — all integration suites combined target <45s. Use mocked query functions; never make real API calls.
7. **Clean up in `afterEach`** — always call `workspace.cleanup()` to remove temp directories.

### Integration Test Patterns

#### Pattern 1: Real filesystem I/O with temp workspace

```typescript
import { createTestWorkspace } from "../helpers/workspace.js";

let workspace: TestWorkspace;
let ctx: ContextManager;

beforeEach(async () => {
  workspace = await createTestWorkspace();
  ctx = new ContextManager(workspace.pipelineDir);
});

afterEach(async () => {
  await workspace.cleanup();
});
```

#### Pattern 2: Event ordering verification

```typescript
const received: string[] = [];
bus.on("stage:start", () => received.push("start"));
bus.on("stage:complete", () => received.push("complete"));

// trigger events...

expect(received).toEqual(["start", "complete"]);
```

#### Pattern 3: Cumulative state across operations

```typescript
const tracker = new TokenTracker();
// Record N stages
stages.forEach((stage) => tracker.record(stage, buildMockResultMessage(), 1000));
// Verify cumulative total
const total = tracker.getTotalUsage();
expect(total.stageCount).toBe(stages.length);
```

---

## Contract and Parity Tests

Cross-forge consumers (the VSCode extension, pipeline skills, the autonomous
scheduler) depend on GitHub and GitLab adapters returning the same logical
shape from the same logical operation. Three test layers enforce that
contract; together they form the parity surface.

### Test Layers

| Layer                | Location                                             | Purpose                                                                           |
| -------------------- | ---------------------------------------------------- | --------------------------------------------------------------------------------- |
| Cassette fixtures    | `internal/gitlab/testdata/cassettes/`                | Deterministic JSON responses replayed by `httptest.Server` stubs                  |
| Edition divergence   | `internal/gitlab/edition_test.go` (CE-vs-EE table)   | Per-feature CE/EE behaviour (weight, health, iteration, push rules, …)            |
| Cross-forge contract | `internal/forge/contract_test.go` (`RunContract`)    | Adapter-agnostic assertions run against both GitHub and GitLab `ForgeClient`      |
| Targeted parity      | `internal/forge/parity_test.go`, `parity_ci_test.go` | Specific cross-forge round-trips (Status/Priority/Size, Iteration, BlockedBy, CI) |

### `RunContract` — How It Works

`RunContract(t, adapter, client, fixtures)` runs an adapter-agnostic suite
against any `forge.ForgeClient`. The caller stands up an adapter-specific
stub server, seeds it with the fixture state, constructs the adapter, and
passes it in:

```go
// internal/forge/contract_test.go
func TestForgeContract_GitLab(t *testing.T) {
    srv := newGitlabContractStub(t)            // adapter-specific stub
    c := gitlab.NewClient(srv.URL, "tok")
    adapter := gitlab.NewForgeAdapter(c, gitlab.WithProject("o", "r"))
    RunContract(t, "GitLab", adapter, ContractFixtures{
        Owner: "o", Repo: "r", IssueNumber: 42,
        IssueTitle: "Sample issue",
        IssueLabels: []string{"bug", "priority:high"},
        IssueState: "opened",
        IncludeBoard: true,
        IncludeCI: true,
        PRNumber: 7,
    })
}
```

Subtest naming embeds adapter + method:

```
--- PASS: TestForgeContract_GitLab/GitLab/Issues/GetIssue
--- PASS: TestForgeContract_GitLab/GitLab/Board/GetItem
--- FAIL: TestForgeContract_GitHub/GitHub/Issues/ListIssues
```

so a regression in either adapter is attributed to the broken method by name,
not by line number.

`IncludeBoard` / `IncludeCI` flags let callers opt into the richer sub-suites.
GitHub currently runs only the Issues sub-suite — its Board (projectV2) and
CI (PR commits→checkSuites) GraphQL traversals are exercised in dedicated
parity files alongside their domain (`parity_test.go`, `parity_ci_test.go`).

### Cassette Fixtures

JSON files under `internal/gitlab/testdata/cassettes/<service>/<method>.json`
are static, handcrafted responses matching the GitLab REST API's shape.
Conventions:

- **Numeric IDs are fixed** (issue IID 42, project ID 5)
- **No timestamps** unless required, in which case they are fixed strings
- **Max 5–10 KB per file** — fixtures should encode the minimum the assertion
  needs
- **`web_url` uses `https://gitlab.example.com/...`**, never a real host

`internal/gitlab/cassettes_test.go` runs at `go test` time and asserts:

1. Every `*.json` under `testdata/cassettes/` parses as valid JSON
2. Each file is under the size cap
3. None contain time-dependent markers (`now()`, `$TIME$`, `{{now}}`)

To add a new cassette: capture the real response, trim to the minimum,
remove or fix timestamps, save under `<service>/<method-slug>.json`, and
serve it from a `stubGitLabServer.handle()` call in the test.

### Edition (CE vs EE) Divergence Cases

`TestEdition_CEvsEE_FeatureDivergence` is a single table-driven test in
`internal/gitlab/edition_test.go` covering ≥10 CE-vs-EE feature divergences:

- Weight / health_status field rejection on CE
- Iteration → milestone fallback on CE; native `iteration_id` on EE
- Push rules (EE-only) returning 404 on CE
- External status checks (Ultimate-only) returning 404 elsewhere
- Approval rules visibility differences
- And more — see the `cases` slice in the test for the canonical list.

Each case runs twice (`/EE` and `/CE` subtests) so failure surfaces pinpoint
the edition: `TestEdition_CEvsEE_FeatureDivergence/weight_field/CE`.

To add a new divergence case, append a `{name, run, wantCEErrIs, wantEEOK}`
entry to the `cases` slice. The `run` closure receives a fresh stub server
and exercises the feature path; it returns the error to be classified.

### `make test-parity`

The `test-parity` Makefile target runs the contract suite and prints a
per-method PASS/FAIL matrix:

```bash
make test-parity
# ...
# === Parity Pass/Fail Matrix ===
# PASS:    TestForgeContract_GitLab/GitLab/Issues/GetIssue
# PASS:    TestForgeContract_GitLab/GitLab/Board/GetItem
# PASS:    TestForgeContract_GitHub/GitHub/Issues/GetIssue
# PASS:    TestParityContract_StatusPriority_Size
# PASS:    TestParityContract_BlockedBy_RoundTrip
# ...
```

Use this target locally before opening a PR that touches either adapter, and
in CI as a gate — a single FAIL line in the matrix immediately attributes the
broken adapter+method.

The target invokes:

```bash
go test -run 'TestParityContract|TestForgeContract' ./... -v -count=1
```

`-count=1` disables result caching so the matrix reflects the current code,
not a stale pass.

### Coverage Gate for `internal/gitlab/`

The `internal/gitlab/` package is held to ≥85% statement coverage to match
the `internal/github/` baseline. Run locally:

```bash
go test ./internal/gitlab/... -coverprofile=/tmp/gl.out -count=1
go tool cover -func=/tmp/gl.out | tail -1
```

When coverage drops, look at the `0.0%` and sub-50% functions — those are
typically the right place to add a narrow boundary test. See
`internal/gitlab/coverage_test.go` for examples of compact tests for
previously-uncovered functions.

## Cross-Model Skill Evaluation Harness (#3814)

Skills are portable SKILL.md instruction files the pipeline runs against a model
chosen at spawn time (a tier alias — `haiku` / `sonnet` / `opus` — passed to the
`claude` CLI `--model` flag). A skill that works on Opus may need more explicit
detail to work on Haiku, and we actively change model routing and bump model
versions. The **cross-model skill evaluation harness** makes regressions from a
skill refactor or a model bump detectable: it runs a small set of representative
scenarios for a skill against each tier and reports pass/fail per
`(scenario, model)` cell.

See **[docs/SKILL_EVALUATION.md](SKILL_EVALUATION.md)** for the scenario format,
assertion reference, and how to add scenarios.

### Quick start

```bash
# Mock mode (deterministic, zero API cost — the default, what CI would run):
npx tsx scripts/evaluate-skills.ts
npx tsx scripts/evaluate-skills.ts --skills feature-planning,pr-create
npx tsx scripts/evaluate-skills.ts --baseline .nightgauge/skill-evals/baseline.jsonl

# Live mode (real `claude --print --model <tier>` calls — opt-in, NOT for CI):
NIGHTGAUGE_SKILL_EVAL_LIVE=1 npx tsx scripts/evaluate-skills.ts --mode live --skills pr-merge
```

The runner prints a pass/fail matrix and writes a JSONL run record to
`.nightgauge/skill-evals/` (gitignored). It exits non-zero when any cell
regresses versus a `--baseline` (or, with no baseline, when any cell fails) so a
future CI job can adopt it unchanged.

### Two-tier mode (mirrors #2092)

Like the `PLATFORM_TEST_URL` pattern from #2092, the harness has two tiers:

- **mock** (default) — resolves each cell's output from a deterministic fixture
  keyed by `(scenarioId, model)` under `evals/fixtures/<skill>/`. No API calls,
  no quota, fully repeatable. This is the only mode CI runs and what the
  harness's own unit tests use.
- **live** (`NIGHTGAUGE_SKILL_EVAL_LIVE=1`) — spawns the `claude` CLI by
  **tier alias**, exactly as the live pipeline does, so a concrete-version bump
  (Opus 4.8 → 4.9) is itself a regression the harness catches. Live mode relies
  on ambient `claude` auth; no API keys are read, stored, or logged.

### Harness unit tests

The harness ships its own mock-mode test suite (no live API):

```bash
npx -w @nightgauge/sdk vitest run tests/eval/
```

Coverage: the assertion-engine truth table (every assertion type, pass + fail),
schema validation (malformed scenarios rejected), matrix expansion, JSONL
round-trip, and the regression diff (a baseline `pass → fail` flip is flagged a
regression; a no-baseline cell is `added`, never a regression).

## Pipeline Regression Detection

Synthetic regression tests guard against failure classes that were explicitly
eliminated from the pipeline. Unlike integration tests, these run without Docker
or a live GitHub API — they exercise in-process gate logic only.

### What It Guards

Issue #3261 eliminated the `skill-no-op` failure class, which occurred when a
skill exited 0 but produced no actual state change. Without a CI guard, any
future change to `internal/orchestrator/`, `HeadlessOrchestrator.ts`, or
`skills/**` could silently re-introduce the class.

The synthetic regression suite (Issue #3270) asserts four invariants on every
PR that touches the relevant code paths:

1. Every stage's `StageGate.Verify()` returns `KindOK` — not `KindNoOp`.
2. `V2RunRecord.OutcomeType` ≠ `"skill-no-op"`.
3. `Tokens.EstimatedCostUSD` < $0.50.
4. No stage has `FailureCategory == "stop-hook-error"`.

### Fixture

**`tests/fixtures/pipeline/synthetic-noop.json`** — a minimal issue fixture
(number 9999, `size:XS`, `type:chore`) representing a single-line README edit.
Issue 9999 is a sentinel sentinel number — it does not exist on GitHub and must
never be used as input to a real pipeline run.

### How to Run Locally

```bash
# Run only the primary KindNoOp regression check (fast, no gh required):
go test ./tests/synthetic/... -run TestSyntheticNoOpRegression -count=1 -v

# Run the full synthetic suite:
go test ./tests/synthetic/... -count=1 -v
```

The full suite completes in under 10 seconds. The `TestSyntheticNoOpRegression`
sub-tests for `pr-create` and `pr-merge` make `gh pr view` calls and may be
slow (~3 s each) when no GitHub credentials are present; they will still pass
because those gates return `KindFail` (not `KindNoOp`) on network failure.

### CI Workflow

**`.github/workflows/synthetic-regression.yml`** triggers on PRs that touch:

- `internal/orchestrator/**`
- `packages/nightgauge-vscode/src/services/HeadlessOrchestrator.ts`
- `skills/**`
- `tests/fixtures/pipeline/**`
- `tests/synthetic/**`

It builds the Go binary and runs `TestSyntheticNoOpRegression` with a 120-second
timeout. The job uses `cancel-in-progress: true` so stale runs are evicted when
a new commit is pushed.

### Adding a New Regression Class

When a new failure class is eliminated and needs a guard:

1. Add the assertion to `tests/synthetic/regression_test.go` (new `Test*`
   function or a new sub-case in `TestSyntheticNoOpRegression`).
2. Update `tests/fixtures/pipeline/synthetic-noop.json` if the fixture needs new
   fields (bump `schema_version` if required).
3. Extend the path filter in `.github/workflows/synthetic-regression.yml` if the
   new guard covers a different code path.
4. Document the new invariant in this section.

## Author

nightgauge
