package overseer

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"time"
)

var (
	//DisabledState is a placeholder state for when
	//overseer is disabled and the program function
	//is run manually.
	DisabledState = State{Enabled: false}
)

// State contains the current run-time state of overseer
type State struct {
	//whether overseer is running enabled. When enabled,
	//this program will be running in a child process and
	//overseer will perform rolling upgrades.
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
	//Program's first listening address
	Address string
	//Program's listening addresses
	Addresses []string
	//GracefulShutdown will be filled when its time to perform
	//a graceful shutdown.
	GracefulShutdown chan bool
	//Path of the binary currently being executed
	BinPath string
}

//a overseer slave process

type slave struct {
	*Config
	state      *State
	id         string
	masterProc *os.Process
}

func (sp *slave) run() error {
	sp.debugf("run")
	sp.id = os.Getenv(envSlaveID)
	if sp.Grace == nil {
		sp.Grace = &tcpSlave{}
	}
	pid, proc, err := sp.watchParent()
	if err != nil {
		return err
	}
	sp.masterProc = proc
	fds, err := sp.initFileDescriptors()
	if err != nil {
		return err
	}
	resource := ForkResource{
		Uid:        os.Getenv(envSlaveID),
		MasterPid:  pid,
		MasterProc: proc,
		Fds:        fds,
	}

	listeners, err := sp.Grace.Init(resource, *sp.Config)
	if err != nil {
		return err
	}
	state := &State{
		Enabled:          true,
		ID:               os.Getenv(envBinID),
		StartedAt:        time.Now(),
		Address:          sp.Config.Address,
		Addresses:        sp.Config.Addresses,
		GracefulShutdown: make(chan bool, 1),
		BinPath:          os.Getenv(envBinPath),
		Listeners:        listeners,
	}
	if len(listeners) > 0 {
		state.Listener = listeners[0]
	}
	sp.watchSignal()
	//run program with state
	sp.debugf("start program")

	sp.state = state
	sp.Grace.SafeHandler(state)
	return nil
}

func (sp *slave) initFileDescriptors() ([]uintptr, error) {
	//inspect file descriptors
	numFDs, err := strconv.Atoi(os.Getenv(envNumFDs))
	if err != nil {
		return nil, fmt.Errorf("invalid %s integer", envNumFDs)
	}
	fds := make([]uintptr, 0)
	for i := 0; i < numFDs; i++ {
		fds = append(fds, uintptr(3+i))
	}
	return fds, nil
}

func (sp *slave) watchSignal() {
	signals := make(chan os.Signal)
	signal.Notify(signals, sp.Config.RestartSignal)
	go func() {
		<-signals
		signal.Stop(signals)
		sp.debugf("graceful shutdown requested")
		//master wants to restart,
		close(sp.state.GracefulShutdown)
		//release any sockets and notify master
		sp.Grace.OnOver()
		//start death-timer
		go func() {
			time.Sleep(sp.Config.TerminateTimeout)
			sp.debugf("timeout. forceful shutdown")
			os.Exit(1)
		}()
	}()
}

func (sp *slave) triggerRestart() {
	if err := sp.masterProc.Signal(sp.Config.RestartSignal); err != nil {
		os.Exit(1)
	}
}

func (sp *slave) debugf(f string, args ...interface{}) {
	if sp.Config.Debug {
		log.Printf("[overseer slave#"+sp.id+"] "+f, args...)
	}
}

func (sp *slave) warnf(f string, args ...interface{}) {
	if sp.Config.Debug || !sp.Config.NoWarn {
		log.Printf("[overseer slave#"+sp.id+"] "+f, args...)
	}
}
