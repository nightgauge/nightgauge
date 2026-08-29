//go:build linux

package ipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// platformPeerUID reads the peer uid via SO_PEERCRED. The value is captured by
// the kernel at connect time, so it cannot be spoofed by the client.
func platformPeerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix socket connection (%T)", conn)
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("syscall conn: %w", err)
	}

	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("control: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("getsockopt SO_PEERCRED: %w", credErr)
	}

	return cred.Uid, nil
}
