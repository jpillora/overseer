// +build windows

package overseer

import (
	"fmt"
	"os/exec"
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
	//HACK: we're shelling out to move because windows
	//throws errors when crossing device boundaryes.
	// https://www.microsoft.com/resources/documentation/windows/xp/all/proddocs/en-us/move.mspx?mfr=true
	cmd := exec.Command("cmd", "/c", `move /y "`+src+`" "`+dst+`"`)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%q: %v: %v", cmd.Args, b, err)
	}
	return nil
}
