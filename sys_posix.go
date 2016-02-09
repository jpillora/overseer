// +build linux darwin

package overseer

//this file attempts to contain all posix
//specific stuff, that needs to be implemented
//in some other way on other OSs... TODO!

import (
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
	//HACK: we're shelling out to mv because linux
	//throws errors when we use Rename/Create a
	//running binary.
	return exec.Command("mv", src, dst).Run()
}
