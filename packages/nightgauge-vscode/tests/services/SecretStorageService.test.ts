import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";

vi.mock("vscode", () => ({
  // minimal mock — SecretStorageService only uses vscode for types
}));

import { SecretStorageService } from "../../src/services/SecretStorageService";

function createMockSecretStorage(): vscode.SecretStorage {
  const store = new Map<string, string>();
  return {
    get: vi.fn((key: string) => Promise.resolve(store.get(key))),
    store: vi.fn((key: string, value: string) => {
      store.set(key, value);
      return Promise.resolve();
    }),
    delete: vi.fn((key: string) => {
      store.delete(key);
      return Promise.resolve();
    }),
    onDidChange: vi.fn(),
  } as unknown as vscode.SecretStorage;
}

describe("SecretStorageService", () => {
  beforeEach(() => {
    SecretStorageService.resetInstance();
  });

  it("returns null before initialization", () => {
    const instance = SecretStorageService.getInstance();
    expect(instance).toBeNull();
  });

  it("initializes and retrieves singleton", () => {
    const mockSecrets = createMockSecretStorage();
    SecretStorageService.initialize(mockSecrets);

    const instance = SecretStorageService.getInstance();
    expect(instance).not.toBeNull();
    expect(instance).toBeInstanceOf(SecretStorageService);
  });

  it("returns the same instance on multiple getInstance calls", () => {
    const mockSecrets = createMockSecretStorage();
    SecretStorageService.initialize(mockSecrets);

    const first = SecretStorageService.getInstance();
    const second = SecretStorageService.getInstance();
    expect(first).toBe(second);
  });

  it("does not reinitialize if already initialized", () => {
    const firstSecrets = createMockSecretStorage();
    const secondSecrets = createMockSecretStorage();

    SecretStorageService.initialize(firstSecrets);
    SecretStorageService.initialize(secondSecrets);

    // The singleton should still use the first secrets store
    const instance = SecretStorageService.getInstance();
    expect(instance).not.toBeNull();
    expect(firstSecrets.get).not.toHaveBeenCalled();
    expect(secondSecrets.store).not.toHaveBeenCalled();
  });

  describe("getApiKey", () => {
    it("retrieves a stored gemini API key using the correct storage key", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("gemini", "test-gemini-key");
      const result = await service.getApiKey("gemini");

      expect(result).toBe("test-gemini-key");
      expect(mockSecrets.get).toHaveBeenCalledWith("nightgauge.gemini.apiKey");
    });

    it("retrieves a stored anthropic API key using the correct storage key", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("anthropic", "test-anthropic-key");
      const result = await service.getApiKey("anthropic");

      expect(result).toBe("test-anthropic-key");
      expect(mockSecrets.get).toHaveBeenCalledWith("nightgauge.anthropic.apiKey");
    });

    it("returns undefined for a key that has not been set", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      const result = await service.getApiKey("gemini");
      expect(result).toBeUndefined();
    });
  });

  describe("setApiKey", () => {
    it("stores a gemini API key under the correct storage key", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("gemini", "my-gemini-key");

      expect(mockSecrets.store).toHaveBeenCalledWith("nightgauge.gemini.apiKey", "my-gemini-key");
    });

    it("stores an anthropic API key under the correct storage key", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("anthropic", "my-anthropic-key");

      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.anthropic.apiKey",
        "my-anthropic-key"
      );
    });
  });

  describe("deleteApiKey", () => {
    it("deletes a gemini API key and it is no longer retrievable", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("gemini", "key-to-delete");
      await service.deleteApiKey("gemini");
      const result = await service.getApiKey("gemini");

      expect(result).toBeUndefined();
      expect(mockSecrets.delete).toHaveBeenCalledWith("nightgauge.gemini.apiKey");
    });

    it("deletes an anthropic API key and it is no longer retrievable", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("anthropic", "key-to-delete");
      await service.deleteApiKey("anthropic");
      const result = await service.getApiKey("anthropic");

      expect(result).toBeUndefined();
      expect(mockSecrets.delete).toHaveBeenCalledWith("nightgauge.anthropic.apiKey");
    });

    it("does not throw when deleting a key that does not exist", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await expect(service.deleteApiKey("gemini")).resolves.toBeUndefined();
    });
  });

  describe("hasApiKey", () => {
    it("returns false when the key has not been set", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      const result = await service.hasApiKey("gemini");
      expect(result).toBe(false);
    });

    it("returns true when the gemini key has been set", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("gemini", "some-key");
      const result = await service.hasApiKey("gemini");

      expect(result).toBe(true);
    });

    it("returns true when the anthropic key has been set", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("anthropic", "some-key");
      const result = await service.hasApiKey("anthropic");

      expect(result).toBe(true);
    });

    it("returns false after the key has been deleted", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      await service.setApiKey("gemini", "some-key");
      await service.deleteApiKey("gemini");
      const result = await service.hasApiKey("gemini");

      expect(result).toBe(false);
    });

    it("returns false for an empty string value", async () => {
      const mockSecrets = createMockSecretStorage();
      // Override get to return an empty string for this test
      (mockSecrets.get as ReturnType<typeof vi.fn>).mockResolvedValueOnce("");
      SecretStorageService.initialize(mockSecrets);
      const service = SecretStorageService.getInstance()!;

      const result = await service.hasApiKey("gemini");
      expect(result).toBe(false);
    });
  });

  describe("resetInstance", () => {
    it("clears the singleton so getInstance returns null", () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      expect(SecretStorageService.getInstance()).not.toBeNull();

      SecretStorageService.resetInstance();
      expect(SecretStorageService.getInstance()).toBeNull();
    });

    it("allows re-initialization after reset", () => {
      const firstSecrets = createMockSecretStorage();
      SecretStorageService.initialize(firstSecrets);
      SecretStorageService.resetInstance();

      const secondSecrets = createMockSecretStorage();
      SecretStorageService.initialize(secondSecrets);

      expect(SecretStorageService.getInstance()).not.toBeNull();
    });
  });
});
