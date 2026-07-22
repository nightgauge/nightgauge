import { describe, it, expect, vi, beforeEach } from "vitest";
import * as path from "node:path";

// Mock the SDK modules before imports
vi.mock("@nightgauge/sdk", () => {
  const TemplateRegistry = vi.fn(function () {
    return {
      loadTemplates: vi.fn().mockResolvedValue(3),
      getTemplate: vi.fn().mockReturnValue(null),
      size: 3,
      isLoaded: false,
      register: vi.fn(),
      clear: vi.fn(),
      listTemplates: vi.fn().mockReturnValue([]),
    };
  });

  const PromptRenderer = vi.fn(function () {
    return {
      render: vi.fn().mockReturnValue("rendered content"),
      renderRaw: vi.fn().mockReturnValue("raw rendered"),
      clearCache: vi.fn(),
      cacheSize: 0,
    };
  });

  return { TemplateRegistry, PromptRenderer };
});

import { PromptTemplateService } from "../../src/services/PromptTemplateService.js";
import { TemplateRegistry, PromptRenderer } from "@nightgauge/sdk";

describe("PromptTemplateService", () => {
  let service: PromptTemplateService;
  let mockRegistry: ReturnType<(typeof TemplateRegistry)["prototype"]["constructor"]>;
  let mockRenderer: ReturnType<(typeof PromptRenderer)["prototype"]["constructor"]>;

  const extensionPath = "/workspace/packages/nightgauge-vscode";
  const expectedWorkspaceRoot = path.resolve(extensionPath, "..", "..");

  beforeEach(() => {
    vi.clearAllMocks();
    service = new PromptTemplateService(extensionPath);
    // Get the mock instances created by the constructor
    mockRegistry = (TemplateRegistry as unknown as ReturnType<typeof vi.fn>).mock.results.at(
      -1
    )!.value;
    mockRenderer = (PromptRenderer as unknown as ReturnType<typeof vi.fn>).mock.results.at(
      -1
    )!.value;
  });

  describe("initialize()", () => {
    it("loads templates from skills/templates relative to workspace root", async () => {
      await service.initialize();
      const expectedDir = path.join(expectedWorkspaceRoot, "skills", "templates");
      expect(mockRegistry.loadTemplates).toHaveBeenCalledWith(expectedDir, {
        ignore: true,
      });
    });

    it("uses provided workspaceRoot when given", async () => {
      const customRoot = "/custom/workspace";
      const s = new PromptTemplateService(extensionPath, customRoot);
      const reg = (TemplateRegistry as unknown as ReturnType<typeof vi.fn>).mock.results.at(
        -1
      )!.value;
      await s.initialize();
      expect(reg.loadTemplates).toHaveBeenCalledWith(path.join(customRoot, "skills", "templates"), {
        ignore: true,
      });
    });

    it("sets isInitialized to true after initialize()", async () => {
      expect(service.isInitialized).toBe(false);
      await service.initialize();
      expect(service.isInitialized).toBe(true);
    });
  });

  describe("renderSystemPrompt()", () => {
    beforeEach(async () => {
      await service.initialize();
    });

    it("looks up template with -system suffix appended", () => {
      mockRegistry.getTemplate.mockReturnValueOnce({
        name: "feature-planning-system",
        version: "1.0.0",
        layer: "skill",
        description: "test",
        params: [],
        content: "Hello {{issueNumber}}",
        filePath: "test.handlebars",
      });
      service.renderSystemPrompt("feature-planning", { issueNumber: 42 });
      expect(mockRegistry.getTemplate).toHaveBeenCalledWith("feature-planning-system", undefined);
    });

    it("does not double-append -system suffix", () => {
      service.renderSystemPrompt("feature-planning-system", {});
      expect(mockRegistry.getTemplate).toHaveBeenCalledWith("feature-planning-system", undefined);
    });

    it("returns null when template is not found", () => {
      mockRegistry.getTemplate.mockReturnValue(null);
      expect(service.renderSystemPrompt("unknown-stage", {})).toBeNull();
    });
  });

  describe("renderComplexityAssessment()", () => {
    it("looks up complexity-assessment-dialog template", async () => {
      await service.initialize();
      service.renderComplexityAssessment({ assessedComplexity: "M" });
      expect(mockRegistry.getTemplate).toHaveBeenCalledWith(
        "complexity-assessment-dialog",
        undefined
      );
    });
  });

  describe("renderApprovalPrompt()", () => {
    it("looks up approval-prompt-dialog template", async () => {
      await service.initialize();
      service.renderApprovalPrompt({
        stageName: "feature-planning",
        summary: "Ready",
      });
      expect(mockRegistry.getTemplate).toHaveBeenCalledWith("approval-prompt-dialog", undefined);
    });
  });

  describe("hasTemplate()", () => {
    it("returns false when template is not found", async () => {
      await service.initialize();
      mockRegistry.getTemplate.mockReturnValue(null);
      expect(service.hasTemplate("unknown")).toBe(false);
    });

    it("returns true when template is found", async () => {
      await service.initialize();
      mockRegistry.getTemplate.mockReturnValue({
        name: "found",
        version: "1.0.0",
      });
      expect(service.hasTemplate("found")).toBe(true);
    });
  });

  describe("before initialize()", () => {
    it("throws when renderSystemPrompt is called before initialize()", () => {
      expect(() => service.renderSystemPrompt("feature-planning", {})).toThrow("call initialize()");
    });

    it("throws when renderComplexityAssessment is called before initialize()", () => {
      expect(() => service.renderComplexityAssessment({})).toThrow("call initialize()");
    });
  });
});
