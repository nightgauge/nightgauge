import { execFile } from "node:child_process";
import { promisify } from "node:util";
import type { ProjectEntry } from "../config/schema";
import { BinaryResolver } from "./BinaryResolver";

const execFileAsync = promisify(execFile);

export interface LinkedProject {
  id: string;
  owner: string;
  number: number;
  title: string;
}

export interface ProjectAssignment extends ProjectEntry {
  source: "team" | "local";
}

export interface RepositoryProjectSettingsState {
  repositories: Array<{ name: string; owner: string; repo: string }>;
  selectedRepository?: string;
  assignments: ProjectAssignment[];
  linkedProjects: LinkedProject[];
  discovery: "idle" | "loading" | "ready" | "unavailable";
  error?: string;
}

export function normalizeProjectAssignments(
  config: { project?: { number?: number }; projects?: ProjectEntry[] },
  source: "team" | "local"
): ProjectAssignment[] {
  if (config.projects?.length) {
    const hasDefault = config.projects.some((entry) => entry.default);
    return config.projects.map((entry, index) => ({
      ...entry,
      default: hasDefault ? entry.default === true : index === 0,
      source,
    }));
  }
  if (config.project?.number) {
    return [
      {
        name: "Default",
        number: config.project.number,
        default: true,
        source,
      },
    ];
  }
  return [];
}

export function withSingleDefault(
  assignments: ProjectAssignment[],
  projectNumber: number
): ProjectAssignment[] {
  return assignments.map((entry) => ({ ...entry, default: entry.number === projectNumber }));
}

export class RepositoryProjectSettingsService {
  async discover(owner: string, repo: string, cwd: string): Promise<LinkedProject[]> {
    const binary = await BinaryResolver.fromVSCode().resolve();
    if (!binary) {
      throw new Error("Nightgauge binary not found");
    }
    const { stdout } = await execFileAsync(
      binary,
      ["workspace", "projects-for-repo", "--owner", owner, "--repo", repo],
      { cwd, timeout: 15_000, maxBuffer: 1024 * 1024 }
    );
    const parsed: unknown = JSON.parse(stdout);
    if (!Array.isArray(parsed)) {
      throw new Error("Project discovery returned an invalid response");
    }
    return parsed.filter(
      (value): value is LinkedProject =>
        typeof value === "object" &&
        value !== null &&
        typeof (value as LinkedProject).number === "number" &&
        typeof (value as LinkedProject).title === "string"
    );
  }
}
