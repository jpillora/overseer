package upgrade

import (
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

var (
	//DisabledState is a placeholder state for when
	//go-upgrade is disabled and the program function
	//is run manually.
	DisabledState = State{Enabled: false}
)

type State struct {
	//whether go-upgrade is running enabled. When enabled,
	//this program will be running in a child process and
	//go-upgrade will perform rolling upgrades.
	Enabled bool
	//ID is a SHA-1 hash of the current running binary
	ID string
	//StartedAt records the start time of the program
	StartedAt time.Time
	//Listener is the first net.Listener in Listeners
	Listener net.Listener
	//Listeners are the set of acquired sockets by the master
	//process. These are all passed into this program in the
	//same order they are specified in Config.Addresses.
	Listeners []net.Listener
}

//a go-upgrade slave process

type slave struct {
	Config
	listeners  []*upListener
	masterPid  int
	masterProc *os.Process
	state      State
}

func (sp *slave) run() {
	sp.state.Enabled = true
	sp.state.ID = os.Getenv(envBinID)
	sp.state.StartedAt = time.Now()
	sp.watchParent()
	sp.initFileDescriptors()
	sp.watchSignal()
	//run program with state
	sp.logf("start program")
	sp.Config.Program(sp.state)
}

func (sp *slave) watchParent() {
	sp.masterPid = os.Getppid()
	proc, err := os.FindProcess(sp.masterPid)
	if err != nil {
		fatalf("parent process %s", err)
	}
	sp.masterProc = proc
	go func() {
		for {
			//sending signal 0 should not error as long as the process is alive
			if err := sp.masterProc.Signal(syscall.Signal(0)); err != nil {
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
		}
	}()
}

func (sp *slave) initFileDescriptors() {
	//inspect file descriptors
	numFDs, err := strconv.Atoi(os.Getenv(envNumFDs))
	if err != nil {
		fatalf("invalid %s integer", envNumFDs)
	}
	sp.listeners = make([]*upListener, numFDs)
	sp.state.Listeners = make([]net.Listener, numFDs)
	for i := 0; i < numFDs; i++ {
		f := os.NewFile(uintptr(3+i), "")
		l, err := net.FileListener(f)
		if err != nil {
			fatalf("failed to inherit file descriptor: %d", i)
		}
		u := newUpListener(l)
		sp.listeners[i] = u
		sp.state.Listeners[i] = u
	}
	if len(sp.state.Listeners) > 0 {
		sp.state.Listener = sp.state.Listeners[0]
	}
}

func (sp *slave) watchSignal() {
	signals := make(chan os.Signal)
	signal.Notify(signals, sp.Config.Signal)
	go func() {
		<-signals
		signal.Stop(signals)
		sp.logf("graceful shutdown requested")
		//master wants to restart,
		//perform graceful shutdown:
		for _, l := range sp.listeners {
			l.release(sp.Config.TerminateTimeout)
		}
		sp.logf("released")
		//signal released fds
		sp.masterProc.Signal(syscall.SIGUSR1)
		sp.logf("notify USR1")
		//listeners should be waiting on connections to close...
	}()
}

func (sp *slave) logf(f string, args ...interface{}) {
	if sp.Log {
		log.Printf("[go-upgrade slave] "+f, args...)
	}
}
