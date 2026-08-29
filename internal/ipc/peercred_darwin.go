//go:build darwin

package ipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// platformPeerUID reads the peer uid via LOCAL_PEERCRED, the Darwin/BSD
// equivalent of SO_PEERCRED. The value is captured by the kernel at connect
// time, so it cannot be spoofed by the client.
func platformPeerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix socket connection (%T)", conn)
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("syscall conn: %w", err)
	}

	var cred *unix.Xucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("control: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("getsockopt LOCAL_PEERCRED: %w", credErr)
	}

	return cred.Uid, nil
}
