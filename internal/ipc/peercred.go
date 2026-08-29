package ipc

// Peer authentication for the workspace daemon socket (ADR 017 R-2, #378).
//
// #370 gave the socket a run IDENTITY: a caller can no longer collide with
// another run's identity by picking the wrong key. It deliberately did not
// close the FORGERY half - a writer that can reach the socket could still mint
// a fresh run id and drive a run of its own. This closes that half at the
// transport, before any verb is dispatched.
//
// What the check is
//
// The socket is a trusted-operator channel (ADR 015 section N). The trust
// boundary is therefore the operator's own uid, and the check is a peer
// credential comparison: the kernel reports the uid on the other end of the
// connection, and a uid that is not the daemon's own is refused before the
// request is even read.
//
// Why not rely on the 0600 socket mode alone
//
// ListenSocket already chmods the socket to 0600, and on Linux and macOS the
// kernel does enforce those permissions on connect(). That is a real control
// and it stays. It is not sufficient on its own for three reasons, all of
// which are about the permission bits ceasing to mean what they say:
//
//  1. The mode is set AFTER net.Listen returns, so there is a window in which
//     the socket exists at the default umask. The window is small; it is not
//     zero.
//  2. Not every filesystem carries the bits faithfully. A workspace on a
//     network or FUSE mount can silently widen them.
//  3. Historically, not every Unix enforced permissions on socket connect at
//     all. Relying on the bits alone makes correctness a property of the
//     platform rather than of this code.
//
// A peer credential check is decided in this process, from a value the kernel
// supplies about the connection itself, and none of the three apply to it.
//
// What this does NOT defend against, stated plainly
//
// A process running as the SAME uid is inside the declared trust boundary and
// is not defended against here. On a single-user developer machine that
// includes anything the operator runs - a package postinstall script, a
// compromised dependency. Unix domain sockets offer no mechanism to separate
// same-uid callers, and a shared secret on disk would not either: a same-uid
// process can read the secret file exactly as it can dial the socket.
//
// Closing that would mean changing the declared trust model, not adding a
// check here. ADR 015 section N and ADR 017 R-2 say so, and this file exists
// to close the cross-uid half rather than to imply the other half is closed.

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// ErrPeerCredUnsupported reports that this platform cannot tell us who is on
// the other end of the connection.
//
// It is a distinct sentinel from a failed lookup on purpose - the two get
// opposite policies in authorizeConn, and collapsing them would silently turn
// a broken lookup into an open door.
var ErrPeerCredUnsupported = errors.New("ipc: peer credentials unsupported on this platform")

// peerUID reports the uid of the process on the other end of conn.
//
// A package variable rather than a direct call so tests can present a peer uid
// the test process does not have. Without the seam the rejection path could
// only be exercised by running the suite as two different users, which is to
// say it would never be exercised.
var peerUID = platformPeerUID

// authorizeConn decides whether a freshly accepted connection may proceed.
//
// The three outcomes are deliberately different:
//
//   - uid matches         -> allow.
//   - uid does not match  -> refuse. This is the case the issue is about.
//   - lookup unsupported  -> allow, because refusing would make the daemon
//     unusable on the platform, and the 0600 socket mode still applies. The
//     caller logs this once so the weaker posture is visible rather than
//     assumed.
//   - lookup failed       -> refuse. On a platform that supports the lookup, a
//     failure is anomalous, and "I could not determine who you are" is not a
//     reason to let someone in.
func (s *Server) authorizeConn(conn net.Conn) error {
	uid, err := peerUID(conn)
	if errors.Is(err, ErrPeerCredUnsupported) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("peer credential check failed: %w", err)
	}

	if self := os.Getuid(); int(uid) != self {
		return fmt.Errorf("connection from uid %d refused: socket accepts uid %d only", uid, self)
	}
	return nil
}
