//go:build !windows

package task

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func tryPlatformStoreLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockPlatformStoreLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func isPlatformStoreLockContention(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
