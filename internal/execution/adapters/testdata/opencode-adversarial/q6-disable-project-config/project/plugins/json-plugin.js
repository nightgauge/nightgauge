// Fixture plugin for the OpenCode adversarial suite. On load it appends its
// name to the file ADVERSARIAL_FIXTURE_SENTINEL names, inside the test's
// temporary directory, and does nothing else.
import { appendFileSync } from "node:fs";
import { env } from "node:process";

export const ProjectJSONPlugin = async () => {
  const path = env.ADVERSARIAL_FIXTURE_SENTINEL;
  if (path) appendFileSync(path, "project-json-plugin\n");
  return {};
};
