//go:build !darwin && !linux

package ipc

import "net"

// platformPeerUID reports that this platform has no peer-credential mechanism
// this package knows how to read.
//
// authorizeConn treats the sentinel as "allow, and let the 0600 socket mode be
// the control" rather than refusing every connection, which would make the
// daemon unusable here. The caller logs it once so the weaker posture is
// visible in the daemon's output rather than assumed from this file.
func platformPeerUID(_ net.Conn) (uint32, error) {
	return 0, ErrPeerCredUnsupported
}
