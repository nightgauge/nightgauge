# Project Selection (Phase 1)

Procedural detail for Phase 1. Phase 1 has five ordered steps. Complete them in
sequence — each step gates the next.

## Contents

- [Step 1.1: Check Whether the Repo Is Already Linked to a Project](#step-11-check-whether-the-repo-is-already-linked-to-a-project)
- [Step 1.2: List Existing Projects — Org First, Then Active User](#step-12-list-existing-projects--org-first-then-active-user)
- [Step 1.3: Resolve --project N (If Passed)](#step-13-resolve---project-n-if-passed)
- [Step 1.4: Prompt If No Project Resolved Yet](#step-14-prompt-if-no-project-resolved-yet)
- [Step 1.5: Confirm Project Before Proceeding](#step-15-confirm-project-before-proceeding)

---

## Step 1.1: Check Whether the Repo Is Already Linked to a Project

Before listing or creating anything, check the repo's existing project links via
GraphQL. This is the fastest path — if the repo is already linked, we can skip
project discovery entirely.

```bash
# Determine whether OWNER is an org or a user (used throughout Phase 1 and Phase 4)
OWNER_TYPE=$(nightgauge forge api "users/$OWNER" --jq '.type' 2>/dev/null)
# GitHub API returns "Organization" or "User"

# repository.projectsV2 works regardless of org vs user — no branching needed here
LINKED_PROJECTS=$(nightgauge forge graphql -f query='
{
  repository(owner: "'"$OWNER"'", name: "'"$REPO_NAME"'") {
    projectsV2(first: 10) {
      nodes {
        number
        title
        url
        owner {
          ... on Organization { login }
          ... on User { login }
        }
      }
    }
  }
}' | jq -r '.data.repository.projectsV2.nodes[]? | "\(.number)\t\(.title)\t\(.url)\t\(.owner.login)"')

if [ -n "$LINKED_PROJECTS" ]; then
  echo "Repository is already linked to the following project(s):"
  echo "$LINKED_PROJECTS" | while IFS=$'\t' read -r num title url owner_login; do
    echo "  #$num — $title (owner: $owner_login)"
    echo "  URL: $url"
  done
fi
```

If one project is found and no `--project` was passed, **use that project** and
skip Steps 1.2–1.4 (go directly to Step 1.5: Confirm).

If multiple are found, surface all of them in the Step 1.4 prompt for the user
to choose.

## Step 1.2: List Existing Projects — Org First, Then Active User

When the repo is not already linked (or to populate the prompt options), list
available projects. **Always prefer org-owned projects when the repo belongs to
an org.** Personal projects cannot be linked to org repos via the GitHub API.

```bash
ORG_PROJECTS=""
USER_PROJECTS=""

# Org projects (preferred when repo belongs to an org)
if [ "$OWNER_TYPE" = "Organization" ]; then
  ORG_PROJECTS=$(nightgauge forge graphql -f query='query($owner:String!){organization(login:$owner){projectsV2(first:30){nodes{number,title,url}}}}' -f owner="$OWNER" 2>/dev/null \
    | jq -r '.data.organization.projectsV2.nodes[]? | "\(.number)\t\(.title)\t\(.url)"')
  if [ -n "$ORG_PROJECTS" ]; then
    echo "Org projects for $OWNER:"
    echo "$ORG_PROJECTS" | while IFS=$'\t' read -r num title url; do
      echo "  #$num — $title ($url)"
    done
  fi
fi

# Active user projects (fallback or supplemental)
USER_PROJECTS=$(nightgauge forge graphql -f query='query($owner:String!){user(login:$owner){projectsV2(first:30){nodes{number,title,url}}}}' -f owner="$ACTIVE_USER" 2>/dev/null \
  | jq -r '.data.user.projectsV2.nodes[]? | "\(.number)\t\(.title)\t\(.url)"')
if [ -n "$USER_PROJECTS" ]; then
  echo "User projects for $ACTIVE_USER:"
  echo "$USER_PROJECTS" | while IFS=$'\t' read -r num title url; do
    echo "  #$num — $title ($url)"
  done
fi
```

## Step 1.3: Resolve --project N (If Passed)

If `--project N` was passed on the command line, resolve it unambiguously using
the Go binary. The binary tries org ownership first (preferred — personal
projects cannot be linked to org repos) and falls back to user ownership.

```bash
RESOLVED_PROJECT_NUMBER=""
RESOLVED_PROJECT_OWNER=""
RESOLVED_PROJECT_OWNER_TYPE=""
RESOLVED_PROJECT_TITLE=""
RESOLVED_PROJECT_URL=""
RESOLVED_PROJECT_ID=""

if [ -n "$PROJECT_ARG" ]; then
  RESOLVE_JSON=$(nightgauge project resolve --number "$PROJECT_ARG" --owner "$OWNER" --json 2>&1)
  RESOLVE_EXIT=$?

  if [ $RESOLVE_EXIT -eq 0 ]; then
    RESOLVED_PROJECT_NUMBER=$(echo "$RESOLVE_JSON" | jq -r '.number')
    RESOLVED_PROJECT_OWNER=$(echo "$RESOLVE_JSON" | jq -r '.owner')
    RESOLVED_PROJECT_OWNER_TYPE=$(echo "$RESOLVE_JSON" | jq -r '.owner_type')
    RESOLVED_PROJECT_TITLE=$(echo "$RESOLVE_JSON" | jq -r '.title')
    RESOLVED_PROJECT_URL=$(echo "$RESOLVE_JSON" | jq -r '.url')
    RESOLVED_PROJECT_ID=$(echo "$RESOLVE_JSON" | jq -r '.id')
    echo "Resolved project #$RESOLVED_PROJECT_NUMBER: $RESOLVED_PROJECT_TITLE ($RESOLVED_PROJECT_OWNER_TYPE: $RESOLVED_PROJECT_OWNER)"
  else
    echo "ERROR: Project #$PROJECT_ARG not found under org or user '$OWNER'."
    echo "Verify the project number and that the token has access."
    echo "$RESOLVE_JSON"
    exit 1
  fi
fi
```

## Step 1.4: Prompt If No Project Resolved Yet

If no project has been resolved (no `--project` flag, no existing config, repo
not already linked), prompt the user with the discovered projects as options:

```json
{
  "questions": [
    {
      "question": "Which GitHub Project should this repository use?",
      "header": "Project Selection",
      "multiSelect": false,
      "options": [
        {
          "label": "#N — <title> (org: OWNER) [RECOMMENDED — org project]",
          "description": "<project URL>"
        },
        {
          "label": "#N — <title> (user: ACTIVE_USER)",
          "description": "<project URL>"
        },
        {
          "label": "I don't have a project yet",
          "description": "Create one at github.com/orgs/OWNER/projects/new, then re-run"
        }
      ]
    }
  ]
}
```

Populate the options list from the projects discovered in Step 1.2. Always list
org-owned projects first, labeling them `[RECOMMENDED — org project]` when the
repo belongs to an org. If no projects were found, show only the "I don't have a
project yet" option.

If user selects "I don't have a project yet", output:

```
To create a GitHub Project:
1. Go to: https://github.com/orgs/OWNER/projects/new
2. Choose "Board" template
3. Note the project number from the URL
4. Re-run /nightgauge:repo-init --project <NUMBER>
```

Then exit cleanly.

## Step 1.5: Confirm Project Before Proceeding

**Before writing any config**, display the resolved project and ask for
confirmation. This prevents writing config with the wrong project.

```
Resolved project:

  Title:  <RESOLVED_PROJECT_TITLE>
  Owner:  <RESOLVED_PROJECT_OWNER>
  Number: #<RESOLVED_PROJECT_NUMBER>
  URL:    <RESOLVED_PROJECT_URL>

This project will be linked to <REPO> and its ID will be written to
.nightgauge/config.yaml. Confirm?
```

```json
{
  "questions": [
    {
      "question": "Use this project for <REPO>?\n\n  Title:  <title>\n  Owner:  <owner>\n  #:      <number>\n  URL:    <url>",
      "header": "Confirm Project",
      "multiSelect": false,
      "options": [
        {
          "label": "Yes, use this project",
          "description": "Proceed with setup"
        },
        {
          "label": "No, choose a different project",
          "description": "Return to project selection"
        }
      ]
    }
  ]
}
```

If user selects "No", return to Step 1.4.

Set final variables for the rest of the skill:

```bash
PROJECT_NUMBER="$RESOLVED_PROJECT_NUMBER"
OWNER="$RESOLVED_PROJECT_OWNER"   # Use project's owner for all subsequent API calls
```

> **Note:** When the project owner differs from the repo owner (e.g., an org
> project linked to a user-owned repo), use the project owner for GraphQL
> queries in Phase 4 and Phase 5, and the repo owner for label and link
> operations.
