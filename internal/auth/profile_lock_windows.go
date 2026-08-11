//go:build windows

package auth

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockOAuthProfileFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped)
}

func unlockOAuthProfileFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
}
