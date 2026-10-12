package tools

// Error is a tool call refused or failed, with a fixed code the transcript
// shows. A refusal happens before anything is written.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Error codes.
const (
	// CodeInvalidArguments: the arguments did not parse or are out of bounds.
	CodeInvalidArguments = "invalid_arguments"
	// CodeOutsideWorkspace: the path is absolute, climbs out of the
	// workspace root, or resolves outside it through a symlink.
	CodeOutsideWorkspace = "path_outside_workspace"
	// CodeSecretPath: the path names a secret-bearing file.
	CodeSecretPath = "path_secret"
	// CodeNotFound: nothing exists at the path.
	CodeNotFound = "path_not_found"
	// CodeNotText: the file is not text, or too large to read.
	CodeNotText = "not_text"
	// CodeRepoNotReadable: the poster may not read the repository.
	CodeRepoNotReadable = "repo_not_readable"
	// CodeNotRequested: the poster's own message in this turn does not ask
	// for the action.
	CodeNotRequested = "not_requested_by_poster"
	// CodePosterUnknown: the turn names no posting account to act for.
	CodePosterUnknown = "poster_unknown"
	// CodeAppUnavailable: a teammate's forge write needs the GitHub App
	// installation, and none is usable; no other identity is tried.
	CodeAppUnavailable = "app_unavailable"
	// CodeLimitReached: the turn already filed as many issues as one turn
	// may.
	CodeLimitReached = "limit_reached"
	// CodeForgeError: the forge refused or failed the request.
	CodeForgeError = "forge_error"
	// CodeUnknownTool: no conversation tool has that name.
	CodeUnknownTool = "unknown_tool"
	// CodeRepoNotInWorkspace: the workspace has no checkout of the
	// repository.
	CodeRepoNotInWorkspace = "repo_not_in_workspace"
	// CodeStateUnavailable: this machine's pipeline state cannot be read
	// for the turn.
	CodeStateUnavailable = "state_unavailable"
	// CodeHostedUnavailable: the hosted service's turn-scoped routes are
	// not reachable from this turn.
	CodeHostedUnavailable = "hosted_unavailable"
	// CodeHostedRefused: the hosted service refused the call; the message
	// carries its code (act_not_permitted, card_outside_target, ...).
	CodeHostedRefused = "hosted_refused"
	// CodeDuplicateAct: the turn already made, or tried, this act on this
	// card.
	CodeDuplicateAct = "duplicate_act"
)

func fail(code, msg string) *Error { return &Error{Code: code, Message: msg} }
