import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { spawn } from "node:child_process";
import { once } from "node:events";
import fs from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import test from "node:test";

const script = path.resolve(".github/scripts/cla-check.mjs");
const agreement = "I have read the CLA Document and I hereby sign the CLA";

// `faults` scripts transport failures per request URL, so the retry behaviour
// added for #976 can be driven end to end through the real script. A value of
// `[500, 429]` is consumed one entry per hit and then falls through to the
// normal payload; a bare `500` (or `"destroy"`) applies to every hit. The
// literal "destroy" kills the socket before any response byte is written, so
// `globalThis.fetch` REJECTS instead of returning a response — the third
// retryable class, which a status code cannot exercise.
async function runGate(event, initialSignatures = [], corporateEntities = [], options = {}) {
  const { faults = {}, expectFailure = false } = options;
  const state = {
    signatures: initialSignatures,
    prompts: [],
    statuses: [],
    writes: 0,
    // url -> array of Date.now() per hit. Timestamps rather than a count so a
    // test can assert the backoff actually slept between attempts without
    // measuring the child process's own startup.
    hits: {},
  };
  const server = http.createServer(async (request, response) => {
    (state.hits[request.url] ??= []).push(Date.now());
    const scripted = faults[request.url];
    const fault = Array.isArray(scripted) ? (scripted.length ? scripted.shift() : null) : scripted;
    if (fault === "destroy") return request.socket.destroy();
    if (typeof fault === "number") {
      response.writeHead(fault, { "content-type": "application/json" });
      return response.end(JSON.stringify({ message: `simulated ${fault}` }));
    }
    const body = await new Promise((resolve) => {
      let value = "";
      request.on("data", (chunk) => (value += chunk));
      request.on("end", () => resolve(value ? JSON.parse(value) : null));
    });
    const send = (value, status = 200) => {
      response.writeHead(status, { "content-type": "application/json" });
      response.end(JSON.stringify(value));
    };

    if (request.url === "/repos/nightgauge/nightgauge/pulls/7") {
      return send({
        user: { login: "external-contributor", id: 4242 },
        head: { sha: "abc123" },
      });
    }
    if (
      request.url === "/repos/nightgauge/.cla-signatures/contents/signatures/version1/cla.json" &&
      request.method === "GET"
    ) {
      return send({
        sha: "store-sha",
        content: Buffer.from(
          `${JSON.stringify({ signedContributors: state.signatures })}\n`
        ).toString("base64"),
      });
    }
    if (
      request.url === "/repos/nightgauge/.cla-signatures/contents/signatures/version1/cla.json" &&
      request.method === "PUT"
    ) {
      state.signatures = JSON.parse(
        Buffer.from(body.content, "base64").toString("utf8")
      ).signedContributors;
      state.writes += 1;
      return send({ content: { sha: "new-sha" } });
    }
    if (
      request.url ===
        "/repos/nightgauge/.cla-signatures/contents/signatures/version1/corporate.json" &&
      request.method === "GET"
    ) {
      return send({
        sha: "corporate-sha",
        content: Buffer.from(`${JSON.stringify({ entities: corporateEntities })}\n`).toString(
          "base64"
        ),
      });
    }
    if (request.url === "/repos/nightgauge/nightgauge/contents/CLA/individual.md?ref=main") {
      return send({ sha: "cla-document-sha" });
    }
    if (request.url === "/repos/nightgauge/nightgauge/issues/7/comments?per_page=100") {
      return send([]);
    }
    if (
      request.url === "/repos/nightgauge/nightgauge/issues/7/comments" &&
      request.method === "POST"
    ) {
      state.prompts.push(body.body);
      return send({ id: 1 }, 201);
    }
    if (
      request.url === "/repos/nightgauge/nightgauge/statuses/abc123" &&
      request.method === "POST"
    ) {
      state.statuses.push(body);
      return send({ id: 2 }, 201);
    }
    return send({ error: request.url }, 404);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");

  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "cla-gate-test-"));
  const eventPath = path.join(directory, "event.json");
  await fs.writeFile(eventPath, JSON.stringify(event));
  const child = spawn(process.execPath, [script], {
    env: {
      ...process.env,
      GITHUB_API_URL: `http://127.0.0.1:${server.address().port}`,
      GITHUB_EVENT_PATH: eventPath,
      GITHUB_REPOSITORY: "nightgauge/nightgauge",
      GITHUB_TOKEN: "github-test-token",
      CLA_SIGNATURES_TOKEN: "signatures-test-token",
      CLA_SIGNATURES_REPOSITORY: "nightgauge/.cla-signatures",
      CLA_SIGNATURES_PATH: "signatures/version1/cla.json",
      CLA_CORPORATE_SIGNATURES_PATH: "signatures/version1/corporate.json",
    },
    stdio: "pipe",
  });
  let stderr = "";
  child.stderr.on("data", (chunk) => (stderr += chunk));
  const [code] = await once(child, "exit");
  server.closeAllConnections();
  server.close();
  await fs.rm(directory, { recursive: true, force: true });
  state.code = code;
  state.stderr = stderr;
  if (expectFailure) {
    assert.notEqual(code, 0, "expected the gate to exit non-zero");
  } else {
    assert.equal(code, 0, stderr);
  }
  return state;
}

const pullPath = "/repos/nightgauge/nightgauge/pulls/7";

test("prompts an unsigned contributor and publishes a pending status", async () => {
  const state = await runGate({ pull_request: { number: 7 } });
  assert.equal(state.prompts.length, 1);
  assert.match(state.prompts[0], /nightgauge-cla-gate/);
  assert.equal(state.statuses.at(-1).context, "cla");
  assert.equal(state.statuses.at(-1).state, "pending");
});

test("records an exact author agreement and publishes success", async () => {
  const state = await runGate({
    issue: { number: 7, pull_request: {} },
    comment: {
      body: agreement,
      user: { login: "external-contributor" },
      html_url: "https://github.com/nightgauge/nightgauge/pull/7#issuecomment-99",
    },
  });
  assert.equal(state.writes, 1);
  assert.equal(state.signatures[0].login, "external-contributor");
  assert.equal(state.signatures[0].id, 4242);
  assert.equal(state.signatures[0].pull_request, 7);
  assert.equal(
    state.signatures[0].agreement_comment_url,
    "https://github.com/nightgauge/nightgauge/pull/7#issuecomment-99"
  );
  assert.equal(state.signatures[0].cla_commit_sha, "cla-document-sha");
  assert.equal(state.statuses.at(-1).state, "success");
  assert.equal(state.prompts.length, 0);
});

test("accepts an immutable-id individual signature after a login change", async () => {
  const state = await runGate({ pull_request: { number: 7 } }, [
    { id: 4242, login: "former-login" },
  ]);
  assert.equal(state.statuses.at(-1).state, "success");
  assert.equal(state.prompts.length, 0);
});

test("accepts a contributor authorized by a corporate agreement", async () => {
  const state = await runGate(
    { pull_request: { number: 7 } },
    [],
    [{ name: "Example Corp", authorized_ids: [4242], authorized_logins: [] }]
  );
  assert.equal(state.statuses.at(-1).state, "success");
  assert.equal(state.prompts.length, 0);
});

test("retries a transient 500 and still publishes a status", async () => {
  const state = await runGate({ pull_request: { number: 7 } }, [], [], {
    faults: { [pullPath]: [500, 500] },
  });
  const hits = state.hits[pullPath];
  assert.equal(hits.length, 3);
  // Pins the backoff itself: a retry loop whose sleep is a no-op passes every
  // other assertion here. 700ms is the -25% jitter floor of the first delay.
  assert.ok(hits[1] - hits[0] >= 700, `first retry waited only ${hits[1] - hits[0]}ms`);
  assert.equal(state.statuses.at(-1).context, "cla");
  assert.equal(state.statuses.at(-1).state, "pending");
});

test("retries a 429 rate-limit response", async () => {
  const state = await runGate({ pull_request: { number: 7 } }, [], [], {
    faults: { [pullPath]: [429] },
  });
  assert.equal(state.hits[pullPath].length, 2);
  assert.equal(state.statuses.at(-1).state, "pending");
});

test("retries a network-level failure", async () => {
  const state = await runGate({ pull_request: { number: 7 } }, [], [], {
    faults: { [pullPath]: ["destroy"] },
  });
  assert.equal(state.hits[pullPath].length, 2);
  assert.equal(state.statuses.at(-1).state, "pending");
});

test("does not retry a non-retryable 404", async () => {
  const state = await runGate({ pull_request: { number: 7 } }, [], [], {
    faults: { [pullPath]: 404 },
    expectFailure: true,
  });
  assert.equal(state.hits[pullPath].length, 1);
  assert.match(state.stderr, /GitHub API 404 for \/repos\/nightgauge\/nightgauge\/pulls\/7/);
  assert.doesNotMatch(state.stderr, /attempts/);
});

test("names the attempt count when HTTP retries are exhausted", async () => {
  const state = await runGate({ pull_request: { number: 7 } }, [], [], {
    faults: { [pullPath]: 500 },
    expectFailure: true,
  });
  assert.equal(state.hits[pullPath].length, 3);
  assert.match(state.stderr, /GitHub API 500 for .* after 3 attempts/);
});

test("names the attempt count when network retries are exhausted", async () => {
  const state = await runGate({ pull_request: { number: 7 } }, [], [], {
    faults: { [pullPath]: "destroy" },
    expectFailure: true,
  });
  assert.equal(state.hits[pullPath].length, 3);
  assert.match(state.stderr, /failed after 3 attempts/);
});
