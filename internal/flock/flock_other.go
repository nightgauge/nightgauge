//go:build !unix

package flock

import (
	"os"
	"time"
)

const supported = false

func exclusive(_ *os.File, _ time.Duration) error { return ErrUnsupported }

func unlock(_ *os.File) error { return nil }
