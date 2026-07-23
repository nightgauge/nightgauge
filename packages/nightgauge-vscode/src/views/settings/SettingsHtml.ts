/**
 * SettingsHtml - HTML template generator for the settings WebView
 *
 * Generates the HTML, CSS, and JavaScript for rendering the settings panel.
 * Uses VSCode CSS variables for consistent theming.
 * Supports multi-tier configuration with source badges.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 * @see Issue #440 - Multi-tier config GUI support
 */

import * as vscode from "vscode";
import type { IncrediConfig, ViewTier, TierViewState } from "./types";
import { SETTINGS_SECTIONS, DEFAULT_CONFIG, TIER_TABS, PIPELINE_LOCKED_SECTIONS } from "./types";
import { getForgeInstancesSectionHtml } from "./ForgeInstancesSection";
import { mergeWithDefaults, getConfigValue } from "./IncrediYamlService";
import type { ConfigSourceMap, TrustedStage } from "../../config/schema";
import { getTierBadgeHtml, getTierBadgeStyles, getUxTierBadgeHtml } from "./TierBadge";
import type { TierAuditEntry } from "../../services/IpcClientBase";
import { modelSupportsEffort } from "../../utils/incrediConfig";
import type { DefaultModel } from "../../utils/incrediConfig";
import { CODEX_DEFAULT_BASE_MODEL } from "@nightgauge/sdk";

/**
 * Pre-computed mode-aware preview row supplied by `SettingsPanel`.
 *
 * Computed in the extension host (so we can reuse `resolveStageAdapter` and
 * `getModeStageAdapterModel` directly) and rendered as a read-only summary
 * table in the per-stage adapter matrix.
 *
 * @see Issue #3225 - settings UI per-stage adapter selector
 */
export interface StageAdapterPreviewRow {
  stage: string;
  adapter: string;
  source: string;
  model: string;
  modelMismatch?: boolean;
}

export interface SettingsHtmlOptions {
  codexModels?: string[];
  lmStudioModels?: Array<{
    id: string;
    loaded?: boolean;
    maxContextLength?: number;
    currentContextLength?: number;
  }>;
  /**
   * Per-stage `(adapter, model)` resolution preview computed from
   * `resolveStageAdapter` + `getModeStageAdapterModel` under the active
   * `performance_mode`. Read-only display in the Per-Stage Adapter Routing
   * subsection. Empty/undefined → preview hidden.
   */
  stageAdapterPreview?: StageAdapterPreviewRow[];
  /** Active performance mode label, used in the preview heading. */
  performanceMode?: string;
  /** Forge instances list from IPC forge.list, rendered in the Forge Instances section. */
  forgeInstances?: import("./ForgeInstancesSection").ForgeInstanceRow[];
  /** The default_forge value from config, used to mark the default row. */
  defaultForgeId?: string;
  /** Tier audit entries for drift badge + banner. */
  tierAuditEntries?: TierAuditEntry[];
  /** Whether the drift banner has been dismissed this session. */
  driftBannerDismissed?: boolean;
}

/**
 * Six pipeline stages exposed in the per-stage adapter matrix. Mirrors
 * `PipelineStageSchema.options` minus the synthetic `pipeline-start` /
 * `pipeline-finish` markers.
 */
export const STAGE_ADAPTER_STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

/**
 * Adapter dropdown options for the per-stage matrix. The leading entry has
 * an empty value representing "(Use global default)" — i.e. the resolver
 * falls through to `ui.core.adapter`.
 *
 * Only agentic adapters belong here. `AdapterEnumSchema` intentionally remains
 * broader for backward-compatible parsing and non-pipeline eval/judge surfaces;
 * Gemini SDK, LM Studio, and Ollama have no repository-changing tool loop.
 */
export const STAGE_ADAPTER_OPTIONS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "", label: "(Use global default)" },
  { value: "claude", label: "Claude" },
  { value: "codex", label: "Codex (Beta)" },
  { value: "gemini", label: "Gemini CLI (Experimental)" },
  { value: "copilot", label: "GitHub Copilot CLI (Experimental)" },
];

/**
 * Per-stage model dropdown options, bound to `pipeline.stage_models.<stage>`.
 *
 * Values are canonical TIER keywords — the routing/validation layer (#4021)
 * resolves each tier to the concrete model for whichever adapter the stage runs
 * on (e.g. `sonnet` → `claude-sonnet-4-6` for Claude, `gpt-5.4` for Codex). Tier
 * keywords are therefore the portable, adapter-neutral choice: valid for every
 * adapter by construction, with the resolved `(adapter, model)` shown in the
 * preview table below the matrix. The leading empty entry clears the override so
 * the stage falls back to the global default model.
 */
export const STAGE_MODEL_OPTIONS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "", label: "(Use global default)" },
  { value: "haiku", label: "haiku (light tier)" },
  { value: "sonnet", label: "sonnet (standard tier)" },
  { value: "opus", label: "opus (heavy tier)" },
  { value: "fable", label: "fable" },
];

const STAGE_LABELS: Record<string, string> = {
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Feature Planning",
  "feature-dev": "Feature Dev",
  "feature-validate": "Feature Validate",
  "pr-create": "PR Create",
  "pr-merge": "PR Merge",
};

/**
 * Generate nonce for script security
 */
function getNonce(): string {
  let text = "";
  const possible = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  for (let i = 0; i < 32; i++) {
    text += possible.charAt(Math.floor(Math.random() * possible.length));
  }
  return text;
}

/**
 * Escape HTML special characters
 */
function escapeHtml(text: string): string {
  const htmlEntities: Record<string, string> = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  };
  return text.replace(/[&<>"']/g, (char) => htmlEntities[char] || char);
}

/**
 * Get codicon class for section icon
 */
function getCodiconClass(icon: string): string {
  return `codicon codicon-${icon}`;
}

/**
 * Get the source tier for a config path
 */
function getSourceForPath(path: string, sources: ConfigSourceMap): ViewTier | "cli" | "default" {
  return (sources[path] as ViewTier | "cli") ?? "default";
}

/**
 * Check if current tier allows editing for this setting
 */
function isEditableInCurrentTier(
  currentTier: ViewTier,
  source: ViewTier | "cli" | "default"
): boolean {
  // In merged view, settings are editable
  if (currentTier === "merged") return true;
  // In project/local views, those tiers are editable
  if (currentTier === "project" || currentTier === "local") return true;
  // Global, default, and env views are read-only
  return false;
}

/**
 * Generate "Edit team config" button for project-tier controls in merged view.
 *
 * Posts { type: 'action', action: 'edit-team-config' } via the webview JS
 * handler added to the JS section. Uses data-action so the generic
 * .action-btn handler picks it up.
 */
function getEditTeamConfigBtnHtml(): string {
  return `<button type="button" class="edit-team-config-btn" data-action="edit-team-config"
    title="Open team config (.nightgauge/config.yaml) in editor">Edit team config</button>`;
}

/**
 * Generate tier info banner HTML for merged view (Issue #3339).
 *
 * Explains the Team/You/This-run tier model with a "Learn more" link.
 * Only rendered when currentTier === "merged".
 */
function getTierInfoBannerHtml(currentTier: ViewTier): string {
  if (currentTier !== "merged") return "";
  return `
    <div class="tier-info-banner" role="note">
      <span class="codicon codicon-info tier-info-banner-icon"></span>
      <span class="tier-info-banner-text">
        Settings come from three tiers:
        <span class="tier-badge tier-badge-ux tier-badge-ux-team" style="background:var(--vscode-charts-green);color:var(--vscode-editor-background);">Team</span>
        (project config, committed to git),
        <span class="tier-badge tier-badge-ux tier-badge-ux-you" style="background:var(--vscode-charts-blue);color:var(--vscode-editor-background);">You</span>
        (your machine only), and
        <span class="tier-badge tier-badge-ux tier-badge-ux-this-run" style="background:var(--vscode-charts-purple);color:var(--vscode-editor-background);">This run</span>
        (never committed). Edits made here save to your local override file
        (config.local.yaml) — the committed team config only changes via the
        Project tab.
      </span>
      <a href="#" class="tier-info-banner-learn-more" data-doc="docs/CONFIGURATION.md">Learn more</a>
    </div>
  `;
}

/**
 * Generate drift banner HTML for the top of the settings panel.
 *
 * Shows a warning when one or more config keys are stored in the wrong tier
 * (DRIFT status from the tier audit). The banner is hidden when dismissed.
 */
function getDriftBannerHtml(entries: TierAuditEntry[], dismissed: boolean): string {
  if (dismissed) return "";
  const driftRows = entries.filter((e) => e.status.startsWith("DRIFT"));
  if (driftRows.length === 0) return "";
  const count = driftRows.length;
  const noun = count !== 1 ? "settings" : "setting";
  return `
    <div class="drift-banner" role="alert" aria-live="polite">
      <span class="drift-banner-icon">⚠</span>
      <span class="drift-banner-msg">${count} ${noun} stored in the wrong config tier.</span>
      <button class="drift-banner-btn" onclick="vscode.postMessage({type:'showDriftedKeysOnly'})" title="Filter to drifted settings">Show drifted keys</button>
      <button class="drift-banner-btn drift-banner-dismiss" onclick="vscode.postMessage({type:'dismissDriftBanner'})" aria-label="Dismiss drift banner">Dismiss</button>
    </div>`;
}

/**
 * Generate section header HTML
 */
function getSectionHeaderHtml(
  section: (typeof SETTINGS_SECTIONS)[number],
  locked: boolean = false
): string {
  const docLinkHtml = section.docLink
    ? `<a href="#" class="doc-link" data-doc="${escapeHtml(section.docLink)}" title="View documentation">
         <span class="codicon codicon-link-external"></span>
       </a>`
    : "";

  const lockIndicator = locked
    ? '<span class="section-lock-icon codicon codicon-lock" title="Locked while pipeline is running"></span>'
    : "";

  return `
    <div class="section-header" data-section="${section.id}">
      <div class="section-title-row">
        <span class="section-chevron codicon codicon-chevron-down"></span>
        <span class="${getCodiconClass(section.icon)} section-icon"></span>
        <h3 class="section-title">${escapeHtml(section.title)}</h3>
        ${lockIndicator}
        ${docLinkHtml}
      </div>
      <p class="section-description">${escapeHtml(section.description)}</p>
    </div>
  `;
}

/**
 * Generate drift badge HTML for a specific config key.
 *
 * Returns empty string when the key has no drift or options are not provided.
 */
function getDriftBadgeForKeyHtml(key: string, options?: SettingsHtmlOptions): string {
  if (!options?.tierAuditEntries) return "";
  const entry = options.tierAuditEntries.find((e) => e.key === key);
  if (!entry || !entry.status.startsWith("DRIFT")) return "";
  const tooltip = escapeHtml(
    `Tier drift: stored in ${entry.effectiveTier}, target ${entry.targetTier}`
  );
  return `<span class="drift-badge" title="${tooltip}" aria-label="${tooltip}">Drift</span>`;
}

/**
 * Generate "Move to <tier>" button HTML for a drifted config key.
 *
 * Returns empty string when the key has no drift or options are not provided.
 */
function getDriftMoveButtonHtml(key: string, options?: SettingsHtmlOptions): string {
  if (!options?.tierAuditEntries) return "";
  const entry = options.tierAuditEntries.find((e) => e.key === key);
  if (!entry || !entry.status.startsWith("DRIFT")) return "";
  const safeKey = escapeHtml(key);
  const safeTier = escapeHtml(entry.targetTier);
  return `<button class="drift-move-btn" title="Move to ${safeTier} tier"
    onclick="vscode.postMessage({type:'moveTierKey',key:'${safeKey}',targetTier:'${safeTier}'})">Move to ${safeTier} tier</button>`;
}

/**
 * Generate toggle input HTML
 */
function getToggleHtml(
  id: string,
  label: string,
  description: string,
  value: boolean,
  disabled: boolean = false,
  source?: ViewTier | "cli" | "default",
  showBadge: boolean = true,
  options?: SettingsHtmlOptions
): string {
  const badgeHtml = showBadge && source ? getUxTierBadgeHtml(source) : "";
  const driftBadgeHtml = getDriftBadgeForKeyHtml(id, options);
  const driftMoveBtn = getDriftMoveButtonHtml(id, options);
  const modifiedClass = source && source !== "default" ? "setting-modified" : "";
  const teamTierDisabled = showBadge && source === "project";
  const isDisabled = disabled || teamTierDisabled;
  const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";

  return `
    <div class="setting-row ${modifiedClass}">
      <div class="setting-info">
        <label for="${id}" class="setting-label">${escapeHtml(label)} ${badgeHtml}${driftBadgeHtml}${editBtnHtml}</label>
        <p class="setting-description">${escapeHtml(description)}</p>
        ${driftMoveBtn}
      </div>
      <div class="setting-control">
        <input type="checkbox"
               id="${id}"
               class="toggle-input"
               data-path="${id}"
               ${value ? "checked" : ""}
               ${isDisabled ? "disabled" : ""}>
        <label for="${id}" class="toggle-switch"></label>
      </div>
    </div>
  `;
}

/**
 * Generate text input HTML
 */
function getTextInputHtml(
  id: string,
  label: string,
  description: string,
  value: string,
  placeholder: string = "",
  disabled: boolean = false,
  source?: ViewTier | "cli" | "default",
  showBadge: boolean = true,
  options?: SettingsHtmlOptions
): string {
  const badgeHtml = showBadge && source ? getUxTierBadgeHtml(source) : "";
  const driftBadgeHtml = getDriftBadgeForKeyHtml(id, options);
  const driftMoveBtn = getDriftMoveButtonHtml(id, options);
  const modifiedClass = source && source !== "default" ? "setting-modified" : "";
  const teamTierDisabled = showBadge && source === "project";
  const isDisabled = disabled || teamTierDisabled;
  const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";

  return `
    <div class="setting-row ${modifiedClass}">
      <div class="setting-info">
        <label for="${id}" class="setting-label">${escapeHtml(label)} ${badgeHtml}${driftBadgeHtml}${editBtnHtml}</label>
        <p class="setting-description">${escapeHtml(description)}</p>
        ${driftMoveBtn}
      </div>
      <div class="setting-control">
        <input type="text"
               id="${id}"
               class="text-input"
               data-path="${id}"
               value="${escapeHtml(value)}"
               placeholder="${escapeHtml(placeholder)}"
               ${isDisabled ? "disabled" : ""}>
      </div>
    </div>
  `;
}

function getSecretInputHtml(
  id: string,
  label: string,
  description: string,
  value: string,
  placeholder: string = "",
  disabled: boolean = false,
  source?: ViewTier | "cli" | "default",
  showBadge: boolean = true,
  options?: SettingsHtmlOptions
): string {
  const badgeHtml = showBadge && source ? getUxTierBadgeHtml(source) : "";
  const driftBadgeHtml = getDriftBadgeForKeyHtml(id, options);
  const driftMoveBtn = getDriftMoveButtonHtml(id, options);
  const modifiedClass = source && source !== "default" ? "setting-modified" : "";
  const teamTierDisabled = showBadge && source === "project";
  const isDisabled = disabled || teamTierDisabled;
  const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";
  const toggleBtn = !isDisabled
    ? `<button type="button" class="secret-toggle-btn" data-target="${id}" title="Show/hide value" aria-label="Toggle visibility">
        <span class="codicon codicon-eye"></span>
      </button>`
    : "";

  return `
    <div class="setting-row ${modifiedClass}">
      <div class="setting-info">
        <label for="${id}" class="setting-label">${escapeHtml(label)} ${badgeHtml}${driftBadgeHtml}${editBtnHtml}</label>
        <p class="setting-description">${escapeHtml(description)}</p>
        ${driftMoveBtn}
      </div>
      <div class="setting-control">
        <div class="secret-input-wrapper">
          <input type="password"
                 id="${id}"
                 class="text-input"
                 data-path="${id}"
                 value="${escapeHtml(value)}"
                 placeholder="${escapeHtml(placeholder)}"
                 autocomplete="off"
                 ${isDisabled ? "disabled" : ""}>
          ${toggleBtn}
        </div>
      </div>
    </div>
  `;
}

/**
 * Generate number input HTML
 */
function getNumberInputHtml(
  id: string,
  label: string,
  description: string,
  value: number | undefined,
  min?: number,
  max?: number,
  step?: number,
  disabled: boolean = false,
  source?: ViewTier | "cli" | "default",
  showBadge: boolean = true,
  options?: SettingsHtmlOptions
): string {
  const minAttr = min !== undefined ? `min="${min}"` : "";
  const maxAttr = max !== undefined ? `max="${max}"` : "";
  const stepAttr = step !== undefined ? `step="${step}"` : "";
  const valueStr = value !== undefined ? String(value) : "";
  const badgeHtml = showBadge && source ? getUxTierBadgeHtml(source) : "";
  const driftBadgeHtml = getDriftBadgeForKeyHtml(id, options);
  const driftMoveBtn = getDriftMoveButtonHtml(id, options);
  const modifiedClass = source && source !== "default" ? "setting-modified" : "";
  const teamTierDisabled = showBadge && source === "project";
  const isDisabled = disabled || teamTierDisabled;
  const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";

  return `
    <div class="setting-row ${modifiedClass}">
      <div class="setting-info">
        <label for="${id}" class="setting-label">${escapeHtml(label)} ${badgeHtml}${driftBadgeHtml}${editBtnHtml}</label>
        <p class="setting-description">${escapeHtml(description)}</p>
        ${driftMoveBtn}
      </div>
      <div class="setting-control">
        <input type="number"
               id="${id}"
               class="number-input"
               data-path="${id}"
               value="${valueStr}"
               ${minAttr}
               ${maxAttr}
               ${stepAttr}
               ${isDisabled ? "disabled" : ""}>
      </div>
    </div>
  `;
}

function getInlineActionBarHtml(
  actions: { action: string; label: string; title?: string }[],
  disabled: boolean = false
): string {
  const buttonsHtml = actions
    .map(
      (item) => `
        <button type="button"
                class="btn inline-action-btn"
                data-action="${escapeHtml(item.action)}"
                ${item.title ? `title="${escapeHtml(item.title)}"` : ""}
                ${disabled ? "disabled" : ""}>
          ${escapeHtml(item.label)}
        </button>
      `
    )
    .join("");

  return `<div class="inline-action-bar">${buttonsHtml}</div>`;
}

function getLmStudioModelOptions(
  currentModel: string,
  models: Array<{
    id: string;
    loaded?: boolean;
    maxContextLength?: number;
    currentContextLength?: number;
  }>
): Array<{
  value: string;
  label: string;
  loaded?: boolean;
  maxContextLength?: number;
  currentContextLength?: number;
}> {
  const options: Array<{
    value: string;
    label: string;
    loaded?: boolean;
    maxContextLength?: number;
    currentContextLength?: number;
  }> = [{ value: "", label: models.length > 0 ? "Select a model..." : "Refresh models..." }];

  const seen = new Set<string>();
  for (const model of models) {
    if (!model.id || seen.has(model.id)) continue;
    seen.add(model.id);
    options.push({
      value: model.id,
      label: model.loaded ? `${model.id} (loaded)` : model.id,
      loaded: model.loaded,
      maxContextLength: model.maxContextLength,
      currentContextLength: model.currentContextLength,
    });
  }

  if (currentModel && !seen.has(currentModel)) {
    options.push({ value: currentModel, label: `${currentModel} (configured)` });
  }

  return options;
}

function getAdapterModelOptions(
  currentModel: string,
  recommendedModels: string[]
): Array<{ value: string; label: string }> {
  const options = recommendedModels.map((model) => ({ value: model, label: model }));
  if (currentModel && !recommendedModels.includes(currentModel)) {
    options.unshift({ value: currentModel, label: `${currentModel} (configured)` });
  }
  return options;
}

function getLmStudioModelSelectHtml(
  id: string,
  label: string,
  description: string,
  value: string,
  options: Array<{
    value: string;
    label: string;
    loaded?: boolean;
    maxContextLength?: number;
    currentContextLength?: number;
  }>,
  disabled: boolean = false,
  source?: ViewTier | "cli" | "default",
  showBadge: boolean = true
): string {
  const optionsHtml = options
    .map((opt) => {
      const maxContextAttr =
        opt.maxContextLength !== undefined
          ? ` data-max-context-length="${opt.maxContextLength}"`
          : "";
      const currentContextAttr =
        opt.currentContextLength !== undefined
          ? ` data-current-context-length="${opt.currentContextLength}"`
          : "";
      return `<option value="${escapeHtml(opt.value)}" ${value === opt.value ? "selected" : ""}${maxContextAttr}${currentContextAttr}>${escapeHtml(opt.label)}</option>`;
    })
    .join("");
  const badgeHtml = showBadge && source ? getUxTierBadgeHtml(source) : "";
  const modifiedClass = source && source !== "default" ? "setting-modified" : "";
  const teamTierDisabled = showBadge && source === "project";
  const isDisabled = disabled || teamTierDisabled;
  const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";

  return `
    <div class="setting-row ${modifiedClass}">
      <div class="setting-info">
        <label for="${id}" class="setting-label">${escapeHtml(label)} ${badgeHtml}${editBtnHtml}</label>
        <p class="setting-description">${escapeHtml(description)}</p>
        <p id="lm-studio-model-capacity" class="setting-description"></p>
      </div>
      <div class="setting-control">
        <select id="${id}"
                class="select-input"
                data-path="${id}"
                ${isDisabled ? "disabled" : ""}>
          ${optionsHtml}
        </select>
      </div>
    </div>
  `;
}

function getLmStudioContextLengthHtml(
  value: number | undefined,
  disabled: boolean,
  source: ViewTier | "cli" | "default",
  showBadge: boolean
): string {
  const badgeHtml = showBadge && source ? getUxTierBadgeHtml(source) : "";
  const modifiedClass = source && source !== "default" ? "setting-modified" : "";
  const teamTierDisabled = showBadge && source === "project";
  const isDisabled = disabled || teamTierDisabled;
  const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";
  const valueStr = value !== undefined ? String(value) : "";

  return `
    <div class="setting-row ${modifiedClass}">
      <div class="setting-info">
        <label for="lm_studio.context_length" class="setting-label">Context Length ${badgeHtml}${editBtnHtml}</label>
        <p class="setting-description">Applied when loading the model from this panel. Existing LM Studio sessions keep their current context window.</p>
      </div>
      <div class="setting-control context-length-control">
        <input type="number"
               id="lm_studio.context_length"
               class="number-input"
               data-path="lm_studio.context_length"
               value="${valueStr}"
               min="1"
               step="1"
               ${isDisabled ? "disabled" : ""}>
        <button type="button"
                id="lm-studio-use-max-context"
                class="btn inline-action-btn"
                title="Set context length to the selected model's maximum supported value"
                ${isDisabled ? "disabled" : ""}>
          Use Max
        </button>
      </div>
    </div>
  `;
}

/**
 * Generate select input HTML
 */
function getSelectHtml(
  id: string,
  label: string,
  description: string,
  value: string,
  selectOptions: { value: string; label: string }[],
  disabled: boolean = false,
  source?: ViewTier | "cli" | "default",
  showBadge: boolean = true,
  panelOptions?: SettingsHtmlOptions
): string {
  const optionsHtml = selectOptions
    .map(
      (opt) =>
        `<option value="${escapeHtml(opt.value)}" ${value === opt.value ? "selected" : ""}>${escapeHtml(opt.label)}</option>`
    )
    .join("");
  const badgeHtml = showBadge && source ? getUxTierBadgeHtml(source) : "";
  const driftBadgeHtml = getDriftBadgeForKeyHtml(id, panelOptions);
  const driftMoveBtn = getDriftMoveButtonHtml(id, panelOptions);
  const modifiedClass = source && source !== "default" ? "setting-modified" : "";
  const teamTierDisabled = showBadge && source === "project";
  const isDisabled = disabled || teamTierDisabled;
  const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";

  return `
    <div class="setting-row ${modifiedClass}">
      <div class="setting-info">
        <label for="${id}" class="setting-label">${escapeHtml(label)} ${badgeHtml}${driftBadgeHtml}${editBtnHtml}</label>
        <p class="setting-description">${escapeHtml(description)}</p>
        ${driftMoveBtn}
      </div>
      <div class="setting-control">
        <select id="${id}"
                class="select-input"
                data-path="${id}"
                ${isDisabled ? "disabled" : ""}>
          ${optionsHtml}
        </select>
      </div>
    </div>
  `;
}

/**
 * Generate list input HTML (for arrays)
 */
function getListInputHtml(
  id: string,
  label: string,
  description: string,
  values: string[],
  placeholder: string = "Add item...",
  disabled: boolean = false,
  source?: ViewTier | "cli" | "default",
  showBadge: boolean = true,
  options?: SettingsHtmlOptions
): string {
  const itemsHtml = values
    .map(
      (val, idx) => `
        <div class="list-item" data-index="${idx}">
          <span class="list-item-text">${escapeHtml(val)}</span>
          <button class="list-item-remove" data-path="${id}" data-index="${idx}" ${disabled ? "disabled" : ""}>
            <span class="codicon codicon-close"></span>
          </button>
        </div>
      `
    )
    .join("");
  const badgeHtml = showBadge && source ? getUxTierBadgeHtml(source) : "";
  const driftBadgeHtml = getDriftBadgeForKeyHtml(id, options);
  const driftMoveBtn = getDriftMoveButtonHtml(id, options);
  const modifiedClass = source && source !== "default" ? "setting-modified" : "";
  const teamTierDisabled = showBadge && source === "project";
  const isDisabled = disabled || teamTierDisabled;
  const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";

  return `
    <div class="setting-row setting-row-list ${modifiedClass}">
      <div class="setting-info">
        <label for="${id}" class="setting-label">${escapeHtml(label)} ${badgeHtml}${driftBadgeHtml}${editBtnHtml}</label>
        <p class="setting-description">${escapeHtml(description)}</p>
        ${driftMoveBtn}
      </div>
      <div class="setting-control list-control">
        <div class="list-items" id="${id}-items">
          ${itemsHtml || '<div class="list-empty">No items</div>'}
        </div>
        <div class="list-add">
          <input type="text"
                 id="${id}-input"
                 class="list-input"
                 placeholder="${escapeHtml(placeholder)}"
                 ${isDisabled ? "disabled" : ""}>
          <button class="list-add-btn" data-path="${id}" ${isDisabled ? "disabled" : ""}>
            <span class="codicon codicon-add"></span>
          </button>
        </div>
      </div>
    </div>
  `;
}

/**
 * Generate Project section HTML
 */
function getProjectSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const project = config.project ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      ${getNumberInputHtml(
        "project.number",
        "Project Number",
        "GitHub Project board number (required for issue tracking)",
        project.number,
        1,
        undefined,
        undefined,
        disabled,
        g("project.number"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "project.auto_dates",
        "Auto-set Dates",
        "Automatically update start/end dates on status changes",
        project.auto_dates ?? true,
        disabled,
        g("project.auto_dates"),
        showBadges,
        options
      )}
      ${getTextInputHtml(
        "project.sprint.current",
        "Current Sprint",
        "Current sprint name or identifier",
        project.sprint?.current ?? "",
        "Sprint 1",
        disabled,
        g("project.sprint.current"),
        showBadges,
        options
      )}
      ${getNumberInputHtml(
        "project.sprint.duration_weeks",
        "Sprint Duration",
        "Sprint duration in weeks",
        project.sprint?.duration_weeks,
        1,
        8,
        undefined,
        disabled,
        g("project.sprint.duration_weeks"),
        showBadges,
        options
      )}
    </div>
  `;
}

/**
 * Generate Pull Request section HTML
 */
function getPullRequestSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const pr = config.pull_request ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      ${getSelectHtml(
        "pull_request.merge_strategy",
        "Merge Strategy",
        "How PRs are merged into the base branch",
        pr.merge_strategy ?? "squash",
        [
          { value: "squash", label: "Squash and merge" },
          { value: "merge", label: "Create a merge commit" },
          { value: "rebase", label: "Rebase and merge" },
        ],
        disabled,
        g("pull_request.merge_strategy"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "pull_request.delete_branch",
        "Delete Branch",
        "Delete source branch after merge",
        pr.delete_branch ?? true,
        disabled,
        g("pull_request.delete_branch"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "pull_request.auto_merge",
        "Auto Merge",
        "Automatically merge when checks pass",
        pr.auto_merge ?? false,
        disabled,
        g("pull_request.auto_merge"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "pull_request.auto_merge_epic",
        "Auto Merge Epic PR",
        "Auto-merge epic→main PR when all sub-issues complete",
        pr.auto_merge_epic ?? true,
        disabled,
        g("pull_request.auto_merge_epic"),
        showBadges,
        options
      )}
      ${getSelectHtml(
        "pull_request.epic_merge_strategy",
        "Epic Merge Strategy",
        "How epic branches are merged into main (merge preserves sub-issue commits)",
        pr.epic_merge_strategy ?? "merge",
        [
          { value: "merge", label: "Create a merge commit (recommended)" },
          { value: "squash", label: "Squash and merge" },
          { value: "rebase", label: "Rebase and merge" },
        ],
        disabled,
        g("pull_request.epic_merge_strategy"),
        showBadges,
        options
      )}
      ${getListInputHtml(
        "pull_request.reviewers",
        "Default Reviewers",
        "GitHub usernames to request reviews from",
        pr.reviewers ?? [],
        "username",
        disabled,
        g("pull_request.reviewers"),
        showBadges,
        options
      )}
    </div>
  `;
}

/**
 * Generate Branch section HTML
 */
function getBranchSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const branch = config.branch ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      ${getTextInputHtml(
        "branch.base",
        "Base Branch",
        "Default target branch for pull requests",
        branch.base ?? "main",
        "main",
        disabled,
        g("branch.base"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "branch.suggestions",
        "Branch Suggestions",
        "Suggest branch names based on issue",
        branch.suggestions ?? true,
        disabled,
        g("branch.suggestions"),
        showBadges,
        options
      )}
      ${getListInputHtml(
        "branch.protected",
        "Protected Branches",
        "Branches that should not receive direct pushes",
        branch.protected ?? ["main", "master"],
        "branch-name",
        disabled,
        g("branch.protected"),
        showBadges,
        options
      )}
    </div>
  `;
}

/**
 * Generate Issue section HTML
 */
function getIssueSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const issue = config.issue ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      ${getSelectHtml(
        "issue.default_status",
        "Default Project Status",
        "Initial project status for newly created issues",
        issue.default_status ?? "backlog",
        [
          { value: "backlog", label: "Backlog" },
          { value: "ready", label: "Ready" },
        ],
        disabled,
        g("issue.default_status"),
        showBadges,
        options
      )}
    </div>
  `;
}

/**
 * Render the Per-Stage Adapter Routing matrix.
 *
 * Six stage rows, each with an adapter dropdown bound to
 * `pipeline.stage_adapters.<stage>`, an auth-status indicator (driven later
 * by a `validate-stage-adapter` action result), and a "Reset to global"
 * button that reuses the existing `reset-setting` message type.
 *
 * Below the matrix, a read-only "Mode-aware Resolution Preview" table
 * shows the `(adapter, model)` pair each stage will resolve to under the
 * currently active `performance_mode` (computed by `SettingsPanel`).
 *
 * @see Issue #3225 - VSCode settings UI per-stage adapter selector
 */
function getStageAdapterMatrixHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options: SettingsHtmlOptions
): string {
  const stageAdapters = (config.pipeline?.stage_adapters ?? {}) as Record<string, string>;
  const stageModels = (config.pipeline?.stage_models ?? {}) as Record<string, string>;
  const adapterOptions = STAGE_ADAPTER_OPTIONS.map((o) => ({ ...o }));

  const rowsHtml = STAGE_ADAPTER_STAGES.map((stage) => {
    const path = `pipeline.stage_adapters.${stage}`;
    const value = stageAdapters[stage] ?? "";
    const optionsHtml = adapterOptions
      .map(
        (opt) =>
          `<option value="${escapeHtml(opt.value)}" ${value === opt.value ? "selected" : ""}>${escapeHtml(opt.label)}</option>`
      )
      .join("");

    // Per-stage model override (#4030), bound to pipeline.stage_models.<stage>.
    const modelPath = `pipeline.stage_models.${stage}`;
    const modelValue = stageModels[stage] ?? "";
    const modelOptionsHtml = STAGE_MODEL_OPTIONS.map(
      (opt) =>
        `<option value="${escapeHtml(opt.value)}" ${modelValue === opt.value ? "selected" : ""}>${escapeHtml(opt.label)}</option>`
    ).join("");
    const modelSource = getSourceForPath(modelPath, sources);
    const modelTeamTierDisabled = showBadges && modelSource === "project";
    const modelBadgeHtml =
      showBadges && modelSource && modelSource !== "default" ? getUxTierBadgeHtml(modelSource) : "";
    const modelEditBtnHtml = modelTeamTierDisabled ? getEditTeamConfigBtnHtml() : "";
    const stageLabel = STAGE_LABELS[stage] ?? stage;

    const source = getSourceForPath(path, sources);
    const badgeHtml =
      showBadges && source && source !== "default" ? getUxTierBadgeHtml(source) : "";
    const modifiedClass = source && source !== "default" ? "setting-modified" : "";
    const teamTierDisabled = showBadges && source === "project";
    const editBtnHtml = teamTierDisabled ? getEditTeamConfigBtnHtml() : "";

    return `
      <div class="setting-row stage-adapter-row ${modifiedClass}" data-stage="${escapeHtml(stage)}">
        <div class="setting-info">
          <label for="${escapeHtml(path)}" class="setting-label">${escapeHtml(stageLabel)} ${badgeHtml}${editBtnHtml}</label>
        </div>
        <div class="setting-control stage-adapter-control">
          <select id="${escapeHtml(path)}"
                  class="select-input stage-adapter-select"
                  data-path="${escapeHtml(path)}"
                  data-stage="${escapeHtml(stage)}"
                  aria-label="Execution adapter for ${escapeHtml(stageLabel)}"
                  ${disabled || teamTierDisabled ? "disabled" : ""}>
            ${optionsHtml}
          </select>
          <select id="${escapeHtml(modelPath)}"
                  class="select-input stage-model-select"
                  data-path="${escapeHtml(modelPath)}"
                  data-stage="${escapeHtml(stage)}"
                  aria-label="Model tier for ${escapeHtml(stageLabel)}"
                  title="Model tier for this stage — resolved per-adapter (#4021). Empty = global default."
                  ${disabled || modelTeamTierDisabled ? "disabled" : ""}>
            ${modelOptionsHtml}
          </select>
          ${modelBadgeHtml}${modelEditBtnHtml}
          <span class="auth-indicator"
                data-stage="${escapeHtml(stage)}"
                data-status="unknown"
                title="Auth status (probed asynchronously when an adapter is selected)">
            <span class="codicon codicon-circle-outline"></span>
          </span>
          <button type="button"
                  class="btn reset-stage-adapter-btn"
                  data-path="${escapeHtml(path)}"
                  data-stage="${escapeHtml(stage)}"
                  title="Clear this override and use the global Execution Adapter"
                  ${disabled || value === "" ? "disabled" : ""}>
            Reset
          </button>
        </div>
      </div>
    `;
  }).join("");

  const previewRows = options.stageAdapterPreview ?? [];
  const previewModeLabel = options.performanceMode
    ? ` (${escapeHtml(options.performanceMode)})`
    : "";
  const previewHtml =
    previewRows.length === 0
      ? ""
      : `
      <div class="subsection stage-adapter-preview">
        <h4 class="subsection-title">Mode-aware Resolution Preview${previewModeLabel}</h4>
        <p class="section-note">
          Resolved <code>(adapter, model)</code> per stage under the active performance mode.
          Auto-router, env, and YAML overrides are reflected here.
        </p>
        <table class="stage-adapter-preview-table">
          <thead>
            <tr>
              <th>Stage</th>
              <th>Adapter</th>
              <th>Source</th>
              <th>Model</th>
            </tr>
          </thead>
          <tbody>
            ${previewRows
              .map(
                (row) => `
              <tr data-stage="${escapeHtml(row.stage)}">
                <td>${escapeHtml(STAGE_LABELS[row.stage] ?? row.stage)}</td>
                <td>${escapeHtml(row.adapter)}</td>
                <td>${escapeHtml(row.source)}</td>
                <td>${escapeHtml(row.model)}${row.modelMismatch ? ' <span class="preview-mismatch" title="Adapter has no mapping for this tier — falls back to its configured default">(mismatch)</span>' : ""}</td>
              </tr>
            `
              )
              .join("")}
          </tbody>
        </table>
      </div>
    `;

  return `
    <div class="subsection stage-adapter-matrix" id="stage-adapter-matrix">
      <h4 class="subsection-title">Per-Stage Adapter &amp; Model Routing</h4>
      <p class="section-note">
        Override the global Execution Adapter and model tier for individual
        pipeline stages. Empty adapter rows fall back to <code>ui.core.adapter</code>;
        empty model rows fall back to the global default model. Model tiers are
        canonical and resolved per-adapter (#4021) — the resolved
        <code>(adapter, model)</code> is shown in the preview below.
        The runtime resolver honours these overrides automatically; see
        <code>resolveStageAdapter()</code> for full precedence.
      </p>
      ${rowsHtml}
      ${previewHtml}
    </div>
  `;
}

/**
 * Generate Core section HTML
 */
function getCoreSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options: SettingsHtmlOptions
): string {
  const core = config.ui?.core ?? {};
  const adapter = core.adapter ?? "claude";
  const lmStudio = config.lm_studio ?? {};
  const ollama = config.ollama ?? {};
  const g = (path: string) => getSourceForPath(path, sources);
  const isClaudeAdapter = adapter === "claude";
  const isCodexAdapter = adapter === "codex";
  const isLmStudioAdapter = adapter === "lm-studio";
  const isOllamaAdapter = adapter === "ollama";
  const codex = core.codex ?? {};
  const lmStudioModelOptions = getLmStudioModelOptions(
    lmStudio.model ?? "",
    options.lmStudioModels ?? []
  );
  const codexModelOptions = getAdapterModelOptions(
    codex.model ?? CODEX_DEFAULT_BASE_MODEL,
    Array.from(new Set([...(options.codexModels ?? []), ...(codex.model ? [codex.model] : [])]))
  );

  return `
    <div class="section-content">
      ${getSelectHtml(
        "ui.core.adapter",
        "Execution Adapter",
        "Select pipeline execution backend",
        core.adapter ?? "claude",
        [
          { value: "claude", label: "Claude" },
          { value: "codex", label: "Codex" },
          { value: "gemini", label: "Gemini CLI" },
          { value: "gemini-sdk", label: "Gemini SDK" },
          { value: "lm-studio", label: "LM Studio" },
          { value: "ollama", label: "Ollama" },
          { value: "copilot", label: "GitHub Copilot CLI" },
        ],
        disabled,
        g("ui.core.adapter"),
        showBadges,
        options
      )}
      ${getStageAdapterMatrixHtml(config, disabled, sources, showBadges, options)}
      <div id="core-claude-settings" ${isClaudeAdapter ? "" : 'style="display:none;"'}>
        ${getSelectHtml(
          "ui.core.auth_provider",
          "Auth Provider",
          "Authentication provider used by Claude-backed flows",
          core.auth_provider ?? "max",
          [
            { value: "max", label: "Max" },
            { value: "bedrock", label: "Bedrock" },
            { value: "vertex", label: "Vertex" },
          ],
          disabled,
          g("ui.core.auth_provider"),
          showBadges,
          options
        )}
        ${getSelectHtml(
          "ui.core.default_model",
          "Default Model",
          "Default model used when stage-level model is not specified",
          core.default_model ?? "sonnet",
          [
            { value: "sonnet", label: "Sonnet" },
            { value: "opus", label: "Opus" },
            { value: "haiku", label: "Haiku" },
          ],
          disabled,
          g("ui.core.default_model"),
          showBadges,
          options
        )}
      </div>
      <div id="core-codex-settings" ${isCodexAdapter ? "" : 'style="display:none;"'}>
        <p class="section-note">Codex runs through your local <code>codex</code> CLI session. This adapter does not require a direct OpenAI API key and is the path that uses your Codex/ChatGPT local login instead of separate API billing.</p>
        ${getInlineActionBarHtml([{ action: "codex-refresh-models", label: "Refresh Models" }], disabled)}
        ${getSelectHtml(
          "ui.core.codex.model",
          "Codex Model",
          "Models are loaded from your local Codex model catalog. The configured value is passed to the Codex CLI with --model.",
          codex.model ?? CODEX_DEFAULT_BASE_MODEL,
          codexModelOptions,
          disabled,
          g("ui.core.codex.model"),
          showBadges,
          options
        )}
        ${getTextInputHtml(
          "ui.core.codex.cli_command",
          "CLI Command",
          "Codex executable name or absolute path",
          codex.cli_command ?? "codex",
          "codex",
          disabled,
          g("ui.core.codex.cli_command"),
          showBadges,
          options
        )}
        ${getTextInputHtml(
          "ui.core.codex.cli_args",
          "CLI Args Override",
          "Optional raw arguments override. Leave blank to use the adapter defaults.",
          codex.cli_args ?? "",
          "exec --full-auto --sandbox danger-full-access --json",
          disabled,
          g("ui.core.codex.cli_args"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "ui.core.codex.resume_enabled",
          "Enable Session Resume",
          "Use Codex session resume support when the pipeline has a resumable session ID",
          codex.resume_enabled ?? false,
          disabled,
          g("ui.core.codex.resume_enabled"),
          showBadges,
          options
        )}
      </div>
      <div id="core-lm-studio-settings" ${isLmStudioAdapter ? "" : 'style="display:none;"'}>
        <p class="section-note">LM Studio uses one configured backend model for all pipeline stages. Claude stage routing does not apply.</p>
        ${getInlineActionBarHtml(
          [
            { action: "lm-studio-start-server", label: "Start Server" },
            { action: "lm-studio-refresh-models", label: "Refresh Models" },
            {
              action: "lm-studio-load-model",
              label: "Load Model",
              title: "Loads the selected model into LM Studio using the configured context length",
            },
          ],
          disabled
        )}
        ${getLmStudioModelSelectHtml(
          "lm_studio.model",
          "Selected Model",
          "Model identifier from the LM Studio server and used for every stage run",
          lmStudio.model ?? "",
          lmStudioModelOptions,
          disabled,
          g("lm_studio.model"),
          showBadges
        )}
        ${getLmStudioContextLengthHtml(
          lmStudio.context_length ?? 32768,
          disabled,
          g("lm_studio.context_length"),
          showBadges
        )}
        ${getTextInputHtml(
          "lm_studio.base_url",
          "Base URL",
          "LM Studio OpenAI-compatible server base URL",
          lmStudio.base_url ?? "http://127.0.0.1:1234/v1",
          "http://127.0.0.1:1234/v1",
          disabled,
          g("lm_studio.base_url"),
          showBadges,
          options
        )}
        ${getTextInputHtml(
          "lm_studio.api_key",
          "API Key",
          "Auth header value sent to LM Studio; any string is accepted by the local server",
          lmStudio.api_key ?? "lm-studio",
          "lm-studio",
          disabled,
          g("lm_studio.api_key"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "lm_studio.timeout_ms",
          "Timeout (ms)",
          "Request timeout for local inference",
          lmStudio.timeout_ms ?? 180000,
          1000,
          undefined,
          1000,
          disabled,
          g("lm_studio.timeout_ms"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "lm_studio.max_tokens",
          "Max Tokens",
          "Maximum completion tokens requested from LM Studio",
          lmStudio.max_tokens ?? 8192,
          1,
          undefined,
          1,
          disabled,
          g("lm_studio.max_tokens"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "lm_studio.tool_calling",
          "Enable Tool Calling",
          "Allow tool calling for models that reliably support it",
          lmStudio.tool_calling ?? false,
          disabled,
          g("lm_studio.tool_calling"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "lm_studio.stream_options.include_usage",
          "Include Usage In Stream",
          "Request token usage metadata in streamed responses",
          lmStudio.stream_options?.include_usage ?? true,
          disabled,
          g("lm_studio.stream_options.include_usage"),
          showBadges,
          options
        )}
      </div>
      <div id="core-ollama-settings" ${isOllamaAdapter ? "" : 'style="display:none;"'}>
        <p class="section-note">Ollama uses one configured backend model for all pipeline stages. Claude stage routing does not apply.</p>
        ${getTextInputHtml(
          "ollama.model",
          "Selected Model",
          "Model identifier pulled into Ollama and used for every stage run",
          ollama.model ?? "",
          "llama3.1",
          disabled,
          g("ollama.model"),
          showBadges,
          options
        )}
        ${getTextInputHtml(
          "ollama.base_url",
          "Base URL",
          "Ollama OpenAI-compatible server base URL",
          ollama.base_url ?? "http://localhost:11434/v1",
          "http://localhost:11434/v1",
          disabled,
          g("ollama.base_url"),
          showBadges,
          options
        )}
        ${getTextInputHtml(
          "ollama.api_key",
          "API Key",
          "Auth header value sent to Ollama; local installs usually accept the default placeholder",
          ollama.api_key ?? "ollama",
          "ollama",
          disabled,
          g("ollama.api_key"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "ollama.timeout_ms",
          "Timeout (ms)",
          "Request timeout for local inference",
          ollama.timeout_ms ?? 300000,
          1000,
          undefined,
          1000,
          disabled,
          g("ollama.timeout_ms"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "ollama.max_tokens",
          "Max Tokens",
          "Maximum completion tokens requested from Ollama",
          ollama.max_tokens ?? 8192,
          1,
          undefined,
          1,
          disabled,
          g("ollama.max_tokens"),
          showBadges,
          options
        )}
      </div>
      <p id="core-non-claude-note" class="section-note" ${isClaudeAdapter || isCodexAdapter || isLmStudioAdapter || isOllamaAdapter ? 'style="display:none;"' : ""}>Non-Claude adapters use adapter-specific authentication and model settings. Claude-specific auth provider and model controls are hidden.</p>
      ${getTextInputHtml(
        "ui.core.context_path",
        "Context Path",
        "Directory for pipeline context JSON artifacts",
        core.context_path ?? ".nightgauge/pipeline",
        ".nightgauge/pipeline",
        disabled,
        g("ui.core.context_path"),
        showBadges,
        options
      )}
      ${getTextInputHtml(
        "ui.core.plans_path",
        "Plans Path",
        "Directory for generated plan markdown artifacts",
        core.plans_path ?? ".nightgauge/plans",
        ".nightgauge/plans",
        disabled,
        g("ui.core.plans_path"),
        showBadges,
        options
      )}
    </div>
  `;
}

/**
 * Generate Pipeline section HTML
 */
function getPipelineSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const pipeline = config.pipeline ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  const concurrentSlotOptions = [1, 2, 3, 4, 5].map((n) => ({
    value: String(n),
    label: n === 1 ? "1 (sequential)" : String(n),
  }));

  return `
    <div class="section-content">
      ${getSelectHtml(
        "pipeline.max_concurrent",
        "Max Concurrent Slots",
        "Maximum number of pipeline issues running at the same time. Applies to both manually-picked-up issues and the autonomous scheduler. Changes take effect live — no restart required.",
        String(pipeline.max_concurrent ?? 3),
        concurrentSlotOptions,
        disabled,
        g("pipeline.max_concurrent"),
        showBadges,
        options
      )}
      ${getNumberInputHtml(
        "pipeline.ci_timeout",
        "CI Timeout",
        "Maximum time to wait for CI checks (minutes)",
        pipeline.ci_timeout ?? 10,
        1,
        60,
        undefined,
        disabled,
        g("pipeline.ci_timeout"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "pipeline.auto_fix",
        "Auto Fix",
        "Automatically fix linting issues during development",
        pipeline.auto_fix ?? true,
        disabled,
        g("pipeline.auto_fix"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "pipeline.architecture_approval.enabled",
        "Architecture Approval Gate",
        "Pause before feature-dev when a plan is high-impact (production-touching area, major dependency bumps) until a human approves via the approved:architecture label or the approval prompt. Disable for fully-autonomous operation.",
        pipeline.architecture_approval?.enabled ?? true,
        disabled,
        g("pipeline.architecture_approval.enabled"),
        showBadges,
        options
      )}
      <div class="subsection">
        <h4 class="subsection-title">Skip Checks</h4>
        ${getToggleHtml(
          "pipeline.skip_checks.tests",
          "Skip Tests",
          "Skip running tests during pipeline",
          pipeline.skip_checks?.tests ?? false,
          disabled,
          g("pipeline.skip_checks.tests"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "pipeline.skip_checks.lint",
          "Skip Lint",
          "Skip linting during pipeline",
          pipeline.skip_checks?.lint ?? false,
          disabled,
          g("pipeline.skip_checks.lint"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "pipeline.skip_checks.typecheck",
          "Skip Type Check",
          "Skip type checking during pipeline",
          pipeline.skip_checks?.typecheck ?? false,
          disabled,
          g("pipeline.skip_checks.typecheck"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "pipeline.skip_checks.build",
          "Skip Build",
          "Skip build verification during pipeline",
          pipeline.skip_checks?.build ?? false,
          disabled,
          g("pipeline.skip_checks.build"),
          showBadges,
          options
        )}
      </div>
    </div>
  `;
}

/**
 * Generate Commands section HTML
 */
function getCommandsSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const commands = config.commands ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      <p class="section-note">Leave empty to use auto-detected commands from package.json</p>
      ${getTextInputHtml(
        "commands.test",
        "Test Command",
        "Custom command to run tests",
        commands.test ?? "",
        "npm test",
        disabled,
        g("commands.test"),
        showBadges,
        options
      )}
      ${getTextInputHtml(
        "commands.lint",
        "Lint Command",
        "Custom command to run linter",
        commands.lint ?? "",
        "npm run lint",
        disabled,
        g("commands.lint"),
        showBadges,
        options
      )}
      ${getTextInputHtml(
        "commands.typecheck",
        "Type Check Command",
        "Custom command to run type checker",
        commands.typecheck ?? "",
        "npm run typecheck",
        disabled,
        g("commands.typecheck"),
        showBadges,
        options
      )}
      ${getTextInputHtml(
        "commands.format",
        "Format Command",
        "Custom command to format code",
        commands.format ?? "",
        "npm run format",
        disabled,
        g("commands.format"),
        showBadges,
        options
      )}
      ${getTextInputHtml(
        "commands.build",
        "Build Command",
        "Custom command to build the project",
        commands.build ?? "",
        "npm run build",
        disabled,
        g("commands.build"),
        showBadges,
        options
      )}
    </div>
  `;
}

/**
 * Generate Validation section HTML
 */
function getValidationSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const validation = config.validation ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      ${getToggleHtml(
        "validation.require_tests",
        "Require Tests",
        "Require tests to pass before creating PR",
        validation.require_tests ?? true,
        disabled,
        g("validation.require_tests"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "validation.require_changelog",
        "Require Changelog",
        "Require changelog entry for PRs",
        validation.require_changelog ?? false,
        disabled,
        g("validation.require_changelog"),
        showBadges,
        options
      )}
      ${getNumberInputHtml(
        "validation.max_files_changed",
        "Max Files Changed",
        "Maximum number of files in a PR (0 = unlimited)",
        validation.max_files_changed ?? 50,
        0,
        undefined,
        undefined,
        disabled,
        g("validation.max_files_changed"),
        showBadges,
        options
      )}
      ${getNumberInputHtml(
        "validation.max_lines_changed",
        "Max Lines Changed",
        "Maximum lines changed in a PR (0 = unlimited)",
        validation.max_lines_changed ?? 2000,
        0,
        undefined,
        undefined,
        disabled,
        g("validation.max_lines_changed"),
        showBadges,
        options
      )}
    </div>
  `;
}

/**
 * Generate Sanitization section HTML
 */
function getSanitizationSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const sanitization = config.sanitization ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      ${getToggleHtml(
        "sanitization.enabled",
        "Enable Sanitization",
        "Enable prompt injection protection",
        sanitization.enabled ?? true,
        disabled,
        g("sanitization.enabled"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "sanitization.sanitize_input",
        "Sanitize Input",
        "Check user prompts for injection attempts",
        sanitization.sanitize_input ?? false,
        disabled,
        g("sanitization.sanitize_input"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "sanitization.logging",
        "Enable Logging",
        "Log all sanitization events",
        sanitization.logging ?? true,
        disabled,
        g("sanitization.logging"),
        showBadges,
        options
      )}
      ${getToggleHtml(
        "sanitization.warn_only",
        "Warn Only",
        "Log but do not block suspicious content",
        sanitization.warn_only ?? false,
        disabled,
        g("sanitization.warn_only"),
        showBadges,
        options
      )}
      ${getListInputHtml(
        "sanitization.allowlist",
        "Allowlist",
        "Patterns that bypass sanitization",
        sanitization.allowlist ?? [],
        "pattern",
        disabled,
        g("sanitization.allowlist"),
        showBadges,
        options
      )}
      ${getListInputHtml(
        "sanitization.blocklist",
        "Blocklist",
        "Patterns that are always blocked",
        sanitization.blocklist ?? [],
        "pattern",
        disabled,
        g("sanitization.blocklist"),
        showBadges,
        options
      )}
    </div>
  `;
}

/**
 * Generate Enforcement section HTML
 */
function getEnforcementSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const enforcement = config.enforcement ?? {};
  const deps = enforcement.dependencies ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      <div class="subsection">
        <h4 class="subsection-title">Dependencies</h4>
        ${getToggleHtml(
          "enforcement.dependencies.enabled",
          "Enable Dependency Checking",
          "Check issue dependencies before allowing issue-pickup to proceed",
          deps.enabled ?? true,
          disabled,
          g("enforcement.dependencies.enabled"),
          showBadges,
          options
        )}
        ${getSelectHtml(
          "enforcement.dependencies.mode",
          "Enforcement Mode",
          "How dependency violations are handled: warn logs a warning but allows proceed, block prevents pickup, ignore skips the check",
          deps.mode ?? "warn",
          [
            { value: "warn", label: "Warn" },
            { value: "block", label: "Block" },
            { value: "ignore", label: "Ignore" },
          ],
          disabled,
          g("enforcement.dependencies.mode"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "enforcement.dependencies.check_transitive",
          "Check Transitive Dependencies",
          "Also check indirect dependencies (A depends on B, B depends on C)",
          deps.check_transitive ?? false,
          disabled,
          g("enforcement.dependencies.check_transitive"),
          showBadges,
          options
        )}
      </div>
    </div>
  `;
}

/**
 * Generate Routing section HTML
 */
function getRoutingSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const mr = config.model_routing ?? {};
  const thresholds = mr.complexity_thresholds ?? {};
  const g = (path: string) => getSourceForPath(path, sources);
  const activeModel: DefaultModel = (config.ui?.core?.default_model as DefaultModel) ?? "sonnet";
  const effortSupported = modelSupportsEffort(activeModel);

  return `
    <div class="section-content">
      <div class="subsection">
        <h4 class="subsection-title">Model Routing</h4>
        ${getSelectHtml(
          "model_routing.mode",
          "Routing Mode",
          "Manual: use per-stage config only. Automatic: AutoModelSelector determines model for every stage. Hybrid: AutoModelSelector runs but per-stage config overrides take precedence.",
          mr.mode ?? "automatic",
          [
            { value: "manual", label: "Manual" },
            { value: "automatic", label: "Automatic" },
            { value: "hybrid", label: "Hybrid" },
          ],
          disabled,
          g("model_routing.mode"),
          showBadges,
          options
        )}
        ${
          effortSupported
            ? getSelectHtml(
                "model_routing.default_effort",
                "Default Effort",
                `Default Claude effort level applied to all stages for the active model (${activeModel}). Overridden by per-stage stage_efforts entries. Silently ignored for models that do not support --effort.`,
                mr.default_effort ?? "",
                [
                  { value: "", label: "None (use stage defaults)" },
                  { value: "low", label: "Low" },
                  { value: "medium", label: "Medium" },
                  { value: "high", label: "High" },
                ],
                disabled,
                g("model_routing.default_effort"),
                showBadges
              )
            : ""
        }
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Complexity Thresholds</h4>
        ${getNumberInputHtml(
          "model_routing.complexity_thresholds.haiku_max",
          "Haiku Max Complexity",
          "Maximum complexity score for Haiku model. Issues at or below this score use Haiku.",
          thresholds.haiku_max ?? 3,
          0,
          10,
          undefined,
          disabled,
          g("model_routing.complexity_thresholds.haiku_max"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "model_routing.complexity_thresholds.sonnet_max",
          "Sonnet Max Complexity",
          "Maximum complexity score for Sonnet model. Issues above haiku_max but at or below this score use Sonnet. Above this uses Opus.",
          thresholds.sonnet_max ?? 6,
          0,
          10,
          undefined,
          disabled,
          g("model_routing.complexity_thresholds.sonnet_max"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "model_routing.confidence_threshold",
          "Confidence Threshold",
          "Minimum confidence for automatic model selection (0.0-1.0). Below this threshold, the selected model is upgraded one tier.",
          mr.confidence_threshold ?? 0.7,
          0,
          1,
          0.05,
          disabled,
          g("model_routing.confidence_threshold"),
          showBadges,
          options
        )}
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Advanced</h4>
        ${getToggleHtml(
          "model_routing.auto_tune",
          "Auto-Tune Thresholds",
          "Automatically adjust complexity thresholds based on execution history analysis",
          mr.auto_tune ?? false,
          disabled,
          g("model_routing.auto_tune"),
          showBadges,
          options
        )}
      </div>
    </div>
  `;
}

/**
 * Generate Ralph Loop section HTML
 */
function getRalphLoopSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const rl = config.ralph_loop ?? {};
  const limits = rl.limits ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      <div class="subsection">
        <h4 class="subsection-title">General</h4>
        ${getToggleHtml(
          "ralph_loop.enabled",
          "Enabled",
          "Enable Ralph Loop self-healing for build and test failures",
          rl.enabled ?? true,
          disabled,
          g("ralph_loop.enabled"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "ralph_loop.build",
          "Build Auto-Fix",
          "Automatically fix build errors during validation",
          rl.build ?? true,
          disabled,
          g("ralph_loop.build"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "ralph_loop.tests",
          "Test Auto-Fix",
          "Automatically fix failing tests during validation",
          rl.tests ?? true,
          disabled,
          g("ralph_loop.tests"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "ralph_loop.lint",
          "Lint Auto-Fix",
          "Automatically fix lint errors during validation",
          rl.lint ?? false,
          disabled,
          g("ralph_loop.lint"),
          showBadges,
          options
        )}
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Safety Limits</h4>
        ${getNumberInputHtml(
          "ralph_loop.limits.max_iterations",
          "Max Iterations",
          "Maximum retry attempts per error type before escalating to human",
          limits.max_iterations ?? 3,
          1,
          undefined,
          undefined,
          disabled,
          g("ralph_loop.limits.max_iterations"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "ralph_loop.limits.token_budget_per_iteration",
          "Token Budget Per Iteration",
          "Maximum tokens consumed per fix attempt",
          limits.token_budget_per_iteration ?? 2000,
          0,
          undefined,
          undefined,
          disabled,
          g("ralph_loop.limits.token_budget_per_iteration"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "ralph_loop.limits.total_token_budget",
          "Total Token Budget",
          "Maximum total tokens for all Ralph Loop iterations combined",
          limits.total_token_budget ?? 10000,
          0,
          undefined,
          undefined,
          disabled,
          g("ralph_loop.limits.total_token_budget"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "ralph_loop.limits.iteration_timeout_ms",
          "Iteration Timeout (ms)",
          "Maximum time in milliseconds for a single fix attempt",
          limits.iteration_timeout_ms ?? 60000,
          0,
          undefined,
          undefined,
          disabled,
          g("ralph_loop.limits.iteration_timeout_ms"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "ralph_loop.limits.total_timeout_ms",
          "Total Timeout (ms)",
          "Maximum total time in milliseconds for all Ralph Loop iterations",
          limits.total_timeout_ms ?? 300000,
          0,
          undefined,
          undefined,
          disabled,
          g("ralph_loop.limits.total_timeout_ms"),
          showBadges,
          options
        )}
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Abort Patterns</h4>
        ${getListInputHtml(
          "ralph_loop.abort_patterns",
          "Abort Patterns",
          "Error patterns that immediately abort the loop and escalate to human intervention",
          rl.abort_patterns ?? [],
          "Add pattern...",
          disabled,
          g("ralph_loop.abort_patterns"),
          showBadges,
          options
        )}
      </div>
    </div>
  `;
}

/**
 * Generate Automations section HTML
 */
function getAutomationsSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const auto = config.automations ?? {};
  const triggers = auto.triggers ?? [];
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      <div class="subsection">
        <h4 class="subsection-title">General</h4>
        ${getToggleHtml(
          "automations.enabled",
          "Enabled",
          "Enable workflow automations to execute actions on status transitions",
          auto.enabled ?? true,
          disabled,
          g("automations.enabled"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "automations.dry_run",
          "Dry Run",
          "Log automation actions without executing them — useful for testing triggers",
          auto.dry_run ?? false,
          disabled,
          g("automations.dry_run"),
          showBadges,
          options
        )}
        ${getTextInputHtml(
          "automations.log_file",
          "Log File",
          "Path to the JSON Lines audit log for automation executions",
          auto.log_file ?? ".nightgauge/automations.log",
          ".nightgauge/automations.log",
          disabled,
          g("automations.log_file"),
          showBadges,
          options
        )}
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Triggers</h4>
        <div class="setting-row">
          <div class="setting-info">
            <span class="setting-label">Configured Triggers</span>
            <span class="setting-description">
              ${triggers.length} trigger${triggers.length !== 1 ? "s" : ""} configured.
              Triggers define actions (Slack notifications, label management, reviewer
              assignment, scripts) that execute on issue status transitions.
              Edit triggers in <code>.nightgauge/config.yaml</code> under
              <code>automations.triggers</code>.
              See <a href="docs/AUTOMATIONS.md">Automations documentation</a> for
              configuration reference.
            </span>
          </div>
        </div>
      </div>
    </div>
  `;
}

/**
 * Generate Autonomous section HTML
 */
function getAutonomousSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const auto = config.autonomous ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      <div class="subsection">
        <h4 class="subsection-title">Issue Refinement</h4>
        ${getToggleHtml(
          "autonomous.refinement_enabled",
          "Enable Issue Refinement",
          "Enable the autonomous refinement scheduler to continuously improve issue quality.",
          auto.refinement_enabled ?? true,
          disabled,
          g("autonomous.refinement_enabled"),
          showBadges,
          options
        )}
        ${getTextInputHtml(
          "autonomous.refinement_interval",
          "Refinement Interval",
          'Time between refinement scan cycles (e.g. "60s", "5m"). Minimum: 30s.',
          auto.refinement_interval ?? "60s",
          "60s",
          disabled,
          g("autonomous.refinement_interval"),
          showBadges,
          options
        )}
        ${getNumberInputHtml(
          "autonomous.refinement_max_concurrent",
          "Max Concurrent Refinements",
          "Maximum number of issues refined at the same time (1–3).",
          auto.refinement_max_concurrent ?? 1,
          1,
          3,
          undefined,
          disabled,
          g("autonomous.refinement_max_concurrent"),
          showBadges,
          options
        )}
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Auto-Actionable</h4>
        ${getToggleHtml(
          "autonomous.auto_actionable",
          "Auto-Actionable",
          "⚠️ When enabled, AI-refined issues are automatically set to Ready for pipeline processing. When disabled, refined issues go to Backlog for manual review.",
          auto.auto_actionable ?? false,
          disabled,
          g("autonomous.auto_actionable"),
          showBadges,
          options
        )}
      </div>
    </div>
  `;
}

/**
 * Generate Human-in-the-Loop section HTML
 */
function getHumanInTheLoopSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const hitl = config.human_in_the_loop ?? {};
  const trustedStages = hitl.trusted_stages ?? [];
  const g = (path: string) => getSourceForPath(path, sources);

  const allStages = [
    { value: "issue-pickup", label: "Issue Pickup" },
    { value: "feature-planning", label: "Feature Planning" },
    { value: "feature-dev", label: "Feature Dev" },
    { value: "feature-validate", label: "Feature Validate" },
    { value: "pr-create", label: "PR Create" },
    { value: "pr-merge", label: "PR Merge" },
  ];

  const trustedSource = g("human_in_the_loop.trusted_stages");
  const trustedBadgeHtml =
    showBadges && trustedSource
      ? getTierBadgeHtml(trustedSource as ViewTier, { compact: true })
      : "";
  const trustedModifiedClass =
    trustedSource && trustedSource !== "default" ? "setting-modified" : "";

  const checkboxesHtml = allStages
    .map(
      (stage) => `
        <label class="checkbox-group-item">
          <input type="checkbox"
                 class="checkbox-group-input"
                 data-path="human_in_the_loop.trusted_stages"
                 data-value="${stage.value}"
                 ${trustedStages.includes(stage.value as TrustedStage) ? "checked" : ""}
                 ${disabled ? "disabled" : ""}>
          <span class="checkbox-group-label">${escapeHtml(stage.label)}</span>
        </label>`
    )
    .join("");

  return `
    <div class="section-content">
      <div class="subsection">
        <h4 class="subsection-title">Auto-Accept</h4>
        ${getToggleHtml(
          "human_in_the_loop.auto_accept_stages",
          "Auto-Accept Stages",
          "Automatically approve all pipeline stage gates without prompting",
          hitl.auto_accept_stages ?? true,
          disabled,
          g("human_in_the_loop.auto_accept_stages"),
          showBadges,
          options
        )}
        ${getToggleHtml(
          "human_in_the_loop.auto_accept_permissions",
          "Auto-Accept Permissions",
          "Automatically accept Claude tool and file permission prompts",
          hitl.auto_accept_permissions ?? false,
          disabled,
          g("human_in_the_loop.auto_accept_permissions"),
          showBadges,
          options
        )}
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Trusted Stages</h4>
        <div class="setting-row setting-row-list ${trustedModifiedClass}">
          <div class="setting-info">
            <span class="setting-label">Trusted Stages ${trustedBadgeHtml}</span>
            <p class="setting-description">Pipeline stages that skip approval prompts even when auto-accept is off</p>
          </div>
          <div class="setting-control checkbox-group">
            ${checkboxesHtml}
          </div>
        </div>
      </div>
    </div>
  `;
}

/**
 * Generate section content based on section ID
 */
function getPlatformSectionHtml(
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options?: SettingsHtmlOptions
): string {
  const platform = config.platform ?? {};
  const g = (path: string) => getSourceForPath(path, sources);

  return `
    <div class="section-content">
      <div class="subsection">
        <h4 class="subsection-title">License</h4>
        ${getSecretInputHtml(
          "platform.license_key",
          "License Key",
          "Your Nightgauge license key (format: ib_live_xxx). Enables paid tier features. Leave blank to run in community tier.",
          platform.license_key ?? "",
          "ib_live_...",
          disabled,
          g("platform.license_key"),
          showBadges,
          options
        )}
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Connection</h4>
        ${getToggleHtml(
          "platform.enabled",
          "Platform Features",
          "Enable platform API communication. Set to off for fully offline mode.",
          platform.enabled ?? false,
          disabled,
          g("platform.enabled"),
          showBadges,
          options
        )}
        ${getTextInputHtml(
          "platform.api_url",
          "API URL",
          "Platform API endpoint. Change only for dev or self-hosted environments.",
          platform.api_url ?? "",
          "https://api.nightgauge.dev",
          disabled,
          g("platform.api_url"),
          showBadges,
          options
        )}
      </div>
      <div class="subsection">
        <h4 class="subsection-title">Advanced</h4>
        ${getSelectHtml(
          "platform.tier_override",
          "Tier Override",
          "Force a specific tier, bypassing license validation. Leave blank to use automatic tier from your license key.",
          platform.tier_override ?? "",
          [
            { value: "", label: "Auto (use license key)" },
            { value: "community", label: "Community" },
            { value: "pro", label: "Pro" },
            { value: "team", label: "Team" },
            { value: "enterprise", label: "Enterprise" },
          ],
          disabled,
          g("platform.tier_override"),
          showBadges,
          options
        )}
      </div>
    </div>
  `;
}

function getSectionContentHtml(
  sectionId: string,
  config: IncrediConfig,
  disabled: boolean,
  sources: ConfigSourceMap,
  showBadges: boolean,
  options: SettingsHtmlOptions
): string {
  switch (sectionId) {
    case "core":
      return getCoreSectionHtml(config, disabled, sources, showBadges, options);
    case "platform":
      return getPlatformSectionHtml(config, disabled, sources, showBadges, options);
    case "project":
      return getProjectSectionHtml(config, disabled, sources, showBadges, options);
    case "pull_request":
      return getPullRequestSectionHtml(config, disabled, sources, showBadges, options);
    case "branch":
      return getBranchSectionHtml(config, disabled, sources, showBadges, options);
    case "issue":
      return getIssueSectionHtml(config, disabled, sources, showBadges, options);
    case "pipeline":
      return getPipelineSectionHtml(config, disabled, sources, showBadges, options);
    case "commands":
      return getCommandsSectionHtml(config, disabled, sources, showBadges, options);
    case "validation":
      return getValidationSectionHtml(config, disabled, sources, showBadges, options);
    case "sanitization":
      return getSanitizationSectionHtml(config, disabled, sources, showBadges, options);
    case "routing":
      return getRoutingSectionHtml(config, disabled, sources, showBadges, options);
    case "enforcement":
      return getEnforcementSectionHtml(config, disabled, sources, showBadges, options);
    case "ralph_loop":
      return getRalphLoopSectionHtml(config, disabled, sources, showBadges, options);
    case "automations":
      return getAutomationsSectionHtml(config, disabled, sources, showBadges, options);
    case "autonomous":
      return getAutonomousSectionHtml(config, disabled, sources, showBadges, options);
    case "human_in_the_loop":
      return getHumanInTheLoopSectionHtml(config, disabled, sources, showBadges, options);
    case "forges":
      return getForgeInstancesSectionHtml(
        options.forgeInstances ?? [],
        disabled,
        options.defaultForgeId
      );
    default:
      return "";
  }
}

/**
 * Get CSS styles for the settings panel
 */
function getStyles(): string {
  return `
    :root {
      --spacing-xs: 4px;
      --spacing-sm: 8px;
      --spacing-md: 16px;
      --spacing-lg: 24px;
      --spacing-xl: 32px;
      --border-radius: 4px;
    }

    * {
      box-sizing: border-box;
      margin: 0;
      padding: 0;
    }

    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      line-height: 1.5;
      padding: 0;
    }

    .settings-container {
      max-width: 800px;
      margin: 0 auto;
      padding: var(--spacing-md);
    }

    /* Header */
    .settings-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: var(--spacing-md) 0;
      border-bottom: 1px solid var(--vscode-panel-border);
      margin-bottom: var(--spacing-md);
      position: sticky;
      top: 0;
      background: var(--vscode-editor-background);
      z-index: 100;
    }

    .settings-header h1 {
      font-size: 1.3em;
      font-weight: 600;
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .header-actions {
      display: flex;
      gap: var(--spacing-sm);
    }

    .btn {
      display: inline-flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-size: 0.9em;
    }

    .btn:hover:not(:disabled) {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .btn:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .btn-primary {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
    }

    .btn-primary:hover:not(:disabled) {
      background: var(--vscode-button-hoverBackground);
    }

    /* Search */
    .search-container {
      margin-bottom: var(--spacing-md);
    }

    .search-input {
      width: 100%;
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      font-size: 0.95em;
    }

    .search-input:focus {
      outline: none;
      border-color: var(--vscode-focusBorder);
    }

    /* Locked notice */
    .locked-notice {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-inputValidation-warningBackground);
      border: 1px solid var(--vscode-inputValidation-warningBorder);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
      color: var(--vscode-inputValidation-warningForeground);
    }

    .locked-notice.hidden {
      display: none;
    }

    /* Sections */
    .section {
      margin-bottom: var(--spacing-md);
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      overflow: hidden;
    }

    .section.collapsed .section-content {
      display: none;
    }

    .section.collapsed .section-chevron {
      transform: rotate(-90deg);
    }

    .section-header {
      padding: var(--spacing-md);
      cursor: pointer;
      user-select: none;
    }

    .section-header:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .section-title-row {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .section-chevron {
      transition: transform 0.2s;
      color: var(--vscode-descriptionForeground);
    }

    .section-icon {
      color: var(--vscode-textLink-foreground);
    }

    .section-title {
      flex: 1;
      font-size: 1em;
      font-weight: 600;
    }

    .doc-link {
      color: var(--vscode-textLink-foreground);
      opacity: 0.7;
    }

    .doc-link:hover {
      opacity: 1;
    }

    .section-description {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs);
      margin-left: calc(var(--spacing-md) + var(--spacing-sm) + 16px);
    }

    /* Section lock indicator */
    .section-locked {
      opacity: 0.7;
      border-color: var(--vscode-inputValidation-warningBorder);
    }

    .section-lock-icon {
      color: var(--vscode-inputValidation-warningForeground, var(--vscode-descriptionForeground));
      font-size: 14px;
      opacity: 0.8;
    }

    .section-content {
      padding: 0 var(--spacing-md) var(--spacing-md);
      border-top: 1px solid var(--vscode-panel-border);
    }

    .section-note {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      font-style: italic;
      margin-bottom: var(--spacing-md);
      padding-top: var(--spacing-md);
    }

    /* Subsections */
    .subsection {
      margin-top: var(--spacing-md);
      padding-top: var(--spacing-md);
      border-top: 1px solid var(--vscode-panel-border);
    }

    .subsection-title {
      font-size: 0.9em;
      font-weight: 500;
      color: var(--vscode-descriptionForeground);
      margin-bottom: var(--spacing-sm);
    }

    /* Setting rows */
    .setting-row {
      display: flex;
      justify-content: space-between;
      align-items: flex-start;
      padding: var(--spacing-md) 0;
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .setting-row:first-child {
      padding-top: var(--spacing-md);
    }

    .setting-row:last-child {
      border-bottom: none;
    }

    .setting-row-list {
      flex-direction: column;
      gap: var(--spacing-sm);
    }

    .setting-info {
      flex: 1;
      padding-right: var(--spacing-md);
    }

    .setting-label {
      font-weight: 500;
      display: block;
    }

    .setting-description {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs);
    }

    .setting-control {
      flex-shrink: 0;
    }

    .inline-action-bar {
      display: flex;
      gap: var(--spacing-sm);
      margin-bottom: var(--spacing-md);
      flex-wrap: wrap;
    }

    .inline-action-btn {
      padding: 4px 10px;
    }

    .stage-adapter-matrix .setting-row.stage-adapter-row {
      align-items: center;
    }

    .stage-adapter-control {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .auth-indicator {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 18px;
      height: 18px;
      border-radius: 50%;
    }

    .auth-indicator[data-status="ok"] {
      color: var(--vscode-testing-iconPassed, #6a9955);
    }

    .auth-indicator[data-status="error"] {
      color: var(--vscode-testing-iconFailed, #f48771);
    }

    .auth-indicator[data-status="warn"] {
      color: var(--vscode-testing-iconQueued, #cca700);
    }

    .auth-indicator[data-status="pending"] {
      color: var(--vscode-descriptionForeground);
      opacity: 0.7;
    }

    .auth-indicator[data-status="unknown"] {
      color: var(--vscode-descriptionForeground);
      opacity: 0.5;
    }

    .stage-adapter-preview-table {
      width: 100%;
      border-collapse: collapse;
      margin-top: var(--spacing-sm);
      font-size: 0.9em;
    }

    .stage-adapter-preview-table th,
    .stage-adapter-preview-table td {
      text-align: left;
      padding: 4px 8px;
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .stage-adapter-preview-table .preview-mismatch {
      color: var(--vscode-testing-iconQueued, #cca700);
      font-size: 0.85em;
    }

    .context-length-control {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    /* Toggle switch */
    .toggle-input {
      display: none;
    }

    .toggle-switch {
      display: block;
      width: 40px;
      height: 22px;
      background: var(--vscode-input-background);
      border: 1px solid var(--vscode-input-border);
      border-radius: 11px;
      position: relative;
      cursor: pointer;
      transition: background 0.2s;
    }

    .toggle-switch::after {
      content: '';
      position: absolute;
      top: 2px;
      left: 2px;
      width: 16px;
      height: 16px;
      background: var(--vscode-foreground);
      border-radius: 50%;
      transition: transform 0.2s;
    }

    .toggle-input:checked + .toggle-switch {
      background: var(--vscode-button-background);
      border-color: var(--vscode-button-background);
    }

    .toggle-input:checked + .toggle-switch::after {
      transform: translateX(18px);
      background: var(--vscode-button-foreground);
    }

    .toggle-input:disabled + .toggle-switch {
      opacity: 0.5;
      cursor: not-allowed;
    }

    /* Text input */
    .text-input,
    .number-input {
      width: 200px;
      padding: var(--spacing-sm);
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
    }

    .text-input:focus,
    .number-input:focus {
      outline: none;
      border-color: var(--vscode-focusBorder);
    }

    .text-input:disabled,
    .number-input:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .number-input {
      width: 100px;
    }

    .secret-input-wrapper {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
    }

    .secret-toggle-btn {
      background: none;
      border: none;
      cursor: pointer;
      color: var(--vscode-foreground);
      opacity: 0.6;
      padding: 4px;
      display: flex;
      align-items: center;
      border-radius: var(--border-radius);
    }

    .secret-toggle-btn:hover {
      opacity: 1;
      background: var(--vscode-toolbar-hoverBackground);
    }

    /* Select input */
    .select-input {
      width: 200px;
      padding: var(--spacing-sm);
      background: var(--vscode-dropdown-background);
      color: var(--vscode-dropdown-foreground);
      border: 1px solid var(--vscode-dropdown-border);
      border-radius: var(--border-radius);
    }

    .select-input:focus {
      outline: none;
      border-color: var(--vscode-focusBorder);
    }

    .select-input:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    /* List input */
    .list-control {
      width: 100%;
    }

    .list-items {
      display: flex;
      flex-wrap: wrap;
      gap: var(--spacing-xs);
      margin-bottom: var(--spacing-sm);
      min-height: 28px;
    }

    .list-item {
      display: inline-flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      border-radius: var(--border-radius);
      font-size: 0.85em;
    }

    .list-item-remove {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      padding: 2px;
      background: transparent;
      border: none;
      color: inherit;
      cursor: pointer;
      opacity: 0.7;
    }

    .list-item-remove:hover:not(:disabled) {
      opacity: 1;
    }

    .list-item-remove:disabled {
      cursor: not-allowed;
    }

    .list-empty {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
      font-style: italic;
    }

    .list-add {
      display: flex;
      gap: var(--spacing-xs);
    }

    .list-input {
      flex: 1;
      padding: var(--spacing-sm);
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
    }

    .list-input:focus {
      outline: none;
      border-color: var(--vscode-focusBorder);
    }

    .list-input:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .list-add-btn {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      padding: var(--spacing-sm);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
    }

    .list-add-btn:hover:not(:disabled) {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .list-add-btn:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    /* Validation errors */
    .validation-error {
      color: var(--vscode-errorForeground);
      font-size: 0.85em;
      margin-top: var(--spacing-xs);
    }

    .input-error {
      border-color: var(--vscode-inputValidation-errorBorder) !important;
    }

    /* Modified indicator */
    .modified-indicator {
      display: none;
      width: 8px;
      height: 8px;
      background: var(--vscode-editorInfo-foreground);
      border-radius: 50%;
      margin-left: var(--spacing-sm);
    }

    .settings-container.modified .modified-indicator {
      display: inline-block;
    }

    /* Status bar */
    .status-bar {
      position: fixed;
      bottom: 0;
      left: 0;
      right: 0;
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-statusBar-background);
      color: var(--vscode-statusBar-foreground);
      display: flex;
      justify-content: space-between;
      align-items: center;
      font-size: 0.85em;
    }

    .status-bar.hidden {
      display: none;
    }

    /* Codicon styles */
    .codicon {
      font-family: 'codicon';
      font-size: 16px;
      line-height: 1;
    }

    /* Filter highlight */
    .setting-row.filtered-out {
      display: none;
    }

    /* Tier tabs */
    .tier-tabs {
      display: flex;
      gap: var(--spacing-xs);
      margin-bottom: var(--spacing-md);
      padding: var(--spacing-xs);
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      overflow-x: auto;
    }

    .tier-tab {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: var(--spacing-sm) var(--spacing-md);
      background: transparent;
      color: var(--vscode-foreground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-size: 0.9em;
      white-space: nowrap;
      transition: background 0.15s;
    }

    .tier-tab:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .tier-tab.active {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
    }

    .tier-tab.disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .tier-tab .codicon {
      font-size: 14px;
    }

    .tier-tab-label {
      font-weight: 500;
    }

    .tier-tab-file {
      font-size: 0.8em;
      opacity: 0.7;
      margin-left: var(--spacing-xs);
    }

    /* Tier info bar */
    /* Tier info banner — merged view only (Issue #3339) */
    .tier-info-banner {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-inputValidation-infoBackground);
      border: 1px solid var(--vscode-inputValidation-infoBorder);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-sm);
      font-size: 0.85em;
      flex-wrap: wrap;
    }

    .tier-info-banner-icon {
      color: var(--vscode-editorInfo-foreground);
      flex-shrink: 0;
    }

    .tier-info-banner-text {
      flex: 1;
      color: var(--vscode-foreground);
      line-height: 1.5;
    }

    .tier-info-banner-learn-more {
      color: var(--vscode-textLink-foreground);
      text-decoration: none;
      white-space: nowrap;
    }

    .tier-info-banner-learn-more:hover {
      text-decoration: underline;
    }

    /* Tier drift banner (Issue #3645) */
    .drift-banner {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-inputValidation-warningBackground);
      border: 1px solid var(--vscode-inputValidation-warningBorder);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-sm);
      font-size: 0.85em;
      flex-wrap: wrap;
    }

    .drift-banner-icon {
      flex-shrink: 0;
      color: var(--vscode-editorWarning-foreground);
    }

    .drift-banner-msg {
      flex: 1;
      color: var(--vscode-foreground);
    }

    .drift-banner-btn {
      padding: 2px 8px;
      font-size: 0.85em;
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: 1px solid var(--vscode-button-border, transparent);
      border-radius: var(--border-radius);
      cursor: pointer;
      white-space: nowrap;
    }

    .drift-banner-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    /* Tier drift badge for per-key DRIFT annotation (Issue #3645) */
    .drift-badge {
      display: inline-block;
      padding: 1px 5px;
      border-radius: 3px;
      font-size: 0.75em;
      font-weight: 600;
      background: var(--vscode-inputValidation-warningBackground);
      color: var(--vscode-inputValidation-warningForeground, var(--vscode-editorWarning-foreground));
      border: 1px solid var(--vscode-inputValidation-warningBorder);
      cursor: default;
      vertical-align: middle;
      margin-left: 4px;
    }

    .drift-move-btn {
      display: inline-block;
      padding: 1px 6px;
      margin-left: 6px;
      font-size: 0.75em;
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: 1px solid var(--vscode-button-border, transparent);
      border-radius: var(--border-radius);
      cursor: pointer;
      vertical-align: middle;
    }

    .drift-move-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    /* "Edit team config" affordance button (Issue #3339) */
    .edit-team-config-btn {
      display: inline-flex;
      align-items: center;
      gap: 3px;
      margin-left: 6px;
      padding: 1px 6px;
      font-size: 0.75em;
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: 1px solid var(--vscode-button-border, transparent);
      border-radius: var(--border-radius);
      cursor: pointer;
      vertical-align: middle;
    }

    .edit-team-config-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .tier-info {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
      font-size: 0.85em;
    }

    .tier-info-text {
      color: var(--vscode-descriptionForeground);
    }

    .tier-info-actions {
      display: flex;
      gap: var(--spacing-sm);
    }

    .tier-info-action {
      display: inline-flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-size: 0.85em;
    }

    .tier-info-action:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    /* Read-only notice */
    .readonly-notice {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-inputValidation-infoBackground);
      border: 1px solid var(--vscode-inputValidation-infoBorder);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
      color: var(--vscode-inputValidation-infoForeground);
      font-size: 0.85em;
    }

    .readonly-notice.hidden {
      display: none;
    }

    /* Checkbox group */
    .checkbox-group {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-xs);
      width: 100%;
    }

    .checkbox-group-item {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-xs) var(--spacing-sm);
      border-radius: var(--border-radius);
      cursor: pointer;
    }

    .checkbox-group-item:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .checkbox-group-input {
      accent-color: var(--vscode-button-background);
      cursor: pointer;
    }

    .checkbox-group-input:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .checkbox-group-label {
      font-size: 0.9em;
    }
  `;
}

/**
 * Get JavaScript for the settings WebView
 */
function getScript(): string {
  return `
    (function() {
      const vscode = acquireVsCodeApi();
      let modified = false;
      let config = {};

      // Initialize from state
      const state = vscode.getState() || {};
      config = state.config || {};

      // Section toggle
      document.querySelectorAll('.section-header').forEach(header => {
        header.addEventListener('click', (e) => {
          if (e.target.closest('.doc-link')) return;
          const section = header.closest('.section');
          section.classList.toggle('collapsed');
        });
      });

      // Handle input changes
      function updateCoreAdapterVisibility(adapterValue) {
        const claudeSettings = document.getElementById('core-claude-settings');
        const codexSettings = document.getElementById('core-codex-settings');
        const lmStudioSettings = document.getElementById('core-lm-studio-settings');
        const ollamaSettings = document.getElementById('core-ollama-settings');
        const nonClaudeNote = document.getElementById('core-non-claude-note');
        const isClaude = adapterValue === 'claude';
        const isCodex = adapterValue === 'codex';
        const isLmStudio = adapterValue === 'lm-studio';
        const isOllama = adapterValue === 'ollama';

        if (claudeSettings) {
          claudeSettings.style.display = isClaude ? '' : 'none';
        }
        if (codexSettings) {
          codexSettings.style.display = isCodex ? '' : 'none';
        }
        if (lmStudioSettings) {
          lmStudioSettings.style.display = isLmStudio ? '' : 'none';
        }
        if (ollamaSettings) {
          ollamaSettings.style.display = isOllama ? '' : 'none';
        }
        if (nonClaudeNote) {
          nonClaudeNote.style.display = !isClaude && !isCodex && !isLmStudio && !isOllama ? '' : 'none';
        }
      }

      function updateLmStudioModelMetadata() {
        const modelSelect = document.querySelector('[data-path="lm_studio.model"]');
        const metadataEl = document.getElementById('lm-studio-model-capacity');
        const useMaxBtn = document.getElementById('lm-studio-use-max-context');

        if (!modelSelect || !metadataEl) return;

        const selectedOption = modelSelect.options[modelSelect.selectedIndex];
        const maxContext = Number(selectedOption?.dataset?.maxContextLength || '');
        const currentContext = Number(selectedOption?.dataset?.currentContextLength || '');
        const hasMaxContext = Number.isFinite(maxContext) && maxContext > 0;
        const hasCurrentContext = Number.isFinite(currentContext) && currentContext > 0;

        if (hasMaxContext && hasCurrentContext) {
          metadataEl.textContent = 'Detected max context: ' + maxContext.toLocaleString() + ' tokens. Loaded context: ' + currentContext.toLocaleString() + ' tokens.';
        } else if (hasMaxContext) {
          metadataEl.textContent = 'Detected max context: ' + maxContext.toLocaleString() + ' tokens.';
        } else {
          metadataEl.textContent = 'Refresh models to detect this model\\'s maximum supported context length.';
        }

        if (useMaxBtn) {
          useMaxBtn.disabled = !hasMaxContext || modelSelect.disabled;
        }
      }

      function handleChange(element) {
        const path = element.dataset.path;
        if (!path) return;

        let value;
        if (element.type === 'checkbox') {
          value = element.checked;
        } else if (element.type === 'number') {
          value = element.value === '' ? undefined : Number(element.value);
        } else {
          value = element.value;
        }

        setModified(true);
        if (path === 'ui.core.adapter') {
          updateCoreAdapterVisibility(value);
        }
        if (path === 'lm_studio.model') {
          updateLmStudioModelMetadata();
        }
        vscode.postMessage({ type: 'change', path, value });
        if (path.indexOf('pipeline.stage_adapters.') === 0) {
          const stage = path.slice('pipeline.stage_adapters.'.length);
          const indicator = document.querySelector('.auth-indicator[data-stage="' + stage + '"]');
          const resetBtn = document.querySelector('.reset-stage-adapter-btn[data-stage="' + stage + '"]');
          if (indicator) {
            indicator.setAttribute('data-status', value ? 'pending' : 'unknown');
          }
          if (resetBtn) {
            resetBtn.toggleAttribute('disabled', !value);
          }
          if (value) {
            vscode.postMessage({
              type: 'action',
              action: 'validate-stage-adapter',
              payload: { stage: stage, adapter: value },
            });
          }
        }
      }

      // Toggle inputs
      document.querySelectorAll('.toggle-input').forEach(input => {
        input.addEventListener('change', () => handleChange(input));
      });

      // Text inputs
      document.querySelectorAll('.text-input, .number-input').forEach(input => {
        input.addEventListener('change', () => handleChange(input));
        input.addEventListener('input', () => setModified(true));
      });

      // Secret input show/hide toggles
      document.querySelectorAll('.secret-toggle-btn').forEach(btn => {
        btn.addEventListener('click', () => {
          const targetId = btn.dataset.target;
          const input = document.getElementById(targetId);
          if (!input) return;
          const isPassword = input.type === 'password';
          input.type = isPassword ? 'text' : 'password';
          const icon = btn.querySelector('.codicon');
          if (icon) {
            icon.className = isPassword ? 'codicon codicon-eye-closed' : 'codicon codicon-eye';
          }
        });
      });

      // Select inputs
      document.querySelectorAll('.select-input').forEach(select => {
        select.addEventListener('change', () => handleChange(select));
      });

      document.getElementById('lm-studio-use-max-context')?.addEventListener('click', (e) => {
        e.preventDefault();
        e.stopPropagation();

        const modelSelect = document.querySelector('[data-path="lm_studio.model"]');
        const contextInput = document.querySelector('[data-path="lm_studio.context_length"]');
        if (!modelSelect || !contextInput) return;

        const selectedOption = modelSelect.options[modelSelect.selectedIndex];
        const maxContext = Number(selectedOption?.dataset?.maxContextLength || '');
        if (!Number.isFinite(maxContext) || maxContext <= 0) return;

        contextInput.value = String(maxContext);
        handleChange(contextInput);
      });

      // Checkbox group inputs (trusted_stages)
      document.querySelectorAll('.checkbox-group-input').forEach(input => {
        input.addEventListener('change', () => {
          const path = input.dataset.path;
          if (!path) return;
          // Collect all checked values for this path
          const checked = [];
          document.querySelectorAll('.checkbox-group-input[data-path="' + path + '"]').forEach(cb => {
            if (cb.checked) {
              checked.push(cb.dataset.value);
            }
          });
          setModified(true);
          vscode.postMessage({ type: 'change', path, value: checked });
        });
      });

      // List add buttons
      document.querySelectorAll('.list-add-btn').forEach(btn => {
        btn.addEventListener('click', () => {
          const path = btn.dataset.path;
          const input = document.getElementById(path + '-input');
          if (input && input.value.trim()) {
            vscode.postMessage({ type: 'list-add', path, value: input.value.trim() });
            input.value = '';
            setModified(true);
          }
        });
      });

      // List input enter key
      document.querySelectorAll('.list-input').forEach(input => {
        input.addEventListener('keydown', (e) => {
          if (e.key === 'Enter' && input.value.trim()) {
            const path = input.id.replace('-input', '');
            vscode.postMessage({ type: 'list-add', path, value: input.value.trim() });
            input.value = '';
            setModified(true);
          }
        });
      });

      // List remove buttons
      document.querySelectorAll('.list-item-remove').forEach(btn => {
        btn.addEventListener('click', () => {
          const path = btn.dataset.path;
          const index = parseInt(btn.dataset.index, 10);
          vscode.postMessage({ type: 'list-remove', path, index });
          setModified(true);
        });
      });

      // Save button
      document.getElementById('saveBtn')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'save' });
      });

      // Reset button
      document.getElementById('resetBtn')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'reset' });
      });

      // Search/filter
      document.getElementById('searchInput')?.addEventListener('input', (e) => {
        const query = e.target.value.toLowerCase();
        document.querySelectorAll('.setting-row').forEach(row => {
          const label = row.querySelector('.setting-label')?.textContent.toLowerCase() || '';
          const desc = row.querySelector('.setting-description')?.textContent.toLowerCase() || '';
          const matches = !query || label.includes(query) || desc.includes(query);
          row.classList.toggle('filtered-out', !matches);
        });

        // Show sections that have visible settings
        document.querySelectorAll('.section').forEach(section => {
          const hasVisible = section.querySelector('.setting-row:not(.filtered-out)');
          section.style.display = hasVisible ? '' : 'none';
        });
      });

      // Doc links
      document.querySelectorAll('.doc-link').forEach(link => {
        link.addEventListener('click', (e) => {
          e.preventDefault();
          e.stopPropagation();
          const doc = link.dataset.doc;
          vscode.postMessage({ type: 'open-doc', path: doc });
        });
      });

      // Inline action buttons
      document.querySelectorAll('.inline-action-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          e.preventDefault();
          e.stopPropagation();

          const textValue = (path) => {
            const el = document.querySelector('[data-path="' + path + '"]');
            return el ? el.value : undefined;
          };
          const numberValue = (path) => {
            const el = document.querySelector('[data-path="' + path + '"]');
            if (!el || el.value === '') return undefined;
            const parsed = Number(el.value);
            return Number.isFinite(parsed) ? parsed : undefined;
          };

          vscode.postMessage({
            type: 'action',
            action: btn.dataset.action,
            payload: {
              'lm_studio.model': textValue('lm_studio.model'),
              'lm_studio.base_url': textValue('lm_studio.base_url'),
              'lm_studio.api_key': textValue('lm_studio.api_key'),
              'lm_studio.context_length': numberValue('lm_studio.context_length'),
            },
          });
        });
      });

      // Per-stage adapter reset buttons
      document.querySelectorAll('.reset-stage-adapter-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          e.preventDefault();
          e.stopPropagation();
          if (btn.disabled) return;
          const path = btn.dataset.path;
          const stage = btn.dataset.stage;
          if (!path) return;
          vscode.postMessage({ type: 'reset-setting', path: path, toTier: 'default' });
          const select = document.querySelector('.stage-adapter-select[data-stage="' + stage + '"]');
          if (select) {
            select.value = '';
          }
          const indicator = document.querySelector('.auth-indicator[data-stage="' + stage + '"]');
          if (indicator) {
            indicator.setAttribute('data-status', 'unknown');
          }
          btn.disabled = true;
          setModified(true);
        });
      });

      // Tier tabs
      document.querySelectorAll('.tier-tab').forEach(tab => {
        tab.addEventListener('click', () => {
          if (tab.disabled) return;
          const tier = tab.dataset.tier;
          vscode.postMessage({ type: 'switch-tier', tier });
        });
      });

      // Tier info actions (e.g., Open in Editor)
      document.querySelectorAll('.tier-info-action').forEach(btn => {
        btn.addEventListener('click', () => {
          const action = btn.dataset.action;
          const tier = btn.dataset.tier;
          if (action === 'open-tier-file' && tier) {
            vscode.postMessage({ type: 'open-tier-file', tier });
          }
        });
      });

      // "Edit team config" buttons (team-tier read-only controls in merged view)
      document.querySelectorAll('.edit-team-config-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          e.preventDefault();
          e.stopPropagation();
          vscode.postMessage({ type: 'action', action: 'edit-team-config' });
        });
      });

      // "Learn more" link in tier info banner
      document.querySelectorAll('.tier-info-banner-learn-more').forEach(link => {
        link.addEventListener('click', (e) => {
          e.preventDefault();
          const doc = link.dataset.doc;
          if (doc) vscode.postMessage({ type: 'open-doc', path: doc });
        });
      });

      // Set modified state
      function setModified(value) {
        modified = value;
        document.querySelector('.settings-container')?.classList.toggle('modified', value);
        document.getElementById('saveBtn')?.toggleAttribute('disabled', !value);
      }

      // Initialize adapter-specific visibility on initial render.
      const adapterSelect = document.querySelector('[data-path="ui.core.adapter"]');
      if (adapterSelect) {
        updateCoreAdapterVisibility(adapterSelect.value);
      }
      updateLmStudioModelMetadata();

      // Handle messages from extension
      window.addEventListener('message', event => {
        const message = event.data;
        switch (message.type) {
          case 'update':
          case 'update-tiered':
            config = message.config;
            vscode.setState({ config });
            // Full page refresh to show new values
            location.reload();
            break;
          case 'tier-changed':
            // Panel will be re-rendered by extension
            break;
          case 'saved':
            setModified(false);
            break;
          case 'patch-values':
            Object.entries(message.values || {}).forEach(([path, value]) => {
              const element = document.querySelector('[data-path="' + path + '"]');
              if (!element) return;
              if (element.type === 'checkbox') {
                element.checked = Boolean(value);
              } else if (value !== undefined && value !== null) {
                element.value = String(value);
              } else {
                element.value = '';
              }
               if (path === 'ui.core.adapter') {
                 updateCoreAdapterVisibility(String(value || ''));
               }
               if (path === 'lm_studio.model') {
                 updateLmStudioModelMetadata();
               }
             });
            setModified(true);
            break;
          case 'stage-adapter-auth-result': {
            const stage = message.stage;
            const status = message.status; // 'ok' | 'error' | 'unknown'
            const indicator = document.querySelector('.auth-indicator[data-stage="' + stage + '"]');
            if (indicator) {
              indicator.setAttribute('data-status', status || 'unknown');
              if (message.reason) {
                indicator.setAttribute('title', message.reason);
              } else {
                indicator.setAttribute('title', 'Auth status: ' + (status || 'unknown'));
              }
            }
            break;
          }
          case 'error':
            // Show error notification (handled by extension)
            break;
        }
      });
    })();
  `;
}

/**
 * Generate tier tabs HTML
 */
function getTierTabsHtml(currentTier: ViewTier, tierState: TierViewState): string {
  const tabs = TIER_TABS.map((tab) => {
    const isActive = currentTier === tab.id;
    // The Global tab is always selectable: it is an editable machine-tier
    // target, so the user can open it to create ~/.nightgauge/config.yaml
    // even when one does not exist yet (#3997).
    const isDisabled =
      (tab.id === "local" && !tierState.hasLocalConfig) ||
      (tab.id === "env" && tierState.activeEnvVars.length === 0);

    const activeClass = isActive ? "active" : "";
    const disabledClass = isDisabled ? "disabled" : "";
    const disabledAttr = isDisabled ? "disabled" : "";

    return `
      <button class="tier-tab ${activeClass} ${disabledClass}"
              data-tier="${tab.id}"
              ${disabledAttr}
              title="${escapeHtml(tab.description)}">
        <span class="codicon codicon-${tab.icon}"></span>
        <span class="tier-tab-label">${escapeHtml(tab.label)}</span>
        ${tab.filePath ? `<span class="tier-tab-file">${escapeHtml(tab.filePath)}</span>` : ""}
      </button>
    `;
  }).join("");

  return `<div class="tier-tabs">${tabs}</div>`;
}

/**
 * Generate tier info bar HTML
 */
function getTierInfoHtml(currentTier: ViewTier, tierState: TierViewState): string {
  const tierConfig = TIER_TABS.find((t) => t.id === currentTier);
  if (!tierConfig) return "";

  const isReadOnly = !tierConfig.editable;
  const showOpenFile =
    currentTier === "project" || currentTier === "local" || currentTier === "global";

  let infoText = tierConfig.description;
  if (currentTier === "env" && tierState.activeEnvVars.length > 0) {
    infoText = `${tierState.activeEnvVars.length} environment variable(s) active: ${tierState.activeEnvVars.slice(0, 3).join(", ")}${tierState.activeEnvVars.length > 3 ? "..." : ""}`;
  }

  const openFileButton = showOpenFile
    ? `<button class="tier-info-action" data-action="open-tier-file" data-tier="${currentTier}">
         <span class="codicon codicon-go-to-file"></span>
         Open in Editor
       </button>`
    : "";

  return `
    <div class="tier-info">
      <span class="tier-info-text">${escapeHtml(infoText)}</span>
      <div class="tier-info-actions">
        ${openFileButton}
      </div>
    </div>
  `;
}

/**
 * Generate read-only notice HTML
 */
function getReadOnlyNoticeHtml(currentTier: ViewTier): string {
  const tierConfig = TIER_TABS.find((t) => t.id === currentTier);
  if (!tierConfig || tierConfig.editable) return "";

  let message = "This tier is read-only.";
  if (currentTier === "global") {
    message = 'Global settings are read-only here. Use "Open in Editor" to modify.';
  } else if (currentTier === "env") {
    message = "Environment variable overrides cannot be changed from the GUI.";
  } else if (currentTier === "default") {
    message = "Default values cannot be changed. Override them in Project or Local.";
  } else if (currentTier === "merged") {
    message = "Merged view shows effective values. Edit in Project or Local tabs.";
  }

  return `
    <div class="readonly-notice">
      <span class="codicon codicon-info"></span>
      <span>${escapeHtml(message)}</span>
    </div>
  `;
}

/**
 * Generate the full settings HTML
 */
export function getSettingsHtml(
  webview: vscode.Webview,
  config: IncrediConfig,
  lockedSections: Set<string> = new Set(),
  sources: ConfigSourceMap = {},
  tierState?: TierViewState,
  options: SettingsHtmlOptions = {}
): string {
  const nonce = getNonce();

  // Default tier state if not provided
  const effectiveTierState: TierViewState = tierState ?? {
    currentTier: "merged",
    defaultEditTier: "local",
    hasGlobalConfig: false,
    hasLocalConfig: false,
    hasProjectConfig: true,
    activeEnvVars: [],
  };

  // Determine if settings should be disabled
  const currentTierConfig = TIER_TABS.find((t) => t.id === effectiveTierState.currentTier);
  const isTierReadOnly = currentTierConfig ? !currentTierConfig.editable : false;
  const saveDestination =
    effectiveTierState.currentTier === "merged"
      ? ".nightgauge/config.local.yaml"
      : effectiveTierState.currentTier === "local"
        ? ".nightgauge/config.local.yaml"
        : effectiveTierState.currentTier === "global"
          ? "~/.nightgauge/config.yaml"
          : ".nightgauge/config.yaml";

  // Show badges only in merged view
  const showBadges = effectiveTierState.currentTier === "merged";

  // Merge with defaults for display
  const displayConfig = mergeWithDefaults(config);

  // Generate section HTML with per-section lock state
  const sectionsHtml = SETTINGS_SECTIONS.map((section) => {
    const sectionLocked = lockedSections.has(section.id);
    const sectionDisabled = sectionLocked || isTierReadOnly;
    return `
      <div class="section ${sectionLocked ? "section-locked" : ""}" id="section-${section.id}">
        ${getSectionHeaderHtml(section, sectionLocked)}
        ${getSectionContentHtml(
          section.id,
          displayConfig,
          sectionDisabled,
          sources,
          showBadges,
          options
        )}
      </div>
    `;
  }).join("");

  const hasLockedSections = lockedSections.size > 0;

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}'; font-src ${webview.cspSource};">
  <title>Nightgauge Settings</title>
  <style>
    ${getStyles()}
  </style>
</head>
<body>
  <div class="settings-container">
    <header class="settings-header">
      <h1>
        <span class="codicon codicon-settings-gear"></span>
        Nightgauge Settings
        <span class="modified-indicator" title="Unsaved changes"></span>
      </h1>
      <div class="header-actions">
        <button class="btn" id="resetBtn" title="Reset all settings to defaults">
          <span class="codicon codicon-discard"></span>
          Reset
        </button>
        <button class="btn btn-primary" id="saveBtn" disabled title="Save changes to ${saveDestination}">
          <span class="codicon codicon-save"></span>
          Save to ${effectiveTierState.currentTier === "merged" ? "Local" : "Current Tier"}
        </button>
      </div>
    </header>

    <div class="locked-notice ${hasLockedSections ? "" : "hidden"}">
      <span class="codicon codicon-lock"></span>
      <span>Some settings are locked while a pipeline is running</span>
    </div>

    ${getTierTabsHtml(effectiveTierState.currentTier, effectiveTierState)}

    ${getTierInfoBannerHtml(effectiveTierState.currentTier)}

    ${getDriftBannerHtml(options.tierAuditEntries ?? [], options.driftBannerDismissed ?? false)}

    ${getTierInfoHtml(effectiveTierState.currentTier, effectiveTierState)}

    ${getReadOnlyNoticeHtml(effectiveTierState.currentTier)}

    <div class="search-container">
      <input type="text"
             id="searchInput"
             class="search-input"
             placeholder="Search settings...">
    </div>

    ${sectionsHtml}
  </div>

  <script nonce="${nonce}">
    ${getScript()}
  </script>
</body>
</html>`;
}
