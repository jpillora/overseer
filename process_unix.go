//go:build !windows

package overseer

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"time"
)

type workerControl struct{}

const defaultSanityCheckTimeout = 5 * time.Second

func (mp *master) prepareWorker(cmd *exec.Cmd) (func(), error) {
	cmd.ExtraFiles = mp.workerExtraFiles
	return func() {}, nil
}
func (mp *master) workerStarted(*exec.Cmd)                          {}
func (mp *master) signalWorker(proc *os.Process, s os.Signal) error { return proc.Signal(s) }

func (mp *master) retreiveFileDescriptors() error {
	mp.workerExtraFiles = make([]*os.File, len(mp.Config.Addresses))
	for i, addr := range mp.Config.Addresses {
		a, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			return fmt.Errorf("Invalid address %s (%s)", addr, err)
		}
		l, err := net.ListenTCP("tcp", a)
		if err != nil {
			return err
		}
		f, err := l.File()
		if err != nil {
			return fmt.Errorf("Failed to retreive fd for: %s (%s)", addr, err)
		}
		if err := l.Close(); err != nil {
			return fmt.Errorf("Failed to close listener for: %s (%s)", addr, err)
		}
		mp.workerExtraFiles[i] = f
	}
	return nil
}

func (sp *worker) initFileDescriptors() error {
	//inspect file descriptors
	numFDs, err := strconv.Atoi(os.Getenv(envNumFDs))
	if err != nil {
		return fmt.Errorf("invalid %s integer", envNumFDs)
	}
	sp.listeners = make([]*overseerListener, numFDs)
	sp.state.Listeners = make([]net.Listener, numFDs)
	for i := 0; i < numFDs; i++ {
		f := os.NewFile(uintptr(3+i), "")
		l, err := net.FileListener(f)
		f.Close() // FileListener owns a duplicate; do not leak the bootstrap FD.
		if err != nil {
			return fmt.Errorf("failed to inherit file descriptor: %d", i)
		}
		u := newOverseerListener(l)
		sp.listeners[i] = u
		sp.state.Listeners[i] = u
	}
	if len(sp.state.Listeners) > 0 {
		sp.state.Listener = sp.state.Listeners[0]
	}
	return nil
}

func (sp *worker) watchSignal() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, sp.Config.RestartSignal)
	go func() {
		<-signals
		signal.Stop(signals)
		sp.infof("graceful shutdown requested")
		//master wants to restart,
		close(sp.state.GracefulShutdown)
		//release any sockets and notify master
		if len(sp.listeners) > 0 {
			//perform graceful shutdown
			for _, l := range sp.listeners {
				l.release(sp.Config.TerminateTimeout)
			}
			//signal release of held sockets, allows master to start
			//a new process before this child has actually exited.
			//early restarts not supported with restarts disabled.
			if !sp.NoRestart {
				sp.masterProc.Signal(SIGUSR1)
			}
			//listeners should be waiting on connections to close...
		}
		//start death-timer
		go func() {
			time.Sleep(sp.Config.TerminateTimeout)
			sp.infof("timeout. forceful shutdown")
			os.Exit(1)
		}()
	}()
}

func (sp *worker) triggerRestart() {
	if err := sp.masterProc.Signal(sp.Config.RestartSignal); err != nil {
		os.Exit(1)
	}
}
