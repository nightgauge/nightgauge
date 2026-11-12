//go:build !darwin && !linux

package procstart

import "errors"

// Start is unavailable here; callers then record 0 and fall back to
// liveness alone.
func Start(int) (int64, error) { return 0, errors.New("process start time unsupported") }
