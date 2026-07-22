/**
 * Regression tests for the IntegrationTestGate — Issue #2909.
 *
 * Covers the invariant from the issue: a repo with a trivially-broken
 * integration test must produce `validation_status: "failed"`, never a
 * silent pass. The three units under test compose to make that true.
 */

import { describe, expect, it } from "vitest";

import {
  classifyIntegrationOutcome,
  detectIntegrationRequirement,
  evaluateGate,
} from "../../../tools/integration-test-gate/index.js";

describe("detectIntegrationRequirement", () => {
  it("returns not-required when no signals are present", () => {
    const result = detectIntegrationRequirement({});
    expect(result.required).toBe(false);
    expect(result.commands).toEqual([]);
  });

  it("detects a package.json test:integration script", () => {
    const result = detectIntegrationRequirement({
      packageScripts: {
        test: "vitest run",
        "test:integration": "docker compose up -d && vitest run tests/integration",
      },
    });
    expect(result.required).toBe(true);
    expect(result.commands).toContain("npm run test:integration");
    expect(result.detectedVia).toContain("test:integration");
  });

  it("detects an integration step in the CI workflow", () => {
    const result = detectIntegrationRequirement({
      workflowRunLines: [
        "npm ci",
        "docker compose -f docker-compose.ci.yml up -d",
        "npm run test:integration -- --runInBand",
      ],
    });
    expect(result.required).toBe(true);
    expect(result.commands.some((c) => c.includes("test:integration"))).toBe(true);
  });

  it("falls back to tests/integration directory when no script is declared", () => {
    const result = detectIntegrationRequirement({ hasIntegrationTestDir: true });
    expect(result.required).toBe(true);
    expect(result.commands).toContain("npm test -- tests/integration");
  });

  it("ignores echo/comment noise inside workflow steps", () => {
    const result = detectIntegrationRequirement({
      workflowRunLines: [
        "# integration-tests (docker)",
        "echo 'starting integration tests'",
        "export FOO=bar",
      ],
    });
    expect(result.required).toBe(false);
  });
});

describe("classifyIntegrationOutcome", () => {
  it("treats exit 0 as ran + passed", () => {
    const result = classifyIntegrationOutcome({ exitCode: 0, stdout: "", stderr: "" });
    expect(result).toEqual({
      ran: true,
      passed: true,
      environmentalFailure: false,
      reason: "tests-passed",
    });
  });

  it("flags missing docker daemon as environmental", () => {
    const result = classifyIntegrationOutcome({
      exitCode: 1,
      stdout: "",
      stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
    });
    expect(result.ran).toBe(false);
    expect(result.environmentalFailure).toBe(true);
    expect(result.reason).toMatch(/docker/);
  });

  it("flags ECONNREFUSED on a service port as environmental", () => {
    const result = classifyIntegrationOutcome({
      exitCode: 1,
      stdout: "",
      stderr: "Error: connect ECONNREFUSED 127.0.0.1:5432",
    });
    expect(result.ran).toBe(false);
    expect(result.environmentalFailure).toBe(true);
  });

  it("flags postgres auth / db-missing as environmental", () => {
    const result = classifyIntegrationOutcome({
      exitCode: 1,
      stdout: "",
      stderr: 'database "app_test" does not exist',
    });
    expect(result.ran).toBe(false);
    expect(result.environmentalFailure).toBe(true);
    expect(result.reason).toMatch(/database/);
  });

  it("treats an assertion failure as a real test failure (not environmental)", () => {
    const result = classifyIntegrationOutcome({
      exitCode: 1,
      stdout: "FAIL tests/integration/auth.test.ts\n  expected 200, got 404",
      stderr: "",
    });
    expect(result.ran).toBe(true);
    expect(result.passed).toBe(false);
    expect(result.environmentalFailure).toBe(false);
    expect(result.reason).toBe("assertion-failure");
  });
});

describe("evaluateGate", () => {
  it("passes when mode=off regardless of requirement", () => {
    const decision = evaluateGate({
      requirement: { required: true, commands: ["npm run test:integration"], detectedVia: "x" },
      outcome: undefined,
      mode: "off",
    });
    expect(decision.validationStatus).toBe("passed");
  });

  it("passes when integration tests are not required", () => {
    const decision = evaluateGate({
      requirement: { required: false, commands: [], detectedVia: "no signals" },
      mode: "strict",
    });
    expect(decision.validationStatus).toBe("passed");
  });

  it("fails in strict mode when required tests were not attempted", () => {
    const decision = evaluateGate({
      requirement: {
        required: true,
        commands: ["npm run test:integration"],
        detectedVia: "script",
      },
      mode: "strict",
    });
    expect(decision.validationStatus).toBe("failed");
    expect(decision.shouldEmitFeedback).toBe(true);
  });

  it("passes (with warning) in best_effort mode when required tests were skipped", () => {
    const decision = evaluateGate({
      requirement: {
        required: true,
        commands: ["npm run test:integration"],
        detectedVia: "script",
      },
      outcome: {
        ran: false,
        passed: false,
        environmentalFailure: true,
        reason: "environmental: docker-daemon-unavailable",
      },
      mode: "best_effort",
    });
    expect(decision.validationStatus).toBe("passed");
    expect(decision.shouldEmitFeedback).toBe(true);
  });

  it("fails in strict mode on environmental failure — the core #2909 invariant", () => {
    const decision = evaluateGate({
      requirement: {
        required: true,
        commands: ["npm run test:integration"],
        detectedVia: "script",
      },
      outcome: {
        ran: false,
        passed: false,
        environmentalFailure: true,
        reason: "environmental: postgres-unreachable",
      },
      mode: "strict",
    });
    expect(decision.validationStatus).toBe("failed");
    expect(decision.reason).toMatch(/could not run/);
  });

  it("fails regardless of mode when tests ran and legitimately failed", () => {
    for (const mode of ["strict", "best_effort"] as const) {
      const decision = evaluateGate({
        requirement: {
          required: true,
          commands: ["npm run test:integration"],
          detectedVia: "script",
        },
        outcome: {
          ran: true,
          passed: false,
          environmentalFailure: false,
          reason: "assertion-failure",
        },
        mode,
      });
      expect(decision.validationStatus).toBe("failed");
    }
  });

  it("passes when tests ran and passed", () => {
    const decision = evaluateGate({
      requirement: {
        required: true,
        commands: ["npm run test:integration"],
        detectedVia: "script",
      },
      outcome: { ran: true, passed: true, environmentalFailure: false, reason: "tests-passed" },
      mode: "strict",
    });
    expect(decision.validationStatus).toBe("passed");
  });
});

describe("#2909 regression scenarios", () => {
  it("trivially-broken integration test → validate-failure outcome", () => {
    const requirement = detectIntegrationRequirement({
      packageScripts: { "test:integration": "vitest run tests/integration" },
    });
    const outcome = classifyIntegrationOutcome({
      exitCode: 1,
      stdout: "FAIL tests/integration/broken.test.ts\n  expected true but got false",
      stderr: "",
    });
    const decision = evaluateGate({ requirement, outcome, mode: "strict" });
    expect(decision.validationStatus).toBe("failed");
  });

  it("docker stack missing locally → strict mode blocks PR publication", () => {
    const requirement = detectIntegrationRequirement({
      workflowRunLines: ["docker compose up -d", "npm run test:integration:docker"],
    });
    const outcome = classifyIntegrationOutcome({
      exitCode: 125,
      stdout: "",
      stderr:
        "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
    });
    const decision = evaluateGate({ requirement, outcome, mode: "strict" });
    expect(decision.validationStatus).toBe("failed");
    expect(decision.shouldEmitFeedback).toBe(true);
  });
});
