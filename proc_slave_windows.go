package overseer

import (
	"fmt"
	"github.com/shirou/gopsutil/process"
	"os"
	"syscall"
	"time"
)

func (sp *slave) watchParent() error {
	sp.masterPid = os.Getppid()
	proc, err := os.FindProcess(sp.masterPid)
	if err != nil {
		return fmt.Errorf("master process: %s", err)
	}
	sp.masterProc = proc
	go func() {
		//check process exists
		for {
			//should not error as long as the process is alive
			if _, err := process.GetWin32Proc(int32(sp.masterPid)); err != nil {
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
		}
	}()
	return nil
}
