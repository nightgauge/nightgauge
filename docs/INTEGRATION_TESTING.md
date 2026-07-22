# Integration Testing

This document covers the integration and end-to-end testing strategy for the
nightgauge Go binary's platform API integration.

## Test Tiers

### Tier 1: Unit Tests (always run)

Unit tests use `httptest.NewServer` to mock the platform API. They run as part
of the standard `go test ./...` suite with no external dependencies.

```bash
# Run all unit tests
go test ./internal/platform/... -count=1

# Run specific service tests
go test ./internal/platform/... -run TestAuthService -count=1
go test ./internal/platform/... -run TestSkillService -count=1
go test ./internal/platform/... -run TestLicenseService -count=1
```

**Files:**

- `internal/platform/auth_test.go` — AuthService unit tests
- `internal/platform/license_test.go` — LicenseService unit tests
- `internal/platform/skills_test.go` — SkillService unit tests
- `internal/platform/client_test.go` — Client and health polling tests

### Tier 2: IPC Integration Tests (always run)

IPC integration tests build the real `nightgauge` binary, start it as a
subprocess, and exercise the full request-dispatch-response cycle over
JSON-over-stdio transport. Platform tests inject a mock platform URL via
environment variables.

```bash
# Run all IPC integration tests
go test ./internal/ipc/... -count=1

# Run platform-specific IPC tests
go test ./internal/ipc/... -run TestIPC_Platform -count=1
go test ./internal/ipc/... -run TestIPC_Auth -count=1
```

**Files:**

- `internal/ipc/server_integration_test.go` — Core IPC tests (ready event,
  wire format, queue)
- `internal/ipc/platform_integration_test.go` — Platform and auth IPC tests

### Tier 3: E2E Tests (Docker Compose required)

E2E tests run against a real platform instance. They use the `integration` build
tag and are NOT included in standard `go test ./...` runs.

```bash
# Run E2E tests
make test-integration

# Or manually:
go test -tags integration ./internal/platform/... -count=1
```

## E2E Setup

### Prerequisites

1. Clone the platform repo:

   ```bash
   cd ../acme-platform
   ```

2. Start the platform:

   ```bash
   docker compose up -d
   npm run -w @acme-platform/db migrate
   ```

3. Set environment variables:

   ```bash
   export PLATFORM_E2E_URL=http://localhost:3000
   export PLATFORM_E2E_GITHUB_TOKEN=<PAT with read:user user:email>
   export PLATFORM_E2E_LICENSE_KEY=<valid license key>
   export PLATFORM_E2E_API_KEY=<API key if required>
   ```

4. Run tests:
   ```bash
   make test-integration
   ```

### Required Environment Variables

| Variable                    | Required          | Description                            |
| --------------------------- | ----------------- | -------------------------------------- |
| `PLATFORM_E2E_URL`          | Yes               | Platform API base URL                  |
| `PLATFORM_E2E_GITHUB_TOKEN` | For auth tests    | GitHub PAT with `read:user user:email` |
| `PLATFORM_E2E_LICENSE_KEY`  | For license tests | Valid license key                      |
| `PLATFORM_E2E_API_KEY`      | Optional          | API key for authenticated requests     |

Tests that require missing env vars are automatically skipped via `t.Skip()`.

## CI Status

E2E tests are **not** run in CI. Docker Compose is not yet available in the CI
environment. The `make test-integration` target is for local development only.

Standard CI runs `go test ./...` which covers Tier 1 and Tier 2 tests.

## IPC Test Harness

The IPC integration tests use `ipcTestHarness` (defined in
`server_integration_test.go`) which:

1. Builds the real binary via `TestMain`
2. Starts it with `serve --workspace <temp-dir>`
3. Communicates via stdin/stdout JSON-over-stdio protocol
4. Provides helpers: `sendRequest()`, `readResponseFor()`, `awaitReady()`

For platform tests, `newIpcTestHarnessWithEnv()` (in
`platform_integration_test.go`) extends the harness with additional environment
variables to inject a mock platform URL.

## Adding New Tests

### Unit Test (Tier 1)

Follow the pattern in `auth_test.go`:

```go
func TestMyService_Method_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Handle routes
    }))
    defer srv.Close()

    cfg := Config{BaseURL: srv.URL}
    c, err := NewClient(cfg)
    // ...
}
```

### IPC Integration Test (Tier 2)

Follow the pattern in `platform_integration_test.go`:

```go
func TestIPC_MyMethod_Success(t *testing.T) {
    h, _ := harnessWithPlatform(t)
    h.awaitReady()

    id := h.sendRequest("my.method", map[string]interface{}{...})
    resp := h.readResponseFor(id, nil)
    // Assert on resp
}
```
