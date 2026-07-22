#!/usr/bin/env bash
set -euo pipefail

PLATFORM_DIR="$(git rev-parse --show-toplevel)/../acme-platform"
PLATFORM_PORT=3000
PLATFORM_TEST_URL="http://localhost:${PLATFORM_PORT}"

cleanup() {
  echo "Cleaning up..."
  kill "$API_PID" 2>/dev/null || true
  cd "$PLATFORM_DIR" 2>/dev/null && docker compose down 2>/dev/null || true
}
trap cleanup EXIT

echo "=== Nightgauge E2E Platform Tests ==="
echo "Platform directory: $PLATFORM_DIR"

if [ ! -d "$PLATFORM_DIR" ]; then
  echo "ERROR: Platform directory not found at $PLATFORM_DIR"
  echo "Clone acme/platform alongside this repo."
  exit 1
fi

# Start platform services
cd "$PLATFORM_DIR"
docker compose up -d postgres redis

# Wait for postgres
echo "Waiting for postgres..."
for i in $(seq 1 30); do
  if docker compose exec -T postgres pg_isready -U postgres 2>/dev/null; then
    echo "Postgres ready."
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "ERROR: Postgres did not become ready in 60s"
    exit 1
  fi
  sleep 2
done

# Run migrations
echo "Running migrations..."
npm run -w @acme-platform/db migrate 2>/dev/null || true

# Start API (background)
echo "Starting API server..."
npm run -w @acme-platform/api dev &
API_PID=$!

# Wait for API health
echo "Waiting for API health at $PLATFORM_TEST_URL/v1/health ..."
for i in $(seq 1 30); do
  if curl -sf "${PLATFORM_TEST_URL}/v1/health" > /dev/null 2>&1; then
    echo "API healthy."
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "ERROR: API did not become healthy in 60s"
    exit 1
  fi
  sleep 2
done

cd "$(git rev-parse --show-toplevel)"

export PLATFORM_TEST_URL

# Run Go integration tests
echo ""
echo "--- Running Go integration tests ---"
PLATFORM_TEST_URL="$PLATFORM_TEST_URL" go test ./internal/ipc/... -run TestIPCPlatform -v -count=1

# Run TypeScript integration tests
echo ""
echo "--- Running TypeScript integration tests ---"
PLATFORM_TEST_URL="$PLATFORM_TEST_URL" npx -w nightgauge-vscode vitest run tests/integration/authFlowsIntegration.test.ts

echo ""
echo "=== E2E platform tests complete ==="
