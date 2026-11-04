//go:build linux

package queuewait

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processStart is pid's start time in clock ticks since boot (/proc/<pid>/stat
// field 22). It is compared only with itself, so the unit does not matter.
func processStart(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// The command name (field 2) may hold spaces; fields resume after ')'.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is field 3 (state); field 22 is fields[19].
	if len(fields) < 20 {
		return 0, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	return strconv.ParseInt(fields[19], 10, 64)
}
