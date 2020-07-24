package overseer

import (
	"fmt"
	"net"
	"os"
)

//a overseer slave process

type tcpSlave struct {
	id         string
	listeners  []*overseerListener
	masterPid  int
	masterProc *os.Process
	State      *State
	Config     Config
}

func (sp *tcpSlave) Init(resource ForkResource, config Config) ([]net.Listener, error) {
	sp.id = resource.Uid
	sp.masterPid = resource.MasterPid
	sp.masterProc = resource.MasterProc
	sp.Config = config

	sp.listeners = make([]*overseerListener, len(resource.Fds))
	listeners := make([]net.Listener, len(resource.Fds))
	for i := 0; i < len(resource.Fds); i++ {
		f := os.NewFile(resource.Fds[i], "")
		l, err := net.FileListener(f)
		if err != nil {
			return nil, fmt.Errorf("failed to inherit file descriptor: %d", i)
		}
		u := newOverseerListener(l)
		sp.listeners[i] = u
		listeners[i] = u
	}
	return listeners, nil
}

func (sp *tcpSlave) SafeHandler(state *State) {
	sp.State = state
	sp.Config.Program(*state)
}

func (sp *tcpSlave) OnOver() {
	if len(sp.listeners) > 0 {
		//perform graceful shutdown
		for _, l := range sp.listeners {
			l.release(sp.Config.TerminateTimeout)
		}
		//signal release of held sockets, allows master to start
		//a new process before this child has actually exited.
		//early restarts not supported with restarts disabled.
		if !sp.Config.NoRestart {
			sp.masterProc.Signal(SIGUSR1)
		}
		//listeners should be waiting on connections to close...
	}
}
