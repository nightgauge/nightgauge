package forgecmd

// Long-form help strings live in this file so help-text tests can
// assert each leaf command documents the GitHub-vs-GitLab difference
// in a single place. Each `long*` constant is the `Long:` value of the
// corresponding Cobra command.
//
// Convention: every leaf command's Long text MUST contain either
//   - the literal string "GitLab equivalent:" describing the mapping, or
//   - the literal string "(no GitLab equivalent — GitHub only)"
// help_text_test.go asserts this rule.

const longRoot = `Unified forge operations across GitHub and GitLab.

The forge subcommand routes every operation through the forge-agnostic
ForgeClient interface (ADR-006). It is a parallel surface to the
existing top-level commands — those remain unchanged for now.

Subcommand groups:
  issue   List, view, create, edit, close, reopen, comment.
  pr|mr   Pull/merge request CRUD plus check-status rollup.
  project Project board field/item operations.
  label   Label CRUD plus add/remove on issues and PRs.
  auth    Token status and config-file token management.

Forge selection precedence (highest to lowest):
  --forge <id>      explicit flag (e.g. --forge github)
  IB_FORGE          environment variable
  --repo <owner/r>  inferred from the workspace forges: map
  default forge     declared in the workspace config
`

// --- issue ---

const longIssue = `Issue operations on a forge.

GitLab equivalent: GitLab Issues. The 'number' field maps to GitLab's
'iid' (project-scoped issue id), not the global 'id'. Sub-issue linking
is GitHub-native; GitLab adapters emulate via 'related to' issue links.
`

const longIssueList = `List issues in a repo.

GitLab equivalent: GET /projects/:id/issues. Filtering by labels uses
the same comma-separated syntax on both forges.
`

const longIssueView = `View a single issue by number.

GitLab equivalent: GET /projects/:id/issues/:iid. The 'number' field in
the JSON output is the iid on GitLab.
`

const longIssueCreate = `Create a new issue.

GitLab equivalent: POST /projects/:id/issues. Labels and assignees are
both passed by name on GitHub; GitLab requires user IDs for assignees,
which the adapter resolves transparently when given names.
`

const longIssueEdit = `Edit an existing issue's body or labels.

GitLab equivalent: PUT /projects/:id/issues/:iid. Closing/reopening is
handled by the dedicated 'forge issue close|reopen' verbs.
`

const longIssueClose = `Close an issue by node id or number.

GitLab equivalent: PUT /projects/:id/issues/:iid?state_event=close.
`

const longIssueReopen = `Reopen a closed issue.

GitLab equivalent: PUT /projects/:id/issues/:iid?state_event=reopen.
`

const longIssueComment = `Add a comment to an issue.

GitLab equivalent: POST /projects/:id/issues/:iid/notes. Both forges
accept Markdown bodies.
`

// --- pr / mr ---

const longPR = `Pull request operations on a forge ('mr' alias for GitLab parlance).

GitLab equivalent: GitLab Merge Requests. The 'mr' alias is registered
on the parent command so 'forge mr ...' works the same as 'forge pr ...'.
The 'number' field maps to the merge request iid on GitLab.
`

const longPRList = `List PRs / MRs in a repo.

GitLab equivalent: GET /projects/:id/merge_requests.
`

const longPRView = `View a single PR by number.

GitLab equivalent: GET /projects/:id/merge_requests/:iid. The 'mergeable'
field maps to GitLab's 'merge_status'.
`

const longPRCreate = `Create a new PR / MR.

GitLab equivalent: POST /projects/:id/merge_requests. Both forges
require source/target branches; we accept --head and --base on either
forge for symmetry.
`

const longPREdit = `Edit a PR's title, body, or draft state.

GitLab equivalent: PUT /projects/:id/merge_requests/:iid. The 'draft'
flag maps to GitLab's 'work_in_progress'.
`

const longPRMerge = `Merge a PR / MR.

GitLab equivalent: PUT /projects/:id/merge_requests/:iid/merge. Strategy
flags ('squash', 'rebase') are honoured on both forges; merge commit
strategy on GitHub maps to 'merge_commit' on GitLab.
`

const longPRClose = `Close a PR / MR without merging.

GitLab equivalent: PUT /projects/:id/merge_requests/:iid?state_event=close.
`

const longPRComment = `Add a comment to a PR / MR.

GitLab equivalent: POST /projects/:id/merge_requests/:iid/notes.
`

const longPRChecks = `Show the CI/check rollup for a PR / MR.

GitLab equivalent: GitLab CI Pipelines API. The 'state' field rolls up
GitHub check runs and GitLab pipeline status into a forge-agnostic
'success|failure|pending' value. Schema-versioned (v: 1).
`

// --- project ---

const longProject = `Project board operations.

GitLab equivalent: GitLab Issue Boards. GitHub Projects V2 single-select
fields map to GitLab scoped labels; date/number fields are GitLab
custom milestones or labels depending on the field type.
`

const longProjectFieldList = `List the project's fields with type metadata.

GitLab equivalent: GitLab issue board lists. Single-select fields map
to scoped label sets.
`

const longProjectFieldSet = `Set a field on a project board item.

GitLab equivalent: scoped label set/replace. Single-select fields are
mutually-exclusive scoped labels; text/number/date fields map to
adapter-specific GitLab features.
`

const longProjectFieldGet = `Read a single field's value off a board item.

GitLab equivalent: GET /projects/:id/issues/:iid + label inspection.
`

const longProjectItemList = `List items on the project board, optionally filtered by status.

GitLab equivalent: GitLab Issue Board view. Filtering by status uses the
forge-agnostic Status string; adapters translate to scoped labels.
`

const longProjectItemAdd = `Add an issue to the project board by number.

GitLab equivalent: assign issue to a board (GitLab issue boards are
implicit per-project, so this is a no-op on default boards).
`

const longProjectItemRemove = `Remove an issue from the project board.

(no GitLab equivalent — GitHub only) — GitLab issues belong to a project
implicitly; removal would mean closing the issue. The GitHub adapter
returns ErrUnsupported for now; a follow-up issue tracks the GraphQL
ProjectV2 'deleteProjectV2Item' mutation that this verb will dispatch.
`

// --- label ---

const longLabel = `Label CRUD plus add/remove on issues and PRs.

GitLab equivalent: GitLab Project Labels. Label colour syntax is the
same hex string on both forges.
`

const longLabelList = `List labels defined on the repository.

GitLab equivalent: GET /projects/:id/labels.
`

const longLabelCreate = `Create a new repository label.

GitLab equivalent: POST /projects/:id/labels. Both forges accept name,
description, and a hex colour.
`

const longLabelDelete = `Delete a repository label by id.

GitLab equivalent: DELETE /projects/:id/labels/:label_id.
`

const longLabelAdd = `Add labels to an issue or PR.

GitLab equivalent: PUT issue with 'add_labels' / merge_request with
'add_labels'. Both forges accept comma-separated label names.
`

const longLabelRemove = `Remove labels from an issue or PR.

GitLab equivalent: PUT issue with 'remove_labels'. Same syntax as add.
`

// --- auth ---

const longAuth = `Token status and config-file token management.

GitLab equivalent: glab auth status / login. The auth subcommand reads
and writes the token in .nightgauge/config.yaml under
github_auth.token (or the GitLab equivalent for non-github forges).
Browser-based OAuth is intentionally out of scope; the implementation
manages tokens already obtained via 'gh auth login' or PAT generation.
`

const longAuthStatus = `Report token validity, scopes, and the resolution source.

GitLab equivalent: glab auth status. Tokens are masked to
'prefix****suffix' before display — the raw token never appears in
output.
`

const longAuthLogin = `Write a token to the project config.

GitLab equivalent: glab auth login --token. Use --from-stdin to pass
the token via standard input (preferred — keeps tokens out of shell
history). The token is validated against the forge before being
written; insufficient scopes emit a warning but still write the value.
`

const longAuthLogout = `Clear the token from the project config.

GitLab equivalent: glab auth logout. Idempotent — safe to run when no
token is stored.
`

const longAuthRefresh = `Re-read the token from the system gh / glab CLI and rewrite the config.

GitLab equivalent: glab auth refresh. Useful after rotating a PAT in
gh's keychain — the new value is copied into the project config so
non-gh callers (e.g. automation that does not source gh) pick it up.
`

const longAuthWhoami = `Print the login of the currently authenticated actor.

GitLab equivalent: GET /user. Mirrors 'gh api user --jq .login' — used
by repo-init to discover the active user for personal-project lookup.
The JSON envelope is {v, login}; the bare login is on the 'login' key
so 'jq -r .login' works without modification.
`

const longAuthToken = `Print the resolved GitHub token to stdout.

Resolution chain: config github_auth.token → GITHUB_TOKEN env → 'gh auth
token --user <github_user>'. Skills and the VSCode extension host use this
to authenticate their own gh/gh-api subprocesses as the configured pipeline
identity rather than the machine's ambient active gh account. Only the bare
token is printed, so it composes: export GITHUB_TOKEN=$(... forge auth token).

(no GitLab equivalent — GitHub only)
`

const longAuthAssert = `Assert the resolved per-repo identity can act on a target repo.

Deterministic preflight permission check (#4068). Resolves the configured
github_user for the target owner, confirms the EFFECTIVE login (Whoami, after
token resolution with ambient env stripped) equals that github_user, and
confirms the identity has push access on owner/repo via the collaborator
permission endpoint. With --admin, additionally requires admin (needed to
bypass a required-review ruleset / branch protection).

Exits 0 when the identity matches and has the required access; exits non-zero
with a one-line remediation otherwise — so a misconfigured identity fails loudly
here, never silently at a later merge/push. Use --json for skill consumption.

Example:
  nightgauge forge auth assert --repo Acme-Community/acmesvc-tracker
  nightgauge forge auth assert --repo Acme-Community/acmesvc-tracker --admin --json

(no GitLab equivalent — GitHub only)
`

// --- repo ---

const longRepo = `Repository metadata operations.

GitLab equivalent: GitLab Projects API. The 'view' subverb returns the
canonical owner / name pair via the same field set as 'gh repo view
--json nameWithOwner,owner,name'.
`

const longRepoView = `View repository metadata (nameWithOwner / owner / name).

GitLab equivalent: GET /projects/:id. Mirrors 'gh repo view --json
nameWithOwner,owner,name' so jq pipelines parsing the gh output can be
reused verbatim. The owner field is the namespace path (group/user) on
GitLab.
`

// --- graphql ---

const longGraphQL = `Execute a raw GraphQL query or mutation against the active forge.

GitLab equivalent: GitLab GraphQL API at /api/graphql. The GitLab
adapter does not yet expose a GraphQL transport (see ADR-008 for the
carve-out rationale) — invocations against --forge gitlab return
ErrUnsupported.

Flags follow the 'gh api graphql' convention:
  -f key=value     string variable (use 'query' for the GraphQL document)
  -F key=value     raw variable (parsed as JSON: numbers, booleans, null)
  --query-file P   read the GraphQL document from <path> (alternative to
                   '-f query=@<path>')

The output is the raw JSON envelope returned by the forge — use jq to
extract fields. This subcommand exists for the carve-out cases listed
in ADR-008 (sub-issue linking, addBlockedBy, project view-create) where
no typed forge surface exists yet.
`
