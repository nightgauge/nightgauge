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

  private constructor() {
    super();

    // Listen for protocol version from Go binary
    this.on("ipc.ready", (data) => {
      const payload = data as { protocolVersion?: number };
      if (
        payload?.protocolVersion !== undefined &&
        payload.protocolVersion !== IPC_PROTOCOL_VERSION
      ) {
        this.log(
          `WARNING: Binary protocol version ${payload.protocolVersion} does not match ` +
            `expected ${IPC_PROTOCOL_VERSION}. Update your extension or binary.`
        );
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
