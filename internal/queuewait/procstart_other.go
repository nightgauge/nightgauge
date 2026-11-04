//go:build !darwin && !linux

package queuewait

import "errors"

// processStart is unavailable here; intervals then carry Start 0 and only
// the writer's liveness is checked.
func processStart(int) (int64, error) { return 0, errors.New("process start time unsupported") }
