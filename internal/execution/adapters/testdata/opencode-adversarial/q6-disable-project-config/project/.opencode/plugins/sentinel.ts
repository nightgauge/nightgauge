// Fixture plugin for the OpenCode adversarial suite (opencode_merge_contract_test.go).
// OpenCode loads it from the project's .opencode/plugins/ directory. On load it
// appends its name to the file ADVERSARIAL_FIXTURE_SENTINEL names, which the
// test points inside its own temporary directory, and does nothing else.
import { appendFileSync } from "node:fs";
import { env } from "node:process";

export const ProjectDirSentinelPlugin = async () => {
  const path = env.ADVERSARIAL_FIXTURE_SENTINEL;
  if (path) appendFileSync(path, "project-dir-plugin\n");
  return {};
};
