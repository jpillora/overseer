// +build !windows

package overseer

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func (sp *slave) watchParent() (int, *os.Process, error) {
	masterPid := os.Getppid()
	proc, err := os.FindProcess(masterPid)
	if err != nil {
		return 0, nil, fmt.Errorf("master process: %s", err)
	}
	go func() {
		//send signal 0 to master process forever
		for {
			//should not error as long as the process is alive
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
		}
	}()
	return masterPid, proc, nil
}

func overwrite(dst, src string) error {
	return move(dst, src)
}
