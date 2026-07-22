import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import { PIPELINE_LOCKED_SECTIONS, SETTINGS_SECTIONS } from "../../../src/views/settings/types";
import type { IncrediConfig } from "../../../src/views/settings/types";

const mockWebview = { cspSource: "test-csp" } as any;

// Editable tier state so inputs aren't disabled by read-only tier
const editableTierState = {
  currentTier: "project" as const,
  defaultEditTier: "project" as const,
  hasGlobalConfig: false,
  hasLocalConfig: false,
  hasProjectConfig: true,
  activeEnvVars: [],
};

describe("SettingsHtml per-section lock", () => {
  it("renders locked sections with disabled inputs when pipeline is running", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const lockedSections = new Set(PIPELINE_LOCKED_SECTIONS);
    const html = getSettingsHtml(mockWebview, config, lockedSections, {}, editableTierState);

    // Locked sections should have section-locked class
    for (const sectionId of PIPELINE_LOCKED_SECTIONS) {
      const sectionRegex = new RegExp(`class="section section-locked" id="section-${sectionId}"`);
      expect(html).toMatch(sectionRegex);
    }
  });

  it("renders unlocked sections with enabled inputs during pipeline", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const lockedSections = new Set(PIPELINE_LOCKED_SECTIONS);
    const html = getSettingsHtml(mockWebview, config, lockedSections, {}, editableTierState);

    // Unlocked sections should NOT have section-locked class
    const unlockedSections = SETTINGS_SECTIONS.filter(
      (s) => !PIPELINE_LOCKED_SECTIONS.includes(s.id)
    );

    for (const section of unlockedSections) {
      const sectionRegex = new RegExp(`class="section " id="section-${section.id}"`);
      expect(html).toMatch(sectionRegex);
    }

    // Verify project.number is enabled since 'project' is unlocked
    const projectSectionMatch = html.match(
      /id="section-project"[\s\S]*?(?=<div class="section[ "])/
    );
    expect(projectSectionMatch).not.toBeNull();
    const projectNumberMatch = projectSectionMatch![0].match(/id="project\.number"[^>]*/);
    expect(projectNumberMatch).not.toBeNull();
    expect(projectNumberMatch![0]).not.toContain("disabled");
  });

  it("shows locked notice when sections are locked", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const lockedSections = new Set(PIPELINE_LOCKED_SECTIONS);
    const html = getSettingsHtml(mockWebview, config, lockedSections);

    expect(html).toContain('class="locked-notice "');
    expect(html).toContain("Some settings are locked while a pipeline is running");
  });

  it("hides locked notice when no sections are locked", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config);

    expect(html).toContain('class="locked-notice hidden"');
  });

  it("shows lock indicator on section headers for locked sections", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const lockedSections = new Set(PIPELINE_LOCKED_SECTIONS);
    const html = getSettingsHtml(mockWebview, config, lockedSections);

    // Each locked section should have a lock icon in its header
    for (const sectionId of PIPELINE_LOCKED_SECTIONS) {
      const sectionMatch = html.match(
        new RegExp(`data-section="${sectionId}"[\\s\\S]*?</div>\\s*</div>`)
      );
      expect(sectionMatch).not.toBeNull();
      expect(sectionMatch![0]).toContain("section-lock-icon");
      expect(sectionMatch![0]).toContain("codicon-lock");
    }
  });

  it("does not show lock indicator on unlocked section headers", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const lockedSections = new Set(PIPELINE_LOCKED_SECTIONS);
    const html = getSettingsHtml(mockWebview, config, lockedSections);

    // project section header should NOT have lock icon
    const projectHeaderMatch = html.match(/data-section="project"[\s\S]*?<\/div>\s*<\/div>/);
    expect(projectHeaderMatch).not.toBeNull();
    expect(projectHeaderMatch![0]).not.toContain("section-lock-icon");
  });

  it("keeps search input enabled during lock", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const lockedSections = new Set(PIPELINE_LOCKED_SECTIONS);
    const html = getSettingsHtml(mockWebview, config, lockedSections);

    const searchMatch = html.match(/id="searchInput"[^>]*/);
    expect(searchMatch).not.toBeNull();
    expect(searchMatch![0]).not.toContain("disabled");
  });

  it("renders all sections without lock styling when no lock is active", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config);

    // No section div should have section-locked class (check section elements, not CSS)
    for (const section of SETTINGS_SECTIONS) {
      const sectionRegex = new RegExp(`class="section " id="section-${section.id}"`);
      expect(html).toMatch(sectionRegex);
    }
    // No section header should have lock icon
    for (const section of SETTINGS_SECTIONS) {
      const headerMatch = html.match(
        new RegExp(`data-section="${section.id}"[\\s\\S]*?</div>\\s*</div>`)
      );
      expect(headerMatch).not.toBeNull();
      expect(headerMatch![0]).not.toContain("section-lock-icon");
    }
  });

  it("disables inputs in locked pipeline section", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const lockedSections = new Set(PIPELINE_LOCKED_SECTIONS);
    const html = getSettingsHtml(mockWebview, config, lockedSections, {}, editableTierState);

    // pipeline.ci_timeout should be disabled since 'pipeline' is locked
    const pipelineSection = html.match(/id="section-pipeline"[\s\S]*?(?=id="section-|$)/);
    expect(pipelineSection).not.toBeNull();
    const ciTimeoutMatch = pipelineSection![0].match(/id="pipeline\.ci_timeout"[^>]*/);
    expect(ciTimeoutMatch).not.toBeNull();
    expect(ciTimeoutMatch![0]).toContain("disabled");
  });
});
