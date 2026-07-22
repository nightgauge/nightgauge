# Feature Assessment: [Feature Name]

**Version:** [Claude Code release version, e.g., 2.1.82]
**Date:** [Assessment date, e.g., 2026-03-24]
**Assessor:** [Your name or "auto" if generated]

---

## Feature Description

[Concise 2–3 sentence explanation of what this feature does, why it was added to Claude Code, and any key caveats or limitations mentioned in the release notes.]

Example template:

> "The `--bare` flag enables Claude Code to be invoked in scripted workflows without interactive prompts. It's designed for CI/CD pipelines and automation tools that need deterministic, headless execution. Limitations: cannot re-enter interactive mode; all input must be provided via stdin or flags."

---

## Dimension Scores

| Dimension                 | Score | Max     | Rationale                                            |
| ------------------------- | ----- | ------- | ---------------------------------------------------- |
| Pipeline Stage Impact     | ?     | 30      | [See stage-by-stage section below]                   |
| Automation Potential      | ?     | 20      | [How much does this reduce manual steps?]            |
| Safety & Reliability      | ?     | 15      | [Does this improve safety? Any new risks?]           |
| Developer Experience      | ?     | 15      | [Does this improve how developers use the pipeline?] |
| Implementation Complexity | ?     | 10      | [How hard is it to integrate?]                       |
| Cross-Repo Applicability  | ?     | 10      | [Is this useful across all repos or niche?]          |
| **TOTAL**                 | **?** | **100** | [Brief summary of overall impact]                    |

---

## Classification: [High/Medium/Low]

**Justification:** [1–2 sentences explaining why this feature falls into this classification. Reference the total score and any override rules that applied.]

Example:

> "**Medium (48 points).** Strong automation potential (18 points) and good cross-repo applicability (10 points) offset by zero stage-specific impact and massive implementation complexity. No override rules apply. Recommendation: backlog for next planning cycle."

---

## Stage-by-Stage Impact

[Break down how this feature affects each of the six pipeline stages, if at all. Score 0–5 for each.]

| Stage                | Impact (0–5) | Notes                                                                            |
| -------------------- | ------------ | -------------------------------------------------------------------------------- |
| **issue-pickup**     | ?            | How does this feature affect issue claiming, branch creation, and context setup? |
| **feature-planning** | ?            | Does it help with docs reading, architecture analysis, PLAN.md creation?         |
| **feature-dev**      | ?            | Does it help with code implementation, building, or file manipulation?           |
| **feature-validate** | ?            | Does it help with testing, validation, screenshot capture, or error detection?   |
| **pr-create**        | ?            | Does it help with PR creation, linking, or description building?                 |
| **pr-merge**         | ?            | Does it help with review monitoring, CI checks, or merging?                      |

**Summary:** [If most stages are 0, explain why. If one stage is 3–5, highlight it.]

---

## Automation Impact

**Current workflow impact:** [How do teams currently handle the workflow this feature addresses? What's manual?]

Example:

> "Currently, feature-validate requires developers to manually run `flutter build ios --simulator` in separate terminal, launch Simulator.app, and perform visual inspection. 100% manual process."

**With this feature:** [How would the workflow change if we adopt this feature?]

Example:

> "With computer use, pipeline could automatically launch Simulator, navigate app screens, capture screenshots, and compare against baseline. Manual effort reduced from 30 min to 5 min (approval + review of results)."

**Net automation gain:** [How many manual steps are eliminated? Is full automation possible?]

Example:

> "Eliminates screenshot capture step (fully automated). Reduces manual navigation to single-button approval. Not fully autonomous without human validation of visual results."

---

## Safety & Reliability Assessment

**Does this improve pipeline safety?** [Yes/No, and why]

Example:

> "Computer use is a safety risk without constraints. Requires denied-app lists and activity audit trails. No, not a safety improvement without mitigation infrastructure."

**Does this introduce new risks?** [Yes/No, and what are they?]

Example:

> "Yes. Unsandboxed desktop access, visual recognition fragility, and prompt injection via screen content. Requires dedicated machine and permission controls."

**Mitigation strategies (if adopting):** [What safeguards should be in place before using this feature in the pipeline?]

Example:

> "Dedicated macOS machine, deny-list for Terminal/Finder/System Preferences, activity audit trail enabled, budget caps on computer use calls, approval gates for initial phases."

---

## Developer Experience Impact

**Current developer experience:** [What do developers currently do? How long does it take? What's frustrating?]

Example:

> "Developers manually type `/nightgauge feature-validate` and wait for test output in terminal. No visibility into which tests passed/failed until completion. 5–10 min per run."

**With this feature:** [How would developer workflow improve?]

Example:

> "Developers could see inline test results in VSCode output panel, with visual diffs for failures. Context-switching to terminal eliminated. 5–10 min run time unchanged, but DX improved."

**Net UX gain:** [Is this a convenience, workflow improvement, or fundamental shift?]

Example:

> "Incremental convenience (saves 1–2 min context-switching per run). Not a fundamental shift to how developers invoke the pipeline."

---

## Implementation Complexity Assessment

**Integration points:** [What parts of the codebase need to change?]

Checklist:

- [ ] Go binary (`cmd/nightgauge/`) — New commands or flags?
- [ ] SDK (`packages/nightgauge-sdk/`) — New modules or changes to existing?
- [ ] VSCode extension (`packages/nightgauge-vscode/`) — UI changes?
- [ ] Skills (`skills/`) — Updates to SKILL.md frontmatter, config, or logic?
- [ ] Config (`docs/CONFIGURATION.md`) — New config options?
- [ ] Tests — New test files or extensive test rewrites?

Example:

> "Requires new Go command `nightgauge feature-validate --with-computer-use`, SDK module `ComputerUseRunner`, VSCode WebView for screenshot display, and new SKILL.md field `computer-use: enabled`. Changes span all 4 major components."

**Estimated effort:** [Quick estimate of integration time]

Example:

> "3–4 days for MVP (enables feature-validate with computer use, no safety infrastructure). Additional 2–3 days for safety guards (deny-list, audit trail, budget capping)."

**Risk factors:** [What could go wrong during integration?]

Example:

> "Computer use is asynchronous and may timeout unpredictably. If integration uses blocking calls, extension will hang. Requires careful async/await handling and timeout logic. Risk: high."

---

## Cross-Repo Applicability

**nightgauge:** [Score 0–5. How applicable is this to the main pipeline repo?]

**acme-mobile:** [Score 0–5. How applicable to the Flutter cross-repo?]

**acme-platform:** [Score 0–5. How applicable to the server-side platform?]

**acme-dashboard:** [Score 0–5. How applicable to the web dashboard?]

**Portability:** [Can a single implementation serve all repos, or does each need custom logic?]

Example:

> "Computer use is environment-specific. iOS Simulator applies only to Flutter. For VSCode extension, testing applies to all repos but requires separate setup. Not portable; each repo needs custom integration."

**Overall score (0–10):** [Based on above]

Example:

> "5 — Useful in 2 out of 4 repos (Flutter, nightgauge). Angular and platform derive no direct benefit."

---

## Risks & Concerns

**Known limitations (from release notes):**

- [Limitation 1]
- [Limitation 2]
- [Limitation 3]

Example:

> - Computer use requires macOS (no Windows/Linux support)
> - Requires a compatible paid plan and a dedicated automation seat
> - Not sandboxed (real desktop access)
> - Requires active display (no headless execution)
> - Slower than programmatic APIs (screenshot-based recognition)

**Potential blockers for pipeline adoption:**

- [Blocker 1]
- [Blocker 2]

Example:

> - Safety infrastructure not yet in place (no budget capping, no denied-app list)
> - Requires dedicated machine (cost and ops overhead)
> - Visual recognition is fragile to UI changes

**Mitigations or dependencies:**

- [Mitigation 1]
- [Mitigation 2]

Example:

> - Can proceed with Phase 1 (manual-only) without infrastructure
> - Phase 2 (supervised) requires dedicated machine and safety config
> - Phase 3 (autonomous) requires 95%+ reliability from Phase 2

---

## Implementation Approach

[Concrete, numbered steps to integrate this feature into the pipeline if we decide to adopt it. Format as a mini-roadmap.]

Example:

1. **Phase 1 (Manual-only, immediate)**
   - Document iOS Simulator testing workflow in `docs/`
   - Developers use computer use manually via Claude Desktop, not through pipeline
   - Capture learnings (success rate, common pain points, time savings)

2. **Phase 2 (Supervised, 2–3 months)**
   - Add `--with-computer-use` flag to `nightgauge feature-validate`
   - Require approval before computer use invocations
   - Implement budget capping (max 50 calls per run)
   - Enable activity audit trail

3. **Phase 3 (Autonomous, 6+ months)**
   - Remove approval gates
   - Add safety infrastructure (denied-app list, visual regression detection)
   - Monitor for regressions; enable rollback if needed

---

## Recommended Decision

| Option                  | Recommendation                                                                             | Timeline                     |
| ----------------------- | ------------------------------------------------------------------------------------------ | ---------------------------- |
| **Adopt Immediately**   | Only if feature is unambiguously high-value (≥70 points) with zero implementation blockers | Now                          |
| **Plan for Next Cycle** | Medium-score features that fit current priorities                                          | 2–4 weeks                    |
| **Backlog / Monitor**   | Medium-score features that can wait; reassess quarterly                                    | Q2/Q3                        |
| **Defer / Skip**        | Low-score features or high-risk features without compelling use case                       | Revisit if priorities change |

**This assessment recommends: [Adopt/Plan/Backlog/Defer]**

---

## Decision Rationale

[2–3 sentences summarizing why the team should prioritize this feature the way recommended. Reference the total score, critical dimensions, and business drivers.]

Example:

> "Computer use fills a genuine gap in iOS Simulator testing, scoring high on stage impact (8) and cross-repo applicability (5), but the massive implementation complexity (1) and safety concerns defer pipeline integration to Phase 2. Recommend manual-only workflows for Q2, with supervised automation in Q3 if Phase 2 proves reliable. Autonomous execution deferred until safety infrastructure is mature."

---

## Follow-Up Actions

- [ ] Share assessment with team for feedback
- [ ] Schedule discussion if decision is "Plan" or higher priority
- [ ] Add to backlog if decision is "Backlog"
- [ ] Set calendar reminder for quarterly reassessment if decision is "Monitor"
- [ ] Link related GitHub issues (e.g., epics for implementation phases)

---

## Appendix: Supporting Details

[Optional section for additional context, links, or reference material]

Example:

> **Release Notes:** https://github.com/anthropics/claude-code/releases/tag/v2.1.82
> **Claude Code Docs:** https://claude.ai/docs/features/computer-use
> **Related Issue:** #2385 (release-watch skill)
> **Implementation Epic (if created):** #2386 (feature assessment engine)

---

**Assessment Version:** 1.0
**Last Updated:** [Date]
**Reviewed By:** [Names, if team review completed]
