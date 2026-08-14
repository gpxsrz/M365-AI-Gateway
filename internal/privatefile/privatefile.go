package privatefile

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenRegular opens a private regular file without following a symbolic link.
// Existing files are tightened to mode 0600 before being returned.
func OpenRegular(path, label string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must not be a symbolic link", label)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", label)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		return nil, fmt.Errorf("%s identity changed while opening", label)
	}
	if opened.Mode().Perm() != 0o600 {
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, fmt.Errorf("secure %s permissions: %w", label, err)
		}
	}
	return file, nil
}

// WriteAtomic persists private bytes using a same-directory temporary file,
// file fsync, atomic rename, and a best-effort directory fsync.
func WriteAtomic(path, label, tempPattern string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s must not be a symbolic link", label)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file", label)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
