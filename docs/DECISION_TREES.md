# Decision Trees: Mapping User Intents to Skills

When you're thinking "I want to do X," these decision trees help you find the right Nightgauge skill to use. Each tree starts with a user intent and walks through a few key questions to guide you to one or more skills.

**What is this for?**

- You have a development goal but aren't sure which skill to invoke
- You want to see how different skills relate to common workflow patterns
- You're exploring the pipeline and want to understand skill sequencing

**How to use these trees:**

1. Find your intent in the tree at the top of this document
2. Answer the questions as you walk down the flowchart
3. The leaf nodes show you the skill name to invoke
4. Follow the **Quick Answer** box for the most common path
5. See [docs/SKILLS_USAGE_GUIDE.md](SKILLS_USAGE_GUIDE.md) for complete skill documentation

---

## Quick Reference: Intent → Primary Skill

| User Intent                 | Primary Skill                                                                                                   | Documentation                                                                  |
| --------------------------- | --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| Fix a bug                   | `/nightgauge-issue-pickup` or `/nightgauge-feature-dev`                                                         | [SKILLS_USAGE_GUIDE.md](SKILLS_USAGE_GUIDE.md#core-pipeline-skills)            |
| Add a feature               | `/nightgauge-issue-pickup` → `/nightgauge-feature-planning`                                                     | [Core Pipeline](SKILLS_USAGE_GUIDE.md#core-pipeline-skills)                    |
| Check code quality          | `/nightgauge-health-check`                                                                                      | [Quality & Audit](SKILLS_USAGE_GUIDE.md#quality--audit-skills)                 |
| Secure the codebase         | `/nightgauge-security-audit`                                                                                    | [Quality & Audit](SKILLS_USAGE_GUIDE.md#quality--audit-skills)                 |
| Modernize code              | `/nightgauge-modernize-plan`                                                                                    | [Modernization](SKILLS_USAGE_GUIDE.md#modernization--refactoring-skills)       |
| Set up a new repo/workspace | `/nightgauge-repo-init` (per repo) → `/nightgauge-workspace-init` (multi-repo) → `/smart-setup` (AI-ready docs) | [Repository Setup](SKILLS_USAGE_GUIDE.md#repository-setup--operations)         |
| Manage my backlog           | `/nightgauge-backlog-groom` or `/nightgauge-queue`                                                              | [Backlog & Queue](SKILLS_USAGE_GUIDE.md#backlog--queue-management)             |
| Write/update docs           | `/nightgauge-docs-write` or `/update-docs`                                                                      | [Documentation](SKILLS_USAGE_GUIDE.md#documentation)                           |
| Run tests                   | `/nightgauge-test-gen` or `/nightgauge-test-scaffold`                                                           | [Quality & Audit](SKILLS_USAGE_GUIDE.md#quality--audit-skills)                 |
| Monitor pipeline            | `/nightgauge-pipeline-audit` or `/nightgauge-pipeline-health`                                                   | [Pipeline Monitoring](SKILLS_USAGE_GUIDE.md#pipeline-monitoring--optimization) |

---

## Tree 1: "I want to fix a bug"

**Intent:** You've discovered a bug and want to fix it quickly.

```mermaid
flowchart TD
    A["I want to fix a bug"] --> B{Is there a GitHub issue?}
    B -->|No| C["Create issue first<br/>/nightgauge-issue-create"]
    C --> D{Do you have time<br/>to plan?}
    B -->|Yes| E{Is it assigned to me?}
    E -->|No| F["/nightgauge-issue-pickup<br/>to claim it"]
    E -->|Yes| G{Is the fix simple?<br/>Single file, obvious change}
    F --> H{Do you understand<br/>the fix?}
    G -->|Yes| I["/nightgauge-feature-dev<br/>→ /nightgauge-feature-validate<br/>→ /nightgauge-pr-create"]
    G -->|No| H
    H -->|Yes, go straight to coding| I
    H -->|No, need to understand| J["/nightgauge-feature-planning<br/>→ /nightgauge-feature-dev"]
    D -->|Yes| J
    D -->|No| I
    I --> K["/nightgauge-pr-merge"]
    J --> L["/nightgauge-feature-validate"]
    L --> M["/nightgauge-pr-create"]
    M --> K
```

**Quick Answer:**
Most bugs follow this path:

1. `nightgauge-issue-pickup` (claim it)
2. `nightgauge-feature-dev` (implement)
3. `nightgauge-feature-validate` (test)
4. `nightgauge-pr-create` (create PR)
5. `nightgauge-pr-merge` (merge)

If you need to understand the codebase first, insert `nightgauge-feature-planning` after pickup.

---

## Tree 2: "I want to add a feature"

**Intent:** You have a feature request and want to implement it end-to-end.

```mermaid
flowchart TD
    A["I want to add a feature"] --> B{Is there a GitHub issue?}
    B -->|No| C["/nightgauge-issue-create<br/>to create it"]
    B -->|Yes| D{Is it part of<br/>a larger epic?}
    C --> E{Is it assigned<br/>to me?}
    D -->|Yes, this is<br/>an epic sub-issue| F["Use<br/>/nightgauge-assess-epic<br/>to plan batch strategy"]
    D -->|No, standalone<br/>feature| G{Does it span<br/>multiple repos?}
    E -->|No| H["/nightgauge-issue-pickup"]
    E -->|Yes| H
    G -->|Yes| I["/nightgauge-feature-planning<br/>→ note cross-repo<br/>dependencies"]
    G -->|No| J{Do you understand<br/>the approach?}
    F --> K["/nightgauge-issue-pickup<br/>then follow single-<br/>issue path"]
    J -->|Yes| L["/nightgauge-feature-dev"]
    J -->|No| M["/nightgauge-feature-planning"]
    I --> N["/nightgauge-feature-dev"]
    M --> N
    L --> O["/nightgauge-feature-validate"]
    N --> O
    O --> P["/nightgauge-pr-create"]
    P --> Q["/nightgauge-pr-merge"]
```

**Quick Answer:**
Standard feature flow:

1. `nightgauge-issue-pickup` (claim it)
2. `nightgauge-feature-planning` (design with docs-first approach)
3. `nightgauge-feature-dev` (implement)
4. `nightgauge-feature-validate` (test)
5. `nightgauge-pr-create` (create PR)
6. `nightgauge-pr-merge` (merge)

For epics with multiple sub-issues, use `nightgauge-assess-epic` first to understand the batch strategy.

---

## Tree 3: "I want to check code quality"

**Intent:** You want to assess the overall health and quality of the codebase.

```mermaid
flowchart TD
    A["I want to check code quality"] --> B{What aspect<br/>matters most?}
    B -->|Overall health &<br/>comprehensive score| C["/nightgauge-health-check"]
    B -->|Security posture| D["/nightgauge-security-audit"]
    B -->|API & cross-repo| E["/nightgauge-integration-audit"]
    B -->|Product features &<br/>documentation| F["/nightgauge-product-audit"]
    B -->|Test coverage gaps| G["/nightgauge-test-scaffold"]
    C --> H{Want actionable<br/>improvement plan?}
    H -->|Yes| I["/nightgauge-modernize-plan<br/>to create roadmap"]
    H -->|No| J["Review score<br/>and recommendations"]
    D --> K{Find security<br/>issues?}
    K -->|Yes| L["/nightgauge-issue-create<br/>for each issue"]
    K -->|No| M["Security posture<br/>is strong"]
    E --> N{Find API drift?}
    N -->|Yes| O["/nightgauge-docs-write<br/>to fix docs or code"]
    N -->|No| P["APIs are aligned"]
    F --> Q{Find feature<br/>gaps or doc drift?}
    Q -->|Yes| R["/nightgauge-issue-create<br/>to track gaps"]
    Q -->|No| S["Product is healthy"]
    G --> T{Want to create<br/>test-first fixes?}
    T -->|Yes| U["/nightgauge-test-gen<br/>for missing coverage"]
    T -->|No| V["Note coverage gaps<br/>for future work"]
    I --> W["Create issues for<br/>top priorities"]
```

**Quick Answer:**
Quick quality snapshot: `/nightgauge-health-check`

- Gives you 6 dimensions (architecture, testing, docs, security, dependencies, API)
- If you want a detailed plan: add `/nightgauge-modernize-plan`

For targeted audits:

- Security: `/nightgauge-security-audit`
- Cross-repo API alignment: `/nightgauge-integration-audit`
- Test coverage: `/nightgauge-test-scaffold`
- Entire product suite: `/nightgauge-product-audit`

---

## Tree 4: "I want to modernize or refactor code"

**Intent:** You want to update dependencies, improve code quality, or make architectural changes.

```mermaid
flowchart TD
    A["I want to modernize<br/>or refactor code"] --> B{What's the scope?}
    B -->|Just dependencies| C["/nightgauge-dep-modernize"]
    B -->|Major component or<br/>architectural refactor| D{Should I rewrite<br/>or refactor?}
    B -->|Entire codebase| E["/nightgauge-health-check<br/>+ /nightgauge-modernize-plan"]
    C --> F{Find breaking<br/>changes?}
    F -->|Yes| G["/nightgauge-issue-create<br/>to track migration"]
    F -->|No| H["Update dependencies<br/>and test"]
    D -->|Assess first| I["/nightgauge-refactor-rewrite<br/>decision analysis"]
    D -->|I know what to do| J["/nightgauge-test-scaffold<br/>for safety net"]
    I --> K{Recommendation:<br/>Refactor or Rewrite?}
    K -->|Refactor| J
    K -->|Rewrite| L["/nightgauge-feature-planning<br/>for rewrite design"]
    E --> M["/nightgauge-modernize-plan<br/>produces phased<br/>roadmap"]
    M --> N["Create issues for<br/>each phase"]
    J --> O{Ready to<br/>implement?}
    O -->|Yes| P["/nightgauge-feature-dev"]
    O -->|Need a plan| Q["/nightgauge-feature-planning"]
    L --> Q
    Q --> P
    P --> R["/nightgauge-feature-validate"]
    R --> S["/nightgauge-pr-create"]
```

**Quick Answer:**
Full modernization flow:

1. `nightgauge-health-check` (assess current state)
2. `nightgauge-modernize-plan` (get phased roadmap)
3. For each phase: `nightgauge-feature-dev` or `nightgauge-feature-planning` + `nightgauge-feature-dev`

For quick dependency updates:

- `nightgauge-dep-modernize` (handles compatibility + breaking changes)

For architectural refactor:

- `nightgauge-refactor-rewrite` (assess refactor vs rewrite)
- Then `nightgauge-test-scaffold` (create safety net)
- Then implement via feature pipeline

---

## Tree 5: "I want to set up a new repository or workspace"

**Intent:** You have a new repo (or a parent folder grouping several repos)
and want it ready for the Nightgauge pipeline and/or AI-assisted development.

```mermaid
flowchart TD
    A["I want to set up<br/>a new repository or workspace"] --> B{Is this a new repo<br/>for Nightgauge?}
    B -->|Yes| Q{Single repo, or a<br/>multi-repo parent folder?}
    Q -->|Single repo| C["/nightgauge-repo-init<br/>Creates labels, board fields,<br/>config.yaml"]
    Q -->|Multi-repo parent folder| R["Run /nightgauge-repo-init<br/>in EACH member repo"]
    R --> S["/nightgauge-workspace-init<br/>once at the parent folder<br/>(N:1 shared-project manifest)"]
    S --> T["Workspace ready —<br/>shared board renders"]
    B -->|No| D["/smart-setup<br/>Make it AI-ready<br/>AGENTS.md, CLAUDE.md"]
    C --> E["Initialize GitHub Project<br/>board fields"]
    E --> F["/nightgauge-project-sync<br/>Sync existing issues<br/>to board"]
    F --> G["/nightgauge-backlog-preflight<br/>Validate backlog<br/>quality"]
    G --> H{Have epics<br/>to set up?}
    H -->|Yes| I["/nightgauge-issue-create<br/>Create epic + sub-issues"]
    H -->|No| J["Repository ready<br/>for pipeline"]
    I --> K["/nightgauge-epic-validate<br/>Verify linking &<br/>board setup"]
    K --> J
    D --> L{Is this a<br/>Nightgauge repo?}
    L -->|Yes| Q
    L -->|No| M["Repo is now<br/>AI-ready"]
```

**Quick Answer:**
For a new Nightgauge repo:

1. `nightgauge-repo-init` (creates labels & board config)
2. `nightgauge-project-sync` (sync existing issues)
3. `nightgauge-backlog-preflight` (validate quality)
4. Start using the pipeline: `nightgauge-issue-pickup`

For a multi-repo parent folder (several repos sharing one GitHub Project):

1. `nightgauge-repo-init` in **each** member repo first
2. `nightgauge-workspace-init` **once** at the parent folder — scaffolds
   `.vscode/nightgauge-workspace.yaml` so the shared board renders

For any other repo (pipeline or not) that just needs AI-ready docs:

- `smart-setup` (adds AGENTS.md, CLAUDE.md, basic docs)

---

## Tree 6: "I want to manage my backlog"

**Intent:** You want to triage, organize, or prioritize your issue backlog.

```mermaid
flowchart TD
    A["I want to manage<br/>my backlog"] --> B{What's the task?}
    B -->|Triage & hygiene<br/>weekly or monthly| C["/nightgauge-backlog-groom<br/>Find stale, duplicate,<br/>unlinked issues"]
    B -->|Validate issues are<br/>pipeline-ready| D["/nightgauge-backlog-preflight<br/>Check labels, criteria,<br/>quality"]
    B -->|Queue issues for<br/>batch pipeline run| E["/nightgauge-queue<br/>Add, remove, reorder<br/>issues"]
    B -->|Manage epic<br/>sub-issues| F["/nightgauge-assess-epic<br/>Plan batch vs<br/>sequential strategy"]
    C --> G{Fix issues found?}
    G -->|Yes| H["/nightgauge-issue-create<br/>or edit labels"]
    G -->|No| I["Backlog is healthy"]
    D --> J{Issues pass<br/>validation?}
    J -->|No| K["Fix issues before<br/>pipeline"]
    J -->|Yes| L["Issues are ready"]
    E --> M{Add to pipeline?}
    M -->|Yes| N["/nightgauge-queue add"]
    M -->|No| O["Queue updated"]
    F --> P{Single issue or<br/>batch mode?}
    P -->|Single| Q["Run one at a time"]
    P -->|Batch| R["/nightgauge-queue<br/>Add all to queue"]
    R --> S["Run batch pipeline"]
```

**Quick Answer:**
Regular backlog maintenance:

- `nightgauge-backlog-groom` (weekly/monthly)
- `nightgauge-backlog-preflight` (before starting pipeline)
- `nightgauge-queue` (add issues to pipeline)

For epic management:

- `nightgauge-assess-epic` (understand strategy)
- Then queue or run sequentially

---

## Tree 7: "I want to create or update documentation"

**Intent:** You want to write, generate, or update documentation in the codebase.

```mermaid
flowchart TD
    A["I want to create or<br/>update documentation"] --> B{What type<br/>of docs?}
    B -->|API documentation<br/>code comments| C["/nightgauge-doc-gen<br/>Auto-generate JSDoc,<br/>docstrings, README"]
    B -->|Architecture,<br/>narrative docs| D["/nightgauge-docs-write<br/>Write long-form<br/>documentation"]
    B -->|Verify docs match<br/>current code| E["/update-docs<br/>Detect drift &<br/>update"]
    B -->|Monitor Claude Code<br/>feature changes| F["/nightgauge-docs-watch<br/>Check new features<br/>you can use"]
    C --> G{Approve generated<br/>docs?}
    G -->|Yes| H["/nightgauge-feature-dev<br/>to commit docs"]
    G -->|No| I["Edit generated docs<br/>manually"]
    D --> J["Review content<br/>accuracy"]
    J --> K{Ready to<br/>commit?}
    K -->|Yes| L["/nightgauge-feature-dev<br/>or direct commit"]
    K -->|No| M["Refine content"]
    E --> N{Fix issues found?}
    N -->|Yes| O["/nightgauge-feature-dev"]
    N -->|No| P["Docs are current"]
    F --> Q["Review new features<br/>and update docs<br/>if needed"]
```

**Quick Answer:**
Auto-generate API docs: `/nightgauge-doc-gen`
Write narrative docs: `/nightgauge-docs-write`
Check for drift: `/update-docs`

---

## Tree 8: "I want to run tests"

**Intent:** You want to generate tests, find coverage gaps, or validate a feature.

```mermaid
flowchart TD
    A["I want to run tests"] --> B{What's the goal?}
    B -->|Generate new<br/>test suites| C["/nightgauge-test-gen<br/>Parallel subagents<br/>for each file"]
    B -->|Find coverage gaps<br/>before refactor| D["/nightgauge-test-scaffold<br/>Safety net tests"]
    B -->|Validate feature<br/>before PR| E["/nightgauge-feature-validate<br/>Integration/E2E<br/>+ Ralph Loop"]
    C --> F{What framework?}
    F -->|Jest, Vitest| G["Generate JS tests"]
    F -->|Pytest| H["Generate Python tests"]
    F -->|Dotnet test| I["Generate C# tests"]
    F -->|Gradle| J["Generate Java tests"]
    G --> K["Review & commit<br/>tests via feature-dev"]
    H --> K
    I --> K
    J --> K
    D --> L{Approve safety<br/>net tests?}
    L -->|Yes| M["Commit tests"]
    L -->|No| N["Edit tests manually"]
    E --> O{Tests pass?}
    O -->|Yes| P["/nightgauge-pr-create"]
    O -->|No| Q["/nightgauge-feature-dev<br/>to fix"]
    M --> R["Now safe to refactor"]
```

**Quick Answer:**
Generate tests: `/nightgauge-test-gen` (parallel subagents)
Safety net before refactor: `/nightgauge-test-scaffold`
Validate feature: `/nightgauge-feature-validate` (includes Ralph Loop auto-healing)

---

## Tree 9: "I want to monitor pipeline health"

**Intent:** You want to understand how well the pipeline is performing and where to improve.

```mermaid
flowchart TD
    A["I want to monitor<br/>pipeline health"] --> B{What information<br/>do you need?}
    B -->|Quick snapshot<br/>token usage, cost| C["/nightgauge-pipeline-audit<br/>Efficiency insights<br/>5-minute run"]
    B -->|Deep analysis<br/>7 dimensions| D["/nightgauge-pipeline-health<br/>Token economics,<br/>reliability, velocity"]
    B -->|Recent failure<br/>analysis| E["/nightgauge-retro<br/>Root cause,<br/>patterns, lessons"]
    B -->|Continuous<br/>improvement cycle| F["/nightgauge-continuous-improvement<br/>Orchestrates all<br/>self-improvement"]
    C --> G{Find optimization<br/>opportunities?}
    G -->|Yes| H["Note for<br/>next planning"]
    G -->|No| I["Pipeline is efficient"]
    D --> J{Identify issues<br/>to fix?}
    J -->|Yes| K["/nightgauge-issue-create<br/>for each improvement"]
    J -->|No| L["Pipeline is healthy"]
    E --> M{Find pattern<br/>to prevent?}
    M -->|Yes| N["/nightgauge-issue-create<br/>for fix"]
    M -->|No| O["Failure analyzed"]
    F --> P["Monthly review<br/>dogfood + customer<br/>feedback"]
    P --> Q["/nightgauge-issue-create<br/>for improvements"]
```

**Quick Answer:**
Quick check: `/nightgauge-pipeline-audit` (token usage, cost, trends)
Deep dive: `/nightgauge-pipeline-health` (7-dimension analysis)
After a failure: `/nightgauge-retro` (root cause + prevention)
Monthly review: `/nightgauge-continuous-improvement` (dogfood + customer feedback)

---

## Tree 10: "I want to create a pull request"

**Intent:** You have code ready and want to create or validate a pull request.

```mermaid
flowchart TD
    A["I want to create<br/>a pull request"] --> B{What stage are<br/>you at?}
    B -->|About to create PR<br/>from finished code| C["/nightgauge-feature-validate<br/>Final validation<br/>tests pass?"]
    B -->|Already have open PR<br/>to merge| D["/nightgauge-pr-merge"]
    B -->|Unsure if PR is<br/>ready| E["/pr-preflight<br/>Check before<br/>submitting"]
    C --> F{Tests pass?}
    F -->|Yes| G["/nightgauge-pr-create<br/>Create PR with<br/>validation summary"]
    F -->|No| H["/nightgauge-feature-dev<br/>Fix issues"]
    E --> I{Checks pass?}
    I -->|Yes| J["/nightgauge-pr-create"]
    I -->|No| K["Fix issues<br/>before creating PR"]
    D --> L{Waiting for<br/>reviews?}
    L -->|Yes| M["Monitor reviews<br/>and feedback"]
    L -->|No| N["Address feedback<br/>and re-test"]
    G --> O["/nightgauge-pr-merge<br/>Squash and merge"]
    J --> O
    M --> P{Ready to<br/>merge?}
    P -->|Yes| O
    P -->|No| Q["Wait for approval"]
    N --> R["/nightgauge-feature-dev<br/>to fix feedback"]
    R --> G
```

**Quick Answer:**
Standard PR creation:

1. `nightgauge-feature-validate` (final tests)
2. `nightgauge-pr-create` (create PR)
3. `nightgauge-pr-merge` (merge after reviews)

Quick pre-flight check: `/pr-preflight` (validates common issues)

---

## Tree 11: "I want to work with epics"

**Intent:** You want to create, assess, or validate epics and their sub-issues.

```mermaid
flowchart TD
    A["I want to work<br/>with epics"] --> B{What's the task?}
    B -->|Create new epic<br/>from scratch| C["/nightgauge-issue-create<br/>Create epic + link<br/>sub-issues"]
    B -->|Plan epic<br/>processing strategy| D["/nightgauge-assess-epic<br/>Batch vs sequential<br/>analysis"]
    B -->|Validate epic<br/>structure & board| E["/nightgauge-epic-validate<br/>Verify linking,<br/>blockedBy, board"]
    C --> F["Add type:epic<br/>label to parent"]
    F --> G["Create sub-issues<br/>manually or via<br/>issue-create"]
    G --> H["Link sub-issues<br/>to epic"]
    H --> I["/nightgauge-epic-validate<br/>to verify setup"]
    D --> J{Recommend<br/>strategy?}
    J -->|Batch mode| K["/nightgauge-queue<br/>Add all issues"]
    J -->|Sequential mode| L["Run issues one<br/>by one"]
    J -->|Mixed mode| M["Batch + sequential<br/>hybrid"]
    E --> N{Issues linked<br/>correctly?}
    N -->|Yes| O{All issues on<br/>board?}
    N -->|No| P["Fix linking"]
    O -->|Yes| Q{"blockedBy<br/>set?"}
    Q -->|Yes| R["Epic ready<br/>for pipeline"]
    Q -->|No| S["Set dependencies<br/>with blockedBy"]
    O -->|No| T["Add missing<br/>issues to board"]
    P --> I
    S --> R
    T --> I
    K --> U["Run batch<br/>pipeline"]
    L --> V["Run issues<br/>sequentially"]
    M --> W["Hybrid<br/>execution"]
```

**Quick Answer:**
Create an epic:

1. `nightgauge-issue-create` (parent + sub-issues)
2. `nightgauge-epic-validate` (verify structure)
3. `nightgauge-assess-epic` (plan processing)
4. `nightgauge-queue` (add to pipeline)

Validate existing epic: `/nightgauge-epic-validate`
Plan epic processing: `/nightgauge-assess-epic`

---

## Tree 12: "I want to check security"

**Intent:** You want to audit the codebase for security vulnerabilities and issues.

```mermaid
flowchart TD
    A["I want to check<br/>security"] --> B{Scope?}
    B -->|This repo| C["/nightgauge-security-audit<br/>7-dimension scan<br/>OWASP Top 10"]
    B -->|Cross-repo product| D["/nightgauge-product-audit<br/>Includes security<br/>dimension"]
    C --> E{Find vulnerabilities?}
    E -->|Yes| F["/nightgauge-issue-create<br/>Create security<br/>issue"]
    E -->|No| G["Security posture<br/>is strong"]
    D --> H{Find product-wide<br/>issues?}
    H -->|Yes| I["/nightgauge-issue-create"]
    H -->|No| J["Product security<br/>is strong"]
    F --> K{Fix now or<br/>backlog?}
    K -->|Fix now| L["/nightgauge-feature-dev"]
    K -->|Backlog| M["Add to queue"]
    I --> N{Priority?}
    N -->|High| O["/nightgauge-feature-dev"]
    N -->|Low| P["Backlog it"]
```

**Quick Answer:**
Security audit: `/nightgauge-security-audit` (7 dimensions: vulnerabilities, hardcoded secrets, OWASP Top 10, weak crypto, input validation, auth, misconfiguration)

If you find issues:

1. `nightgauge-issue-create` (track them)
2. `nightgauge-feature-dev` (fix)
3. `nightgauge-feature-validate` + `nightgauge-pr-create`

---

## Tree 13: "I want to assess API and integration health"

**Intent:** You want to validate that your APIs and cross-repository integrations are working correctly.

```mermaid
flowchart TD
    A["I want to assess<br/>API & integration<br/>health"] --> B{What to check?}
    B -->|Cross-repo API<br/>alignment| C["/nightgauge-integration-audit<br/>Client API calls<br/>match platform"]
    B -->|Client configuration| D["/nightgauge-config-show<br/>Display effective<br/>configuration"]
    C --> E{Find API drift<br/>or gaps?}
    E -->|Yes| F["/nightgauge-docs-write<br/>to fix docs"]
    E -->|No, APIs aligned| G["Integration is healthy"]
    F --> H{Also fix code?}
    H -->|Yes| I["/nightgauge-feature-dev"]
    H -->|No| J["Docs updated"]
    D --> K["Review config<br/>sources and values"]
    K --> L{Fix config<br/>issue?}
    L -->|Yes| M["/nightgauge-feature-dev"]
    L -->|No| N["Config is correct"]
    I --> O["/nightgauge-pr-create"]
    J --> P{Ready to commit?}
    P -->|Yes| Q["/nightgauge-feature-dev"]
    P -->|No| R["Edit manually"]
```

**Quick Answer:**
Check API alignment: `/nightgauge-integration-audit`
Display effective config: `/nightgauge-config-show`

---

## Choosing Between Similar Skills

Some pairs of skills overlap. Here's how to choose:

### Pipeline Audit vs Pipeline Health

- **Pipeline Audit** (`/nightgauge-pipeline-audit`) — Quick 5-minute check on token usage, cost, and trends
- **Pipeline Health** (`/nightgauge-pipeline-health`) — Deep 7-dimension analysis of reliability, economics, velocity, and self-improvement

**Choose:** Audit for quick insights, Health for comprehensive analysis.

### Test Gen vs Test Scaffold

- **Test Gen** (`/nightgauge-test-gen`) — Generate comprehensive test suites from scratch using parallel subagents
- **Test Scaffold** (`/nightgauge-test-scaffold`) — Create focused safety net tests before refactoring

**Choose:** Gen for building test coverage, Scaffold for safety before refactoring.

### Docs Write vs Doc Gen

- **Docs Write** (`/nightgauge-docs-write`) — Write narrative architecture/design documentation
- **Doc Gen** (`/nightgauge-doc-gen`) — Auto-generate API docs, JSDoc, docstrings

**Choose:** Write for long-form docs, Gen for API/code documentation.

### Update Docs vs Docs Watch

- **Update Docs** (`/update-docs`) — Verify existing documentation matches current code
- **Docs Watch** (`/nightgauge-docs-watch`) — Monitor Claude Code for new features

**Choose:** Update-docs for periodic sync, Docs-watch for following Claude Code updates.

### Backlog Groom vs Backlog Preflight

- **Backlog Groom** (`/nightgauge-backlog-groom`) — Periodic hygiene: find stale, duplicate, unlinked issues
- **Backlog Preflight** (`/nightgauge-backlog-preflight`) — Validate backlog is ready for pipeline processing

**Choose:** Groom weekly/monthly for maintenance, Preflight before starting pipeline.

### Product Audit vs Health Check

- **Health Check** (`/nightgauge-health-check`) — Single repository, 6 dimensions (architecture, testing, docs, security, dependencies, API)
- **Product Audit** (`/nightgauge-product-audit`) — Multiple repositories, 8 dimensions (adds feature parity and CI/CD)

**Choose:** Health Check for single repo, Product Audit for cross-repo product.

### Modernize Plan vs Refactor Rewrite

- **Modernize Plan** (`/nightgauge-modernize-plan`) — Creates phased roadmap consuming assessments
- **Refactor Rewrite** (`/nightgauge-refactor-rewrite`) — Decides whether to refactor or rewrite a component

**Choose:** Refactor-rewrite when assessing a single component, Modernize-plan after running health-check for full roadmap.

---

## Common Workflow Sequences

### "I'm starting a new feature from scratch"

```
1. /nightgauge-issue-pickup [#]         (claim issue)
2. /nightgauge-feature-planning          (design with docs-first)
3. /nightgauge-feature-dev               (implement)
4. /nightgauge-feature-validate          (test & validate)
5. /nightgauge-pr-create                 (create PR)
6. /nightgauge-pr-merge                  (merge)
```

### "I'm fixing a simple bug"

```
1. /nightgauge-issue-pickup [#]         (claim issue)
2. /nightgauge-feature-dev               (implement quick fix)
3. /nightgauge-feature-validate          (test)
4. /nightgauge-pr-create                 (create PR)
5. /nightgauge-pr-merge                  (merge)
```

### "I'm planning a modernization effort"

```
1. /nightgauge-health-check             (assess current state)
2. /nightgauge-modernize-plan           (create phased roadmap)
3. For each phase:
   - /nightgauge-feature-planning        (design phase)
   - /nightgauge-test-scaffold           (create safety net)
   - /nightgauge-feature-dev             (implement)
   - /nightgauge-feature-validate        (test)
   - /nightgauge-pr-create               (create PR)
   - /nightgauge-pr-merge                (merge)
```

### "I'm setting up a new repository"

```
1. /nightgauge-repo-init [options]      (create labels, config)
2. /nightgauge-project-sync             (sync existing issues)
3. /nightgauge-backlog-preflight        (validate quality)
4. /nightgauge-issue-pickup [#]         (start first issue)
```

### "I'm running an epic with multiple sub-issues"

```
1. /nightgauge-assess-epic [#]          (plan strategy)
2. /nightgauge-queue add [#1] [#2] ...  (add to queue)
3. /nightgauge-queue process            (run all sequentially)
4. /nightgauge-epic-validate [#]        (verify completion)
```

### "I'm doing a security audit"

```
1. /nightgauge-security-audit [options] (7-dimension scan)
2. /nightgauge-issue-create [...]       (create issues for findings)
3. For each issue:
   - /nightgauge-feature-dev             (implement fix)
   - /nightgauge-feature-validate        (test)
   - /nightgauge-pr-create               (create PR)
   - /nightgauge-pr-merge                (merge)
```

---

## Next Steps

Once you've identified the right skill(s):

1. **Read the skill documentation** — See [docs/SKILLS_USAGE_GUIDE.md](SKILLS_USAGE_GUIDE.md) for detailed skill info
2. **Check the SKILL.md file** — Each skill has a `SKILL.md` file with instructions and examples
3. **Invoke the skill** — Use the invocation pattern for your tool (Claude Code, Copilot, etc.)
4. **Follow the phases** — Skills are structured as execution phases; complete each phase

---

## See Also

- [docs/SKILLS_USAGE_GUIDE.md](SKILLS_USAGE_GUIDE.md) — Complete skill reference and documentation
- [skills/README.md](../skills/README.md) — Skill catalog and lifecycle
- [docs/CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md) — Pipeline context handoff schemas
- [docs/PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md) — How the pipeline executes (interactive vs headless)

---

**Last Updated:** March 2026
