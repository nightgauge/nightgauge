/**
 * IpcContract.coverage.test.ts — Structural contract verification (no binary required).
 *
 * Verifies the TypeScript ↔ Go IPC contract at the source-file level:
 *
 *   1. Every non-skip //ipc:method annotation in server.go has a corresponding
 *      `async <tsName>` method in IpcClient.generated.ts.
 *   2. No generated method exists without a matching //ipc:method annotation
 *      (orphan detection).
 *   3. IPC_PROTOCOL_VERSION in IpcClient.generated.ts matches ProtocolVersion
 *      in cmd/ipc-codegen/main.go.
 *
 * This test runs in <100ms (file reads only) and enforces the structural
 * TypeScript↔Go contract without spawning the Go binary. It is complementary
 * to the Go runtime contract tests in internal/ipc/ipc_contract_test.go.
 *
 * @see internal/ipc/server.go         — Go handler registrations + //ipc:method annotations
 * @see internal/ipc/ipc_contract_test.go — Go runtime contract tests (real binary)
 * @see src/services/IpcClient.generated.ts — Generated TypeScript methods
 * @see cmd/ipc-codegen/main.go        — Codegen tool (source of ProtocolVersion)
 */

import { describe, it, expect } from "vitest";
import * as fs from "fs";
import * as path from "path";

// Resolve repo root: this file is at packages/nightgauge-vscode/tests/services/
// Going up 4 levels reaches the repo root.
const REPO_ROOT = path.resolve(__dirname, "../../../../");

const SERVER_GO = path.join(REPO_ROOT, "internal", "ipc", "server.go");
const GENERATED_TS = path.join(
  REPO_ROOT,
  "packages",
  "nightgauge-vscode",
  "src",
  "services",
  "IpcClient.generated.ts"
);
const CODEGEN_GO = path.join(REPO_ROOT, "cmd", "ipc-codegen", "main.go");

// ─── Parsers ─────────────────────────────────────────────────────────────────

interface Annotation {
  tsName: string;
  skip: boolean;
}

/**
 * Parse all //ipc:method annotations from server.go.
 *
 * Formats handled:
 *   //ipc:method tsName params:T result:R            → { tsName, skip: false }
 *   //ipc:method tsName params:T result:R skip       → { tsName, skip: true }
 *   //ipc:method tsName params:T result:R ... skip   → { tsName, skip: true }
 *   //ipc:method skip                                → { tsName: '', skip: true }
 */
function parseAnnotations(content: string): Annotation[] {
  const fullAnnotationRe = /^\/\/ipc:method\s+(\S+)\s+params:\S+\s+result:\S+(.*)?$/;
  const simpleSkipRe = /^\/\/ipc:method\s+skip$/;

  const annotations: Annotation[] = [];

  for (const rawLine of content.split("\n")) {
    const line = rawLine.trim();

    if (simpleSkipRe.test(line)) {
      annotations.push({ tsName: "", skip: true });
      continue;
    }

    const m = fullAnnotationRe.exec(line);
    if (m) {
      const modifiers = m[2] ?? "";
      annotations.push({
        tsName: m[1],
        skip: /\bskip\b/.test(modifiers),
      });
    }
  }

  return annotations;
}

/**
 * Parse all `async <methodName>(` declarations from the generated TypeScript
 * class body. Returns only the method names.
 */
function parseGeneratedMethods(content: string): string[] {
  const methodRe = /^\s+async (\w+)\(/gm;
  const methods: string[] = [];
  let match: RegExpExecArray | null;
  while ((match = methodRe.exec(content)) !== null) {
    methods.push(match[1]);
  }
  return methods;
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("IPC contract coverage (structural)", () => {
  it("every non-skip ipc:method annotation has a generated TypeScript method", () => {
    const serverContent = fs.readFileSync(SERVER_GO, "utf8");
    const generatedContent = fs.readFileSync(GENERATED_TS, "utf8");

    const annotations = parseAnnotations(serverContent);
    const nonSkipNames = annotations.filter((a) => !a.skip && a.tsName !== "").map((a) => a.tsName);

    const generatedSet = new Set(parseGeneratedMethods(generatedContent));
    const missing = nonSkipNames.filter((name) => !generatedSet.has(name));

    expect(
      missing,
      `These //ipc:method annotations (non-skip) are missing from IpcClient.generated.ts:\n` +
        `  ${missing.join("\n  ")}\n\n` +
        `Run: make generate-ipc-client`
    ).toHaveLength(0);
  });

  it("no generated method exists without an ipc:method annotation", () => {
    const serverContent = fs.readFileSync(SERVER_GO, "utf8");
    const generatedContent = fs.readFileSync(GENERATED_TS, "utf8");

    const annotations = parseAnnotations(serverContent);
    const annotatedNames = new Set(
      annotations.filter((a) => !a.skip && a.tsName !== "").map((a) => a.tsName)
    );

    const generatedMethods = parseGeneratedMethods(generatedContent);
    const orphans = generatedMethods.filter((name) => !annotatedNames.has(name));

    expect(
      orphans,
      `These methods in IpcClient.generated.ts have no //ipc:method annotation in server.go:\n` +
        `  ${orphans.join("\n  ")}\n\n` +
        `Run: make generate-ipc-client or remove orphan methods from IpcClient.generated.ts`
    ).toHaveLength(0);
  });

  it("TypeScript IPC_PROTOCOL_VERSION matches Go ProtocolVersion in codegen", () => {
    const generatedContent = fs.readFileSync(GENERATED_TS, "utf8");
    const codegenContent = fs.readFileSync(CODEGEN_GO, "utf8");

    const tsMatch = /IPC_PROTOCOL_VERSION\s*=\s*(\d+)/.exec(generatedContent);
    const goMatch = /const\s+ProtocolVersion\s*=\s*(\d+)/.exec(codegenContent);

    expect(tsMatch, "IPC_PROTOCOL_VERSION not found in IpcClient.generated.ts").toBeTruthy();
    expect(goMatch, "ProtocolVersion not found in cmd/ipc-codegen/main.go").toBeTruthy();

    const tsVersion = parseInt(tsMatch![1], 10);
    const goVersion = parseInt(goMatch![1], 10);

    expect(
      tsVersion,
      `TypeScript IPC_PROTOCOL_VERSION (${tsVersion}) does not match Go ProtocolVersion (${goVersion}).\n` +
        `Bump both when the IPC contract changes incompatibly.`
    ).toBe(goVersion);
  });
});
