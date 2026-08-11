//go:build !windows

package auth

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockOAuthProfileFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func unlockOAuthProfileFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
