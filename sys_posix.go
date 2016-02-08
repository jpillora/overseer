// +build linux darwin

package upgrade

//this file attempts to contain all posix
//specific stuff, that needs to be implemented
//in some other way on other OSs... TODO!

import (
	"os/exec"
	"syscall"
)

const supported = true

var (
	SIGUSR1 = syscall.SIGUSR1
	SIGTERM = syscall.SIGTERM
)

func move(dst, src string) error {
	// HACK: we're shelling out to mv because linux
	//throws errors when we use Rename/Create.
	return exec.Command("mv", src, dst).Run()
}
