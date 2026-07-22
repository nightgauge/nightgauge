### Configuration Reference

Pipeline skills read configuration from `.nightgauge/config.yaml`.

**Full reference**: See `docs/CONFIGURATION.md` for the complete configuration
guide including the 6-tier precedence chain, all config options, and examples.

**Schema definition**: `packages/nightgauge-vscode/src/config/schema.ts`

**Key config sections by stage**:

| Stage            | Key Options                                           |
| ---------------- | ----------------------------------------------------- |
| issue-pickup     | `branch.base`, `branch.prefixes.*`, `project.*`       |
| feature-planning | `project.number`, `project.auto_dates`                |
| feature-dev      | `pipeline.auto_fix`, `commands.*`                     |
| feature-validate | `commands.test`, `pipeline.skip.tests`                |
| pr-create        | `pr.draft_by_default`, `pr.reviewers`, `validation.*` |
| pr-merge         | `pr.merge_strategy`, `pr.delete_branch`               |

#### Issue-Pickup Configuration

| Config Key                   | Default     | Description                       |
| ---------------------------- | ----------- | --------------------------------- |
| `branch.base`                | `main`      | Default base branch for PRs       |
| `branch.protected`           | `[main]`    | Branches that cannot be pushed to |
| `branch.prefixes.*`          | (see below) | Custom branch prefix mappings     |
| `project.number`             | -           | GitHub Project number             |
| `project.auto_dates`         | `false`     | Auto-populate Start/Target dates  |
| `project.sprint.auto_assign` | `false`     | Assign current sprint on pickup   |

**Branch Prefixes (defaults):**

| Type       | Default     |
| ---------- | ----------- |
| `feature`  | `feat/`     |
| `bugfix`   | `fix/`      |
| `docs`     | `docs/`     |
| `refactor` | `refactor/` |
| `chore`    | `chore/`    |
| `test`     | `test/`     |
| `hotfix`   | `hotfix/`   |

**Environment overrides:**

```bash
export NIGHTGAUGE_BRANCH_BASE=develop
export NIGHTGAUGE_BRANCH_PROTECTED=main,develop
export NIGHTGAUGE_PROJECT_AUTO_DATES=true
```

#### PR-Merge Configuration

| Config Key                 | Default  | Description                                             |
| -------------------------- | -------- | ------------------------------------------------------- |
| `pr.merge_strategy`        | `squash` | Merge strategy for sub-issue PRs: squash, merge, rebase |
| `pr.epic_merge_strategy`   | `merge`  | Merge strategy for epic→main PRs: merge, squash, rebase |
| `pr.delete_branch`         | `true`   | Delete feature branch after merge                       |
| `pr.auto_fix_ci`           | `true`   | Auto-fix CI failures before merge                       |
| `pr.auto_fix_max_attempts` | `3`      | Maximum auto-fix retry attempts                         |
| `pr.ci_check_timeout`      | `10`     | Timeout for CI checks in minutes                        |
| `project.number`           | -        | GitHub Project number for status sync                   |

**Environment overrides:**

```bash
export NIGHTGAUGE_PR_ADMIN_MERGE=true
export NIGHTGAUGE_PR_MERGE_STRATEGY=rebase
export NIGHTGAUGE_PR_DELETE_BRANCH=false
export NIGHTGAUGE_PR_AUTO_FIX_CI=true
export NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS=3
export NIGHTGAUGE_PR_CI_CHECK_TIMEOUT=10  # minutes (not seconds)
```

**Important notes:**

- There is no admin merge bypass — nothing skips branch protection, and CI
  checks are always waited for unless `--skip-ci-gate` is explicitly used
  (#186).
- If no CI checks are configured on the repository, the skill proceeds normally
  without waiting.

### Config Helper Functions

When skills need to read configuration values, use these inline patterns
rather than undefined helper functions:

#### Single value with default

```bash
VALUE=$(yq -r '.path.to.key // "default"' .nightgauge/config.yaml 2>/dev/null || echo "default")
```

#### Boolean with default

```bash
ENABLED=$(yq -r '.path.to.key // "true"' .nightgauge/config.yaml 2>/dev/null || echo "true")
```

#### With env var override (6-tier precedence)

```bash
# Env var takes priority, then config file, then default
VALUE="${ENV_VAR_OVERRIDE:-$(yq -r '.path.to.key // "default"' .nightgauge/config.yaml 2>/dev/null || echo "default")}"
```

**NOTE**: Do not use `get_config_bool` or `get_config_value` — these functions
are not defined. Use the inline `yq` patterns above.
