/**
 * ConnectivityStateBus — process-wide singleton that broadcasts the current
 * network connectivity state to consumers that cannot take a direct
 * `OfflineManager` reference (notably the function-style `skillRunner.ts`).
 *
 * Producer: `OfflineManager.onStateChanged` is forwarded into this bus from
 * the bootstrap (`services.ts`).
 *
 * Consumers:
 *   - `skillRunner.ts` reads state on every stall-ticker tick to gate the
 *     idle and hard-cap kill paths (Issue #3203).
 *   - `PipelineConnectivityStatusItem` subscribes to render the user-visible
 *     "pipeline paused — offline" status bar entry.
 *
 * The bus defaults to `online` so any consumer reading before the producer
 * fires gets the optimistic state (matches OfflineManager's runtime behavior
 * once it has performed at least one successful health check).
 *
 * @see Issue #3203 - Pipeline pause-on-offline
 */
import * as vscode from "vscode";
import type { ConnectionState } from "./types";

export interface ConnectivityChange {
  previous: ConnectionState;
  current: ConnectionState;
  at: string;
}

class ConnectivityStateBusImpl {
  private _state: ConnectionState = "online";
  // Lazy: many existing test suites mock `vscode` without EventEmitter. The
  // bus is imported transitively via skillRunner, so eager construction would
  // force every such suite to update its mock. Constructing on first
  // `onChanged`/`set` call defers the dependency to the actual consumer.
  private _onChanged: vscode.EventEmitter<ConnectivityChange> | null = null;

  private getEmitter(): vscode.EventEmitter<ConnectivityChange> {
    if (this._onChanged === null) {
      this._onChanged = new vscode.EventEmitter<ConnectivityChange>();
    }
    return this._onChanged;
  }

  get onChanged(): vscode.Event<ConnectivityChange> {
    return this.getEmitter().event;
  }

  get state(): ConnectionState {
    return this._state;
  }

  set(next: ConnectionState): void {
    const prev = this._state;
    if (prev === next) return;
    this._state = next;
    this.getEmitter().fire({
      previous: prev,
      current: next,
      at: new Date().toISOString(),
    });
  }

  /** Reset state for tests. Not for production use. */
  resetForTests(): void {
    this._state = "online";
  }

  dispose(): void {
    this._onChanged?.dispose();
    this._onChanged = null;
  }
}

export const ConnectivityStateBus = new ConnectivityStateBusImpl();
