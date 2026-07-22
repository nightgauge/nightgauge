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
}
