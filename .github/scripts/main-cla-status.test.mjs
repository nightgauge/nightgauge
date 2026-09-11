import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { test } from "node:test";

const script = path.resolve(".github/scripts/main-cla-status.mjs");

function run(env) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [script], {
      env: { ...process.env, ...env },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("close", (code) => resolve({ code, stdout, stderr }));
  });
}

test("posts the trusted main commit's required CLA context", async (t) => {
  let request;
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      request = { method: req.method, url: req.url, headers: req.headers, body: JSON.parse(body) };
      res.writeHead(201, { "Content-Type": "application/json" });
      res.end("{}");
    });
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => server.close());

  const { port } = server.address();
  const sha = "a".repeat(40);
  const result = await run({
    GITHUB_API_URL: `http://127.0.0.1:${port}`,
    GITHUB_REPOSITORY: "nightgauge/nightgauge",
    GITHUB_SHA: sha,
    GITHUB_TOKEN: "test-token",
  });

  assert.equal(result.code, 0, result.stderr);
  assert.equal(request.method, "POST");
  assert.equal(request.url, `/repos/nightgauge/nightgauge/statuses/${sha}`);
  assert.equal(request.headers.authorization, "Bearer test-token");
  assert.deepEqual(request.body, {
    state: "success",
    context: "cla",
    description: "CLA verified before merge",
    target_url: "https://github.com/nightgauge/nightgauge/blob/main/CLA/README.md",
  });
});

test("fails closed for an invalid commit SHA", async () => {
  const result = await run({
    GITHUB_REPOSITORY: "nightgauge/nightgauge",
    GITHUB_SHA: "main",
    GITHUB_TOKEN: "test-token",
  });

  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /40-character commit SHA/);
});

test("workflows emit both missing required contexts on main", async () => {
  const [cla, codeql] = await Promise.all([
    readFile(".github/workflows/cla.yml", "utf8"),
    readFile(".github/workflows/codeql.yml", "utf8"),
  ]);

  assert.match(cla, /push:\n\s+branches: \[main\]/);
  assert.match(cla, /run: node \.github\/scripts\/main-cla-status\.mjs/);
  assert.match(codeql, /codeql:\n\s+name: CodeQL\n\s+if: always\(\)\n\s+needs: analyze/);
  assert.match(codeql, /run: test "\$ANALYZE_RESULT" = success/);
});
