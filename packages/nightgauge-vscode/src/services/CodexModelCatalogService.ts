import fs from "fs";
import os from "os";
import path from "path";
import {
  listCodexModels,
  isValidCodexModel,
  isDeprecatedCodexModel,
  isResearchPreviewCodexModel,
} from "@nightgauge/sdk";

interface CodexCachedModel {
  slug?: string;
  visibility?: string;
  priority?: number;
}

interface CodexModelsCache {
  models?: CodexCachedModel[];
}

export class CodexModelCatalogService {
  constructor(
    private readonly codexHome: string = process.env.CODEX_HOME || path.join(os.homedir(), ".codex")
  ) {}

  listModels(): string[] {
    const cachePath = path.join(this.codexHome, "models_cache.json");

    try {
      if (!fs.existsSync(cachePath)) {
        return listCodexModels();
      }

      const parsed = JSON.parse(fs.readFileSync(cachePath, "utf-8")) as CodexModelsCache;
      const models = Array.isArray(parsed.models) ? parsed.models : [];

      const discovered = models
        .filter(
          (model): model is Required<Pick<CodexCachedModel, "slug">> & CodexCachedModel =>
            typeof model.slug === "string" &&
            model.slug.trim().length > 0 &&
            model.visibility !== "hide"
        )
        .sort((left, right) => {
          const leftPriority = left.priority ?? Number.MAX_SAFE_INTEGER;
          const rightPriority = right.priority ?? Number.MAX_SAFE_INTEGER;
          if (leftPriority !== rightPriority) {
            return leftPriority - rightPriority;
          }
          return left.slug.localeCompare(right.slug);
        })
        .map((model) => model.slug.trim())
        // Drop deprecated/invalid/preview cached slugs so a stale daemon cache
        // can never surface a model the registry has retired or holds back.
        // `isValidCodexModel` is true for deprecated-but-known ids (e.g.
        // gpt-5.3-codex) and for research-preview ids (e.g. gpt-5.3-codex-spark),
        // so we additionally exclude both — keeping the discovered set identical
        // to the listCodexModels() default fallback (current, stable models only).
        .filter(
          (slug) =>
            isValidCodexModel(slug) &&
            !isDeprecatedCodexModel(slug) &&
            !isResearchPreviewCodexModel(slug)
        );

      return discovered.length > 0 ? discovered : listCodexModels();
    } catch {
      return listCodexModels();
    }
  }
}
