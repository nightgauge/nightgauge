/**
 * Mock Memento for testing workspace state persistence
 *
 * Provides an in-memory Map-based implementation of vscode.Memento
 * for testing state management without VSCode environment.
 */

import type * as vscode from "vscode";

/**
 * Creates a mock Memento instance for testing
 *
 * @param initialData Optional initial state data
 * @returns Mock Memento with get/update/keys methods
 */
export function createMockMemento(initialData?: Map<string, unknown>): vscode.Memento {
  const storage = initialData ?? new Map<string, unknown>();

  return {
    keys: () => Array.from(storage.keys()),

    get: <T>(key: string, defaultValue?: T): T | undefined => {
      if (storage.has(key)) {
        return storage.get(key) as T;
      }
      return defaultValue;
    },

    update: async (key: string, value: unknown): Promise<void> => {
      if (value === undefined) {
        storage.delete(key);
      } else {
        storage.set(key, value);
      }
    },

    setKeysForSync: (_keys: readonly string[]): void => {
      // No-op for testing
    },
  } as vscode.Memento;
}
