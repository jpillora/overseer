package overseer

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (mp *master) prepareReady(cmd *exec.Cmd) (<-chan error, func(), error) {
	if mp.ReadyTimeout <= 0 {
		return nil, func() {}, nil
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	h, err := windows.CreateEvent(sa, 0, 0, nil)
	if err != nil {
		return nil, nil, err
	}
	stop, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, nil, err
	}
	cmd.Env = append(cmd.Env, envReadyHandle+"="+strconv.FormatUint(uint64(h), 10))
	cmd.SysProcAttr.AdditionalInheritedHandles = append(cmd.SysProcAttr.AdditionalInheritedHandles, syscall.Handle(h))
	ready := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := windows.WaitForMultipleObjects([]windows.Handle{h, stop}, false, windows.INFINITE)
		if err == nil && result != windows.WAIT_OBJECT_0 {
			err = fmt.Errorf("readiness wait cancelled")
		}
		ready <- err
	}()
	return ready, func() { windows.SetEvent(stop); <-done; windows.CloseHandle(h); windows.CloseHandle(stop) }, nil
}

func (sp *worker) initReady() error {
	value := os.Getenv(envReadyHandle)
	if value == "" {
		return nil
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n == 0 {
		return fmt.Errorf("invalid readiness handle")
	}
	h := windows.Handle(n)
	if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return err
	}
	var once sync.Once
	var result error
	sp.state.ready = func() error {
		once.Do(func() { result = windows.SetEvent(h); windows.CloseHandle(h) })
		return result
	}
	return nil
}
