package github

// OwnerType distinguishes GitHub organizations from user accounts.
// GitHub's GraphQL API uses different root resolvers for each:
//   - organization(login: $owner) { projectV2(...) { ... } }
//   - user(login: $owner) { projectV2(...) { ... } }
type OwnerType string

const (
	OwnerTypeOrg  OwnerType = "org"
	OwnerTypeUser OwnerType = "user"
)

// IsUser returns true if this is a user-owned project.
func (o OwnerType) IsUser() bool {
	return o == OwnerTypeUser
}

// ParseOwnerType normalizes a string to OwnerType. Defaults to org.
func ParseOwnerType(s string) OwnerType {
	if s == "user" {
		return OwnerTypeUser
	}
	return OwnerTypeOrg
}
