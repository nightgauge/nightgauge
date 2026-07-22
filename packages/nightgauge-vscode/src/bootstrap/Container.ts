/**
 * Service container — simple typed registry for extension-level service instances.
 *
 * Not a DI framework: no annotations, no reflection, no lifecycle management.
 * Services are registered once at bootstrap and resolved by typed key.
 * Type safety is enforced via TypeScript generics — no stringly-typed lookups.
 *
 * @see Issue #2771 — DI container proof-of-concept (Part 1: GitHub services)
 * @see Issue #2772 — Part 2: Pipeline services (PipelineStateService, IpcClient, PipelineBridge, SkillRunner)
 * @see Issue #2773 — Part 3: Config, telemetry, and view-provider services
 * @see docs/ARCHITECTURE.md — service dependency architecture overview
 */

import type { GitHubService } from "../services/GitHubService";
import type { ProjectBoardService } from "../services/ProjectBoardService";
import type { GitHubAuthService } from "../services/GitHubAuthService";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { IpcClient } from "../services/IpcClient";
import type { PipelineBridge } from "../services/PipelineBridge";
import type { SkillRunner } from "../services/SkillRunner";
import type { ConfigBridge } from "../services/ConfigBridge";
import type { TelemetryStore } from "../services/TelemetryStore";
import type { TelemetryService } from "../services/TelemetryService";
import type { TelemetryConsentService } from "../services/TelemetryConsentService";
import type { TelemetryUploaderService } from "../services/TelemetryUploaderService";
import type { DiscordService } from "../services/DiscordService";
import type { NotificationDispatcher } from "../services/notifications/NotificationDispatcher";
import type { RepositorySettingsService } from "../services/RepositorySettingsService";
import type { OfflineManager } from "../platform";
import type { KnowledgeTreeProvider } from "../views/KnowledgeTreeProvider";
import type { KnowledgeDocumentLinkProvider } from "../views/KnowledgeDocumentLinkProvider";
import type { RepositoriesTreeProvider, QueryResultsTreeProvider } from "../views";
import type { SlotOutputManager } from "../views/SlotOutputManager";
import type { ActiveIssueKnowledgeProvider } from "../providers/ActiveIssueKnowledgeProvider";

/**
 * Registry of services managed by the container.
 *
 * Each entry is optional at the type level. Callers use `.has()` to check
 * presence, or rely on `.get()` throwing for unregistered services.
 *
 * Part 1 (Issue #2771): GitHub services
 * Part 2 (Issue #2772): Pipeline orchestration services
 * Part 3 (Issue #2773): Config, telemetry, and view-provider services
 */
interface ServiceRegistry {
  // Part 1 — GitHub services
  githubService?: GitHubService;
  projectBoardService?: ProjectBoardService;
  gitHubAuthService?: GitHubAuthService;

  // Part 2 — Pipeline orchestration services
  pipelineStateService?: PipelineStateService;
  ipcClient?: IpcClient;
  pipelineBridge?: PipelineBridge;
  skillRunner?: SkillRunner;

  // Part 3 — Config, telemetry, and view-provider services
  configBridge?: ConfigBridge;
  telemetryStore?: TelemetryStore;
  telemetryService?: TelemetryService;
  telemetryConsentService?: TelemetryConsentService;
  telemetryUploaderService?: TelemetryUploaderService;
  offlineManager?: OfflineManager;
  discordService?: DiscordService;
  notifier?: NotificationDispatcher;
  knowledgeTreeProvider?: KnowledgeTreeProvider;
  knowledgeDocumentLinkProvider?: KnowledgeDocumentLinkProvider;
  activeKnowledgeProvider?: ActiveIssueKnowledgeProvider;
  repositoriesTreeProvider?: RepositoriesTreeProvider;
  queryResultsTreeProvider?: QueryResultsTreeProvider;
  slotOutputManager?: SlotOutputManager;
  repositorySettingsService?: RepositorySettingsService;
}

/**
 * Service container — typed registry with register / get / has methods.
 *
 * @example
 * ```typescript
 * const container = new Container();
 * container.register('projectBoardService', new ProjectBoardService(workspaceRoot));
 *
 * // Resolve — throws if not registered
 * const service = container.get('projectBoardService');
 *
 * // Optional — safe check before get
 * if (container.has('gitHubAuthService')) {
 *   const auth = container.get('gitHubAuthService');
 * }
 * ```
 */
export class Container {
  private services: ServiceRegistry = {};

  /**
   * Register a service under the given key.
   *
   * @throws {Error} If the key is already registered (prevents accidental double-init).
   */
  register<K extends keyof ServiceRegistry>(key: K, service: ServiceRegistry[K]): void {
    if (this.services[key] !== undefined) {
      throw new Error(`Service ${String(key)} already registered`);
    }
    this.services[key] = service;
  }

  /**
   * Retrieve a registered service by key.
   *
   * @throws {Error} If the service has not been registered.
   */
  get<K extends keyof ServiceRegistry>(key: K): NonNullable<ServiceRegistry[K]> {
    const service = this.services[key];
    if (service === undefined) {
      throw new Error(`Service ${String(key)} not found in container`);
    }
    return service as NonNullable<ServiceRegistry[K]>;
  }

  /**
   * Check whether a service is registered without throwing.
   *
   * Use for optional services that may not be available in all configurations
   * (e.g., `gitHubAuthService` is only registered when the platform is enabled).
   */
  has<K extends keyof ServiceRegistry>(key: K): boolean {
    return this.services[key] !== undefined;
  }
}
