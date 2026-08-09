/**
 * IpcClient — Final IPC client class with singleton pattern and manual wrappers.
 *
 * Extends the auto-generated IpcClientGenerated class (which provides all typed
 * API methods) with:
 * - Singleton pattern (getInstance / resetInstance)
 * - Manual wrapper methods (e.g., boardGetReadyItems)
 * - Methods with custom TypeScript signatures that differ from the Go struct layout
 *
 * @see IpcClientBase.ts          — Base class with lifecycle and transport
 * @see IpcClient.generated.ts    — Auto-generated typed API methods
 * @see internal/ipc/protocol.go  — Go-side protocol definition
 */

import * as vscode from "vscode";

import { IpcClientGenerated, IPC_PROTOCOL_VERSION } from "./IpcClient.generated";
import type { MattermostSlashEvent } from "./IpcClientBase";

// Re-export all types from IpcClientBase so consumers keep importing from here.
export type { EventHandler } from "./IpcClientBase";
export type {
  BoardItem,
  IssueDetail,
  EpicProgress,
  PipelineStatus,
  ExecutionInfo,
  ComplexityResult,
  ModelRouteResult,
  FailureClassification,
  CostEstimate,
  PlatformStatus,
  LicenseInfo,
  IpcQueueItem,
  IpcQueueState,
  RunPipelineResult,
  PipelineCompleteEvent,
  MattermostSlashEvent,
  MattermostParsedCommand,
  MattermostCommandType,
  HealthAnalysis,
  GitStatusResult,
  GitLogEntry,
  PullRequestDetail,
} from "./IpcClientBase";

// Re-export protocol version
export { IPC_PROTOCOL_VERSION };

// Re-export base class for tests that need to reference it
export { IpcClientBase } from "./IpcClientBase";

export class IpcClient extends IpcClientGenerated {
  private static instance: IpcClient | null = null;

  /**
   * Latched by the `ipc.ready` handshake when the binary speaks a different
   * IPC protocol version. Once set it is never cleared: the client is
   * unusable for the rest of this activation (ADR-017 § Migration).
   */
  private protocolMismatch: { binary: number; expected: number } | null = null;

  private constructor() {
    super();

    // Protocol version handshake from the Go binary. A mismatch is a HARD
    // FAILURE, not a warning (ADR-017 § Migration): protocol 2 made the run
    // identity mandatory on every `pipeline.*` verb, so an extension and a
    // binary that disagree produce a live run whose every call is refused —
    // zero records, zero learning outcomes, zero telemetry, discovered hours
    // later. Warning-and-continuing is exactly that silent lockout, so the
    // client disconnects and says so in a modal instead.
    this.on("ipc.ready", (data) => {
      const payload = data as { protocolVersion?: number };
      if (
        payload?.protocolVersion !== undefined &&
        payload.protocolVersion !== IPC_PROTOCOL_VERSION
      ) {
        this.failProtocolVersion(payload.protocolVersion);
      }
    });

    // Forward Mattermost slash-command events from the Go inbound
    // receiver (#3376) to the typed event emitter. Consumers subscribe
    // via `onMattermostCommand`.
    this.on("mattermost.command", (data) => {
      this._onMattermostCommand.fire(data as MattermostSlashEvent);
    });
  }

  static getInstance(): IpcClient {
    if (!IpcClient.instance) {
      IpcClient.instance = new IpcClient();
    }
    return IpcClient.instance;
  }

  static resetInstance(): void {
    if (IpcClient.instance) {
      IpcClient.instance.dispose();
    }
    IpcClient.instance = null;
  }

  dispose(): void {
    super.dispose();
    IpcClient.instance = null;
  }

  // -------------------------------------------------------------------------
  // Protocol version hard-fail (ADR-017 § Migration)
  // -------------------------------------------------------------------------

  /**
   * Disconnect and stay disconnected: the binary speaks `binaryVersion`, this
   * extension speaks {@link IPC_PROTOCOL_VERSION}. Kills the transport (no
   * restart ladder — a version disagreement is not a crash to retry), latches
   * the mismatch so every later `call()` rejects before reaching the socket,
   * and raises a blocking modal naming both versions and the fix. Idempotent:
   * a restarting binary can announce the same mismatch repeatedly.
   */
  private failProtocolVersion(binaryVersion: number): void {
    if (this.protocolMismatch) return;
    this.protocolMismatch = { binary: binaryVersion, expected: IPC_PROTOCOL_VERSION };

    const summary =
      `IPC protocol mismatch: the nightgauge binary speaks protocol ${binaryVersion}, ` +
      `this extension speaks ${IPC_PROTOCOL_VERSION}.`;
    this.log(`FATAL: ${summary} Connection closed; every IPC call now fails protocol_mismatch.`);
    this.shutdownTransport(`protocol_mismatch: ${summary}`);

    void vscode.window.showErrorMessage(
      `Nightgauge: ${summary} The connection has been closed — no pipeline commands will run. ` +
        `Update the binary and the extension to the same release, then reload the window.`,
      { modal: true }
    );
  }

  /**
   * Refuses to reconnect once the protocol mismatch is latched. `call()`
   * rejects before it can reach here, but explicit restart paths (a retry
   * command, a re-activation of a stale view) must not respawn a binary this
   * extension cannot speak to.
   */
  async start(): Promise<void> {
    if (this.protocolMismatch) {
      this.log(
        `Refusing to start the Go backend: protocol ${this.protocolMismatch.binary} != ` +
          `${this.protocolMismatch.expected}. Update both sides and reload the window.`
      );
      return;
    }
    return super.start();
  }

  /**
   * Every call goes through here (the generated methods all delegate to
   * `this.call`), so the latch refuses the whole API surface at one point —
   * without touching the socket, which is gone anyway.
   */
  async call<T>(method: string, params?: unknown): Promise<T> {
    if (this.protocolMismatch) {
      const { binary, expected } = this.protocolMismatch;
      throw new Error(
        `protocol_mismatch: ${method} not sent — the nightgauge binary speaks IPC protocol ` +
          `${binary}, this extension speaks ${expected}. Update both and reload the window.`
      );
    }
    return super.call<T>(method, params);
  }

  // -------------------------------------------------------------------------
  // Manual wrapper methods (TS-only, not generated from Go annotations)
  // -------------------------------------------------------------------------

  /** Convenience wrapper — calls boardList with status='Ready'. */
  async boardGetReadyItems(
    owner: string,
    projectNumber: number
  ): Promise<import("./IpcClientBase").BoardItem[]> {
    return this.boardList(owner, projectNumber, "Ready");
  }

  // githubRateLimit — generated from Go annotation in IpcClient.generated.ts

  // -------------------------------------------------------------------------
  // Methods with custom TS signatures (marked "skip" in Go annotations)
  // -------------------------------------------------------------------------

  async issueList(
    owner: string,
    repo: string,
    options?: { epic?: number; labels?: string[] }
  ): Promise<import("./IpcClientBase").IssueDetail[]> {
    return this.call<import("./IpcClientBase").IssueDetail[]>("issue.list", {
      owner,
      repo,
      ...options,
    });
  }

  async intelligenceComplexity(params: {
    title: string;
    body: string;
    labels: string[];
    fileCountEstimate?: number;
    subIssueCount?: number;
  }): Promise<import("./IpcClientBase").ComplexityResult> {
    return this.call<import("./IpcClientBase").ComplexityResult>("intelligence.complexity", params);
  }

  async pipelineRun(
    owner: string,
    repo: string,
    issueNumber: number,
    options?: {
      fromStage?: string;
      targetBranch?: string;
      model?: string;
      adapter?: string;
    }
  ): Promise<import("./IpcClientBase").RunPipelineResult> {
    return this.call<import("./IpcClientBase").RunPipelineResult>("pipeline.run", {
      owner,
      repo,
      issueNumber,
      ...options,
    });
  }

  async pipelineGetState(owner: string, repo: string, issueNumber: number): Promise<unknown> {
    return this.call<unknown>("pipeline.getState", {
      owner,
      repo,
      issueNumber,
    });
  }

  async prList(
    owner: string,
    repo: string,
    options?: { state?: string; headRef?: string }
  ): Promise<import("./IpcClientBase").PullRequestDetail[]> {
    return this.call<import("./IpcClientBase").PullRequestDetail[]>("pr.list", {
      owner,
      repo,
      ...options,
    });
  }
}
