#!/usr/bin/env node

import { Buffer } from "node:buffer";
import process from "node:process";

const api = process.env.GITHUB_API_URL ?? "https://api.github.com";
const agreement = "I have read the CLA Document and I hereby sign the CLA";
const marker = "<!-- nightgauge-cla-gate -->";

const required = [
  "GITHUB_EVENT_PATH",
  "GITHUB_REPOSITORY",
  "GITHUB_TOKEN",
  "CLA_SIGNATURES_TOKEN",
  "CLA_SIGNATURES_REPOSITORY",
  "CLA_SIGNATURES_PATH",
];
for (const name of required) {
  if (!process.env[name]) throw new Error(`Missing required environment: ${name}`);
}

const fs = await import("node:fs/promises");
const event = JSON.parse(await fs.readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
const repository = process.env.GITHUB_REPOSITORY;
const pullNumber = event.pull_request?.number ?? event.issue?.number;
if (!pullNumber) throw new Error("Event is not associated with a pull request");

async function request(path, token, options = {}) {
  const response = await globalThis.fetch(`${api}${path}`, {
    ...options,
    headers: {
      Accept: "application/vnd.github+json",
      Authorization: `Bearer ${token}`,
      "X-GitHub-Api-Version": "2022-11-28",
      "User-Agent": "nightgauge-cla-gate",
      ...options.headers,
    },
  });
  if (!response.ok) {
    const detail = await response.text();
    throw new Error(`GitHub API ${response.status} for ${path}: ${detail}`);
  }
  return response.status === 204 ? null : response.json();
}

const githubToken = process.env.GITHUB_TOKEN;
const signaturesToken = process.env.CLA_SIGNATURES_TOKEN;
const pull = await request(`/repos/${repository}/pulls/${pullNumber}`, githubToken);
const login = pull.user.login;
const userId = pull.user.id;
if (!Number.isInteger(userId)) throw new Error("Pull request author is missing a numeric user id");
const allowlist = new Set(
  (process.env.CLA_ALLOWLIST ?? "")
    .split(",")
    .map((value) => value.trim().toLowerCase())
    .filter(Boolean)
);
const exempt = login.endsWith("[bot]") || allowlist.has(login.toLowerCase());

const [signatureOwner, signatureRepo] = process.env.CLA_SIGNATURES_REPOSITORY.split("/");
const signaturePath = process.env.CLA_SIGNATURES_PATH;
const corporatePath =
  process.env.CLA_CORPORATE_SIGNATURES_PATH ?? "signatures/version1/corporate.json";

async function loadStoreFile(filePath, fallback) {
  try {
    const file = await request(
      `/repos/${signatureOwner}/${signatureRepo}/contents/${filePath}`,
      signaturesToken
    );
    const decoded = Buffer.from(file.content.replace(/\n/g, ""), "base64").toString("utf8");
    return { value: JSON.parse(decoded), sha: file.sha };
  } catch (error) {
    if (fallback !== undefined && String(error).includes("GitHub API 404")) {
      return { value: fallback, sha: null };
    }
    throw error;
  }
}

async function loadSignatures() {
  const { value, sha } = await loadStoreFile(signaturePath);
  const entries = Array.isArray(value) ? value : value.signedContributors;
  if (!Array.isArray(entries)) {
    throw new Error("CLA signature store must contain signedContributors[]");
  }
  return { entries, sha, wrapped: !Array.isArray(value) };
}

async function loadCorporateSignatures() {
  const { value } = await loadStoreFile(corporatePath, { entities: [] });
  if (!Array.isArray(value.entities)) {
    throw new Error("Corporate CLA signature store must contain entities[]");
  }
  return value.entities;
}

async function loadClaRevision() {
  const file = await request(
    `/repos/${repository}/contents/CLA/individual.md?ref=main`,
    githubToken
  );
  if (!file.sha) throw new Error("CLA document response is missing its commit SHA");
  return file.sha;
}

async function recordSignature() {
  const { entries, sha, wrapped } = await loadSignatures();
  if (!entries.some((entry) => entry.id === userId)) {
    const claCommitSha = await loadClaRevision();
    entries.push({
      login,
      id: userId,
      pull_request: pullNumber,
      agreement_comment_url: event.comment.html_url,
      signed_at: new Date().toISOString(),
      cla_document: "CLA/individual.md",
      cla_version: "HA-CLA-I v1.0 (Option Five)",
      cla_commit_sha: claCommitSha,
    });
    entries.sort((a, b) => a.id - b.id);
    const storedValue = wrapped ? { signedContributors: entries } : entries;
    const update = {
      message: `chore(cla): record ${login} agreement`,
      content: Buffer.from(`${JSON.stringify(storedValue, null, 2)}\n`).toString("base64"),
      branch: "main",
    };
    if (sha) update.sha = sha;
    await request(
      `/repos/${signatureOwner}/${signatureRepo}/contents/${signaturePath}`,
      signaturesToken,
      {
        method: "PUT",
        body: JSON.stringify(update),
      }
    );
  }
}

async function setStatus(state, description) {
  await request(`/repos/${repository}/statuses/${pull.head.sha}`, githubToken, {
    method: "POST",
    body: JSON.stringify({
      state,
      context: "cla",
      description,
      target_url: `https://github.com/${repository}/blob/main/CLA/README.md`,
    }),
  });
}

async function ensurePrompt() {
  const comments = await request(
    `/repos/${repository}/issues/${pullNumber}/comments?per_page=100`,
    githubToken
  );
  if (comments.some((comment) => comment.body?.includes(marker))) return;
  await request(`/repos/${repository}/issues/${pullNumber}/comments`, githubToken, {
    method: "POST",
    body: JSON.stringify({
      body: `${marker}\nThanks for contributing! Before this pull request can be merged, please read the [Individual Contributor License Agreement](https://github.com/${repository}/blob/main/CLA/individual.md) and comment with exactly:\n\n> ${agreement}`,
    }),
  });
}

if (event.comment) {
  const isAuthor = event.comment.user?.login === login;
  const agreed = event.comment.body.trim() === agreement;
  if (isAuthor && agreed) await recordSignature();
}

const [{ entries }, corporateEntities] = await Promise.all([
  loadSignatures(),
  loadCorporateSignatures(),
]);
const corporateSigned = corporateEntities.some(
  (entity) =>
    entity.authorized_ids?.includes(userId) ||
    entity.authorized_logins?.some((value) => value.toLowerCase() === login.toLowerCase())
);
const signed =
  exempt ||
  corporateSigned ||
  entries.some(
    (entry) =>
      entry.id === userId ||
      (entry.id == null && entry.login?.toLowerCase() === login.toLowerCase())
  );
if (signed) {
  await setStatus("success", exempt ? "CLA exemption verified" : "CLA signed");
} else {
  await ensurePrompt();
  await setStatus("pending", "CLA signature required");
}
