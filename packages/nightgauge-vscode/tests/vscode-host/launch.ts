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
 *   3. Acquire VSCode — resolve the version and download the build — with a
 *      bounded retry, then launch it with `--extensionDevelopmentPath` at this
 *      package and `--extensionTestsPath` at the bundled entry point.
 *      This happens twice: the main window runs every suite but demo mode;
 *      the demo window opens a copy of the demo workspace and plays the
 *      reference scenario (#2105, #2106, #2108).
 *   4. Verify the in-host module actually ran. A window that dies before
 *      loading it can still exit 0; without this check, that is a green tier
 *      that observed nothing.
 *   5. Run the demo drift guard over the daemon's request log (#2109).
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
import { downloadAndUnzipVSCode, runTests } from "@vscode/test-electron";
import { ACQUIRE_ATTEMPTS, acquireVSCode } from "../launcher/acquireVSCode";
import {
  buildInventory,
  driftProblems,
  parseLog,
  protocolVersionFromClient,
  protocolVersionFromDaemon,
  type IpcInventory,
} from "../../demo/ipc-inventory";

const here = path.dirname(fileURLToPath(import.meta.url));
const packageRoot = path.resolve(here, "..", "..");

const EXTENSION_BUNDLE = path.join(packageRoot, "dist", "extension.cjs");
const TESTS_BUNDLE = path.join(packageRoot, "out", "vscode-host", "index.host.cjs");
const FIXTURE_SOURCE = path.join(packageRoot, "tests", "fixtures", "vscode-host", "populated");
/** The committed demo workspace (#2107), read by the demo-fixture suite. */
const DEMO_WORKSPACE = path.join(packageRoot, "demo", "workspace");
/**
 * The backend this tier runs against: the demo daemon (#2103, #2105), wired
 * in through the same `nightgauge.backend.binaryPath` seam a user would use.
 * It answers from in-memory demo state, touches no network and logs each
 * request, so the tier is deterministic and the log doubles as the inventory
 * of IPC methods the extension calls and as the drift guard's input (#2109).
 */
const IPC_STUB = path.join(packageRoot, "demo", "ipc-stub.cjs");
const DAEMON_SOURCE = path.join(packageRoot, "demo", "daemon", "daemon.cjs");
/**
 * The scenario the daemon loads (#2106). A setting cannot carry arguments,
 * so it arrives through the environment the extension host passes on. The
 * main window gets its seed state only; the demo window also plays it.
 */
const REFERENCE_SCENARIO = path.join(packageRoot, "demo", "scenarios", "reference.json");
/** Scenario playback speed in this tier: the 60 s reference plays in 20 s. */
const DEMO_SPEED = "3";
const IPC_INVENTORY = path.join(packageRoot, "demo", "ipc-inventory.json");
const GENERATED_CLIENT = path.join(packageRoot, "src", "services", "IpcClient.generated.ts");
const MANUAL_CLIENT = path.join(packageRoot, "src", "services", "IpcClient.ts");
/** `--write-ipc-inventory` regenerates demo/ipc-inventory.json from this run. */
const WRITE_INVENTORY = process.argv.includes("--write-ipc-inventory");

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
  requireFile(DEMO_WORKSPACE, "The committed demo workspace is missing from the tree.");
  requireFile(IPC_STUB, "The demo daemon is missing from the tree.");
  requireFile(REFERENCE_SCENARIO, "The reference demo scenario is missing from the tree.");

  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), "nightgauge-host-"));
  const workspace = path.join(scratch, "workspace");
  const extensionsDir = path.join(scratch, "extensions");
  const ipcLog = path.join(scratch, "ipc-stub.jsonl");
  fs.mkdirSync(workspace, { recursive: true });
  fs.mkdirSync(extensionsDir, { recursive: true });

  console.log(`VSCode host smoke tier: workspace ${workspace}`);

  let vscodeExecutablePath: string;
  try {
    vscodeExecutablePath = await acquireVSCode({
      download: () => downloadAndUnzipVSCode(),
      sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      log: (message) => console.log(message),
      warn: (message) => console.error(`WARN: ${message}`),
    });
  } catch (err) {
    console.error(
      `ERROR: could not acquire VSCode after ${ACQUIRE_ATTEMPTS} attempts. ` +
        "The smoke tier never ran."
    );
    console.error(err instanceof Error ? (err.stack ?? err.message) : String(err));
    process.exit(1);
  }

  /**
   * Run the bundled suites in one VSCode window. `window` names the suites it
   * runs (see index.host.ts); each window gets its own user-data dir, so the
   * binary override and every piece of window state stay per-window.
   */
  async function runWindow(
    window: "main" | "demo",
    folder: string,
    env: Record<string, string>
  ): Promise<number> {
    const userDataDir = path.join(scratch, `user-data-${window}`);
    const transcript = path.join(scratch, `transcript-${window}.txt`);
    fs.mkdirSync(path.join(userDataDir, "User"), { recursive: true });
    fs.writeFileSync(
      path.join(userDataDir, "User", "settings.json"),
      JSON.stringify({ "nightgauge.backend.binaryPath": IPC_STUB }, null, 2)
    );
    let code = 0;
    try {
      await runTests({
        vscodeExecutablePath,
        extensionDevelopmentPath: packageRoot,
        extensionTestsPath: TESTS_BUNDLE,
        extensionTestsEnv: {
          NIGHTGAUGE_HOST_WINDOW: window,
          NIGHTGAUGE_HOST_FIXTURE_SOURCE: FIXTURE_SOURCE,
          NIGHTGAUGE_HOST_DEMO_WORKSPACE: DEMO_WORKSPACE,
          NIGHTGAUGE_HOST_TRANSCRIPT: transcript,
          // Keep the tier off the developer's real machine-tier config, the
          // same way tests/setup.ts does for vitest.
          NIGHTGAUGE_CONFIG_HOME: path.join(scratch, "no-machine-tier"),
          // Keep the daemon's serve claim, rate-limit hints and machine id off
          // the developer's real machine-state root (ADR-024 § 8).
          NIGHTGAUGE_STATE_HOME: path.join(scratch, `state-${window}`),
          NIGHTGAUGE_SKIP_AUTH_PREFLIGHT: "1",
          // Read by the daemon (inherited through the extension host) and by
          // the in-host harness, which writes a marker line before each case.
          // Both windows append to one log, so the inventory and the drift
          // guard see every surface.
          NIGHTGAUGE_DEMO_IPC_LOG: ipcLog,
          ...env,
        },
        launchArgs: [
          folder,
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
      // runTests rejects for two very different reasons and they need
      // different treatment.
      //
      // If VSCode STARTED and a test failed, the in-host reporter has already
      // printed the detail and a stack trace from here would bury it.
      //
      // If VSCode never started — the window died on launch, Electron could
      // not reach a display, the bundle threw before the reporter loaded —
      // there is no in-host reporter and this rejection carries the ONLY
      // description of what went wrong. Swallowing it leaves "the in-host
      // test module never wrote its transcript" as the sole output, which
      // says a failure happened and nothing about why. That cost a red `main`
      // and a blind investigation.
      //
      // The transcript is the discriminator: absent means we never got that far.
      if (!fs.existsSync(transcript)) {
        console.error(`ERROR: VSCode (${window} window) failed to launch. Underlying error:`);
        console.error(err instanceof Error ? (err.stack ?? err.message) : String(err));
      }
      code = 1;
    }
    if (!fs.existsSync(transcript)) {
      console.error(
        `ERROR: the in-host test module never wrote its ${window}-window transcript. VSCode ` +
          "exited without running the smoke tier — treat this as a failure, not a pass."
      );
      code = 1;
    }
    return code;
  }

  // The main window: activation on an empty workspace, then every surface
  // against the fixtures, with the daemon serving its built-in seed.
  const mainCode = await runWindow("main", workspace, {});

  // The demo window (#2105, #2106, #2108): a fresh VSCode whose first
  // activation happens on the demo workspace, the way a demo session opens
  // (#2110), so every view resolves the demo repository. The folder starts
  // empty for the same reason the main one does (the observers must be in
  // place before activation); the demo-mode suite copies the demo workspace
  // in and activates. The scenario arrives through the environment, since a
  // setting cannot carry arguments; its steps wait for the start file, which
  // only the demo-mode suite creates.
  const demoWorkspace = path.join(scratch, "demo-workspace");
  fs.mkdirSync(demoWorkspace, { recursive: true });
  const demoCode = await runWindow("demo", demoWorkspace, {
    NIGHTGAUGE_DEMO_SCENARIO: REFERENCE_SCENARIO,
    NIGHTGAUGE_DEMO_SPEED: DEMO_SPEED,
    NIGHTGAUGE_DEMO_START_FILE: path.join(scratch, "demo-start"),
    NIGHTGAUGE_DEMO_EVENT_LOG: path.join(scratch, "demo-events.jsonl"),
  });

  let exitCode = mainCode || demoCode;

  // The drift guard (#2109, ADR-026 decision 7): every method the extension
  // called must have a demo answer and a line in the committed inventory,
  // and the daemon must speak the extension's protocol version. It runs on a
  // failed tier too, so an unanswered method is named whatever else broke.
  {
    const problems = driftProblems({
      entries: fs.existsSync(ipcLog) ? parseLog(fs.readFileSync(ipcLog, "utf8")) : [],
      inventory: WRITE_INVENTORY
        ? undefined
        : (JSON.parse(fs.readFileSync(IPC_INVENTORY, "utf8")) as IpcInventory),
      clientProtocolVersion: protocolVersionFromClient(fs.readFileSync(GENERATED_CLIENT, "utf8")),
      daemonProtocolVersion: protocolVersionFromDaemon(fs.readFileSync(DAEMON_SOURCE, "utf8")),
    });
    if (problems.length > 0) {
      console.error("ERROR: the demo daemon drifted from the extension's IPC surface:");
      for (const problem of problems) console.error(`  - ${problem}`);
      exitCode = 1;
    } else {
      console.log("Demo drift guard: every IPC method the extension called has a demo answer.");
    }
  }

  if (WRITE_INVENTORY) {
    if (exitCode !== 0 || !fs.existsSync(ipcLog)) {
      console.error("ERROR: not writing the IPC inventory from a failed or log-less run.");
      exitCode = 1;
    } else {
      const inventory = buildInventory(
        parseLog(fs.readFileSync(ipcLog, "utf8")),
        fs.readFileSync(GENERATED_CLIENT, "utf8"),
        fs.readFileSync(MANUAL_CLIENT, "utf8"),
        [
          [workspace, "<workspace>"],
          [scratch, "<scratch>"],
          [os.homedir(), "<home>"],
        ]
      );
      fs.writeFileSync(IPC_INVENTORY, `${JSON.stringify(inventory, null, 2)}\n`);
      console.log(
        `Wrote ${path.relative(packageRoot, IPC_INVENTORY)}: ${inventory.methods.length} methods`
      );
    }
  }

  process.exit(exitCode);
}

void main();
