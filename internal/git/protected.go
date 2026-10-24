package git

import "fmt"

// ProtectedBranches is the one definition of the branches no automated flow may
// delete, archive or reset: the conventional trunk names plus the run's base
// branch. The grounding gate and the recovery transaction (#794) both consume
// it; a second hand-written copy of the set is a second answer to "which
// branches are off limits?" waiting to disagree.
func ProtectedBranches(base string) []string {
	set := []string{"main", "master"}
	if base != "" && base != "main" && base != "master" {
		set = append(set, base)
	}
	return set
}

// RefuseBranchMutation returns an error when branch must not be mutated:
// it is protected, it is the repository default branch, or it is the branch
// checked out in the current worktree. The default branch is resolved from the
// repository, so a trunk named neither main nor master is still refused. When
// the default branch cannot be determined the mutation is refused: an unknown
// default must not read as "safe to delete".
func (s *Service) RefuseBranchMutation(branch, base, currentBranch string) error {
	if branch == "" {
		return fmt.Errorf("refusing to mutate an empty branch name")
	}
	for _, p := range ProtectedBranches(base) {
		if branch == p {
			return fmt.Errorf("refusing to mutate protected branch %q", branch)
		}
	}
	if currentBranch != "" && branch == currentBranch {
		return fmt.Errorf("refusing to mutate the current branch %q", branch)
	}
	def, err := s.DefaultBranch()
	if err != nil {
		return fmt.Errorf("refusing to mutate %q: cannot determine default branch: %w", branch, err)
	}
	if branch == def {
		return fmt.Errorf("refusing to mutate default branch %q", branch)
	}
	return nil
}
