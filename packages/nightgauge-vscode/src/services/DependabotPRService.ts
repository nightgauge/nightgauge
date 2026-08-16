import type { IpcClient } from "./IpcClient";
import type { PullRequestDetail } from "./IpcClientBase";
import { isDependabotIssue, getDependabotType } from "../utils/dependabotUtils";
import { tripBreakerIfRateLimited } from "../utils/rateLimitCircuitBreaker";
import type { Logger } from "../utils/logger";

const STALE_DAYS_THRESHOLD = 7;
const CACHE_TTL_MS = 5 * 60 * 1000;

export interface DependabotPR extends PullRequestDetail {
  prType: "security" | "dependency";
  staleDays: number;
  isStale: boolean;
}

export interface DependabotPRData {
  prs: DependabotPR[];
  staleCount: number;
  securityCount: number;
  fetchedAt: string;
}

export class DependabotPRService {
  private cache: DependabotPRData | null = null;
  private cacheAt = 0;

  constructor(
    private readonly ipc: IpcClient,
    private readonly owner: string,
    private readonly repo: string,
    private readonly logger: Logger
  ) {}

  async getData(forceRefresh = false): Promise<DependabotPRData> {
    if (!forceRefresh && this.cache && Date.now() - this.cacheAt < CACHE_TTL_MS) {
      return this.cache;
    }
    try {
      const all = await this.ipc.prList(this.owner, this.repo, { state: "OPEN" });
      const prs: DependabotPR[] = all
        .filter((pr) => isDependabotIssue(pr.labels ?? []))
        .map((pr) => {
          const createdAt = pr.createdAt ? new Date(pr.createdAt) : new Date();
          const staleDays = Math.floor((Date.now() - createdAt.getTime()) / 86_400_000);
          return {
            ...pr,
            prType: (getDependabotType(pr.labels ?? []) ?? "dependency") as
              "security" | "dependency",
            staleDays,
            isStale: staleDays >= STALE_DAYS_THRESHOLD,
          };
        });

      const data: DependabotPRData = {
        prs,
        staleCount: prs.filter((p) => p.isStale).length,
        securityCount: prs.filter((p) => p.prType === "security").length,
        fetchedAt: new Date().toISOString(),
      };
      this.cache = data;
      this.cacheAt = Date.now();
      return data;
    } catch (err) {
      const tripped = await tripBreakerIfRateLimited(err, this.logger, {
        source: "DependabotPRService",
      });
      if (tripped) {
        return this.cache ?? { prs: [], staleCount: 0, securityCount: 0, fetchedAt: "" };
      }
      throw err;
    }
  }

  invalidate(): void {
    this.cacheAt = 0;
  }
}
