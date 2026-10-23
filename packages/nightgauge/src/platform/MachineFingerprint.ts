/**
 * MachineFingerprint — Thin wrapper around vscode.env.machineId providing a
 * consistent machine identifier for license validation and machine binding.
 *
 * @see Issue #1471 - Implement Machine Fingerprinting via vscode.env.machineId
 */

import * as vscode from "vscode";

export class MachineFingerprint {
  private static instance: MachineFingerprint | null = null;

  static initialize(): MachineFingerprint {
    if (!MachineFingerprint.instance) {
      MachineFingerprint.instance = new MachineFingerprint();
    }
    return MachineFingerprint.instance;
  }

  static getInstance(): MachineFingerprint | null {
    return MachineFingerprint.instance;
  }

  /** @internal For testing only. */
  static resetInstance(): void {
    MachineFingerprint.instance = null;
  }

  /**
   * Returns the VSCode machine identifier — a UUID stable across extension
   * restarts and updates, unique per VSCode installation.
   */
  getMachineId(): string {
    return vscode.env.machineId;
  }
}
