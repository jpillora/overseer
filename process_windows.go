package overseer

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	defaultSanityCheckTimeout = 30 * time.Second
	envParentHandle           = "OVERSEER_PARENT_HANDLE"
	envShutdownHandle         = "OVERSEER_SHUTDOWN_HANDLE"
	envRestartHandle          = "OVERSEER_RESTART_HANDLE"
	envAddresses              = "OVERSEER_ADDRESSES"
)

// The handles are inherited only by the intended worker. Unlike process IDs or
// named events they cannot be rebound to a different process after a restart.
type workerControl struct {
	cmd                             *exec.Cmd
	parent, shutdown, restart, stop windows.Handle
	done                            chan struct{}
}

func (mp *master) prepareWorker(cmd *exec.Cmd) (func(), error) {
	c := &workerControl{cmd: cmd}
	closeControl := func() {
		mp.workerMux.Lock()
		if mp.control == c {
			mp.control = nil
		}
		mp.workerMux.Unlock()
		if c.done != nil {
			windows.SetEvent(c.stop)
			<-c.done
		}
		for _, h := range []windows.Handle{c.parent, c.shutdown, c.restart, c.stop} {
			if h != 0 {
				windows.CloseHandle(h)
			}
		}
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var err error
	if c.parent, err = windows.OpenProcess(windows.SYNCHRONIZE, true, uint32(os.Getpid())); err == nil {
		c.shutdown, err = windows.CreateEvent(sa, 0, 0, nil)
	}
	if err == nil {
		c.restart, err = windows.CreateEvent(sa, 0, 0, nil)
	}
	if err == nil {
		c.stop, err = windows.CreateEvent(nil, 0, 0, nil)
	}
	if err != nil {
		closeControl()
		return nil, fmt.Errorf("worker control handles: %w", err)
	}
	addresses, err := json.Marshal(mp.Addresses)
	if err != nil {
		closeControl()
		return nil, err
	}
	cmd.Env = append(cmd.Env,
		envParentHandle+"="+strconv.FormatUint(uint64(c.parent), 10),
		envShutdownHandle+"="+strconv.FormatUint(uint64(c.shutdown), 10),
		envRestartHandle+"="+strconv.FormatUint(uint64(c.restart), 10),
		envAddresses+"="+string(addresses))
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(c.parent), syscall.Handle(c.shutdown), syscall.Handle(c.restart)}}
	mp.workerMux.Lock()
	mp.control = c
	mp.workerMux.Unlock()
	return closeControl, nil
}

func (mp *master) workerStarted(cmd *exec.Cmd) {
	c := mp.control
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		for {
			result, err := windows.WaitForMultipleObjects([]windows.Handle{c.stop, c.restart}, false, windows.INFINITE)
			if err != nil || result != windows.WAIT_OBJECT_0+1 {
				return
			}
			mp.workerMux.Lock()
			current := mp.control == c && mp.workerCmd == cmd
			mp.workerMux.Unlock()
			if current {
				go mp.startRestartFor(cmd, false)
			}
		}
	}()
}

func (mp *master) signalWorker(proc *os.Process, s os.Signal) error {
	if s == os.Kill {
		return proc.Kill()
	}
	mp.workerMux.Lock()
	defer mp.workerMux.Unlock()
	if mp.control == nil || mp.control.cmd.Process != proc {
		return os.ErrProcessDone
	}
	return windows.SetEvent(mp.control.shutdown)
}

func (sp *worker) watchParent() error {
	c := &workerControl{}
	for name, target := range map[string]*windows.Handle{envParentHandle: &c.parent, envShutdownHandle: &c.shutdown, envRestartHandle: &c.restart} {
		value, err := strconv.ParseUint(os.Getenv(name), 10, 64)
		if err != nil || value == 0 {
			return fmt.Errorf("invalid inherited %s", name)
		}
		*target = windows.Handle(value)
		if err := windows.SetHandleInformation(*target, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			return err
		}
	}
	sp.control = c
	go func() {
		_, _ = windows.WaitForSingleObject(c.parent, windows.INFINITE)
		os.Exit(1)
	}()
	return nil
}

func (sp *worker) watchSignal() {
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			sp.infof("graceful shutdown requested")
			close(sp.state.GracefulShutdown)
			for _, l := range sp.listeners {
				l.release(sp.TerminateTimeout)
			}
			// Windows listeners are rebound only after this worker exits.
			go func() { time.Sleep(sp.TerminateTimeout); os.Exit(1) }()
		})
	}
	go func() {
		if result, err := windows.WaitForSingleObject(sp.control.shutdown, windows.INFINITE); err == nil && result == windows.WAIT_OBJECT_0 {
			shutdown()
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() { <-signals; signal.Stop(signals); shutdown() }()
}

func (sp *worker) triggerRestart() {
	if err := windows.SetEvent(sp.control.restart); err != nil {
		os.Exit(1)
	}
}

func (mp *master) retreiveFileDescriptors() error {
	cleanupPreviousBinaries(mp.binPath)
	// Windows cannot use exec.Cmd.ExtraFiles or net.FileListener. Reserve the
	// selected addresses once, then let each worker bind after its predecessor
	// has exited. This also keeps a requested port zero stable across upgrades.
	for i, address := range mp.Addresses {
		ln, err := net.Listen("tcp", address)
		if err != nil {
			return err
		}
		mp.Addresses[i] = ln.Addr().String()
		ln.Close()
	}
	if len(mp.Addresses) > 0 {
		mp.Address = mp.Addresses[0]
	}
	return nil
}

func (sp *worker) initFileDescriptors() error {
	var addresses []string
	if err := json.Unmarshal([]byte(os.Getenv(envAddresses)), &addresses); err != nil {
		return err
	}
	for _, address := range addresses {
		ln, err := net.Listen("tcp", address)
		if err != nil {
			for _, previous := range sp.listeners {
				previous.Listener.Close()
			}
			return err
		}
		l := newOverseerListener(ln)
		sp.listeners = append(sp.listeners, l)
		sp.state.Listeners = append(sp.state.Listeners, l)
	}
	sp.state.Addresses = addresses
	if len(sp.listeners) > 0 {
		sp.state.Listener = sp.listeners[0]
		sp.state.Address = addresses[0]
	}
	return nil
}
