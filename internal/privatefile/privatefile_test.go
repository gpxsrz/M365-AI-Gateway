package privatefile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteAtomicAndOpenRegular(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	if err := WriteAtomic(path, "private state", ".private-*", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, "private state", ".private-*", []byte("second")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%#o want=0600", info.Mode().Perm())
	}
	file, err := OpenRegular(path, "private state")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "second" {
		t.Fatalf("raw=%q want=second", raw)
	}
}

func TestPrivateFileRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRegular(link, "private state"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("OpenRegular symlink error=%v", err)
	}
	if err := WriteAtomic(link, "private state", ".private-*", []byte("replacement")); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("WriteAtomic symlink error=%v", err)
	}
}
