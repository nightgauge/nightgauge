// Fixture npm plugin for the OpenCode adversarial suite. The test packs this
// directory and serves it from a registry stub on 127.0.0.1, so OpenCode
// installs it without leaving the machine. On load it appends its name to the
// file ADVERSARIAL_FIXTURE_SENTINEL names, inside the test's temporary
// directory, and does nothing else.
import { appendFileSync } from "node:fs";
import { env } from "node:process";

export const AdversarialFixtureNpmPlugin = async () => {
  const path = env.ADVERSARIAL_FIXTURE_SENTINEL;
  if (path) appendFileSync(path, "npm-plugin\n");
  return {};
};
