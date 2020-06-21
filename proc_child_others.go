// +build !windows

package overseer

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func (sp *child) watchParent() error {
	sp.parentPid = os.Getppid()
	proc, err := os.FindProcess(sp.parentPid)
	if err != nil {
		return fmt.Errorf("parent process: %s", err)
	}
	sp.parentProc = proc
	go func() {
		//send signal 0 to parent process forever
		for {
			//should not error as long as the process is alive
			if err := sp.parentProc.Signal(syscall.Signal(0)); err != nil {
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
		}
	}()
	return nil
}
