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
 *      This happens twice: the main window runs every suite but the two
 *      demo ones, in a folder outside any git repository; the demo window
 *      makes its folder a git repository holding a copy of the demo
 *      workspace and plays the reference scenario (#2105, #2106, #2108).
 *   4. Verify the in-host module actually ran. A window that dies before
 *      loading it can still exit 0; without this check, that is a green tier
 *      that observed nothing.
 *   5. Run the demo drift guard over the daemon's request log (#2109).
 *   6. Diagnose and clean up (#2747): print the load at each launch, retry a
 *      launch fault once, copy a failed window's VS Code logs beside the
 *      ci-local step log, and remove scratch after a pass (keep it after a
 *      failure).
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
import { agentHomeEnv, scrubInheritedHostEnv, secretStoreArgs } from "../launcher/hostIsolation";
import {
  LAUNCH_ATTEMPTS,
  classifyWindowOutcome,
  copyWindowLogs,
  diagnosticsDir,
  loadAverageLine,
  shouldRemoveScratch,
  shouldRetryLaunch,
  type WindowOutcome,
} from "../launcher/windowDiagnostics";
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

/**
 * Agent-home isolation (#2935). A fresh user-data dir has no install stamp,
 * so activation re-syncs every present skill install (#2762 ADR-003). With
 * the launcher's real HOME that wrote the worktree's skills into the
 * developer's ~/.codex, ~/.grok and ~/.config/opencode. Every window gets a
 * fake HOME seeded with the install a developer would have, holding a
 * sentinel a re-sync would overwrite, and agent-home overrides pointing at a
 * scratch install of its own that the re-sync is expected to reach.
 */
const AGENT_SENTINEL = "nightgauge host smoke sentinel (#2935): a re-sync must not reach this\n";
const SEEDED_SKILLS = ["nightgauge-issue-pickup", "nightgauge-issue-create"];

/** Seed a Nightgauge install for each agent under the given locations. */
function seedAgentInstall(dirs: { codex: string; grok: string; opencode: string; claude: string }) {
  for (const skills of [dirs.codex, dirs.grok, dirs.opencode]) {
    for (const skill of SEEDED_SKILLS) {
      fs.mkdirSync(path.join(skills, skill), { recursive: true });
      fs.writeFileSync(path.join(skills, skill, "SKILL.md"), AGENT_SENTINEL);
      // The marker a real install carries: without it a re-sync leaves the
      // directory alone (#2739), and the regression would never reach it.
      fs.writeFileSync(path.join(skills, skill, ".nightgauge-installed"), AGENT_SENTINEL);
    }
  }
  fs.mkdirSync(dirs.claude, { recursive: true });
  fs.writeFileSync(path.join(dirs.claude, "installed_plugins.json"), AGENT_SENTINEL);
}

/** The first line of a runTests rejection, for the one-line retry notice. */
function firstLine(err: unknown): string {
  if (err === undefined) return "VS Code exited without an error";
  const text = err instanceof Error ? err.message : String(err);
  return text.split("\n")[0] || "no message";
}

function requireFile(file: string, remedy: string): void {
  if (!fs.existsSync(file)) {
    console.error(`ERROR: missing ${path.relative(packageRoot, file)}\n  ${remedy}`);
    process.exit(1);
  }
}

async function main(): Promise<void> {
  // runTests spawns VS Code with this process.env merged in (#2743).
  const scrubbed = scrubInheritedHostEnv();
  if (scrubbed.length > 0) {
    console.log(`[host] dropped inherited ${scrubbed.join(", ")} before starting VS Code`);
  }
  requireFile(
    EXTENSION_BUNDLE,
    "Run `npm run -w packages/nightgauge build` first — the host loads the real bundle, not src/."
  );
  requireFile(
    TESTS_BUNDLE,
    "Run `npm run -w packages/nightgauge build:host-tests` first (the `test:host` script does this for you)."
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

  // Where a failed window's VS Code logs go (#2747): beside the ci-local step
  // log when the gate runs this, otherwise in scratch, which a failure keeps.
  // A previous run's logs would read as this run's, so start empty.
  const diagnostics = diagnosticsDir(process.env, scratch);
  fs.rmSync(diagnostics, { recursive: true, force: true });

  /**
   * Exit, removing scratch after a pass and keeping it, with its path
   * printed, after a failure (#2747): 78 runs had left ~10 MB each behind.
   */
  function finish(code: number): never {
    if (shouldRemoveScratch(code)) {
      fs.rmSync(scratch, { recursive: true, force: true });
    } else {
      console.error(`[host] kept scratch ${scratch}`);
    }
    process.exit(code);
  }

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
    finish(1);
  }

  /**
   * Run the bundled suites in one VSCode window. `window` names the suites it
   * runs (see index.host.ts); each window gets its own user-data dir, so the
   * binary override and every piece of window state stay per-window.
   *
   * A launch fault (no transcript: VS Code never reached the in-host module,
   * so no test ran) is retried once, in fresh directories, and the retry is
   * printed with its error and the load. A failed test is never retried
   * (#2747 ADR-001). `resetBeforeRetry` names files a faulted attempt may
   * have left that the next attempt must not inherit.
   */
  async function runWindow(
    window: "main" | "demo",
    folder: string,
    env: Record<string, string>,
    resetBeforeRetry: string[] = []
  ): Promise<number> {
    for (let attempt = 1; ; attempt++) {
      console.log(
        `[host] ${window} window launch (attempt ${attempt}/${LAUNCH_ATTEMPTS}): ${loadAverageLine()}`
      );
      const { outcome, error, userDataDir } = await launchWindow(window, attempt, folder, env);
      if (outcome === "passed") return 0;
      const label = `${window}-attempt-${attempt}`;
      const copied = copyWindowLogs(userDataDir, label, diagnostics);
      console.error(
        copied
          ? `[host] ${window} window VS Code logs: ${copied}`
          : `[host] ${window} window wrote no VS Code logs under ${userDataDir}`
      );
      if (shouldRetryLaunch(outcome, attempt)) {
        console.error(
          `[host] ${window} window: launch fault on attempt ${attempt}/${LAUNCH_ATTEMPTS} ` +
            `(${firstLine(error)}); retrying. ${loadAverageLine()}`
        );
        for (const file of resetBeforeRetry) fs.rmSync(file, { force: true });
        continue;
      }
      if (outcome === "launch-fault") {
        // A launch that never reached the in-host module, kept distinct from a
        // failed test: a window can die before loading it and still exit 0.
        console.error(
          `ERROR: the in-host test module never wrote its ${window}-window transcript ` +
            `(${attempt} launch attempt${attempt === 1 ? "" : "s"}). VSCode exited without ` +
            "running the smoke tier — treat this as a failure, not a pass."
        );
      }
      return 1;
    }
  }

  /** One launch of one window, in directories of its own. */
  async function launchWindow(
    window: "main" | "demo",
    attempt: number,
    folder: string,
    env: Record<string, string>
  ): Promise<{ outcome: WindowOutcome; error: unknown; userDataDir: string }> {
    const id = `${window}-${attempt}`;
    const userDataDir = path.join(scratch, `user-data-${id}`);
    const transcript = path.join(scratch, `transcript-${id}.txt`);
    fs.mkdirSync(path.join(userDataDir, "User"), { recursive: true });
    const fakeHome = path.join(scratch, `home-${id}`);
    const agentHomes = path.join(scratch, `agent-homes-${id}`);
    seedAgentInstall({
      codex: path.join(fakeHome, ".codex", "skills"),
      grok: path.join(fakeHome, ".grok", "skills"),
      opencode: path.join(fakeHome, ".config", "opencode", "skills"),
      claude: path.join(fakeHome, ".claude", "plugins"),
    });
    seedAgentInstall({
      codex: path.join(agentHomes, "codex", "skills"),
      grok: path.join(agentHomes, "grok", "skills"),
      opencode: path.join(agentHomes, "config", "opencode", "skills"),
      claude: path.join(agentHomes, "claude", "plugins"),
    });
    fs.writeFileSync(
      path.join(userDataDir, "User", "settings.json"),
      // Experiments off: VS Code's own fetch to its experiment service shares the
      // extension host's global fetch. This only reduces that traffic; demo mode's
      // recorder filters the host (VSCODE_CORE_HOSTS), which keeps it deterministic.
      JSON.stringify(
        { "nightgauge.backend.binaryPath": IPC_STUB, "workbench.enableExperiments": false },
        null,
        2
      )
    );
    let error: unknown = undefined;
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
          NIGHTGAUGE_STATE_HOME: path.join(scratch, `state-${id}`),
          NIGHTGAUGE_SKIP_AUTH_PREFLIGHT: "1",
          // Keep activation's skill re-sync off the developer's agent homes
          // (#2935). HOME is fake as well, so a resolver that ignores its
          // override lands on the seeded sentinels the activation suite
          // checks, never on the real home.
          ...agentHomeEnv(fakeHome, agentHomes),
          NIGHTGAUGE_HOST_FAKE_HOME: fakeHome,
          NIGHTGAUGE_HOST_AGENT_HOMES: agentHomes,
          NIGHTGAUGE_HOST_AGENT_SENTINEL: AGENT_SENTINEL,
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
          // HOME is fake (#2935), so the OS keychain is not the user's: keep
          // secrets in memory and Electron's safe storage off the keychain.
          ...secretStoreArgs(),
        ],
      });
    } catch (err) {
      error = err;
    }
    const outcome = classifyWindowOutcome({
      transcriptExists: fs.existsSync(transcript),
      runTestsError: error,
    });
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
    if (outcome === "launch-fault" && error !== undefined) {
      console.error(`ERROR: VSCode (${window} window) failed to launch. Underlying error:`);
      console.error(error instanceof Error ? (error.stack ?? error.message) : String(error));
    }
    return { outcome, error, userDataDir };
  }

  // The main window: activation on an empty workspace, then every surface
  // against the fixtures, with the daemon serving its built-in seed.
  const mainCode = await runWindow("main", workspace, {});

  // The demo window (#2105, #2106, #2108): a fresh VSCode whose first
  // activation happens on the demo workspace, the way a demo session opens
  // (#2110), so every view resolves the demo repository. The folder starts
  // empty for the same reason the main one does (the observers must be in
  // place before activation); the demo suites copy the demo workspace in,
  // making the folder a git repository whose clone directory holds the
  // demo's per-clone data (ADR-024 § 7), and activate. The main window's
  // folder stays outside any git repository. The scenario arrives through the
  // environment, since a setting cannot carry arguments; its steps wait for
  // the start file, which only the demo-mode suite creates.
  const demoWorkspace = path.join(scratch, "demo-workspace");
  fs.mkdirSync(demoWorkspace, { recursive: true });
  // A retried demo launch must not inherit a start file or event log from the
  // faulted attempt. The shared IPC log is left alone: the main window's
  // entries in it stay valid.
  const demoStart = path.join(scratch, "demo-start");
  const demoEvents = path.join(scratch, "demo-events.jsonl");
  const demoCode = await runWindow(
    "demo",
    demoWorkspace,
    {
      NIGHTGAUGE_DEMO_SCENARIO: REFERENCE_SCENARIO,
      NIGHTGAUGE_DEMO_SPEED: DEMO_SPEED,
      NIGHTGAUGE_DEMO_START_FILE: demoStart,
      NIGHTGAUGE_DEMO_EVENT_LOG: demoEvents,
    },
    [demoStart, demoEvents]
  );

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

  finish(exitCode);
}

void main();
