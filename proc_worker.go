package overseer

import (
	"log"
	"net"
	"os"
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

// Ready acknowledges successful application initialization. It is safe to call
// repeatedly. With ReadyTimeout disabled (or overseer disabled) it is a no-op.
// Call only after the application can serve requests and its resources are ready.
func (s State) Ready() error {
	if s.Enabled && s.GracefulShutdown != nil {
		if sp, ok := currentProcess.(*worker); ok && sp.state.GracefulShutdown == s.GracefulShutdown && sp.ready != nil {
			return sp.ready()
		}
	}
	return nil
}

//a overseer worker process

type worker struct {
	*Config
	id         string
	listeners  []*overseerListener
	masterPid  int
	masterProc *os.Process
	control    *workerControl
	state      State
	ready      func() error
}

func (sp *worker) run() (err error) {
	defer func() {
		if err != nil {
			clearBootstrapEnvironment()
		}
	}()
	// Prefer the new env var; fall back to the legacy OVERSEER_SLAVE_ID so a
	// worker forked by a pre-rename master still gets its id.
	if id := os.Getenv(envWorkerID); id != "" {
		sp.id = id
	} else {
		sp.id = os.Getenv(envSlaveID)
	}
	sp.debugf("run")
	sp.state.Enabled = true
	sp.state.ID = os.Getenv(envBinID)
	sp.state.StartedAt = time.Now()
	sp.state.Address = sp.Config.Address
	sp.state.Addresses = sp.Config.Addresses
	sp.state.GracefulShutdown = make(chan bool, 1)
	sp.state.BinPath = os.Getenv(envBinPath)
	if err := sp.watchParent(); err != nil {
		return err
	}
	if err := sp.initFileDescriptors(); err != nil {
		return err
	}
	if err := sp.initReady(); err != nil {
		return err
	}
	sp.watchSignal()
	clearWorkerTransport()
	if err := os.Setenv(envWorkerBootstrapped, "1"); err != nil {
		return err
	}
	//run program with state
	sp.debugf("start program")
	sp.Config.Program(sp.state)
	return nil
}

func (sp *worker) forceNextRestart() {
	//restart policy lives in the master; no-op here
}

func (sp *worker) info() Info {
	//master state is not visible from the worker
	return Info{}
}

func (sp *worker) debugf(f string, args ...interface{}) {
	if sp.Config.Debug {
		log.Printf("[overseer worker#"+sp.id+"] "+f, args...)
	}
}

func (sp *worker) infof(f string, args ...interface{}) {
	if sp.Config.Debug || !sp.Config.NoWarn {
		log.Printf("[overseer worker#"+sp.id+"] "+f, args...)
	}
}

func (sp *worker) warnf(f string, args ...interface{}) {
	if sp.Config.Debug || !sp.Config.NoWarn {
		log.Printf("[overseer worker#"+sp.id+"] "+f, args...)
	}
}
