package github

import "github.com/shurcooL/graphql"

// GraphQL query/mutation types for GitHub API.
// These are structured types consumed by shurcooL/graphql.

// --- Project Board Queries ---

// projectMetaQuery fetches project metadata (id, title, url) for an org-owned project.
type projectMetaQuery struct {
	Organization struct {
		ProjectV2 struct {
			ID    graphql.String
			Title graphql.String
			URL   graphql.String
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"organization(login: $owner)"`
}

// userProjectMetaQuery fetches project metadata (id, title, url) for a user-owned project.
type userProjectMetaQuery struct {
	User struct {
		ProjectV2 struct {
			ID    graphql.String
			Title graphql.String
			URL   graphql.String
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"user(login: $owner)"`
}

// projectV2Query fetches all items (no server-side filter).
type projectV2Query struct {
	Organization struct {
		ProjectV2 struct {
			ID    graphql.ID
			Title graphql.String
			Items struct {
				PageInfo pageInfo
				Nodes    []projectItemNode
			} `graphql:"items(first: $first, after: $after)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"organization(login: $owner)"`
}

// projectV2FilteredQuery fetches items with server-side status filtering.
// Uses the query: parameter for efficient single-page fetches.
type projectV2FilteredQuery struct {
	Organization struct {
		ProjectV2 struct {
			ID    graphql.ID
			Title graphql.String
			Items struct {
				PageInfo pageInfo
				Nodes    []projectItemNode
			} `graphql:"items(first: $first, after: $after, query: $query)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"organization(login: $owner)"`
}

// --- User-owned Project Board Queries ---
// These mirror the organization queries above but use user(login: $owner)
// for GitHub user accounts that are not organizations.

type userProjectV2Query struct {
	User struct {
		ProjectV2 struct {
			ID    graphql.ID
			Title graphql.String
			Items struct {
				PageInfo pageInfo
				Nodes    []projectItemNode
			} `graphql:"items(first: $first, after: $after)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"user(login: $owner)"`
}

type userProjectV2FilteredQuery struct {
	User struct {
		ProjectV2 struct {
			ID    graphql.ID
			Title graphql.String
			Items struct {
				PageInfo pageInfo
				Nodes    []projectItemNode
			} `graphql:"items(first: $first, after: $after, query: $query)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"user(login: $owner)"`
}

type projectItemNode struct {
	ID      graphql.ID
	Content projectItemContent `graphql:"content"`
	// Field values are fetched via fieldValues.
	// We only read 4 project fields (Status, Priority, Size, Pipeline Stage)
	// so first:8 is double our actual need with margin. Was 20 (#3587 follow-up
	// — board scan is the highest-frequency query and its cost scales with the
	// sum of nested `first` values).
	FieldValues struct {
		Nodes []fieldValueNode
	} `graphql:"fieldValues(first: 8)"`
}

// projectItemContent is the per-item fragment of the board scan query
// (queryProjectItems / queryProjectItemsFiltered). All nested `first` values
// here multiply by the page size (100), so each unit of `first` is 100 nodes
// of cost. The values below are tuned for the workspace's actual data and
// fall back to GetIssue / GetEpicProgress for the rare cases needing more.
//
// Per-item cost contribution (sum of nested first values):
//
//	BEFORE: 20 (labels) + 50 (subIssues) + 50×20 (subIssue labels) + 10 + 10 = 1090
//	AFTER:   8 (labels) + 12 (subIssues) + 12×3 (subIssue labels) +  5 +  5 = 66
//
// At 100 items/page that's a 16× drop in query cost. Sub-issue labels are
// rarely consumed (board scan only uses sub-issue id/number/state), so the
// reduction there is essentially free.
type projectItemContent struct {
	TypeName    string `graphql:"__typename"`
	IssueFields struct {
		Number    graphql.Int
		Title     graphql.String
		State     graphql.String
		URL       graphql.String
		CreatedAt graphql.String
		UpdatedAt graphql.String
		Labels    struct {
			Nodes []labelNode
		} `graphql:"labels(first: 8)"`
		Repository struct {
			NameWithOwner graphql.String
		}
		// Board scan only needs IsEpic detection (len > 0) and a short
		// reference list for tree views. Full epic enumeration is the job
		// of GetEpicProgress, which uses nodeQuery (kept at first: 50).
		SubIssues struct {
			Nodes []subIssueNode
		} `graphql:"subIssues(first: 12)"`
		BlockedBy struct {
			Nodes []blockingNode
		} `graphql:"blockedBy(first: 5)"`
		Blocking struct {
			Nodes []blockingNode
		} `graphql:"blocking(first: 5)"`
		Parent struct {
			Number graphql.Int
			Title  graphql.String
		}
	} `graphql:"... on Issue"`
	PRFields struct {
		Number    graphql.Int
		Title     graphql.String
		State     graphql.String
		URL       graphql.String
		CreatedAt graphql.String
		UpdatedAt graphql.String
		Labels    struct {
			Nodes []labelNode
		} `graphql:"labels(first: 8)"`
		Repository struct {
			NameWithOwner graphql.String
		}
	} `graphql:"... on PullRequest"`
}

type fieldValueNode struct {
	TypeName               string `graphql:"__typename"`
	ProjectV2ItemFieldText struct {
		Text  graphql.String
		Field struct {
			ProjectV2Field struct {
				Name graphql.String
			} `graphql:"... on ProjectV2Field"`
		}
	} `graphql:"... on ProjectV2ItemFieldTextValue"`
	ProjectV2ItemFieldSingleSelect struct {
		Name  graphql.String
		Field struct {
			ProjectV2SingleSelectField struct {
				Name graphql.String
			} `graphql:"... on ProjectV2SingleSelectField"`
		}
	} `graphql:"... on ProjectV2ItemFieldSingleSelectValue"`
	ProjectV2ItemFieldNumber struct {
		Number graphql.Float
		Field  struct {
			ProjectV2Field struct {
				Name graphql.String
			} `graphql:"... on ProjectV2Field"`
		}
	} `graphql:"... on ProjectV2ItemFieldNumberValue"`
}

type labelNode struct {
	Name graphql.String
}

type pageInfo struct {
	HasNextPage graphql.Boolean
	EndCursor   graphql.String
}

// --- Issue Queries ---

// issueQuery is used by GetIssue (single issue) and indirectly by
// GetEpicProgressByNumber. SubIssues is kept higher than the board scan (25
// vs 12) because epic-progress-by-number relies on this path for accuracy.
// Epics with > 25 sub-issues should be queried via the dedicated
// GetEpicProgress (nodeQuery) which is paginated separately.
type issueQuery struct {
	Repository struct {
		Issue struct {
			ID          graphql.ID
			Number      graphql.Int
			Title       graphql.String
			Body        graphql.String
			State       graphql.String
			StateReason graphql.String
			URL         graphql.String
			Parent      struct {
				ID     graphql.ID
				Number graphql.Int
				Title  graphql.String
			}
			Labels struct {
				Nodes []labelNode
			} `graphql:"labels(first: 10)"`
			Assignees struct {
				Nodes []assigneeNode
			} `graphql:"assignees(first: 5)"`
			SubIssues struct {
				Nodes []subIssueNode
			} `graphql:"subIssues(first: 25)"`
			BlockedBy struct {
				Nodes []blockingNode
			} `graphql:"blockedBy(first: 5)"`
			Blocking struct {
				Nodes []blockingNode
			} `graphql:"blocking(first: 5)"`
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// subIssueNode is the sub-issue projection used inside parent queries (board
// scan + issue view). Sub-issue labels are only consumed by GetEpicProgress
// for type-detection — that path uses nodeQuery directly. Reducing labels
// here from 20 → 3 is a 17-per-node savings that compounds because subIssues
// is itself a nested connection. Hot-path savings: 100 items × 12 subIssues
// × 17 labels = 20,400 nodes/page off the budget.
type subIssueNode struct {
	ID         graphql.ID
	Number     graphql.Int
	Title      graphql.String
	State      graphql.String
	Repository struct {
		NameWithOwner graphql.String
	}
	Labels struct {
		Nodes []labelNode
	} `graphql:"labels(first: 3)"`
}

type blockingNode struct {
	ID         graphql.ID
	Number     graphql.Int
	Title      graphql.String
	State      graphql.String
	Repository struct {
		NameWithOwner graphql.String
	}
}

type assigneeNode struct {
	Login graphql.String
}

// --- GraphQL Enum Types ---
// Named types matching GitHub's GraphQL enum names are required by the
// shurcooL client, which derives GraphQL type names via reflect.Type.Name().

// PullRequestState is the GitHub GraphQL PullRequestState enum.
type PullRequestState string

// --- GraphQL Input Types ---
// Named types are REQUIRED by the shurcooL graphql client.
// Anonymous structs produce empty type names via reflect.Type.Name(),
// causing "Missing type definition for variable" errors from GitHub's API.

type CreateIssueInput struct {
	RepositoryID graphql.ID     `json:"repositoryId"`
	Title        graphql.String `json:"title"`
	Body         graphql.String `json:"body"`
	LabelIds     []graphql.ID   `json:"labelIds,omitempty"`
}

type CloseIssueInput struct {
	IssueID graphql.ID `json:"issueId"`
}

type ReopenIssueInput struct {
	IssueID graphql.ID `json:"issueId"`
}

type AddLabelsToLabelableInput struct {
	LabelableID graphql.ID   `json:"labelableId"`
	LabelIDs    []graphql.ID `json:"labelIds"`
}

type RemoveLabelsFromLabelableInput struct {
	LabelableID graphql.ID   `json:"labelableId"`
	LabelIDs    []graphql.ID `json:"labelIds"`
}

type AddSubIssueInput struct {
	IssueID    graphql.ID `json:"issueId"`
	SubIssueID graphql.ID `json:"subIssueId"`
}

type RemoveSubIssueInput struct {
	IssueID    graphql.ID `json:"issueId"`
	SubIssueID graphql.ID `json:"subIssueId"`
}

type AddBlockedByInput struct {
	IssueID         graphql.ID `json:"issueId"`
	BlockingIssueID graphql.ID `json:"blockingIssueId"`
}

type RemoveBlockedByInput struct {
	IssueID         graphql.ID `json:"issueId"`
	BlockingIssueID graphql.ID `json:"blockingIssueId"`
}

type CreatePullRequestInput struct {
	RepositoryID graphql.ID     `json:"repositoryId"`
	Title        graphql.String `json:"title"`
	Body         graphql.String `json:"body"`
	HeadRefName  graphql.String `json:"headRefName"`
	BaseRefName  graphql.String `json:"baseRefName"`
}

type MergePullRequestInput struct {
	PullRequestID graphql.ID     `json:"pullRequestId"`
	MergeMethod   graphql.String `json:"mergeMethod"`
}

type DeleteRefInput struct {
	RefID graphql.ID `json:"refId"`
}

type AddCommentInput struct {
	SubjectID graphql.ID     `json:"subjectId"`
	Body      graphql.String `json:"body"`
}

// --- Issue Mutations ---

type createIssueMutation struct {
	CreateIssue struct {
		Issue struct {
			ID     graphql.ID
			Number graphql.Int
			URL    graphql.String
		}
	} `graphql:"createIssue(input: $input)"`
}

type closeIssueMutation struct {
	CloseIssue struct {
		Issue struct {
			ID graphql.ID
		}
	} `graphql:"closeIssue(input: $input)"`
}

type reopenIssueMutation struct {
	ReopenIssue struct {
		Issue struct {
			ID graphql.ID
		}
	} `graphql:"reopenIssue(input: $input)"`
}

type addLabelsMutation struct {
	AddLabelsToLabelable struct {
		Labelable struct {
			TypeName string `graphql:"__typename"`
		}
	} `graphql:"addLabelsToLabelable(input: $input)"`
}

type removeLabelsMutation struct {
	RemoveLabelsFromLabelable struct {
		Labelable struct {
			TypeName string `graphql:"__typename"`
		}
	} `graphql:"removeLabelsFromLabelable(input: $input)"`
}

type addSubIssueMutation struct {
	AddSubIssue struct {
		Issue struct {
			ID graphql.ID
		}
	} `graphql:"addSubIssue(input: $input)"`
}

type removeSubIssueMutation struct {
	RemoveSubIssue struct {
		Issue struct {
			ID graphql.ID
		}
	} `graphql:"removeSubIssue(input: $input)"`
}

type addCommentMutation struct {
	AddComment struct {
		CommentEdge struct {
			Node struct {
				ID graphql.ID
			}
		}
	} `graphql:"addComment(input: $input)"`
}

// --- PR Queries ---

type pullRequestQuery struct {
	Repository struct {
		PullRequest struct {
			ID               graphql.ID
			Number           graphql.Int
			Title            graphql.String
			Body             graphql.String
			State            graphql.String
			HeadRefName      graphql.String
			BaseRefName      graphql.String
			URL              graphql.String
			Mergeable        graphql.String
			MergeStateStatus graphql.String
			IsDraft          graphql.Boolean
			ReviewDecision   graphql.String
			Additions        graphql.Int
			Deletions        graphql.Int
			// MergedAt + MergeCommit are populated only once the PR is MERGED —
			// the post-merge ground-truth breadcrumb (#4133). MergeCommit is a
			// pointer because it is null on un-merged PRs.
			MergedAt    graphql.String
			MergeCommit *struct {
				OID graphql.String
			}
			Labels struct {
				Nodes []labelNode
			} `graphql:"labels(first: 20)"`
			Commits struct {
				Nodes []struct {
					Commit struct {
						StatusCheckRollup *struct {
							State graphql.String
						}
					}
				}
			} `graphql:"commits(last: 1)"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type pullRequestListQuery struct {
	Repository struct {
		PullRequests struct {
			Nodes []struct {
				ID          graphql.ID
				Number      graphql.Int
				Title       graphql.String
				State       graphql.String
				HeadRefName graphql.String
				BaseRefName graphql.String
				URL         graphql.String
				IsDraft     graphql.Boolean
				CreatedAt   graphql.String
				Labels      struct {
					Nodes []labelNode
				} `graphql:"labels(first: 20)"`
			}
		} `graphql:"pullRequests(first: $first, states: $states, headRefName: $headRef)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type pullRequestListByStateQuery struct {
	Repository struct {
		PullRequests struct {
			Nodes []struct {
				ID          graphql.ID
				Number      graphql.Int
				Title       graphql.String
				State       graphql.String
				HeadRefName graphql.String
				BaseRefName graphql.String
				URL         graphql.String
				IsDraft     graphql.Boolean
				CreatedAt   graphql.String
				Labels      struct {
					Nodes []labelNode
				} `graphql:"labels(first: 20)"`
			}
		} `graphql:"pullRequests(first: $first, states: $states)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type createPullRequestMutation struct {
	CreatePullRequest struct {
		PullRequest struct {
			ID     graphql.ID
			Number graphql.Int
			URL    graphql.String
		}
	} `graphql:"createPullRequest(input: $input)"`
}

type mergePullRequestMutation struct {
	MergePullRequest struct {
		PullRequest struct {
			ID          graphql.ID
			State       graphql.String
			MergeCommit *struct {
				OID graphql.String
			}
		}
	} `graphql:"mergePullRequest(input: $input)"`
}

// repositoryRefQuery fetches a git ref's node ID for deletion.
type repositoryRefQuery struct {
	Repository struct {
		Ref *struct {
			ID graphql.ID
		} `graphql:"ref(qualifiedName: $ref)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// deleteRefMutation deletes a git ref (branch) by node ID.
type deleteRefMutation struct {
	DeleteRef struct {
		ClientMutationID *graphql.String
	} `graphql:"deleteRef(input: $input)"`
}

// --- Issue Update ---

// UpdateIssueInput is the named input type for the updateIssue mutation.
// All fields except ID are optional; pointer + omitempty lets callers
// patch any subset of attributes in a single mutation.
type UpdateIssueInput struct {
	ID          graphql.ID      `json:"id"`
	Title       *graphql.String `json:"title,omitempty"`
	Body        *graphql.String `json:"body,omitempty"`
	LabelIDs    *[]graphql.ID   `json:"labelIds,omitempty"`
	AssigneeIDs *[]graphql.ID   `json:"assigneeIds,omitempty"`
	State       *graphql.String `json:"state,omitempty"` // "OPEN" or "CLOSED"
	MilestoneID *graphql.ID     `json:"milestoneId,omitempty"`
}

type updateIssueMutation struct {
	UpdateIssue struct {
		Issue struct {
			ID     graphql.ID
			Number graphql.Int
			Title  graphql.String
			Body   graphql.String
			State  graphql.String
		}
	} `graphql:"updateIssue(input: $input)"`
}

// --- PR Update / Close ---

// UpdatePullRequestInput is the named input type for the updatePullRequest
// mutation. All fields except PullRequestID are optional.
type UpdatePullRequestInput struct {
	PullRequestID graphql.ID      `json:"pullRequestId"`
	Title         *graphql.String `json:"title,omitempty"`
	Body          *graphql.String `json:"body,omitempty"`
	BaseRefName   *graphql.String `json:"baseRefName,omitempty"`
	State         *graphql.String `json:"state,omitempty"` // "OPEN" or "CLOSED"
}

type updatePullRequestMutation struct {
	UpdatePullRequest struct {
		PullRequest struct {
			ID          graphql.ID
			Number      graphql.Int
			Title       graphql.String
			Body        graphql.String
			State       graphql.String
			HeadRefName graphql.String
			BaseRefName graphql.String
			IsDraft     graphql.Boolean
		}
	} `graphql:"updatePullRequest(input: $input)"`
}

// ClosePullRequestInput is the named input type for the closePullRequest
// mutation.
type ClosePullRequestInput struct {
	PullRequestID graphql.ID `json:"pullRequestId"`
}

type closePullRequestMutation struct {
	ClosePullRequest struct {
		PullRequest struct {
			ID    graphql.ID
			State graphql.String
		}
	} `graphql:"closePullRequest(input: $input)"`
}

// --- Search Query ---

// searchIssueNode represents an issue node inside a search result.
type searchIssueNode struct {
	TypeName   string `graphql:"__typename"`
	ID         graphql.ID
	Number     graphql.Int
	Title      graphql.String
	State      graphql.String
	URL        graphql.String
	Repository struct {
		NameWithOwner graphql.String
	}
	Labels struct {
		Nodes []labelNode
	} `graphql:"labels(first: 10)"`
}

// searchIssuesQuery uses the top-level search() field to find issues.
type searchIssuesQuery struct {
	Search struct {
		IssueCount graphql.Int
		Nodes      []searchIssueNode
	} `graphql:"search(query: $q, type: ISSUE, first: $limit)"`
}

// --- Blocking Mutations ---

type addBlockedByMutation struct {
	AddBlockedBy struct {
		ClientMutationID *graphql.String
	} `graphql:"addBlockedBy(input: $input)"`
}

type removeBlockedByMutation struct {
	RemoveBlockedBy struct {
		ClientMutationID *graphql.String
	} `graphql:"removeBlockedBy(input: $input)"`
}

// --- Project Field Introspection (full) ---

type projectFieldsFullQuery struct {
	Organization struct {
		ProjectV2 struct {
			ID     graphql.String
			Fields struct {
				Nodes []projectFieldFullNode
			} `graphql:"fields(first: 30)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"organization(login: $owner)"`
}

type userProjectFieldsFullQuery struct {
	User struct {
		ProjectV2 struct {
			ID     graphql.String
			Fields struct {
				Nodes []projectFieldFullNode
			} `graphql:"fields(first: 30)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"user(login: $owner)"`
}

type projectFieldFullNode struct {
	TypeName     string `graphql:"__typename"`
	GenericField struct {
		ID       graphql.String
		Name     graphql.String
		DataType graphql.String
	} `graphql:"... on ProjectV2Field"`
	SingleSelect struct {
		ID      graphql.String
		Name    graphql.String
		Options []struct {
			ID   graphql.String
			Name graphql.String
		}
	} `graphql:"... on ProjectV2SingleSelectField"`
	Iteration struct {
		ID            graphql.String
		Name          graphql.String
		Configuration struct {
			Iterations []struct {
				ID    graphql.String
				Title graphql.String
			}
		} `graphql:"configuration"`
	} `graphql:"... on ProjectV2IterationField"`
}

// --- Project Item Add ---

type addProjectItemMutation struct {
	AddProjectV2ItemById struct {
		Item struct {
			ID graphql.String
		}
	} `graphql:"addProjectV2ItemById(input: $input)"`
}

type AddProjectV2ItemByIdInput struct {
	ProjectID graphql.ID `json:"projectId"`
	ContentID graphql.ID `json:"contentId"`
}

// --- Project Item Lookup (for findItemID) ---

// issueProjectItemsQuery looks up an issue's project board item ID directly
// via the issue's projectItems connection. This is O(1) vs the old approach
// of paginating through all project items.
type issueProjectItemsQuery struct {
	Repository struct {
		Issue struct {
			ProjectItems struct {
				Nodes []struct {
					ID      graphql.String
					Project struct {
						Number graphql.Int
					}
				}
			} `graphql:"projectItems(first: 10)"`
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// issueProjectItemWithFieldsQuery looks up an issue's project board item ID
// and its field values in a single query. Used to read the current Estimate
// value before conditionally setting it.
type issueProjectItemWithFieldsQuery struct {
	Repository struct {
		Issue struct {
			ProjectItems struct {
				Nodes []struct {
					ID      graphql.String
					Project struct {
						Number graphql.Int
					}
					FieldValues struct {
						Nodes []fieldValueNode
					} `graphql:"fieldValues(first: 20)"`
				}
			} `graphql:"projectItems(first: 10)"`
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// projectItemsLookupQuery is the legacy scan-all-items approach (kept for
// PR lookups where projectItems is not available on PullRequest type).
//
//nolint:unused // kept for backward compatibility
type projectItemsLookupQuery struct {
	Organization struct {
		ProjectV2 struct {
			Items struct {
				PageInfo pageInfo
				Nodes    []projectItemLookupNode
			} `graphql:"items(first: $first, after: $after)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"organization(login: $owner)"`
}

type projectItemLookupNode struct {
	ID      graphql.String
	Content struct {
		TypeName      string `graphql:"__typename"`
		IssueFragment struct {
			Number     graphql.Int
			Repository struct {
				NameWithOwner graphql.String
			}
		} `graphql:"... on Issue"`
		PRFragment struct {
			Number     graphql.Int
			Repository struct {
				NameWithOwner graphql.String
			}
		} `graphql:"... on PullRequest"`
	}
}

// --- Field Update Mutations ---

// UpdateProjectV2ItemFieldValueInput matches GitHub's GraphQL input type exactly.
// The shurcooL library derives the GraphQL type name from the Go struct name.
type UpdateProjectV2ItemFieldValueInput struct {
	ProjectID graphql.ID          `json:"projectId"`
	ItemID    graphql.ID          `json:"itemId"`
	FieldID   graphql.ID          `json:"fieldId"`
	Value     ProjectV2FieldValue `json:"value"`
}

// ProjectV2FieldValue matches GitHub's GraphQL input type.
// All fields are optional (omitempty) — set only the one relevant to the field type.
type ProjectV2FieldValue struct {
	SingleSelectOptionID *graphql.String `json:"singleSelectOptionId,omitempty"`
	Number               *graphql.Float  `json:"number,omitempty"`
	Text                 *graphql.String `json:"text,omitempty"`
	IterationID          *graphql.String `json:"iterationId,omitempty"`
	Date                 *graphql.String `json:"date,omitempty"`
}

type updateProjectFieldMutation struct {
	UpdateProjectV2ItemFieldValue struct {
		ClientMutationID *graphql.String
	} `graphql:"updateProjectV2ItemFieldValue(input: $input)"`
}

// --- Project Field Creation/Update Mutations ---

// SingleSelectFieldOption is a single option for a SINGLE_SELECT project field.
// Color must be a ProjectV2SingleSelectFieldOptionColor enum value (e.g. BLUE, RED, GREEN).
type SingleSelectFieldOption struct {
	Name        graphql.String `json:"name"`
	Color       graphql.String `json:"color"`
	Description graphql.String `json:"description"`
}

// CreateProjectV2FieldInput matches GitHub's GraphQL input type for createProjectV2Field.
type CreateProjectV2FieldInput struct {
	ProjectID           graphql.ID                `json:"projectId"`
	DataType            graphql.String            `json:"dataType"`
	Name                graphql.String            `json:"name"`
	SingleSelectOptions []SingleSelectFieldOption `json:"singleSelectOptions,omitempty"`
}

type createProjectV2FieldMutation struct {
	CreateProjectV2Field struct {
		ProjectV2Field struct {
			ProjectV2FieldCommon struct {
				ID graphql.String
			} `graphql:"... on ProjectV2FieldCommon"`
		} `graphql:"projectV2Field"`
	} `graphql:"createProjectV2Field(input: $input)"`
}

// UpdateProjectV2FieldInput matches GitHub's GraphQL input type for updateProjectV2Field.
// SingleSelectOptions replaces the full option set on the field.
type UpdateProjectV2FieldInput struct {
	FieldID             graphql.ID                `json:"fieldId"`
	SingleSelectOptions []SingleSelectFieldOption `json:"singleSelectOptions,omitempty"`
}

type updateProjectV2FieldMutation struct {
	UpdateProjectV2Field struct {
		ProjectV2Field struct {
			ProjectV2FieldCommon struct {
				ID graphql.String
			} `graphql:"... on ProjectV2FieldCommon"`
		} `graphql:"projectV2Field"`
	} `graphql:"updateProjectV2Field(input: $input)"`
}

// --- Label Queries and Mutations ---

// labelDetailNode holds full label fields for list/mutation responses.
// Distinct from labelNode (name only) which is used for issue label queries.
type labelDetailNode struct {
	ID          graphql.ID
	Name        graphql.String
	Description graphql.String
	Color       graphql.String
}

// listLabelsQuery fetches all labels for a repository (first 100).
type listLabelsQuery struct {
	Repository struct {
		Labels struct {
			Nodes []labelDetailNode
		} `graphql:"labels(first: 100)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// CreateLabelInput is the named input type required by shurcooL/graphql for the
// createLabel mutation. The type name is derived via reflect.Type.Name().
type CreateLabelInput struct {
	RepositoryID graphql.ID     `json:"repositoryId"`
	Name         graphql.String `json:"name"`
	Description  graphql.String `json:"description"`
	Color        graphql.String `json:"color"`
}

// DeleteLabelInput is the named input type for the deleteLabel mutation.
type DeleteLabelInput struct {
	ID graphql.ID `json:"id"`
}

type createLabelMutation struct {
	CreateLabel struct {
		Label labelDetailNode
	} `graphql:"createLabel(input: $input)"`
}

type deleteLabelMutation struct {
	DeleteLabel struct {
		ClientMutationID *graphql.String
	} `graphql:"deleteLabel(input: $input)"`
}

// --- View Queries ---

// viewNode holds the fields returned by the projectV2.views GraphQL query.
type viewNode struct {
	ID     graphql.ID
	Name   graphql.String
	Layout graphql.String
}

// projectV2ViewsQuery fetches all views for an organization-owned project board.
type projectV2ViewsQuery struct {
	Organization struct {
		ProjectV2 struct {
			Views struct {
				Nodes []viewNode
			} `graphql:"views(first: 100)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"organization(login: $owner)"`
}

// userProjectV2ViewsQuery fetches all views for a user-owned project board.
type userProjectV2ViewsQuery struct {
	User struct {
		ProjectV2 struct {
			Views struct {
				Nodes []viewNode
			} `graphql:"views(first: 100)"`
		} `graphql:"projectV2(number: $projectNumber)"`
	} `graphql:"user(login: $owner)"`
}

// --- Node Query (for cross-repo lookups) ---

type nodeQuery struct {
	Node struct {
		TypeName string `graphql:"__typename"`
		Issue    struct {
			ID         graphql.ID
			Number     graphql.Int
			Title      graphql.String
			State      graphql.String
			Repository struct {
				NameWithOwner graphql.String
			}
			SubIssues struct {
				Nodes []subIssueNode
			} `graphql:"subIssues(first: 50)"`
		} `graphql:"... on Issue"`
	} `graphql:"node(id: $id)"`
}

// --- Project-Linked Repositories Queries ---
// Used by FetchProjectLinkedRepos to enumerate repos linked to a ProjectV2.
// Capped at 100 — sufficient for any real workspace.

type projectLinkedReposQuery struct {
	Organization struct {
		ProjectV2 struct {
			Repositories struct {
				Nodes []struct {
					Name  graphql.String
					Owner struct {
						Login graphql.String
					}
				}
			} `graphql:"repositories(first: 100)"`
		} `graphql:"projectV2(number: $number)"`
	} `graphql:"organization(login: $owner)"`
}

type userProjectLinkedReposQuery struct {
	User struct {
		ProjectV2 struct {
			Repositories struct {
				Nodes []struct {
					Name  graphql.String
					Owner struct {
						Login graphql.String
					}
				}
			} `graphql:"repositories(first: 100)"`
		} `graphql:"projectV2(number: $number)"`
	} `graphql:"user(login: $owner)"`
}

// repositoryLinkedProjectsQuery enumerates ProjectV2 boards linked to one
// repository. The repository owner is also the project owner for linkable
// organization/user projects, so callers can use the requested owner as the
// stable display/routing owner without querying the ProjectV2Owner union.
type repositoryLinkedProjectsQuery struct {
	Repository struct {
		ProjectsV2 struct {
			Nodes []struct {
				ID     graphql.ID
				Number graphql.Int
				Title  graphql.String
			}
		} `graphql:"projectsV2(first: 100)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}
