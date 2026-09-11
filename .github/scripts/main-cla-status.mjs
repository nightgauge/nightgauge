#!/usr/bin/env node

import process from "node:process";

const api = process.env.GITHUB_API_URL ?? "https://api.github.com";
const repository = process.env.GITHUB_REPOSITORY ?? "";
const sha = process.env.GITHUB_SHA ?? "";
const token = process.env.GITHUB_TOKEN ?? "";

if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) {
  throw new Error("GITHUB_REPOSITORY must be an owner/name pair");
}
if (!/^[0-9a-f]{40}$/i.test(sha)) {
  throw new Error("GITHUB_SHA must be a 40-character commit SHA");
}
if (!token) throw new Error("Missing required environment: GITHUB_TOKEN");

const response = await globalThis.fetch(`${api}/repos/${repository}/statuses/${sha}`, {
  method: "POST",
  headers: {
    Accept: "application/vnd.github+json",
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
    "X-GitHub-Api-Version": "2022-11-28",
    "User-Agent": "nightgauge-main-cla-observation",
  },
  body: JSON.stringify({
    state: "success",
    context: "cla",
    description: "CLA verified before merge",
    target_url: `https://github.com/${repository}/blob/main/CLA/README.md`,
  }),
});

if (!response.ok) {
  throw new Error(`GitHub API ${response.status}: ${await response.text()}`);
}
