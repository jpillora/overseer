//go:build !windows

package overseer

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
)

func (mp *master) prepareReady(cmd *exec.Cmd) (<-chan error, func(), error) {
	if mp.ReadyTimeout <= 0 {
		return nil, func() {}, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Env = append(cmd.Env, envReadyFD+"="+strconv.Itoa(3+len(cmd.ExtraFiles)))
	cmd.ExtraFiles = append(append([]*os.File{}, cmd.ExtraFiles...), w)
	ready := make(chan error, 1)
	go func() {
		var b [1]byte
		n, err := r.Read(b[:])
		if err == nil && (n != 1 || b[0] != 1) {
			err = fmt.Errorf("invalid readiness acknowledgement")
		}
		ready <- err
	}()
	return ready, func() { r.Close(); w.Close() }, nil
}

func (sp *worker) initReady() error {
	value := os.Getenv(envReadyFD)
	if value == "" {
		return nil
	} // Old master, or readiness disabled.
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return fmt.Errorf("invalid readiness descriptor")
	}
	syscall.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), "overseer-ready")
	var once sync.Once
	var result error
	sp.state.ready = func() error {
		once.Do(func() { _, result = f.Write([]byte{1}); f.Close() })
		return result
	}
	return nil
}
