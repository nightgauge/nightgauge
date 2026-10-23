/**
 * Work-item provider factory.
 *
 * Extracted from bootstrap/services.ts (#3754) so it can be imported on its own
 * without pulling in the entire extension service graph. Importing all of
 * services.ts just to build one provider was heavy enough that the factory's
 * integration test timed out on the (contended) self-hosted CI runner; a
 * focused module keeps both the runtime wiring and the test lightweight.
 *
 * @see Issue #2571 — configuration and wiring
 * @see Issue #2566 — repo provider implementation
 * @see Issue #2567 — composite provider implementation
 */

import type { WorkItemSourceConfig } from "../config/workItemSourceSettings";
import type { IWorkItemProvider } from "../services/types/WorkItemProvider";
import { ProjectBoardService } from "../services/ProjectBoardService";
import { CompositeAdapter } from "../services/adapters/CompositeAdapter";

/**
 * Instantiate the correct work-item provider based on configuration.
 *
 * Defaults to "github" mode (ProjectBoardService) when mode is not set,
 * preserving existing behavior for all config files without work_item_source.
 *
 * Future modes are stubbed with helpful error messages pointing to the
 * implementing issues so users know when to expect support.
 */
export function createWorkItemProvider(
  config: WorkItemSourceConfig,
  workspaceRoot: string
): IWorkItemProvider {
  const mode = config.mode ?? "github";

  console.debug(`[Nightgauge] createWorkItemProvider: mode=${mode}`);

  switch (mode) {
    case "github":
      return new ProjectBoardService(workspaceRoot);

    case "repo":
      // Future: return new RepoIssueProvider(config.provider_options);
      throw new Error(
        "Repo provider not yet implemented (see issue #2566). " +
          "Set work_item_source.mode to 'github' to use the current provider."
      );

    case "composite":
      return new CompositeAdapter(workspaceRoot, new ProjectBoardService(workspaceRoot));

    default: {
      // TypeScript exhaustiveness check — prevents unhandled enum cases
      const _exhaustive: never = mode;
      return _exhaustive;
    }
  }
}
