import type { IpcClient } from "./IpcClient";
import type { PullRequestDetail } from "./IpcClientBase";
import { isDependabotIssue, getDependabotType } from "../utils/dependabotUtils";
import { tripBreakerIfRateLimited } from "../utils/rateLimitCircuitBreaker";
import type { Logger } from "../utils/logger";

/**
 * Dependabot pull requests, and the standing condition a stale one is (#345).
 *
 * This service used to measure staleness and then throw the measurement away:
 * it computed `staleDays` against the threshold and rendered a COUNT on a tab.
 * A count is not an escalation. Dependency PRs were observed open 40+ days in
 * exactly that state — a working automated fix, in an open PR, the whole time,
 * with the number faithfully displayed on every poll.
 *
 * So the service now emits ESCALATIONS: one per remediation PR that is both
 * past the threshold and linked to an open advisory. An escalation is a
 * statement of a standing condition — sticky identity, material fingerprint,
 * the advisory's own severity — and nothing else. It carries no title, no body
 * and no options ON PURPOSE: `attention.raise` accepts a producer plus the
 * condition's facts and builds the card daemon-side from the same builders the
 * Go scheduler uses, precisely so there is one authoritative description of any
 * card. Describing the card here would fork that.
 *
 * SEVERITY COMES FROM THE ADVISORY, NEVER FROM A LABEL. `prType` below is still
 * label-derived and still drives the tab's security/dependency split, which is
 * fine for a badge; it is deliberately NOT what decides an escalation. A
 * `security` label is a boolean guess that renders a critical RCE and a
 * low-severity ReDoS as the same object, which is the exact fabrication the
 * forge security service (issue #343) exists to replace. A PR with no advisory
 * behind it is a routine version bump and does not escalate however old it is.
 *
 * UNMEASURED IS NOT CLEAN. With no advisory source attached — or when reading
 * advisories fails — the escalation report is empty AND `measured` is false.
 * Nobody looked, and a reader that treats the empty list as "nothing is stale"
 * is reading silence as an answer.
 */

/** Days a Dependabot PR may stay open before it is a standing condition. */
export const STALE_DAYS_THRESHOLD = 7;

const CACHE_TTL_MS = 5 * 60 * 1000;

/** Stable producer id for the stale-remediation condition. Half of the sticky
 * (producer, idempotency_key) identity, so it must never change. */
export const PRODUCER_DEPENDABOT_STALE_REMEDIATION = "dependabot-stale-remediation";

/**
 * The advisory's own severity, lower-cased. Mirrors `forgetypes.AlertSeverity`
 * (internal/forge/types/security.go) value for value, including `unknown` —
 * which is a real value the forge emits, not a parse failure.
 */
export type AdvisorySeverity = "unknown" | "low" | "moderate" | "high" | "critical";

/** Most-severe-first ordering. Mirrors `AlertSeverity.Rank()` in Go. */
const SEVERITY_RANK: Record<AdvisorySeverity, number> = {
  critical: 4,
  high: 3,
  moderate: 2,
  low: 1,
  unknown: 0,
};

/**
 * One open advisory, joined to the remediation PR the forge opened for it.
 *
 * The join comes from the forge (`SecurityAlert.Remediation.PRNumber`), never
 * from parsing a PR title. Title parsing is the guess-from-surface-text pattern
 * this epic replaces.
 */
export interface RemediationAdvisory {
  /** Forge's per-repository alert number — stable for the life of the alert. */
  alertNumber: number;
  /** The remediation PR the forge opened for this alert. */
  prNumber: number;
  /** The advisory's own severity. */
  severity: AdvisorySeverity;
  /** Forge advisory identifier (GHSA on GitHub), when reported. */
  advisoryId?: string;
  /** Vulnerable package, when the advisory names one. */
  packageName?: string;
}

/**
 * Resolves this repository's open advisories that have a remediation PR.
 *
 * Injected rather than called directly: the daemon-side read
 * (`forge.SecurityService.ListOpenAlerts`) has no IPC surface yet, so the
 * service states what it needs and refuses to fabricate the answer when nothing
 * supplies it.
 */
export type AdvisorySource = (owner: string, repo: string) => Promise<RemediationAdvisory[]>;

export interface DependabotPR extends PullRequestDetail {
  prType: "security" | "dependency";
  staleDays: number;
  isStale: boolean;
}

/**
 * A remediation PR that has been open past the threshold: a standing condition,
 * expressed as the facts a card is built FROM rather than as a card.
 */
export interface DependabotEscalation {
  /** Producer id — see PRODUCER_DEPENDABOT_STALE_REMEDIATION. */
  producer: string;
  /** "owner/name". */
  repo: string;
  prNumber: number;
  prUrl: string;
  prTitle: string;
  alertNumber: number;
  /** The ADVISORY's severity. Never inferred from a label. */
  severity: AdvisorySeverity;
  advisoryId?: string;
  packageName?: string;
  staleDays: number;
  /**
   * Sticky identity: one condition per remediation PR per repository. A second
   * poll of the same open PR is the same condition, not a new one.
   */
  idempotencyKey: string;
  /**
   * MATERIAL state. Elapsed days are deliberately bucketed by whole threshold
   * multiples rather than carried raw: a raw day count moves on its own and
   * would re-alert on every poll for a condition that has not changed, while
   * crossing 7 → 14 → 21 days genuinely is a transition worth re-alerting on.
   * Severity is included because an advisory upgraded to critical is a new
   * situation for the same PR.
   */
  fingerprint: string;
}

/** The tab's data: what is open, how much of it is old, how much is labelled
 * security. Unchanged by #345 — a count is what a badge needs, and the badge
 * was never the defect. */
export interface DependabotPRData {
  prs: DependabotPR[];
  staleCount: number;
  securityCount: number;
  fetchedAt: string;
}

/**
 * The escalation surface, kept deliberately separate from DependabotPRData.
 *
 * Counts and standing conditions answer different questions and have different
 * honesty requirements: a count can always be produced from the PR list, while
 * an escalation cannot exist without the advisory. Folding `measured` into the
 * tab's data would let a display path quietly drop it and turn "nobody looked"
 * back into "nothing found".
 */
export interface DependabotEscalationReport {
  /** Stale remediation PRs, most severe first. Empty when unmeasured. */
  escalations: DependabotEscalation[];
  /**
   * False when no advisory source is attached, or when reading advisories
   * failed. An empty `escalations` under `measured: false` means NOBODY LOOKED
   * — it is not evidence that nothing is stale.
   */
  measured: boolean;
  observedAt: string;
}

function emptyData(): DependabotPRData {
  return { prs: [], staleCount: 0, securityCount: 0, fetchedAt: "" };
}

function unmeasuredReport(): DependabotEscalationReport {
  return { escalations: [], measured: false, observedAt: "" };
}

export class DependabotPRService {
  private cache: DependabotPRData | null = null;
  private escalationCache: DependabotEscalationReport | null = null;
  private cacheAt = 0;

  constructor(
    private readonly ipc: IpcClient,
    private readonly owner: string,
    private readonly repo: string,
    private readonly logger: Logger,
    private advisorySource?: AdvisorySource
  ) {}

  /** Attach (or replace) the advisory source. Until one is attached the service
   * reports `measured: false` rather than guessing severity. */
  setAdvisorySource(source: AdvisorySource | undefined): void {
    this.advisorySource = source;
    this.invalidate();
  }

  async getData(forceRefresh = false): Promise<DependabotPRData> {
    try {
      return (await this.observe(forceRefresh)).data;
    } catch (err) {
      if (await this.rateLimited(err)) {
        return this.cache ?? emptyData();
      }
      throw err;
    }
  }

  /**
   * The standing conditions among this repository's open remediation PRs.
   *
   * Shares one observation with getData — the PR list is fetched once per TTL
   * whichever surface asks for it.
   */
  async getEscalations(forceRefresh = false): Promise<DependabotEscalationReport> {
    try {
      return (await this.observe(forceRefresh)).report;
    } catch (err) {
      if (await this.rateLimited(err)) {
        return this.escalationCache ?? unmeasuredReport();
      }
      throw err;
    }
  }

  invalidate(): void {
    this.cacheAt = 0;
  }

  /** One fetch, both surfaces, one TTL. */
  private async observe(
    forceRefresh: boolean
  ): Promise<{ data: DependabotPRData; report: DependabotEscalationReport }> {
    if (
      !forceRefresh &&
      this.cache &&
      this.escalationCache &&
      Date.now() - this.cacheAt < CACHE_TTL_MS
    ) {
      return { data: this.cache, report: this.escalationCache };
    }

    const all = await this.ipc.prList(this.owner, this.repo, { state: "OPEN" });
    const prs: DependabotPR[] = all
      .filter((pr) => isDependabotIssue(pr.labels ?? []))
      .map((pr) => {
        const createdAt = pr.createdAt ? new Date(pr.createdAt) : new Date();
        const staleDays = Math.floor((Date.now() - createdAt.getTime()) / 86_400_000);
        return {
          ...pr,
          prType: (getDependabotType(pr.labels ?? []) ?? "dependency") as "security" | "dependency",
          staleDays,
          isStale: staleDays >= STALE_DAYS_THRESHOLD,
        };
      });

    const observedAt = new Date().toISOString();
    const advisories = await this.readAdvisories();
    const data: DependabotPRData = {
      prs,
      staleCount: prs.filter((p) => p.isStale).length,
      securityCount: prs.filter((p) => p.prType === "security").length,
      fetchedAt: observedAt,
    };
    const report: DependabotEscalationReport = {
      escalations: advisories ? this.escalate(prs, advisories) : [],
      measured: advisories !== null,
      observedAt,
    };

    this.cache = data;
    this.escalationCache = report;
    this.cacheAt = Date.now();
    return { data, report };
  }

  private async rateLimited(err: unknown): Promise<boolean> {
    return tripBreakerIfRateLimited(err, this.logger, { source: "DependabotPRService" });
  }

  /**
   * Reads open advisories, or returns null for "not observed".
   *
   * Null is the whole point of the signature: no source attached and a failed
   * read are both failures to OBSERVE, and neither may present as an empty
   * open set. An empty ARRAY is a positive assertion that no remediation PR has
   * an advisory behind it.
   */
  private async readAdvisories(): Promise<RemediationAdvisory[] | null> {
    if (!this.advisorySource) {
      return null;
    }
    try {
      return await this.advisorySource(this.owner, this.repo);
    } catch (err) {
      this.logger.warn(
        "DependabotPRService: could not read security advisories — staleness left unmeasured",
        { error: err instanceof Error ? err.message : String(err) }
      );
      return null;
    }
  }

  /**
   * Escalates every stale PR that an advisory points at.
   *
   * A PR with no advisory behind it is a routine version bump: it is counted on
   * the tab and it does not escalate, at any age. That is the difference
   * between "old" and "an open vulnerability with a fix already written".
   */
  private escalate(prs: DependabotPR[], advisories: RemediationAdvisory[]): DependabotEscalation[] {
    const worstByPR = new Map<number, RemediationAdvisory>();
    for (const advisory of advisories) {
      const held = worstByPR.get(advisory.prNumber);
      // Dependabot groups several alerts behind one PR. The card must name the
      // worst of them; the alert number breaks ties so the sticky identity
      // cannot flip between polls on equal severities.
      if (
        !held ||
        SEVERITY_RANK[advisory.severity] > SEVERITY_RANK[held.severity] ||
        (SEVERITY_RANK[advisory.severity] === SEVERITY_RANK[held.severity] &&
          advisory.alertNumber < held.alertNumber)
      ) {
        worstByPR.set(advisory.prNumber, advisory);
      }
    }

    const out: DependabotEscalation[] = [];
    for (const pr of prs) {
      if (!pr.isStale) {
        continue;
      }
      const advisory = worstByPR.get(pr.number);
      if (!advisory) {
        continue;
      }
      const repo = pr.repo || `${this.owner}/${this.repo}`;
      out.push({
        producer: PRODUCER_DEPENDABOT_STALE_REMEDIATION,
        repo,
        prNumber: pr.number,
        prUrl: pr.url ?? "",
        prTitle: pr.title ?? "",
        alertNumber: advisory.alertNumber,
        severity: advisory.severity,
        advisoryId: advisory.advisoryId,
        packageName: advisory.packageName,
        staleDays: pr.staleDays,
        idempotencyKey: `${PRODUCER_DEPENDABOT_STALE_REMEDIATION}:${repo}#${pr.number}`,
        fingerprint: `sev:${advisory.severity};pr:${pr.number};over:${Math.floor(
          pr.staleDays / STALE_DAYS_THRESHOLD
        )}`,
      });
    }

    out.sort((a, b) => {
      const rank = SEVERITY_RANK[b.severity] - SEVERITY_RANK[a.severity];
      return rank !== 0 ? rank : a.prNumber - b.prNumber;
    });
    return out;
  }
}
