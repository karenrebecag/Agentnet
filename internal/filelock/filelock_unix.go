//go:build unix

package filelock

import (
	"os"
	"syscall"
)

// With holds an exclusive lock for the whole read-modify-write.
// A single O_APPEND of a 16 KiB line is not an atomic record.
func With(path string, fn func(*os.File) error) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn(f)
}
