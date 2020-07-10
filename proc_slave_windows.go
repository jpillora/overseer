// +build windows

package overseer

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/StackExchange/wmi"
)

var (
	Timeout = 3 * time.Second
)

type Win32_Process struct {
	Name                  string
	ExecutablePath        *string
	CommandLine           *string
	Priority              uint32
	CreationDate          *time.Time
	ProcessID             uint32
	ThreadCount           uint32
	Status                *string
	ReadOperationCount    uint64
	ReadTransferCount     uint64
	WriteOperationCount   uint64
	WriteTransferCount    uint64
	CSCreationClassName   string
	CSName                string
	Caption               *string
	CreationClassName     string
	Description           *string
	ExecutionState        *uint16
	HandleCount           uint32
	KernelModeTime        uint64
	MaximumWorkingSetSize *uint32
	MinimumWorkingSetSize *uint32
	OSCreationClassName   string
	OSName                string
	OtherOperationCount   uint64
	OtherTransferCount    uint64
	PageFaults            uint32
	PageFileUsage         uint32
	ParentProcessID       uint32
	PeakPageFileUsage     uint32
	PeakVirtualSize       uint64
	PeakWorkingSetSize    uint32
	PrivatePageCount      uint64
	TerminationDate       *time.Time
	UserModeTime          uint64
	WorkingSetSize        uint64
}

func (sp *slave) watchParent() error {
	sp.masterPid = os.Getppid()
	proc, err := os.FindProcess(sp.masterPid)
	if err != nil {
		return fmt.Errorf("master process: %s", err)
	}
	sp.masterProc = proc
	go func() {
		//send signal 0 to master process forever
		for {
			//should not error as long as the process is alive
			if _, err := GetWin32Proc(int32(sp.masterPid)); err != nil {
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
		}
	}()
	return nil
}

func GetWin32Proc(pid int32) ([]Win32_Process, error) {
	return GetWin32ProcWithContext(context.Background(), pid)
}

func GetWin32ProcWithContext(ctx context.Context, pid int32) ([]Win32_Process, error) {
	var dst []Win32_Process
	query := fmt.Sprintf("WHERE ProcessId = %d", pid)
	q := wmi.CreateQuery(&dst, query)
	err := WMIQueryWithContext(ctx, q, &dst)
	if err != nil {
		return []Win32_Process{}, fmt.Errorf("could not get win32Proc: %s", err)
	}

	if len(dst) == 0 {
		return []Win32_Process{}, fmt.Errorf("could not get win32Proc: empty")
	}

	return dst, nil
}

func WMIQueryWithContext(ctx context.Context, query string, dst interface{}, connectServerArgs ...interface{}) error {
	if _, ok := ctx.Deadline(); !ok {
		ctxTimeout, cancel := context.WithTimeout(ctx, Timeout)
		defer cancel()
		ctx = ctxTimeout
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- wmi.Query(query, dst, connectServerArgs...)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		return err
	}
}

// overwrite: see https://github.com/jpillora/overseer/issues/56#issuecomment-656405955
func overwrite(dst, src string) error {
	old := strings.TrimSuffix(dst, ".exe") + "-old.exe"
	if err := move(old, dst); err != nil {
		return err
	}
	if err := move(dst, src); err != nil {
		return err
	}
	os.Remove(old)
	return nil
}
