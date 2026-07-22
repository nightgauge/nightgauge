import type { WorkspaceRegisterMetadata } from "./AgentRegistrationService";
import type { WorkspaceConfig } from "../types/WorkspaceConfig";

export class WorkspaceRegistrationPayloadBuilder {
  static build(config: WorkspaceConfig | null): WorkspaceRegisterMetadata | undefined {
    if (!config?.workspace?.name) return undefined;
    const slug = this.toSlug(config.workspace.name);
    if (!slug) return undefined;
    return { slug, display_name: config.workspace.name };
  }

  static toSlug(name: string): string {
    return name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 50);
  }
}
