package forgetypes

// TokenScopeInfo holds the result of a token scope validation check.
// Different forges expose different scope models (GitHub OAuth scopes,
// GitLab personal access token scopes, etc.); the field set here is the
// least common denominator.
type TokenScopeInfo struct {
	Scopes         []string `json:"scopes"`
	Login          string   `json:"login"`
	OrgMemberships []string `json:"orgMemberships"`
	Resolution     string   `json:"resolution"`
	MissingScopes  []string `json:"missingScopes"`
	Valid          bool     `json:"valid"`
	// ScopesAdvertised is false when the forge returned no scope list for the
	// token. On GitHub that is every fine-grained PAT and App installation
	// token: they carry per-repository permissions, not OAuth scopes, so
	// MissingScopes is meaningless for them and Valid is decided by what the
	// token can do, not by a scope name.
	ScopesAdvertised bool `json:"scopesAdvertised"`
}
