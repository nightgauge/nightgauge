/**
 * Launcher for the VSCode host smoke tier — runs in plain Node (via tsx),
 * outside the extension host.
 *
 * Responsibilities, in order:
 *   1. Refuse to start unless the artefacts the tier depends on exist.
 *      A host run that boots VSCode against a missing `dist/extension.cjs`
 *      fails deep inside the window with an unhelpful message; failing here
 *      names the missing build step instead.
 *   2. Create a throwaway workspace folder — empty, so nothing in
 *      `activationEvents` matches and the extension stays inert until the
 *      activation suite says otherwise.
 *   3. Download and launch VSCode with `--extensionDevelopmentPath` at this
 *      package and `--extensionTestsPath` at the bundled entry point.
 *   4. Verify the in-host module actually ran. A window that dies before
 *      loading it can still exit 0; without this check, that is a green tier
 *      that observed nothing.
 *
 * Headless: on Linux, Electron needs an X server. CI wraps this whole
 * command in `xvfb-run --auto-servernum`, which is what
 * `@vscode/test-electron`'s own documentation prescribes. There is
 * deliberately no "no display, skipping" branch — a smoke tier that skips
 * itself is worse than absent, because it reports green.
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { runTests } from "@vscode/test-electron";

const here = path.dirname(fileURLToPath(import.meta.url));
const packageRoot = path.resolve(here, "..", "..");

const EXTENSION_BUNDLE = path.join(packageRoot, "dist", "extension.cjs");
const TESTS_BUNDLE = path.join(packageRoot, "out", "vscode-host", "index.host.cjs");
const FIXTURE_SOURCE = path.join(packageRoot, "tests", "fixtures", "vscode-host", "populated");

function requireFile(file: string, remedy: string): void {
  if (!fs.existsSync(file)) {
    console.error(`ERROR: missing ${path.relative(packageRoot, file)}\n  ${remedy}`);
    process.exit(1);
  }
}

async function main(): Promise<void> {
  requireFile(
    EXTENSION_BUNDLE,
    "Run `npm run -w nightgauge-vscode build` first — the host loads the real bundle, not src/."
  );
  requireFile(
    TESTS_BUNDLE,
    "Run `npm run -w nightgauge-vscode build:host-tests` first (the `test:host` script does this for you)."
  );
  requireFile(FIXTURE_SOURCE, "The committed populated fixture is missing from the tree.");

  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), "nightgauge-host-"));
  const workspace = path.join(scratch, "workspace");
  const userDataDir = path.join(scratch, "user-data");
  const extensionsDir = path.join(scratch, "extensions");
  const transcript = path.join(scratch, "transcript.txt");
  for (const dir of [workspace, userDataDir, extensionsDir]) {
    fs.mkdirSync(dir, { recursive: true });
  }

  console.log(`VSCode host smoke tier: workspace ${workspace}`);

  let exitCode = 0;
  try {
    await runTests({
      extensionDevelopmentPath: packageRoot,
      extensionTestsPath: TESTS_BUNDLE,
      extensionTestsEnv: {
        NIGHTGAUGE_HOST_FIXTURE_SOURCE: FIXTURE_SOURCE,
        NIGHTGAUGE_HOST_TRANSCRIPT: transcript,
        // Keep the tier off the developer's real machine-tier config, the
        // same way tests/setup.ts does for vitest.
        NIGHTGAUGE_CONFIG_HOME: path.join(scratch, "no-machine-tier"),
        NIGHTGAUGE_SKIP_AUTH_PREFLIGHT: "1",
      },
      launchArgs: [
        workspace,
        "--user-data-dir",
        userDataDir,
        "--extensions-dir",
        extensionsDir,
        // Third-party extensions in the runner image would add their own
        // commands and their own unhandled rejections to a tier that fails
        // on both.
        "--disable-extensions",
        "--disable-gpu",
        "--disable-workspace-trust",
      ],
    });
  } catch (err) {
    // runTests rejects for two very different reasons and they need different
    // treatment.
    //
    // If VSCode STARTED and a test failed, the in-host reporter has already
    // printed the detail and a stack trace from here would bury it.
    //
    // If VSCode never started — the download failed, the archive was corrupt,
    // the platform build is unavailable — there is no in-host reporter and
    // this rejection carries the ONLY description of what went wrong.
    // Swallowing it leaves "the in-host test module never wrote its
    // transcript" as the sole output, which says a failure happened and
    // nothing about why. That cost a red `main` and a blind investigation.
    //
    // The transcript is the discriminator: absent means we never got that far.
    if (!fs.existsSync(transcript)) {
      console.error("ERROR: VSCode failed to launch. Underlying error:");
      console.error(err instanceof Error ? (err.stack ?? err.message) : String(err));
    }
    exitCode = 1;
  }

  if (!fs.existsSync(transcript)) {
    console.error(
      "ERROR: the in-host test module never wrote its transcript. VSCode exited without " +
        "running the smoke tier — treat this as a failure, not a pass."
    );
    exitCode = 1;
  }

  process.exit(exitCode);
}

void main();
