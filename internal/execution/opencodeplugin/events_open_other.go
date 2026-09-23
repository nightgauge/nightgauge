//go:build !unix

package opencodeplugin

import "os"

// eventsOpenFlags is a plain read-only open on a platform without
// O_NOFOLLOW/O_NONBLOCK. Nightgauge builds only darwin and linux (the
// repository Makefile builds no other GOOS); the descriptor's own Stat still
// refuses anything but a regular file.
const eventsOpenFlags = os.O_RDONLY
