// +build windows

package overseer

//this file attempts to contain all posix
//specific stuff, that needs to be implemented
//in some other way on other OSs... TODO!

import (
	"os/exec"
	"syscall"
)

var (
	uid     = syscall.Getuid()
	gid     = syscall.Getgid()
	SIGUSR1 = syscall.SIGTERM
	SIGUSR2 = syscall.SIGTERM
	SIGTERM = syscall.SIGTERM
)

func move(dst, src string) error {
	//HACK: we're shelling out to mv because linux
	//throws errors when crossing device boundaryes.
	//TODO see sys_posix_mv.go
	return exec.Command("rename", src, dst).Run()
}
