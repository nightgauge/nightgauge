# CI/CD Token Setup Runbook

This runbook covers how to configure GitHub authentication tokens for the
Nightgauge pipeline in automated CI/CD environments.

**See also:**

- [CONFIGURATION.md § github_auth](./CONFIGURATION.md#github_auth) — full token
  configuration reference
- [GITHUB_API_DEPENDENCIES.md § Token Scope Requirements](./GITHUB_API_DEPENDENCIES.md#token-scope-requirements)
  — required OAuth scopes per operation

---

## Overview

CI/CD environments typically lack the `gh` CLI. Without a configured token, the
pipeline will fail with:

```
warning: Using gh CLI for token resolution — configure github_auth.token in config.yaml for reliable multi-org support
no GitHub token available (tried config, GITHUB_TOKEN env, and gh CLI): gh auth token: ...
```

The solution is to configure `github_auth.token` (or `github_auth.tokens[owner]`
for multi-org) with an `env:VAR_NAME` reference, and set that variable as a
secret in your CI/CD system.

---

## Step 1: Create a GitHub Personal Access Token

### Classic PAT (Recommended for Simplicity)

1. Navigate to **GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic)**
2. Click **Generate new token (classic)**
3. Set a descriptive name (e.g., `nightgauge-pipeline-ci`)
4. Set expiry (90 days recommended; rotate regularly)
5. Grant these scopes:
   - `repo` — Full control of private/public repos
   - `project` — Read/write GitHub Projects v2
   - `read:org` — Query org membership
6. Click **Generate token** and copy the value immediately

### Fine-grained PAT (Recommended for Least Privilege)

1. Navigate to **GitHub → Settings → Developer settings → Personal access tokens → Fine-grained tokens**
2. Click **Generate new token**
3. Set Resource owner to the org/account owning your repos
4. Set Repository access: **All repositories** or select specific repos
5. Under **Repository permissions**:
   - Contents: **Read and write**
   - Issues: **Read and write**
   - Pull requests: **Read and write**
   - Metadata: **Read-only** (auto-selected)
6. Under **Organization permissions**:
   - GitHub Projects: **Read and write**
   - Members: **Read-only**
7. Click **Generate token** and copy the value

---

## Step 2: Add the Token as a CI/CD Secret

### GitHub Actions

```bash
gh secret set GITHUB_TOKEN_NIGHTGAUGE --body "ghp_xxxxxx"
# Or use the GitHub UI: Settings → Secrets and variables → Actions → New repository secret
```

Reference in your workflow:

```yaml
# .github/workflows/pipeline.yml
jobs:
  pipeline:
    runs-on: ubuntu-latest
    env:
      GITHUB_TOKEN_NIGHTGAUGE: ${{ secrets.GITHUB_TOKEN_NIGHTGAUGE }}
    steps:
      - uses: actions/checkout@v4
      - name: Run Nightgauge pipeline
        run: nightgauge run
```

### GitLab CI

Store as a protected CI/CD variable (Settings → CI/CD → Variables):

```yaml
# .gitlab-ci.yml
variables:
  GITHUB_TOKEN_NIGHTGAUGE: $GITHUB_TOKEN_NIGHTGAUGE # references protected variable

pipeline:
  script:
    - nightgauge run
```

### Generic CI/CD

Set the variable in your CI/CD system's secret management and ensure it's
available in the pipeline environment:

```bash
# Verify the variable is set before running:
echo "Token length: ${#GITHUB_TOKEN_NIGHTGAUGE}"  # should be >0
nightgauge run
```

---

## Step 3: Configure the Pipeline

Add or update `.nightgauge/config.yaml` in your repository:

### Single-Org Setup

```yaml
# .nightgauge/config.yaml
project:
  owner: nightgauge
  number: 1

github_auth:
  token: env:GITHUB_TOKEN_NIGHTGAUGE
```

### Multi-Org Setup

When your workspace spans multiple GitHub organizations:

```yaml
# ~/.nightgauge/config.yaml  (global — applies to all repos)
github_auth:
  tokens:
    nightgauge: env:GITHUB_TOKEN_NIGHTGAUGE
    PartnerOrg: env:GITHUB_TOKEN_PARTNER
```

Set both secrets in your CI/CD system:

```bash
gh secret set GITHUB_TOKEN_NIGHTGAUGE --body "ghp_acme_token"
gh secret set GITHUB_TOKEN_PARTNER --body "ghp_partner_token"
```

And reference both in your workflow:

```yaml
env:
  GITHUB_TOKEN_NIGHTGAUGE: ${{ secrets.GITHUB_TOKEN_NIGHTGAUGE }}
  GITHUB_TOKEN_PARTNER: ${{ secrets.GITHUB_TOKEN_PARTNER }}
```

---

## Step 4: Verify

Run a pipeline dry-run to verify token resolution:

```bash
nightgauge project list --owner nightgauge
```

If successful, no `warning: Using gh CLI for token resolution` message should
appear. If the warning appears, check:

1. **Environment variable is set**: `echo ${#GITHUB_TOKEN_NIGHTGAUGE}` (length > 0)
2. **Config references correct variable name** (case-sensitive):
   - `env:GITHUB_TOKEN_NIGHTGAUGE` matches the variable name exactly
3. **Token has required scopes**: Create a test request:
   ```bash
   curl -H "Authorization: Bearer $GITHUB_TOKEN_NIGHTGAUGE" https://api.github.com/user
   ```
   Should return a JSON user object (not `401 Unauthorized`).

---

## Troubleshooting

### "warning: Using gh CLI for token resolution"

The pipeline could not find a token from `github_auth.token`, `github_auth.tokens[owner]`,
or `GITHUB_TOKEN`. It fell back to `gh CLI`.

**Fix**: Configure `github_auth.token: env:YOUR_VAR_NAME` and set the env var.

### "no GitHub token available (tried config, GITHUB_TOKEN env, and gh CLI)"

All token sources failed. In CI/CD, `gh` is typically not installed.

**Fix**: Set `github_auth.token: env:YOUR_VAR_NAME` and verify the env var is
set in your CI/CD environment (Step 2 above).

### "401 Unauthorized" from GitHub API

Token is invalid, expired, or revoked.

**Fix**: Regenerate the PAT and update your CI/CD secret. Check expiry date.

### "403 Forbidden" / "resource not accessible by integration"

Token lacks the required scope for the operation.

**Fix**: Regenerate the PAT with `repo`, `project`, and `read:org` scopes (see
[Token Scope Requirements](./GITHUB_API_DEPENDENCIES.md#token-scope-requirements)).

### "secondary rate limit" errors

Pipeline is hitting GitHub API rate limits.

**Fix**: Reduce parallel pipeline stages or increase delay between API calls.
Consider using a GitHub Enterprise token with higher rate limits.

### "environment variable X referenced by config token is not set or empty"

The `env:VAR_NAME` reference in `github_auth.token` or `github_auth.tokens[owner]`
points to a variable that is not set.

**Fix**: Set the environment variable in your CI/CD system. Verify the variable
name matches exactly (case-sensitive).

---

## Token Rotation

GitHub recommends rotating PATs every 90 days. When rotating:

1. Generate a new PAT with the same scopes
2. Update the secret in your CI/CD system:
   ```bash
   gh secret set GITHUB_TOKEN_NIGHTGAUGE --body "ghp_new_token"
   ```
3. Verify the pipeline still works after rotation
4. Revoke the old token in GitHub Settings

---

## Security Notes

- **Never commit tokens** to version control — always use `env:VAR_NAME` syntax
- **Limit token scope** — grant only `repo`, `project`, `read:org`; avoid
  `admin:org`, `delete_repo`, or `write:packages`
- **Use per-org tokens** for multi-org workspaces — scope each token to its org
- **Audit token usage** via GitHub Settings → Developer settings → Personal
  access tokens → Active tokens
