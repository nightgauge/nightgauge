// Package ipc implements JSON-over-stdio IPC for VSCode ↔ Go communication.
// The protocol follows the same pattern as LSP: newline-delimited JSON messages
// on stdin/stdout.
package ipc

import (
	"encoding/json"

	"github.com/nightgauge/nightgauge/internal/config"
)

// Request is an incoming IPC request from VSCode.
type Request struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is an outgoing IPC response to VSCode.
type Response struct {
	ID     int         `json:"id"`
	Result interface{} `json:"result,omitempty"`
	Error  *RPCError   `json:"error,omitempty"`
}

// Event is an unsolicited event from Go to VSCode (no ID).
type Event struct {
	Event string      `json:"event"`
	Data  interface{} `json:"data,omitempty"`
}

// RPCError represents an error in a response.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error codes
const (
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

// ProtocolVersion is the current IPC protocol version.
// Bump when the IPC contract changes incompatibly.
// Must match IPC_PROTOCOL_VERSION in IpcClient.generated.ts.
const ProtocolVersion = 1

// Board method params

// --- Workspace methods ---

// WorkspaceSetRootParams are parameters for workspace.setRoot.
type WorkspaceSetRootParams struct {
	Root string `json:"root"`
}

// WorkspaceSetRootResult is the result for workspace.setRoot.
type WorkspaceSetRootResult struct {
	OK bool `json:"ok"`
}

// WorkspaceRegisterRepoParams are parameters for workspace.registerRepo.
// Called by the VSCode extension during workspace initialization to map
// (owner, repo) → local filesystem path for per-operation identity resolution.
type WorkspaceRegisterRepoParams struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Path  string `json:"path"` // absolute path to the repo root directory
}

// WorkspaceRegisterRepoResult is the result for workspace.registerRepo.
type WorkspaceRegisterRepoResult struct {
	OK bool `json:"ok"`
}

// ConfigureForgeInstanceParams are parameters for workspace.configureForgeInstance.
// Stores the forge kind + host for an (owner, repo) pair so subsequent
// per-repo operations can route to the correct adapter without re-discovering
// the forge each call.
//
// Kind must be "github" or "gitlab". Host is optional — empty defaults to the
// canonical SaaS endpoint for the kind (github.com / gitlab.com). Token is
// optional in this minimal v1 of the command; tokens are still resolved by the
// existing per-repo config layer. The full forge-switching UI (persistence to
// .nightgauge/config.yaml, workspace reload, multi-instance routing) is
// tracked in #3361 — this command exists today as the IPC contract surface
// the TypeScript side can pin tests against.
type ConfigureForgeInstanceParams struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Kind  string `json:"kind"`           // "github" | "gitlab"
	Host  string `json:"host,omitempty"` // optional — defaults to the kind's SaaS host
	Token string `json:"token,omitempty"`
}

// ConfigureForgeInstanceResult is the result for workspace.configureForgeInstance.
type ConfigureForgeInstanceResult struct {
	OK bool `json:"ok"`
	// Kind echoes the resolved forge kind for the (owner, repo). Useful for
	// the TS side to confirm the registry accepted the requested kind rather
	// than silently falling back.
	Kind string `json:"kind"`
}

// NotificationsReloadTokensResult is the result for
// notifications.reloadTokens. Returned by the inbound webhook receiver
// after re-reading config and atomically swapping the in-memory
// signing-token map.
type NotificationsReloadTokensResult struct {
	OK bool `json:"ok"`
}

// CheckAuthorizationParams are parameters for notifications.checkAuthorization.
// TypeScript calls this from its AuthorizeFn hook before dispatching a command.
type CheckAuthorizationParams struct {
	MattermostUserID string `json:"mattermostUserId"`
	CommandType      string `json:"commandType"`
	RepoSlug         string `json:"repoSlug,omitempty"`
	ChannelID        string `json:"channelId,omitempty"`
	Args             string `json:"args,omitempty"`
}

// CheckAuthorizationResult is the result for notifications.checkAuthorization.
type CheckAuthorizationResult struct {
	Allowed        bool   `json:"allowed"`
	MappedIdentity string `json:"mappedIdentity,omitempty"`
	Reason         string `json:"reason"`
}

// --- Config methods ---

// ConfigGetProjectParams are parameters for config.getProjectConfig.
// Root overrides the global workspace root so per-repo services can
// load config for a specific repository without swapping global state.
type ConfigGetProjectParams struct {
	Root string `json:"root,omitempty"`
}

// ConfigGetProjectResult is the result for config.getProjectConfig.
type ConfigGetProjectResult struct {
	Owner         string               `json:"owner"`
	ProjectNumber int                  `json:"projectNumber"`
	Projects      []ConfigProjectEntry `json:"projects,omitempty"`
	DefaultRepo   string               `json:"defaultRepo,omitempty"`
	OwnerType     string               `json:"ownerType,omitempty"` // "org" (default) or "user"
}

type ConfigProjectEntry struct {
	Name       string `json:"name"`
	Number     int    `json:"number"`
	SyncFilter string `json:"syncFilter,omitempty"`
	Default    bool   `json:"default,omitempty"`
}

// ConfigGetHealthThresholdsParams are parameters for config.getHealthThresholds.
// (no fields — Go reads its own config)
type ConfigGetHealthThresholdsParams struct{}

// ConfigGetHealthThresholdsResult is the result for config.getHealthThresholds.
type ConfigGetHealthThresholdsResult struct {
	WarningThreshold    float64 `json:"warningThreshold"`
	CriticalThreshold   float64 `json:"criticalThreshold"`
	EmergencyThreshold  float64 `json:"emergencyThreshold"`
	PoliciesEnabled     bool    `json:"policiesEnabled"`
	ActionsEnabled      bool    `json:"actionsEnabled"`
	FeedbackLoopEnabled bool    `json:"feedbackLoopEnabled"`
}

// ConfigTierAuditParams optionally scopes the audit to a workspace root.
type ConfigTierAuditParams struct {
	Root string `json:"root,omitempty"`
}

// ConfigTierAuditResult is the IPC response for config.tierAudit.
type ConfigTierAuditResult struct {
	Entries  []config.TierAuditEntry `json:"entries"`
	HasDrift bool                    `json:"hasDrift"`
}

// Board method params

// BoardListParams are parameters for board.list.
type BoardListParams struct {
	Owner         string `json:"owner"`
	ProjectNumber int    `json:"projectNumber"`
	Status        string `json:"status,omitempty"`
	OwnerType     string `json:"ownerType,omitempty"`  // "org" (default) or "user"
	GitHubUser    string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// BoardCountsParams are parameters for board.counts.
type BoardCountsParams struct {
	Owner         string `json:"owner"`
	ProjectNumber int    `json:"projectNumber"`
	OwnerType     string `json:"ownerType,omitempty"`  // "org" (default) or "user"
	GitHubUser    string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// GitHubRateLimitParams are parameters for github.rateLimit.
type GitHubRateLimitParams struct {
	GitHubUser string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// IssueViewParams are parameters for issue.view.
type IssueViewParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	GitHubUser string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// CancelActiveForNetworkOutageResult is the response from
// pipeline.cancelActiveForNetworkOutage. Issue #3296.
type CancelActiveForNetworkOutageResult struct {
	// CancelledIssues lists the issue numbers whose stages were signalled.
	// Empty when no stages were active at call time.
	CancelledIssues []int `json:"cancelledIssues"`
}

// IssueViewManyParams are parameters for issue.viewMany — fetches multiple
// issues from one repo in a single batched GraphQL request.
type IssueViewManyParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Numbers    []int  `json:"numbers"`
	GitHubUser string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// IssueListParams are parameters for issue.list.
type IssueListParams struct {
	Owner      string   `json:"owner"`
	Repo       string   `json:"repo"`
	Epic       int      `json:"epic,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	GitHubUser string   `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// PRViewParams are parameters for pr.view.
type PRViewParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	GitHubUser string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// EpicProgressParams are parameters for epic.progress.
type EpicProgressParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	GitHubUser string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// PipelineStatusParams are parameters for pipeline.status.
type PipelineStatusParams struct {
	Owner         string `json:"owner"`
	ProjectNumber int    `json:"projectNumber"`
	ItemID        string `json:"itemId"`
	OwnerType     string `json:"ownerType,omitempty"`  // "org" (default) or "user"
	GitHubUser    string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// --- Intelligence methods ---

// ComplexityEstimateParams are parameters for intelligence.complexity.
type ComplexityEstimateParams struct {
	Title             string   `json:"title"`
	Body              string   `json:"body"`
	Labels            []string `json:"labels"`
	FileCountEstimate int      `json:"fileCountEstimate,omitempty"`
	SubIssueCount     int      `json:"subIssueCount,omitempty"`
}

// ModelRouteParams are parameters for intelligence.route.
type ModelRouteParams struct {
	Stage           string `json:"stage"`
	ComplexityScore int    `json:"complexityScore"`
}

// HealthAnalysisParams are parameters for intelligence.health.
type HealthAnalysisParams struct {
	WorkspaceRoot string `json:"workspaceRoot"`
}

// FailureClassifyParams are parameters for intelligence.classify.
type FailureClassifyParams struct {
	Stage    string `json:"stage"`
	ExitCode int    `json:"exitCode"`
	Stderr   string `json:"stderr"`
}

// CostEstimateParams are parameters for intelligence.cost.
type CostEstimateParams struct {
	Stages          []string `json:"stages"`
	ComplexityScore int      `json:"complexityScore"`
}

// --- Board mutation methods ---

// BoardUpdateStatusParams are parameters for board.updateStatus.
type BoardUpdateStatusParams struct {
	Owner         string `json:"owner"`
	ProjectNumber int    `json:"projectNumber"`
	ItemID        string `json:"itemId"`
	Status        string `json:"status"`
	OwnerType     string `json:"ownerType,omitempty"`
	GitHubUser    string `json:"githubUser,omitempty"` // per-repo gh CLI user for multi-identity auth
}

// --- Issue mutation methods ---

// IssueCreateSubIssueParams are parameters for issue.createSubIssue.
type IssueCreateSubIssueParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	EpicNumber int    `json:"epicNumber"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	GitHubUser string `json:"githubUser,omitempty"`
}

// IssueLinkSubIssueParams are parameters for issue.linkSubIssue.
type IssueLinkSubIssueParams struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	EpicNumber  int    `json:"epicNumber"`
	IssueNumber int    `json:"issueNumber"`
	GitHubUser  string `json:"githubUser,omitempty"`
}

// --- Epic methods ---

// EpicCheckCompletionParams are parameters for epic.checkCompletion.
type EpicCheckCompletionParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	GitHubUser string `json:"githubUser,omitempty"`
}

// EpicTransitionStatusParams are parameters for epic.transitionStatus.
type EpicTransitionStatusParams struct {
	Owner         string `json:"owner"`
	Repo          string `json:"repo"`
	EpicNumber    int    `json:"epicNumber"`
	ProjectNumber int    `json:"projectNumber"`
	NewStatus     string `json:"newStatus"`
	GitHubUser    string `json:"githubUser,omitempty"`
}

// --- Pipeline execution methods ---

// PipelineRunParams are parameters for pipeline.run.
type PipelineRunParams struct {
	Owner        string `json:"owner"`
	Repo         string `json:"repo"`
	IssueNumber  int    `json:"issueNumber"`
	FromStage    string `json:"fromStage,omitempty"`
	TargetBranch string `json:"targetBranch,omitempty"`
	Model        string `json:"model,omitempty"`
	Adapter      string `json:"adapter,omitempty"`
}

// PipelineStopParams are parameters for pipeline.stop.
type PipelineStopParams struct {
	ExecutionID string `json:"executionId"`
}

// PipelinePauseParams are parameters for pipeline.pause.
type PipelinePauseParams struct {
	ExecutionID string `json:"executionId"`
}

// PipelineResumeParams are parameters for pipeline.resume.
type PipelineResumeParams struct {
	ExecutionID string `json:"executionId"`
}

// PipelineSetPausedParams are parameters for pipeline.setPaused.
type PipelineSetPausedParams struct {
	IssueNumber int  `json:"issueNumber"`
	Paused      bool `json:"paused"`
}

// PipelineGetStateParams are parameters for pipeline.getState.
type PipelineGetStateParams struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
}

// --- Queue methods ---

// QueueAddParams are parameters for queue.add.
type QueueAddParams struct {
	Owner       string   `json:"owner"`
	Repo        string   `json:"repo"`
	IssueNumber int      `json:"issueNumber"`
	Title       string   `json:"title,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	// RemoteRunID carries the platform-assigned run_id from a dashboard trigger
	// ack, so the scheduler adopts it instead of minting a fresh one. Without
	// this the command's ack runId and the synced pipeline-run id diverge and a
	// dashboard "view run" deep-link 404s. The scheduler already prefers a
	// queue item's RemoteRunID (#3557); this just lets the enqueue populate it.
	// See #4120.
	RemoteRunID string `json:"remoteRunId,omitempty"`
}

// QueueRemoveParams are parameters for queue.remove.
type QueueRemoveParams struct {
	IssueNumber int `json:"issueNumber"`
}

// QueueRunningRef identifies an in-flight pipeline by repo + issue number so
// the scheduler can enforce per-repo concurrency caps when dequeuing.
type QueueRunningRef struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// QueueDequeueIndependentParams are parameters for queue.dequeueIndependent.
type QueueDequeueIndependentParams struct {
	MaxSlots int `json:"maxSlots"`
	// RunningItems is the set of currently in-flight issues with their repos.
	// Repo is required for per-repo cap enforcement (the IPC dispatch path
	// tracks the running set, not the Go scheduler).
	RunningItems []QueueRunningRef `json:"runningItems"`
}

// QueueEnqueueEpicParams are parameters for queue.enqueueEpic.
type QueueEnqueueEpicParams struct {
	Owner      string   `json:"owner"`
	Repo       string   `json:"repo"`
	EpicNumber int      `json:"epicNumber"`
	Title      string   `json:"title,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	// EligibleSubIssues is an optional whitelist of sub-issue numbers the
	// caller has already filtered by project-board status / open-PR presence.
	// When non-empty, EnqueueEpic only enqueues sub-issues whose number is
	// in the set. When nil or empty, the full set of open sub-issues is
	// enqueued (autonomous path, backward-compatible).
	// @see Issue #2992 — epic drag filter
	EligibleSubIssues []int `json:"eligibleSubIssues,omitempty"`
}

// QueueReorderParams are parameters for queue.reorder.
type QueueReorderParams struct {
	IssueNumbers []int `json:"issueNumbers"`
}

// --- Git methods ---

// GitCurrentBranchParams are parameters for git.currentBranch.
type GitCurrentBranchParams struct {
	WorkDir string `json:"workDir,omitempty"`
}

// GitCheckoutParams are parameters for git.checkout.
type GitCheckoutParams struct {
	Branch  string `json:"branch"`
	WorkDir string `json:"workDir,omitempty"`
}

// GitBranchCreateParams are parameters for git.branchCreate.
type GitBranchCreateParams struct {
	Name    string `json:"name"`
	WorkDir string `json:"workDir,omitempty"`
}

// GitBranchDeleteParams are parameters for git.branchDelete.
type GitBranchDeleteParams struct {
	Name    string `json:"name"`
	WorkDir string `json:"workDir,omitempty"`
}

// GitBranchCleanupParams are parameters for git.branchCleanup (local + remote).
type GitBranchCleanupParams struct {
	Name    string `json:"name"`
	WorkDir string `json:"workDir,omitempty"`
}

// GitCleanupMergedBranchesParams are parameters for git.cleanupMergedBranches.
type GitCleanupMergedBranchesParams struct {
	WorkDir string `json:"workDir,omitempty"`
}

// GitCleanupMergedBranchesResult is the result for git.cleanupMergedBranches.
type GitCleanupMergedBranchesResult struct {
	Deleted []string `json:"deleted"`
	Count   int      `json:"count"`
}

// GitListRemoteBranchesParams are parameters for git.listRemoteBranches.
type GitListRemoteBranchesParams struct {
	WorkDir string `json:"workDir,omitempty"`
}

// GitStatusParams are parameters for git.status.
type GitStatusParams struct {
	WorkDir string `json:"workDir,omitempty"`
}

// GitCommitParams are parameters for git.commit.
type GitCommitParams struct {
	Message string `json:"message"`
	WorkDir string `json:"workDir,omitempty"`
}

// GitLogParams are parameters for git.log.
type GitLogParams struct {
	Limit   int    `json:"limit,omitempty"`
	WorkDir string `json:"workDir,omitempty"`
}

// GitDiffParams are parameters for git.diff.
type GitDiffParams struct {
	WorkDir string `json:"workDir,omitempty"`
}

// GitFetchParams are parameters for git.fetch.
type GitFetchParams struct {
	Prune   bool   `json:"prune,omitempty"`
	WorkDir string `json:"workDir,omitempty"`
}

// GitPushParams are parameters for git.push.
type GitPushParams struct {
	WorkDir string `json:"workDir,omitempty"`
}

// GitAbortPipelineParams are parameters for git.abortPipeline.
type GitAbortPipelineParams struct {
	FeatureBranch string `json:"featureBranch"`
	WorkDir       string `json:"workDir,omitempty"`
}

// GitResetPipelineParams are parameters for git.resetPipeline.
type GitResetPipelineParams struct {
	WorkDir string `json:"workDir,omitempty"`
}

// --- Issue mutation methods (additional) ---

// IssueCreateParams are parameters for issue.create.
type IssueCreateParams struct {
	Owner      string   `json:"owner"`
	Repo       string   `json:"repo"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Labels     []string `json:"labels,omitempty"`
	GitHubUser string   `json:"githubUser,omitempty"`
}

// IssueCloseParams are parameters for issue.close.
type IssueCloseParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	GitHubUser string `json:"githubUser,omitempty"`
}

// IssueReopenParams are parameters for issue.reopen.
type IssueReopenParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	GitHubUser string `json:"githubUser,omitempty"`
}

// --- PR mutation methods ---

// PRListParams are parameters for pr.list.
type PRListParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	State      string `json:"state,omitempty"`
	HeadRef    string `json:"headRef,omitempty"`
	GitHubUser string `json:"githubUser,omitempty"`
}

// PRMergeParams are parameters for pr.merge.
type PRMergeParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	PRNodeID   string `json:"prNodeId"`
	Strategy   string `json:"strategy,omitempty"` // defaults to SQUASH
	GitHubUser string `json:"githubUser,omitempty"`
}

// PRCreateParams are parameters for pr.create.
type PRCreateParams struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	HeadRef    string `json:"headRef"`
	BaseRef    string `json:"baseRef"`
	GitHubUser string `json:"githubUser,omitempty"`
}

// --- Project field methods ---

// ProjectSyncStatusParams are parameters for project.syncStatus.
type ProjectSyncStatusParams struct {
	Owner         string `json:"owner"`
	ProjectNumber int    `json:"projectNumber"`
	Repo          string `json:"repo"`
	IssueNumber   int    `json:"issueNumber"`
	Status        string `json:"status"`
	OwnerType     string `json:"ownerType,omitempty"`
	GitHubUser    string `json:"githubUser,omitempty"`
}

// ProjectSyncIterationParams are parameters for project.syncIteration.
type ProjectSyncIterationParams struct {
	Owner         string `json:"owner"`
	ProjectNumber int    `json:"projectNumber"`
	Repo          string `json:"repo"`
	IssueNumber   int    `json:"issueNumber"`
	Iteration     string `json:"iteration"`
	OwnerType     string `json:"ownerType,omitempty"`
	GitHubUser    string `json:"githubUser,omitempty"`
}

// ProjectSetHoursParams are parameters for project.setHours.
type ProjectSetHoursParams struct {
	Owner         string  `json:"owner"`
	ProjectNumber int     `json:"projectNumber"`
	Repo          string  `json:"repo"`
	IssueNumber   int     `json:"issueNumber"`
	Hours         float64 `json:"hours"`
	OwnerType     string  `json:"ownerType,omitempty"`
	GitHubUser    string  `json:"githubUser,omitempty"`
}

// ProjectAddItemParams are parameters for project.addItem.
type ProjectAddItemParams struct {
	Owner         string `json:"owner"`
	ProjectNumber int    `json:"projectNumber"`
	Repo          string `json:"repo"`
	IssueNumber   int    `json:"issueNumber"`
	OwnerType     string `json:"ownerType,omitempty"`
	GitHubUser    string `json:"githubUser,omitempty"`
}

// --- Platform methods ---

// PlatformStatusParams are parameters for platform.status.
type PlatformStatusParams struct{}

// LicenseValidateParams are parameters for platform.license.
type LicenseValidateParams struct{}

// PlatformResolveSkillParams are parameters for platform.resolveSkill.
type PlatformResolveSkillParams struct {
	SkillID         string `json:"skillId"`
	Model           string `json:"model,omitempty"`
	ComplexityScore int    `json:"complexityScore,omitempty"`
	IssueType       string `json:"issueType,omitempty"`
	SizeLabel       string `json:"sizeLabel,omitempty"`
}

// PlatformValidateLicenseParams are parameters for platform.validateLicense.
type PlatformValidateLicenseParams struct {
	LicenseKey string `json:"licenseKey"`
	MachineID  string `json:"machineId,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

// PlatformStartTrialParams are parameters for platform.startTrial.
type PlatformStartTrialParams struct {
	// AccessToken is the device-flow JWT applied as a per-call bearer (the trial
	// endpoint is JWT-only — a license key is not accepted). Never logged.
	AccessToken string `json:"accessToken"`
}

// PlatformSubmitAnalyticsParams are parameters for platform.submitAnalytics.
type PlatformSubmitAnalyticsParams struct {
	EventType string                 `json:"eventType"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp string                 `json:"timestamp,omitempty"` // ISO 8601
}

// PlatformGetUsageSummaryParams are parameters for platform.getUsageSummary.
type PlatformGetUsageSummaryParams struct{}

// PlatformSyncTelemetryParams are parameters for platform.syncTelemetry.
type PlatformSyncTelemetryParams struct {
	// Limit is the maximum number of recent run records to sync. Default 50.
	Limit int `json:"limit,omitempty"`
	// DaysBack is how many days of history files to scan. Default 7.
	DaysBack int `json:"daysBack,omitempty"`
	// Repo filters records to a specific repo ("owner/repo"). Empty = all.
	Repo string `json:"repo,omitempty"`
}

// PlatformSyncTelemetryResult is the result of platform.syncTelemetry.
type PlatformSyncTelemetryResult struct {
	Synced int      `json:"synced"`
	Failed int      `json:"failed"`
	Errors []string `json:"errors,omitempty"`
}

// PlatformCostAnalyticsParams are parameters for platform.getCostAnalytics.
type PlatformCostAnalyticsParams struct {
	// StartDate filters results to this start date (YYYY-MM-DD). Empty = no lower bound.
	StartDate string `json:"startDate,omitempty"`
	// EndDate filters results to this end date (YYYY-MM-DD). Empty = no upper bound.
	EndDate string `json:"endDate,omitempty"`
}

// PlatformAnalyticsRunsParams are parameters for platform.getAnalyticsRuns (#3319).
type PlatformAnalyticsRunsParams struct {
	// StartDate filters results to this start date (YYYY-MM-DD). Empty = no lower bound.
	StartDate string `json:"startDate,omitempty"`
	// EndDate filters results to this end date (YYYY-MM-DD). Empty = no upper bound.
	EndDate string `json:"endDate,omitempty"`
	// Cursor is the pagination cursor from the previous page response.
	Cursor string `json:"cursor,omitempty"`
	// Outcome filters to a specific run outcome (productive, failed, cancelled, verify-and-close).
	Outcome string `json:"outcome,omitempty"`
	// Branch filters to runs on a specific branch (substring match).
	Branch string `json:"branch,omitempty"`
	// Limit is the maximum number of entries to return per page.
	Limit int `json:"limit,omitempty"`
}

// PlatformGetAnalyticsTrendsParams are parameters for platform.getAnalyticsTrends (#3320).
type PlatformGetAnalyticsTrendsParams struct {
	// Period is the date range: "30d" | "90d" | "180d". Defaults to "30d".
	Period string `json:"period,omitempty"`
}

// PlatformAuditGenerateReportParams are parameters for platform.auditGenerateReport (#3322).
type PlatformAuditGenerateReportParams struct {
	// ReportType is the compliance standard: "soc2" | "iso27001".
	ReportType string `json:"reportType"`
	// StartDate is the period start in YYYY-MM-DD format.
	StartDate string `json:"startDate"`
	// EndDate is the period end in YYYY-MM-DD format.
	EndDate string `json:"endDate"`
	// Format is the output format: "pdf".
	Format string `json:"format"`
}

// PlatformAuditListReportsParams are parameters for platform.auditListReports (#3322).
type PlatformAuditListReportsParams struct {
	// Cursor is the pagination cursor from the previous page response.
	Cursor string `json:"cursor,omitempty"`
	// Limit is the maximum number of entries to return.
	Limit int `json:"limit,omitempty"`
}

// PlatformAuditGetReportParams are parameters for platform.auditGetReport (#3322).
type PlatformAuditGetReportParams struct {
	// ReportID is the ID of the report to fetch.
	ReportID string `json:"reportId"`
}

// AuditGetRetentionConfigParams are parameters for audit.getRetentionConfig (#3323).
type AuditGetRetentionConfigParams struct{}

// AuditUpdateRetentionConfigParams are parameters for audit.updateRetentionConfig (#3323).
type AuditUpdateRetentionConfigParams struct {
	// RetentionDays is the new retention period in days (1–3650).
	RetentionDays int `json:"retentionDays"`
}

// AuditVerifyIntegrityParams are parameters for audit.verifyIntegrity (#3323).
type AuditVerifyIntegrityParams struct {
	// WindowDays is the verification window: 30, 90, or 365.
	WindowDays int `json:"windowDays"`
}

// PlatformGetTeamMembersParams are parameters for platform.getTeamMembers.
type PlatformGetTeamMembersParams struct{}

// PlatformCreatePortalSessionParams are parameters for platform.createPortalSession.
type PlatformCreatePortalSessionParams struct{}

// PlatformHealthCheckParams are parameters for platform.healthCheck.
type PlatformHealthCheckParams struct{}

// PlatformAuthDeviceCodeParams are parameters for platform.authDeviceCode.
// No params needed — device code request has no body.
type PlatformAuthDeviceCodeParams struct{}

// PlatformAuthDeviceTokenParams are parameters for platform.authDeviceToken.
type PlatformAuthDeviceTokenParams struct {
	DeviceCode string `json:"deviceCode"`
}

// PlatformAuthGithubParams are parameters for platform.authGithub.
type PlatformAuthGithubParams struct {
	GithubAccessToken string `json:"githubAccessToken"`
}

// PlatformAuthRefreshParams are parameters for platform.authRefresh.
type PlatformAuthRefreshParams struct {
	RefreshToken string `json:"refreshToken"`
}

// PlatformAuthSignoutParams are parameters for platform.authSignout.
type PlatformAuthSignoutParams struct {
	RefreshToken string `json:"refreshToken"`
}

// --- Auth methods ---

// AuthExchangeGitHubParams are parameters for auth.exchangeGitHub.
type AuthExchangeGitHubParams struct {
	GithubToken string `json:"github_token"`
}

// AuthDeviceFlowStartParams are parameters for auth.deviceFlowStart.
type AuthDeviceFlowStartParams struct{}

// AuthDeviceFlowPollParams are parameters for auth.deviceFlowPoll.
type AuthDeviceFlowPollParams struct {
	DeviceCode string `json:"device_code"`
}

// AuthRefreshParams are parameters for auth.refresh.
type AuthRefreshParams struct {
	RefreshToken string `json:"refresh_token"`
}

// --- Pipeline state notification methods (HeadlessOrchestrator path) ---

// PipelineNotifyStageTransitionParams are parameters for pipeline.notifyStageTransition.
type PipelineNotifyStageTransitionParams struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	Stage       string `json:"stage"`
	Status      string `json:"status"` // "initialized" | "running" | "model-resolved" | "complete" | "failed" | "skipped" | "deferred"
	Title       string `json:"title,omitempty"`
	Branch      string `json:"branch,omitempty"`
	BaseBranch  string `json:"baseBranch,omitempty"`
	Error       string `json:"error,omitempty"`
	// Per-stage usage for "complete" transitions, populated by the extension
	// from its accumulated stage usage (PipelineStateService.tokens.per_stage).
	// Previously the notify path completed stages with hardcoded zeros (#227),
	// so runtime-{N}.json and platform telemetry reported 0 in / 0 out / $0.
	// InputTokens excludes cache reads (CacheReadTokens is a separate field);
	// CompleteStageWithCost combines them, matching the scheduler path.
	InputTokens     int     `json:"inputTokens,omitempty"`
	OutputTokens    int     `json:"outputTokens,omitempty"`
	CacheReadTokens int     `json:"cacheReadTokens,omitempty"`
	CostUsd         float64 `json:"costUsd,omitempty"`
	// Model is the model that ACTUALLY served the stage per the CLI stream
	// (skillRunner servedModel / modelDecision, #91). Threaded here (#268) so
	// the notify handler records it as the runtime's per-stage StageModel and
	// BuildV2Record attributes each stage's ModelSelection — otherwise the V2
	// history record carries a null per-stage model on the VSCode-orchestrated
	// path and the platform's by-model cost breakdown (cost_events.model_id)
	// buckets every stage as 'unknown'.
	Model string `json:"model,omitempty"`
	// Adapter is the adapter/runtime that executed the stage (claude | codex |
	// gemini | gemini-sdk | lm-studio | ollama | copilot). Threaded here (#268)
	// so the notify handler records it as the runtime's per-stage StageAdapter;
	// BuildV2Record projects it onto V2StageTokens.Adapter, which the platform
	// mapper emits as StageMetric.provider (V5) — the wire field the platform
	// persists to cost_events.provider and backfills onto pipeline_events.adapter
	// (the source of the dashboard's Adapter Mix donut). Empty maps to
	// adapter-unknown, never defaulted to claude.
	Adapter string `json:"adapter,omitempty"`
}

// PipelineNotifyStageProgressParams are parameters for pipeline.notifyStageProgress.
// Emitted throttled (>=1 per 5s) by the TypeScript executor DURING a stage from
// a LIVE in-stage token/cost estimate (#233). Unlike notifyStageTransition's
// "complete" usage — which is authoritative, sourced from the terminal CLI
// `result` envelope — these are a growing-context snapshot: InputTokens and
// CacheReadTokens are latest-wins (the full context re-reported each turn),
// OutputTokens is summed per-turn, and CostUsd is pricing-table-computed.
// Best-effort and in-flight only: the handler never mutates CompletedStages, and
// the terminal "complete" transition reconciles the authoritative total. Model
// is attribution-only (optional).
type PipelineNotifyStageProgressParams struct {
	Repo            string  `json:"repo"`
	IssueNumber     int     `json:"issueNumber"`
	Stage           string  `json:"stage"`
	InputTokens     int     `json:"inputTokens,omitempty"`
	OutputTokens    int     `json:"outputTokens,omitempty"`
	CacheReadTokens int     `json:"cacheReadTokens,omitempty"`
	CostUsd         float64 `json:"costUsd,omitempty"`
	Model           string  `json:"model,omitempty"`
}

// PipelineNotifyCompleteParams are parameters for pipeline.notifyComplete.
// Emitted once by the TypeScript HeadlessOrchestrator when a full pipeline run
// terminates (success, failure, or cancellation). Drives the platform's
// `pipeline_done` event so the live Pipelines view transitions the run's status
// from 'running' to 'complete'/'failed'. The Go-scheduler path emits its own
// pipeline_done; this covers the extension/HeadlessOrchestrator path only.
type PipelineNotifyCompleteParams struct {
	Repo            string   `json:"repo"`
	IssueNumber     int      `json:"issueNumber"`
	Success         bool     `json:"success"`
	TotalDurationMs int      `json:"totalDurationMs"`
	StagesRun       []string `json:"stagesRun,omitempty"`
	// PrMerged is the forge-confirmed merge ground truth for the run's PR
	// (#266). When true, the recording boundary must NOT book the run as a
	// failure just because a late per-stage kill (progress-runaway / stall /
	// budget) reported the pr-merge stage failed AFTER the merge already landed.
	// The merge is the valuable outcome; recording it as a phantom stall_kill
	// corrupts failure stats and schedules 30m stall-kill backoff for a run that
	// is actually done. Only honored when the terminal stage is pr-merge.
	PrMerged bool `json:"prMerged,omitempty"`
	// Deferred marks this completion as a NON-FAILURE blocked-dependency
	// deferral (#305): the run was dispatched for an issue whose blockedBy
	// dependencies were still open, so the pipeline deferred before doing work.
	// The TS layer calls notifyComplete with success=false, deferred=true. When
	// true the recording boundary books the run as outcome="cancelled" (the
	// closest non-failure enum value), terminal_failure_kind="", and
	// outcome_type="deferred" — NOT a failure — and the live-view telemetry
	// emits as a non-failure terminal event, never "failed".
	Deferred bool `json:"deferred,omitempty"`
	// StageExecutionPaths records, per stage name, whether the stage ran via the
	// deterministic Go path or the LLM skill path ("deterministic" | "llm") on
	// the TypeScript dogfood path (Issue #297/#309). It is the wire form of the
	// HeadlessOrchestrator's in-memory stageExecutionPaths map — the Go runtime
	// never observed the extension-side deterministic-first pr-create/pr-merge
	// (and issue-pickup) decisions, so BuildV2Record wrote history stage records
	// with no execution_path. The notifyComplete handler replays these onto the
	// run's RuntimeState (RecordExecutionPath) BEFORE it snapshots for
	// BuildV2Record, so execution_path lands on the authoritative record. Absent
	// for runs with no deterministic-first stages.
	StageExecutionPaths map[string]string `json:"stageExecutionPaths,omitempty"`
	// StagePuntReasons records, per stage name, the machine-readable reason a
	// deterministic-first hook declined and the stage fell through to the LLM
	// path (Issue #297/#309). Only present alongside
	// StageExecutionPaths[stage]=="llm" when a deterministic hook actually ran
	// and punted (e.g. "missing-dev-context", "binary-unresolved"). Replayed onto
	// the RuntimeState (RecordStagePuntReason) so punt_reason lands on the
	// authoritative history stage record, letting operators read WHY the
	// expensive LLM path ran from history alone.
	StagePuntReasons map[string]string `json:"stagePuntReasons,omitempty"`
}

// PipelineNotifyPhaseTransitionParams are parameters for pipeline.notifyPhaseTransition.
type PipelineNotifyPhaseTransitionParams struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	Stage       string `json:"stage"`
	Name        string `json:"name"`
	Index       int    `json:"index"`
	Total       int    `json:"total"`
	EventType   string `json:"eventType"` // "start" | "complete"
}

// --- Tree update events ---

// TreeUpdateEvent is emitted when board items change status.
// Event names: "tree.ready.update", "tree.in-progress.update",
//
//	"tree.in-review.update", "tree.backlog.update"
type TreeUpdateEvent struct {
	Owner         string `json:"owner"`
	ProjectNumber int    `json:"projectNumber,omitempty"`
	ChangedItemID string `json:"changedItemId,omitempty"`
	NewStatus     string `json:"newStatus,omitempty"`
}

// --- Remote command methods ---

// RemoteGetCommandHistoryParams are parameters for remote.getCommandHistory.
// (no fields — Go reads from its own in-memory store)
type RemoteGetCommandHistoryParams struct{}

// RemoteCommandHistoryEntry is a single entry returned by remote.getCommandHistory.
type RemoteCommandHistoryEntry struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	Status      string  `json:"status"` // "success" | "failure" | "pending"
	ReceivedAt  string  `json:"receivedAt"`
	CompletedAt *string `json:"completedAt,omitempty"`
	DurationMs  int64   `json:"durationMs,omitempty"`
	Error       string  `json:"error,omitempty"`
}

// RemoteGetCommandHistoryResult is the result for remote.getCommandHistory.
type RemoteGetCommandHistoryResult struct {
	Commands []RemoteCommandHistoryEntry `json:"commands"`
}

// RemoteGetPollingStatusParams are parameters for remote.getPollingStatus.
// (no fields — Go reads from its own polling state)
type RemoteGetPollingStatusParams struct{}

// RemotePollingStatus is the result for remote.getPollingStatus.
type RemotePollingStatus struct {
	Active       bool    `json:"active"`
	LastPolledAt *string `json:"lastPolledAt,omitempty"`
	PendingCount int     `json:"pendingCount"`
	ErrorCount   int     `json:"errorCount"`
}

// --- Agent command methods ---

// AgentAcknowledgeCommandParams are parameters for agent.acknowledgeCommand.
type AgentAcknowledgeCommandParams struct {
	AgentID   string `json:"agentId"`
	CommandID string `json:"commandId"`
}

// AgentAcknowledgeCommandResult is the result for agent.acknowledgeCommand.
type AgentAcknowledgeCommandResult struct {
	RunID string `json:"runId"`
}

// PipelineSetMaxConcurrentParams are parameters for pipeline.setMaxConcurrent.
type PipelineSetMaxConcurrentParams struct {
	MaxConcurrent int  `json:"maxConcurrent"`
	Persist       bool `json:"persist"`
}

// PipelineGetMaxConcurrentParams are parameters for pipeline.getMaxConcurrent (no fields).
type PipelineGetMaxConcurrentParams struct{}

// FocusSetParams are parameters for focus.set.
type FocusSetParams struct {
	Lens string `json:"lens"`
}

// FocusShowParams are parameters for focus.show (no fields).
type FocusShowParams struct{}

// FocusClearParams are parameters for focus.clear (no fields).
type FocusClearParams struct{}

// FocusListParams are parameters for focus.list (no fields).
type FocusListParams struct{}

// AutonomousStartParams are parameters for autonomous.start.
// WorkspaceRepos limits the scheduler to only scan repos currently open in the
// VS Code workspace. When empty, the scheduler uses all repos detected at
// server startup (legacy behavior). Format: ["owner/repo", ...].
type AutonomousStartParams struct {
	WorkspaceRepos []string `json:"workspaceRepos,omitempty"`
}

// AutonomousResumeParams optionally restricts the scheduler to workspace repos
// on resume, preventing it from scanning sibling repos outside the workspace.
type AutonomousResumeParams struct {
	WorkspaceRepos []string `json:"workspaceRepos,omitempty"`
}

// AutonomousUpdateAllowlistParams updates the running scheduler's repo
// allowlist without restarting it. Used by the VS Code extension's
// Repositories tree checkbox so toggling a repo applies live (no
// blocking "Restart Autonomous?" modal). Same shape as
// AutonomousStartParams for symmetry — WorkspaceRepos is intersected
// with the user's autonomous.enabled_repos config (if any) inside
// the server. Issue #3429.
type AutonomousUpdateAllowlistParams struct {
	WorkspaceRepos []string `json:"workspaceRepos,omitempty"`
}

// AutonomousPauseParams carries the reason and source identifier so paused
// state on disk records *why* and *who* paused — eliminating the log archeology
// needed to diagnose stale-badge incidents (Issue #3251). Both fields are
// optional; legacy callers passing no params get "unknown" / "no reason
// provided" persisted instead of empty strings.
type AutonomousPauseParams struct {
	// Reason is a short human-readable explanation, e.g. "user requested via
	// UI", "haltQueueOnSlotFailure: issue #3239 failed at pr-merge".
	Reason string `json:"reason,omitempty"`
	// TriggeredBy is a structured tag identifying the caller, e.g. "user",
	// "haltQueueOnSlotFailure", "safety:rate-limit".
	TriggeredBy string `json:"triggeredBy,omitempty"`
}

// AutonomousCompleteParams notifies the autonomous scheduler that a dispatched
// pipeline run has completed (success or failure).
type AutonomousCompleteParams struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	Success     bool   `json:"success"`
	// ConflictRestart indicates the failure was due to unresolvable merge
	// conflicts (pr-merge stage exited after exhausting rebase attempts).
	// When true, the scheduler skips the circuit-breaker increment so that
	// concurrent-branch collisions don't trip the safety rail — they are
	// infrastructure-level failures, not code-quality failures.
	ConflictRestart bool `json:"conflictRestart,omitempty"`
	// TerminalFailureKind names the terminal failure category when Success
	// is false (see internal/orchestrator/failure_handler.go for the canonical
	// list). Environmental kinds — currently `stream_idle_timeout` (#3398) —
	// route through dedicated retry policies that don't penalize the issue's
	// lifetime failure count for an upstream-API problem and use a longer
	// backoff that clears the rate-limit window. Empty when unknown.
	TerminalFailureKind string `json:"terminalFailureKind,omitempty"`
	// FailureDetail is the raw failure text observed by the TS layer (e.g.
	// the kill marker emitted by skillRunner). Currently used by the
	// autonomous scheduler to extract `resetsAt=<unix>` from
	// `[rate-limit-quota-exhausted]` markers so the global Anthropic-quota
	// cooldown can run until the actual bucket reset rather than a fixed
	// 1-hour floor (#3431). Optional — empty falls back to the floor.
	FailureDetail string `json:"failureDetail,omitempty"`
}

// AutonomousClearIssueFailuresParams clears the per-issue lifetime failure
// counter. Empty Key clears all counters. #3020.
type AutonomousClearIssueFailuresParams struct {
	Key string `json:"key,omitempty"`
}

// AutonomousClearIssueFailuresResult reports how many issue counters were
// cleared.
type AutonomousClearIssueFailuresResult struct {
	Cleared int `json:"cleared"`
}

// AutonomousClearQuotaCooldownParams has no fields — the clear is unconditional.
// Kept as an explicit struct for forward-compatibility (e.g. a future "reason"
// audit field) and to match the params:result codegen convention.
type AutonomousClearQuotaCooldownParams struct{}

// AutonomousClearQuotaCooldownResult reports whether a cooldown was active
// (and therefore cleared) at the time of the IPC call. False means the
// cooldown was already absent — a no-op clear is still a successful response.
// Issue #3446.
type AutonomousClearQuotaCooldownResult struct {
	Cleared bool `json:"cleared"`
	// PreviousUntil is the ISO-8601 cooldown deadline that was in effect
	// immediately before the clear. Empty when no cooldown was active.
	PreviousUntil string `json:"previousUntil,omitempty"`
}

// WorkflowQuotaStateParams are parameters for workflow.quotaState.
//
// GitHubUser selects the per-identity GitHub rate-limit tracker entry the
// caller wants reflected (empty collapses to the shared "default" key). The
// snapshot is served from the persisted shared tracker (no live GraphQL probe)
// so the read is deterministic and sub-millisecond — the workflow executor can
// call it before every large fan-out without burning quota. Issue #3909.
type WorkflowQuotaStateParams struct {
	GitHubUser string `json:"githubUser,omitempty"`
}

// WorkflowQuotaStateResult is the bridged quota/cooldown snapshot the TS
// WorkflowExecutor (#3908) consults before fanning out a large agent batch.
// It single-sources the Go-side signals — the GitHub REST/GraphQL rate-limit
// bucket (from the shared tracker) and the global dispatch cooldown (from the
// autonomous scheduler, which covers both the Anthropic 5-hour bucket and the
// GitHub-quota suspension) — so NO quota arithmetic is duplicated in TypeScript.
//
// The executor distinguishes a genuine exhaustion (Exhausted=true → defer the
// fan-out until ResetsAt/CooldownUntil) from a transient status=allowed stall
// (Exhausted=false → proceed). Issue #3909.
type WorkflowQuotaStateResult struct {
	// Remaining is the GitHub API requests left in the current bucket, from the
	// shared rate-limit tracker. -1 when no tracker reading is available.
	Remaining int `json:"remaining"`
	// Limit is the GitHub API bucket size from the shared tracker. -1 when no
	// reading is available.
	Limit int `json:"limit"`
	// ResetsAt is the Unix-seconds timestamp at which the GitHub bucket refills.
	// 0 when no tracker reading is available.
	ResetsAt int64 `json:"resetsAt"`
	// CooldownUntil is the ISO-8601 wall-clock deadline until which the global
	// dispatch cooldown suspends new runs (Anthropic 5-hour bucket OR GitHub
	// quota). Empty when no cooldown is recorded.
	CooldownUntil string `json:"cooldownUntil,omitempty"`
	// CooldownReason is the human-readable reason carried alongside the cooldown
	// deadline. Empty when no cooldown is recorded.
	CooldownReason string `json:"cooldownReason,omitempty"`
	// Bucket names the binding constraint when Exhausted is true:
	// "anthropic-five-hour" / "github-quota" (from the active dispatch cooldown)
	// or "github-rest" (from a depleted tracker bucket). Empty when not
	// exhausted.
	Bucket string `json:"bucket,omitempty"`
	// Exhausted is the single derived signal the executor gates on: true when a
	// dispatch cooldown is currently active OR the GitHub tracker bucket is
	// depleted to zero. The decision is made in Go so the gate is single-sourced.
	Exhausted bool `json:"exhausted"`
}

// GitHubAuthCheckParams are parameters for github.authCheck.
type GitHubAuthCheckParams struct {
	GitHubUser string `json:"githubUser,omitempty"`
}

// GitHubAuthCheckResult is the result of a github.authCheck call.
type GitHubAuthCheckResult struct {
	Valid          bool     `json:"valid"`
	Login          string   `json:"login"`
	Scopes         []string `json:"scopes"`
	MissingScopes  []string `json:"missingScopes"`
	OrgMemberships []string `json:"orgMemberships"`
	Resolution     string   `json:"resolution"`
	Error          string   `json:"error,omitempty"`
}

// ForgeListResult is the result of forge.list.
type ForgeListResult struct {
	Forges []ForgeListEntry `json:"forges"`
}

// ForgeListEntry describes a single forge instance returned by forge.list.
type ForgeListEntry struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	BaseURL    string `json:"base_url"`
	AuthMethod string `json:"auth_method"`
	CABundle   string `json:"ca_bundle,omitempty"`
}

// ForgeConnectionTestParams are parameters for forge.connectionTest.
type ForgeConnectionTestParams struct {
	InstanceID string `json:"instance_id"`
	// Token is the credential to use for the test when not yet saved to config.
	Token string `json:"token,omitempty"`
}

// ForgeConnectionTestResult is the result of forge.connectionTest.
type ForgeConnectionTestResult struct {
	OK        bool     `json:"ok"`
	LatencyMs int64    `json:"latency_ms"`
	Version   string   `json:"version,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// KnowledgeMetricsParams are parameters for knowledge.metrics (#3600).
// WindowDays defaults to 7 when <= 0; StaleDays defaults to 30 when < 0.
type KnowledgeMetricsParams struct {
	WindowDays int `json:"windowDays,omitempty"`
	StaleDays  int `json:"staleDays,omitempty"`
}

// KnowledgeRecallHit mirrors recall.RecallHit for IPC transport (#2964).
// Kept in protocol.go so the codegen captures the field shape verbatim.
type KnowledgeRecallHit struct {
	Rank        int      `json:"rank"`
	Score       float64  `json:"score"`
	Path        string   `json:"path"`
	Kind        string   `json:"kind"`
	IssueNumber int      `json:"issue_number,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Snippet     string   `json:"snippet"`
	Graduated   bool     `json:"graduated,omitempty"`
}

// KnowledgeSearchParams is the payload for knowledge.search (#2964).
type KnowledgeSearchParams struct {
	Query string   `json:"query"`
	Scope []string `json:"scope,omitempty"` // ["local","cross-repo","workspace"]
	Tags  []string `json:"tags,omitempty"`
	Limit int      `json:"limit,omitempty"` // defaults to 10
}

// KnowledgeSearchResult is the response for knowledge.search.
type KnowledgeSearchResult struct {
	Hits      []KnowledgeRecallHit `json:"hits"`
	TotalHits int                  `json:"total_hits"`
}

// KnowledgeBacklinksParams is the payload for knowledge.backlinks (#2964).
type KnowledgeBacklinksParams struct {
	Path string `json:"path"` // workspace-relative path
}

// KnowledgeBacklinksResult is the response for knowledge.backlinks.
type KnowledgeBacklinksResult struct {
	Backlinks []string `json:"backlinks"` // paths that wiki-link to Path
}

// KnowledgeRelatedToIssueParams is the payload for knowledge.relatedToIssue (#2964).
type KnowledgeRelatedToIssueParams struct {
	IssueNumber int `json:"issueNumber"`
	Limit       int `json:"limit,omitempty"` // defaults to 10
}

// KnowledgeRelatedToIssueResult is the response for knowledge.relatedToIssue.
type KnowledgeRelatedToIssueResult struct {
	Hits []KnowledgeRecallHit `json:"hits"`
}

// RecordStageExitParams is the payload for the "diagnostics.recordStageExit"
// request (TS→Go) added in #3619 retro of #3340.
//
// Background: the original stage-exit diagnostic record wiring (#3605/PR
// #3608) attached `WriteStageExitRecord` only to `scheduler.runPipeline()`
// at internal/orchestrator/scheduler.go:2487. That path runs in CLI/auto
// mode and in VSCode runs that go through `IpcStageRunner`. The user's
// autonomous workflow uses the legacy TS-side
// `headlessOrchestrator.runPipeline()` (services.ts:1060) which never
// round-trips Go's scheduler, so no record was written for IPC-mode
// failures — every failure went into a black box. This IPC method is the
// parallel write path: TS calls it after each stage exit so the JSONL at
// `.nightgauge/pipeline/exit-records/<UTC-day>.jsonl` carries records
// from BOTH dispatch paths.
//
// Field semantics overlap with StageResultParams + StageExitRecord — TS
// callers should populate them identically. Optional fields use `omitempty`
// so a healthy-run record stays terse.
type RecordStageExitParams struct {
	// Required identity
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	Stage       string `json:"stage"`

	// Required outcome
	Success bool `json:"success"`

	// Optional execution context
	RunID          string `json:"runId,omitempty"`
	StageStartedAt string `json:"stageStartedAt,omitempty"`
	Model          string `json:"model,omitempty"`

	// Outcome details
	ExitCode     *int   `json:"exitCode,omitempty"`     // pointer so null distinguishes from 0
	TerminalKind string `json:"terminalKind,omitempty"` // pre-classified by TS, empty defers to Go classifier
	ErrorText    string `json:"errorText,omitempty"`
	ElapsedMs    int64  `json:"elapsedMs,omitempty"`
	IdleMsAtExit int64  `json:"idleMsAtExit,omitempty"`

	// Token / cost
	InputTokens         int     `json:"inputTokens,omitempty"`
	OutputTokens        int     `json:"outputTokens,omitempty"`
	CacheReadTokens     int     `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int     `json:"cacheCreationTokens,omitempty"`
	CostUsd             float64 `json:"costUsd,omitempty"`

	// Signal / kill source (when applicable)
	Signal       string `json:"signal,omitempty"`
	SignalSource string `json:"signalSource,omitempty"`

	// Forensic anchors
	SessionID       string `json:"sessionId,omitempty"`
	LastBashCommand string `json:"lastBashCommand,omitempty"`
	LastBashExit    *int   `json:"lastBashExit,omitempty"`
	StopHookErrored bool   `json:"stopHookErrored,omitempty"`
	StderrTail      string `json:"stderrTail,omitempty"`
}

// RecordStageExitResult is the response payload — `Recorded` is true on
// success. Included for forward compatibility if we add async/queued
// semantics later.
type RecordStageExitResult struct {
	Recorded bool `json:"recorded"`
}

// --- Action Center (DecisionRequest store, ADR 015 §E) ---

// AttentionListParams filters the DecisionRequest list. Both fields optional;
// the default is open-ish requests across all repos.
type AttentionListParams struct {
	// IncludeTerminal includes resolved/expired requests (default: open only).
	IncludeTerminal bool `json:"includeTerminal,omitempty"`
	// Repo restricts to a single "owner/name" when set.
	Repo string `json:"repo,omitempty"`
}

// AttentionResolveParams is the sole mutation: resolve a request by option id,
// with optional free-text steer that becomes pinned next-stage context (§G).
type AttentionResolveParams struct {
	ID        string `json:"id"`
	OptionID  string `json:"optionId"`
	Actor     string `json:"actor,omitempty"`
	SteerText string `json:"steerText,omitempty"`
	Note      string `json:"note,omitempty"`
}

// AttentionAcknowledgeParams marks a request seen (clears the badge) without
// resolving it.
type AttentionAcknowledgeParams struct {
	ID    string `json:"id"`
	Actor string `json:"actor,omitempty"`
}

// IssueRemoveBlockedByParams is the thin IPC wrapper the Action Center adds for
// the existing internal RemoveBlockedByNumber call (ADR 015 §B). Optional fields
// last so the generated TS signature keeps required params ahead of optional.
type IssueRemoveBlockedByParams struct {
	Owner         string `json:"owner"`
	Repo          string `json:"repo"`
	BlockedNumber int    `json:"blockedNumber"`
	BlockerNumber int    `json:"blockerNumber"`
	GitHubUser    string `json:"githubUser,omitempty"`
}
