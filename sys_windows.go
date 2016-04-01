// +build windows

package overseer

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	os.MkdirAll(filepath.Dir(dst), 0755)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	//HACK: we're shelling out to move because windows
	//throws errors when crossing device boundaryes.
	// https://www.microsoft.com/resources/documentation/windows/xp/all/proddocs/en-us/move.mspx?mfr=true
	cmd := exec.Command("cmd", "/c", `move /y "`+src+`" "`+dst+`"`)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %q: %v", cmd.Args, bytes.TrimSpace(b), err)
	}
	return nil
}

func chmod(f *os.File, perms os.FileMode) error {
	_ = f.Chmod(perms)
	return nil
}

func chown(f *os.File, uid, gid int) error {
	_ = f.Chown(uid, gid)
	return nil
}
