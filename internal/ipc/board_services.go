package ipc

import (
	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/state"
)

// The daemon's board surface, and the one place it is constructed.
//
// #845 gave board reads a snapshot cache; #848 asked why a tree refresh inside
// the TTL still spent points. The answer was that nothing in the daemon went
// through the cache at all: every board verb built its own service inline from
// an identity-scoped client, so `board.list` read straight past the snapshot
// and the five mutating verbs wrote straight past the invalidation.
//
// Wrapping only the reads would have been worse than leaving it alone. The
// cache is correct because mutations drop the snapshot they invalidate; a
// read-through cache whose writers bypass the wrapper serves data it already
// knows to be wrong (docs/FAILURE_TAXONOMY.md § Read-Through Cache Without
// Write Interception). An operator dragging an item to Done would have kept
// seeing it in Ready for up to the TTL.
//
// The alternative considered and rejected was invalidating at each of the five
// mutating call sites. It is smaller, and it is the shape AGENTS.md tells us
// not to build: the sixth verb someone adds next year silently reintroduces the
// staleness, and nothing goes red. Interception belongs UNDER the call sites.
// So both halves are built here, together, and TestBoardServicesAreNotBypassed
// fails the build if any handler constructs one directly instead.

// boardServices is the read and write halves of one board, both bound to the
// daemon's shared snapshot cache.
//
// They are returned as a pair on purpose. Handing out a cached reader without
// the invalidating writer is the exact defect this file exists to prevent, so
// the accessor does not offer that shape.
type boardServices struct {
	Board   forge.BoardService
	Project forge.ProjectService
}

// boardServicesFor binds the daemon's shared cache to one board.
//
// projectNumber may be 0: the blocked-by verbs are not board-scoped, and a
// dependency edge is still a field board reads return. boardcache.WrapProject
// handles that by invalidating every board rather than none.
func (s *Server) boardServicesFor(c *gh.Client, owner string, projectNumber int, ownerType gh.OwnerType) boardServices {
	board := gh.NewBoardService(c, owner, projectNumber, ownerType)
	project := gh.NewProjectService(c, owner, projectNumber, ownerType)
	if s.boards == nil {
		return boardServices{Board: board, Project: project}
	}
	return boardServices{
		Board:   s.boards.Wrap(board, owner, projectNumber),
		Project: boardcache.WrapProject(s.boards, project, owner, projectNumber),
	}
}

// boardStateFor returns a BoardStateService whose writes invalidate the board.
//
// BoardStateService used to construct its own ProjectService, which put its
// status writes outside any wrapper — and `board.updateStatus` is the single
// most staleness-visible write the daemon makes. It now takes the service, so
// the same interception covers it.
func (s *Server) boardStateFor(c *gh.Client, owner string, projectNumber int, ownerType gh.OwnerType) *state.BoardStateService {
	svcs := s.boardServicesFor(c, owner, projectNumber, ownerType)
	return state.NewBoardStateService(svcs.Board, svcs.Project)
}
