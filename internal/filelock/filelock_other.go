//go:build !unix

package filelock

import (
	"errors"
	"os"
)

// ErrUnixOnly is returned where the lock is syscall.Flock.
// The first cut does not pretend Windows has that lock.
var ErrUnixOnly = errors.New("file lock requires unix")

func With(string, func(*os.File) error) error {
	return ErrUnixOnly
}
