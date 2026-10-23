/**
 * Tests for KnowledgeDocumentLinkProvider
 *
 * @see Issue #1687 - Implement KnowledgeDocumentLinkProvider
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { KnowledgeDocumentLinkProvider } from "../../src/views/KnowledgeDocumentLinkProvider";

// VSCode mock
vi.mock("vscode", () => ({
  DocumentLink: vi.fn(function (range: any, target: any) {
    return { range, target, tooltip: undefined };
  }),
  Range: vi.fn(function (start: any, end: any) {
    return { start, end };
  }),
  Uri: { file: vi.fn((p: string) => ({ fsPath: p, scheme: "file" })) },
  DiagnosticSeverity: { Warning: 1 },
  Diagnostic: vi.fn(function (range: any, msg: any, severity: any) {
    return { range, message: msg, severity, source: undefined };
  }),
  languages: {
    createDiagnosticCollection: vi.fn(() => ({
      set: vi.fn(),
      delete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

// SDK mock
vi.mock("@nightgauge/sdk", () => ({
  extractWikiLinks: vi.fn(),
  resolveWikiLink: vi.fn(),
}));

// fs/promises mock
vi.mock("node:fs/promises", () => ({
  access: vi.fn(),
}));

import * as vscode from "vscode";
import * as fsMod from "node:fs/promises";
import { extractWikiLinks, resolveWikiLink } from "@nightgauge/sdk";

const mockExtractWikiLinks = vi.mocked(extractWikiLinks);
const mockResolveWikiLink = vi.mocked(resolveWikiLink);
const mockFsAccess = vi.mocked(fsMod.access);

function makeDocument(content: string, fsPath = "/workspace/doc.md") {
  return {
    getText: () => content,
    uri: { fsPath, scheme: "file" },
    positionAt: (offset: number) => ({ line: 0, character: offset }),
  } as any;
}

function makeCancellationToken(cancelled = false) {
  return { isCancellationRequested: cancelled } as vscode.CancellationToken;
}

function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as any;
}

describe("KnowledgeDocumentLinkProvider", () => {
  let provider: KnowledgeDocumentLinkProvider;
  let diagnosticsCollection: ReturnType<typeof vscode.languages.createDiagnosticCollection>;

  beforeEach(() => {
    vi.clearAllMocks();
    diagnosticsCollection = {
      set: vi.fn(),
      delete: vi.fn(),
      dispose: vi.fn(),
    } as any;
    vi.mocked(vscode.languages.createDiagnosticCollection).mockReturnValue(
      diagnosticsCollection as any
    );
    provider = new KnowledgeDocumentLinkProvider("/workspace", makeLogger());
  });

  it("returns empty array when knowledge directory does not exist", async () => {
    mockFsAccess.mockRejectedValue(new Error("ENOENT"));

    const result = await provider.provideDocumentLinks(
      makeDocument("[[some-link]]"),
      makeCancellationToken()
    );

    expect(result).toEqual([]);
    expect(diagnosticsCollection.delete).toHaveBeenCalled();
    expect(mockExtractWikiLinks).not.toHaveBeenCalled();
  });

  it("returns empty array when document has no wiki-links", async () => {
    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([]);

    const result = await provider.provideDocumentLinks(
      makeDocument("No wiki links here."),
      makeCancellationToken()
    );

    expect(result).toEqual([]);
    expect(diagnosticsCollection.delete).toHaveBeenCalled();
  });

  it("returns DocumentLink with file URI for resolved link", async () => {
    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([
      {
        raw: "architecture/ADR-001",
        match: "[[architecture/ADR-001]]",
        index: 0,
      },
    ]);
    mockResolveWikiLink.mockResolvedValue({
      link: "architecture/ADR-001",
      exists: true,
      resolvedPath: "/workspace/.nightgauge/knowledge/architecture/ADR-001.md",
      candidates: ["/workspace/.nightgauge/knowledge/architecture/ADR-001.md"],
      isAmbiguous: false,
    });

    const result = await provider.provideDocumentLinks(
      makeDocument("[[architecture/ADR-001]]"),
      makeCancellationToken()
    );

    expect(result).toHaveLength(1);
    expect(vscode.Uri.file).toHaveBeenCalledWith(
      "/workspace/.nightgauge/knowledge/architecture/ADR-001.md"
    );
  });

  it("sets tooltip to the resolved file path", async () => {
    const resolvedPath = "/workspace/.nightgauge/knowledge/glossary.md";
    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([{ raw: "glossary", match: "[[glossary]]", index: 5 }]);
    mockResolveWikiLink.mockResolvedValue({
      link: "glossary",
      exists: true,
      resolvedPath,
      candidates: [resolvedPath],
      isAmbiguous: false,
    });

    // DocumentLink constructor mock returns object with settable tooltip
    const mockLink = { range: {}, target: {}, tooltip: undefined };
    vi.mocked(vscode.DocumentLink).mockImplementation(function () {
      return mockLink as any;
    });

    await provider.provideDocumentLinks(
      makeDocument("text [[glossary]] text"),
      makeCancellationToken()
    );

    expect(mockLink.tooltip).toBe(resolvedPath);
  });

  it("adds warning diagnostic for unresolved link", async () => {
    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([
      { raw: "nonexistent", match: "[[nonexistent]]", index: 0 },
    ]);
    mockResolveWikiLink.mockResolvedValue({
      link: "nonexistent",
      exists: false,
      resolvedPath: "/workspace/.nightgauge/knowledge/nonexistent.md",
      candidates: [],
      isAmbiguous: false,
    });

    const result = await provider.provideDocumentLinks(
      makeDocument("[[nonexistent]]"),
      makeCancellationToken()
    );

    expect(result).toHaveLength(0);
    expect(vscode.Diagnostic).toHaveBeenCalledWith(
      expect.anything(),
      "Wiki-link 'nonexistent' could not be resolved in knowledge base",
      vscode.DiagnosticSeverity.Warning
    );
    expect(diagnosticsCollection.set).toHaveBeenCalled();
  });

  it("clears diagnostics when document has no wiki-links", async () => {
    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([]);

    await provider.provideDocumentLinks(makeDocument("no links"), makeCancellationToken());

    expect(diagnosticsCollection.delete).toHaveBeenCalled();
    expect(diagnosticsCollection.set).not.toHaveBeenCalled();
  });

  it("returns empty array when cancellation is requested before processing", async () => {
    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([
      { raw: "link1", match: "[[link1]]", index: 0 },
      { raw: "link2", match: "[[link2]]", index: 10 },
    ]);

    const result = await provider.provideDocumentLinks(
      makeDocument("[[link1]] [[link2]]"),
      makeCancellationToken(true) // already cancelled
    );

    // Loop breaks immediately — no resolveWikiLink calls, empty links/diagnostics
    expect(mockResolveWikiLink).not.toHaveBeenCalled();
    expect(result).toHaveLength(0);
  });

  it("handles relative links resolved relative to document file", async () => {
    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([
      { raw: "../shared/glossary", match: "[[../shared/glossary]]", index: 0 },
    ]);
    mockResolveWikiLink.mockResolvedValue({
      link: "../shared/glossary",
      exists: true,
      resolvedPath: "/workspace/.nightgauge/knowledge/shared/glossary.md",
      candidates: ["/workspace/.nightgauge/knowledge/shared/glossary.md"],
      isAmbiguous: false,
    });

    const result = await provider.provideDocumentLinks(
      makeDocument("[[../shared/glossary]]", "/workspace/docs/notes.md"),
      makeCancellationToken()
    );

    expect(mockResolveWikiLink).toHaveBeenCalledWith(
      "../shared/glossary",
      "/workspace/docs/notes.md",
      "/workspace",
      undefined
    );
    expect(result).toHaveLength(1);
  });

  it("handles ambiguous links (returns first candidate, logs warning)", async () => {
    const logger = makeLogger();
    vi.mocked(vscode.languages.createDiagnosticCollection).mockReturnValue(
      diagnosticsCollection as any
    );
    provider = new KnowledgeDocumentLinkProvider("/workspace", logger);

    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([{ raw: "api", match: "[[api]]", index: 0 }]);
    const firstCandidate = "/workspace/.nightgauge/knowledge/api-overview.md";
    mockResolveWikiLink.mockResolvedValue({
      link: "api",
      exists: true,
      resolvedPath: firstCandidate,
      candidates: [firstCandidate, "/workspace/.nightgauge/knowledge/api-reference.md"],
      isAmbiguous: true,
    });

    const result = await provider.provideDocumentLinks(
      makeDocument("[[api]]"),
      makeCancellationToken()
    );

    expect(result).toHaveLength(1);
    expect(logger.info).toHaveBeenCalledWith(
      expect.stringContaining("Ambiguous wiki-link 'api'"),
      expect.objectContaining({ resolvedPath: firstCandidate })
    );
  });

  it("handles multiple wiki-links in same document (resolved + unresolved)", async () => {
    mockFsAccess.mockResolvedValue(undefined);
    mockExtractWikiLinks.mockReturnValue([
      { raw: "exists", match: "[[exists]]", index: 0 },
      { raw: "missing", match: "[[missing]]", index: 15 },
    ]);
    mockResolveWikiLink
      .mockResolvedValueOnce({
        link: "exists",
        exists: true,
        resolvedPath: "/workspace/.nightgauge/knowledge/exists.md",
        candidates: ["/workspace/.nightgauge/knowledge/exists.md"],
        isAmbiguous: false,
      })
      .mockResolvedValueOnce({
        link: "missing",
        exists: false,
        resolvedPath: "/workspace/.nightgauge/knowledge/missing.md",
        candidates: [],
        isAmbiguous: false,
      });

    const result = await provider.provideDocumentLinks(
      makeDocument("[[exists]] and [[missing]]"),
      makeCancellationToken()
    );

    expect(result).toHaveLength(1);
    expect(vscode.Diagnostic).toHaveBeenCalledTimes(1);
    expect(diagnosticsCollection.set).toHaveBeenCalledWith(
      expect.anything(),
      expect.arrayContaining([
        expect.objectContaining({
          message: expect.stringContaining("missing"),
        }),
      ])
    );
  });

  it("disposes diagnostic collection on dispose()", () => {
    provider.dispose();
    expect(diagnosticsCollection.dispose).toHaveBeenCalled();
  });

  describe("cross-repo wiki-links", () => {
    const workspaceConfig = {
      workspace: { name: "test-workspace" },
      repositories: [{ name: "platform", path: "platform" }],
      knowledge: { cross_repo_links: true },
    };

    beforeEach(() => {
      vi.mocked(vscode.languages.createDiagnosticCollection).mockReturnValue(
        diagnosticsCollection as any
      );
      provider = new KnowledgeDocumentLinkProvider(
        "/workspace",
        makeLogger(),
        workspaceConfig as any
      );
    });

    it("passes workspaceConfig to resolveWikiLink when provided", async () => {
      mockFsAccess.mockResolvedValue(undefined);
      mockExtractWikiLinks.mockReturnValue([
        {
          raw: "platform:architecture/ADR-001",
          match: "[[platform:architecture/ADR-001]]",
          index: 0,
        },
      ]);
      mockResolveWikiLink.mockResolvedValue({
        link: "platform:architecture/ADR-001",
        exists: true,
        resolvedPath: "/workspace/platform/.nightgauge/knowledge/architecture/ADR-001.md",
        candidates: ["/workspace/platform/.nightgauge/knowledge/architecture/ADR-001.md"],
        isAmbiguous: false,
        isCrossRepo: true,
        repoName: "platform",
      });

      const result = await provider.provideDocumentLinks(
        makeDocument("[[platform:architecture/ADR-001]]"),
        makeCancellationToken()
      );

      expect(mockResolveWikiLink).toHaveBeenCalledWith(
        "platform:architecture/ADR-001",
        expect.any(String),
        "/workspace",
        workspaceConfig
      );
      expect(result).toHaveLength(1);
    });

    it("shows repo-specific diagnostic message when cross-repo link fails (unknown repo)", async () => {
      mockFsAccess.mockResolvedValue(undefined);
      mockExtractWikiLinks.mockReturnValue([
        {
          raw: "unknown-repo:some/path",
          match: "[[unknown-repo:some/path]]",
          index: 0,
        },
      ]);
      mockResolveWikiLink.mockResolvedValue({
        link: "unknown-repo:some/path",
        exists: false,
        resolvedPath: "",
        candidates: [],
        isAmbiguous: false,
        isCrossRepo: true,
        repoName: "unknown-repo",
      });

      const result = await provider.provideDocumentLinks(
        makeDocument("[[unknown-repo:some/path]]"),
        makeCancellationToken()
      );

      expect(result).toHaveLength(0);
      expect(vscode.Diagnostic).toHaveBeenCalledWith(
        expect.anything(),
        expect.stringContaining("repo 'unknown-repo' not found in workspace config"),
        vscode.DiagnosticSeverity.Warning
      );
    });

    it("shows standard diagnostic for plain unresolved link even when workspaceConfig is set", async () => {
      mockFsAccess.mockResolvedValue(undefined);
      mockExtractWikiLinks.mockReturnValue([
        { raw: "nonexistent", match: "[[nonexistent]]", index: 0 },
      ]);
      mockResolveWikiLink.mockResolvedValue({
        link: "nonexistent",
        exists: false,
        resolvedPath: "/workspace/.nightgauge/knowledge/nonexistent.md",
        candidates: [],
        isAmbiguous: false,
        // isCrossRepo not set — plain link
      });

      await provider.provideDocumentLinks(makeDocument("[[nonexistent]]"), makeCancellationToken());

      expect(vscode.Diagnostic).toHaveBeenCalledWith(
        expect.anything(),
        "Wiki-link 'nonexistent' could not be resolved in knowledge base",
        vscode.DiagnosticSeverity.Warning
      );
    });

    it("plain links still resolve normally when workspaceConfig is provided", async () => {
      mockFsAccess.mockResolvedValue(undefined);
      mockExtractWikiLinks.mockReturnValue([
        {
          raw: "architecture/ADR-001",
          match: "[[architecture/ADR-001]]",
          index: 0,
        },
      ]);
      mockResolveWikiLink.mockResolvedValue({
        link: "architecture/ADR-001",
        exists: true,
        resolvedPath: "/workspace/.nightgauge/knowledge/architecture/ADR-001.md",
        candidates: ["/workspace/.nightgauge/knowledge/architecture/ADR-001.md"],
        isAmbiguous: false,
      });

      const result = await provider.provideDocumentLinks(
        makeDocument("[[architecture/ADR-001]]"),
        makeCancellationToken()
      );

      expect(result).toHaveLength(1);
      expect(vscode.Uri.file).toHaveBeenCalledWith(
        "/workspace/.nightgauge/knowledge/architecture/ADR-001.md"
      );
    });
  });
});
