//go:build windows
// +build windows

package overseer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	supported = true
	uid       = syscall.Getuid()
	gid       = syscall.Getgid()
	SIGUSR1   = syscall.SIGTERM
	SIGUSR2   = syscall.SIGTERM
	SIGTERM   = syscall.SIGTERM
)

func move(dst, src string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

func overwrite(dst, src string) error {
	// The master keeps the original image mapped across every upgrade. A fixed
	// backup name would therefore block the second replacement on Windows.
	f, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".overseer-previous-*"+extension())
	if err != nil {
		return err
	}
	backup := f.Name()
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	if err := os.Rename(dst, backup); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		if rollback := os.Rename(backup, dst); rollback != nil {
			return fmt.Errorf("replace failed: %v; rollback failed: %w (previous binary: %s)", err, rollback, backup)
		}
		return err
	}
	// Mapped images may remain until the master or the previous worker exits.
	// A subsequent replacement can collect them without blocking this upgrade.
	cleanupPreviousBinaries(dst)
	return nil
}

func chmod(f *os.File, perms os.FileMode) error {
	if err := f.Chmod(perms); err != nil && !strings.Contains(err.Error(), "not supported") {
		return err
	}
	return nil
}

func chown(f *os.File, uid, gid int) error { return nil }

func cleanupPreviousBinaries(path string) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return
	}
	prefix := "." + filepath.Base(path) + ".overseer-previous-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), extension()) {
			_ = os.Remove(filepath.Join(filepath.Dir(path), entry.Name()))
		}
	}
}
