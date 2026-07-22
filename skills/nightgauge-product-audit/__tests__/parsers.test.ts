import { describe, it, expect, vi, beforeEach } from "vitest";
import { readFileSync, existsSync } from "fs";
import {
  parseDocumentationEndpoints,
  endpointExistsInRoutes,
  extractRoutesFromFile,
} from "../src/parsers/documentation-parser.js";
import {
  loadOpenApiSpec,
  compareOpenApiSpecs,
  extractPackageVersion,
  compareSharedTypesVersions,
} from "../src/parsers/spec-parser.js";
import { validateReadmeCommands, summarizeValidation } from "../src/matchers/readme-validator.js";

vi.mock("fs");

const mockExistsSync = vi.mocked(existsSync);
const mockReadFileSync = vi.mocked(readFileSync);

beforeEach(() => {
  vi.resetAllMocks();
});

// ---------------------------------------------------------------------------
// documentation-parser
// ---------------------------------------------------------------------------

describe("parseDocumentationEndpoints", () => {
  it("extracts endpoints from a well-formed markdown table", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue(`
## API Endpoints

| Method | Path | Status | Notes |
|--------|------|--------|-------|
| GET | /v1/auth/me | Available | Returns current user |
| POST | /v1/auth/login | Available | Device flow login |
| DELETE | /v1/users/:id | Not implemented | Future work |
`);

    const results = parseDocumentationEndpoints("/fake/ECOSYSTEM.md");
    expect(results).toHaveLength(3);
    expect(results[0]).toMatchObject({
      method: "GET",
      path: "/v1/auth/me",
      status: "Available",
      line: expect.any(Number),
    });
    expect(results[2]).toMatchObject({
      method: "DELETE",
      path: "/v1/users/:id",
      status: "Not implemented",
    });
  });

  it("skips malformed tables and continues parsing", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue(`
## Malformed table

| Foo | Bar | Baz |
|-----|-----|-----|
| a   | b   | c   |

## Good table

| Method | Path | Status |
|--------|------|--------|
| GET | /valid | Available |
`);

    const results = parseDocumentationEndpoints("/fake/ECOSYSTEM.md");
    // Only the good table row is returned (malformed table lacks status column)
    expect(results).toHaveLength(1);
    expect(results[0]?.path).toBe("/valid");
  });

  it("returns empty array when file does not exist", () => {
    mockExistsSync.mockReturnValue(false);
    const results = parseDocumentationEndpoints("/nonexistent.md");
    expect(results).toHaveLength(0);
  });

  it("handles endpoint/path column alias", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue(`
| Method | Endpoint | Status |
|--------|----------|--------|
| POST | /v1/health | Available |
`);

    const results = parseDocumentationEndpoints("/fake/ECOSYSTEM.md");
    expect(results).toHaveLength(1);
    expect(results[0]?.path).toBe("/v1/health");
  });
});

describe("endpointExistsInRoutes", () => {
  it("finds an exact match", () => {
    expect(endpointExistsInRoutes("/v1/auth/me", ["/v1/auth/me"])).toBe(true);
  });

  it("matches case-insensitively", () => {
    expect(endpointExistsInRoutes("/V1/Auth/Me", ["/v1/auth/me"])).toBe(true);
  });

  it("returns false when no match", () => {
    expect(endpointExistsInRoutes("/v1/missing", ["/v1/auth/me"])).toBe(false);
  });

  it("handles trailing slashes", () => {
    expect(endpointExistsInRoutes("/v1/auth/", ["/v1/auth"])).toBe(true);
  });
});

describe("extractRoutesFromFile", () => {
  it("extracts Hono-style routes", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue(`
const app = new Hono();
app.get('/v1/health', healthHandler);
app.post("/v1/auth/login", loginHandler);
app.delete('/v1/users/:id', deleteHandler);
`);

    const routes = extractRoutesFromFile("/fake/routes.ts");
    expect(routes).toContain("/v1/health");
    expect(routes).toContain("/v1/auth/login");
    expect(routes).toContain("/v1/users/:id");
  });

  it("returns empty array for non-existent file", () => {
    mockExistsSync.mockReturnValue(false);
    expect(extractRoutesFromFile("/nonexistent.ts")).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// spec-parser
// ---------------------------------------------------------------------------

describe("loadOpenApiSpec", () => {
  it("loads and parses valid JSON", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue(
      JSON.stringify({
        openapi: "3.0.0",
        paths: { "/v1/health": { get: {} } },
      })
    );

    const spec = loadOpenApiSpec("/fake/openapi.json");
    expect(spec).not.toBeNull();
    expect(spec?.paths?.["/v1/health"]).toBeDefined();
  });

  it("returns null for missing file", () => {
    mockExistsSync.mockReturnValue(false);
    expect(loadOpenApiSpec("/nonexistent.json")).toBeNull();
  });

  it("returns null for invalid JSON", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue("{ invalid json");
    expect(loadOpenApiSpec("/fake/bad.json")).toBeNull();
  });
});

describe("compareOpenApiSpecs", () => {
  it("detects paths missing in Angular", () => {
    const platform = {
      paths: { "/v1/auth": {}, "/v1/billing": {}, "/v1/teams": {} },
    };
    const angular = { paths: { "/v1/auth": {} } };

    const result = compareOpenApiSpecs(platform, angular);
    expect(result.missingInAngular).toContain("/v1/billing");
    expect(result.missingInAngular).toContain("/v1/teams");
    expect(result.extraInAngular).toHaveLength(0);
  });

  it("detects extra paths in Angular", () => {
    const platform = { paths: { "/v1/auth": {} } };
    const angular = {
      paths: { "/v1/auth": {}, "/v1/deprecated": {} },
    };

    const result = compareOpenApiSpecs(platform, angular);
    expect(result.extraInAngular).toContain("/v1/deprecated");
    expect(result.missingInAngular).toHaveLength(0);
  });

  it("handles empty paths gracefully", () => {
    const result = compareOpenApiSpecs({}, {});
    expect(result.missingInAngular).toHaveLength(0);
    expect(result.extraInAngular).toHaveLength(0);
    expect(result.pathCount).toEqual({ platform: 0, angular: 0 });
  });
});

describe("extractPackageVersion", () => {
  it("extracts dependency version", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue(
      JSON.stringify({
        name: "my-app",
        version: "1.0.0",
        dependencies: {
          "@nightgauge/shared-types": "^2.3.1",
        },
      })
    );

    const result = extractPackageVersion("/fake/package.json", "@nightgauge/shared-types");
    expect(result).not.toBeNull();
    expect(result?.version).toBe("2.3.1"); // stripped ^
  });

  it("returns null when package not found", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue(JSON.stringify({ name: "my-app", dependencies: {} }));

    const result = extractPackageVersion("/fake/package.json", "@nightgauge/shared-types");
    expect(result).toBeNull();
  });

  it("returns null for missing file", () => {
    mockExistsSync.mockReturnValue(false);
    expect(extractPackageVersion("/nonexistent.json", "@nightgauge/shared-types")).toBeNull();
  });
});

describe("compareSharedTypesVersions", () => {
  it("detects version mismatch", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync
      .mockReturnValueOnce(
        JSON.stringify({
          name: "nightgauge",
          dependencies: { "@nightgauge/shared-types": "^2.0.0" },
        })
      )
      .mockReturnValueOnce(
        JSON.stringify({
          name: "acme-dashboard",
          dependencies: { "@nightgauge/shared-types": "^1.5.0" },
        })
      );

    const versions = compareSharedTypesVersions([
      "/fake/nightgauge/package.json",
      "/fake/acme-dashboard/package.json",
    ]);

    const values = Array.from(versions.values());
    expect(new Set(values).size).toBe(2); // different versions
  });
});

// ---------------------------------------------------------------------------
// readme-validator
// ---------------------------------------------------------------------------

describe("validateReadmeCommands", () => {
  it("validates npm run commands against package.json", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync
      .mockReturnValueOnce(
        `
# Getting Started

Run \`npm run build\` to build.
Run \`npm run test\` to test.
Run \`npm run nonexistent\` to break.
`
      )
      .mockReturnValueOnce(JSON.stringify({ scripts: { build: "tsc", test: "vitest run" } }));

    const results = validateReadmeCommands("/fake/README.md", "/fake/package.json");
    const invalid = results.filter((r) => !r.valid);
    expect(invalid).toHaveLength(1);
    expect(invalid[0]?.command).toBe("npm run nonexistent");
    expect(invalid[0]?.reason).toContain("nonexistent");
  });

  it("returns empty array for missing README", () => {
    mockExistsSync.mockReturnValue(false);
    expect(validateReadmeCommands("/nonexistent.md")).toHaveLength(0);
  });

  it("treats all npm run commands as valid when package.json missing", () => {
    mockExistsSync.mockReturnValueOnce(true).mockReturnValue(false);
    mockReadFileSync.mockReturnValueOnce("Run `npm run anything` to proceed.");

    const results = validateReadmeCommands("/fake/README.md", "/fake/package.json");
    expect(results.every((r) => r.valid)).toBe(true);
  });
});

describe("summarizeValidation", () => {
  it("correctly counts valid and invalid", () => {
    const results = [
      { command: "npm run build", valid: true, file: "README.md", line: 1 },
      {
        command: "npm run broken",
        valid: false,
        reason: "not found",
        file: "README.md",
        line: 2,
      },
      { command: "go build ./...", valid: true, file: "README.md", line: 3 },
    ];

    const summary = summarizeValidation(results);
    expect(summary.total).toBe(3);
    expect(summary.valid).toBe(2);
    expect(summary.invalid).toBe(1);
    expect(summary.invalidCommands).toHaveLength(1);
  });
});
