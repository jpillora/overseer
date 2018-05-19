// +build linux darwin

package overseer

//this file attempts to contain all posix
//specific stuff, that needs to be implemented
//in some other way on other OSs... TODO!

import (
	"os"
	"os/exec"
	"syscall"
)

var (
	supported = true
	uid       = syscall.Getuid()
	gid       = syscall.Getgid()
	SIGUSR1   = syscall.SIGUSR1
	SIGUSR2   = syscall.SIGUSR2
	SIGTERM   = syscall.SIGTERM
)

func move(dst, src string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	//HACK: we're shelling out to mv because linux
	//throws errors when crossing device boundaries.
	//TODO see sys_posix_mv.go
	if err := exec.Command("mv", src, dst).Run(); err != nil {
		return err
	}

	// Run sync to 'commit' the mv by clearing caches
	return syncCmd().Run()
}

func syncCmd() *exec.Cmd {
	return exec.Command("sync")
}

func chmod(f *os.File, perms os.FileMode) error {
	return f.Chmod(perms)
}
func chown(f *os.File, uid, gid int) error {
	return f.Chown(uid, gid)
}
