//go:build !unix

package sessionlog

import "os"

// appendOpenFlags is a plain append on a platform without O_NOFOLLOW.
// Nightgauge builds only darwin and linux; the path's Lstat and the open
// descriptor's own Stat still refuse anything but a regular file.
const appendOpenFlags = os.O_APPEND | os.O_CREATE | os.O_WRONLY
