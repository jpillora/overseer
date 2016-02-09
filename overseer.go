// Daemonizable self-upgrading binaries in Go (golang).
package overseer

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/jpillora/overseer/fetcher"
)

const (
	envIsSlave  = "GO_UPGRADE_IS_SLAVE"
	envNumFDs   = "GO_UPGRADE_NUM_FDS"
	envBinID    = "GO_UPGRADE_BIN_ID"
	envBinCheck = "GO_UPGRADE_BIN_CHECK"
)

type Config struct {
	//Optional allows overseer to fallback to running
	//running the program in the main process.
	Optional bool
	//Program's main function
	Program func(state State)
	//Program's zero-downtime socket listening address (set this or Addresses)
	Address string
	//Program's zero-downtime socket listening addresses (set this or Address)
	Addresses []string
	//RestartSignal will manually trigger a graceful restart. Defaults to SIGUSR2.
	RestartSignal os.Signal
	//TerminateTimeout controls how long overseer should
	//wait for the program to terminate itself. After this
	//timeout, overseer will issue a SIGKILL.
	TerminateTimeout time.Duration
	//MinFetchInterval defines the smallest duration between Fetch()s.
	//This helps to prevent unwieldy fetch.Interfaces from hogging
	//too many resources. Defaults to 1 second.
	MinFetchInterval time.Duration
	//PreUpgrade runs after a binary has been retreived, user defined checks
	//can be run here and returning an error will cancel the upgrade.
	PreUpgrade func(tempBinaryPath string) error
	//Log enables [overseer] logs to be sent to stdout.
	Log bool
	//NoRestartAfterFetch disables automatic restarts after each upgrade.
	NoRestartAfterFetch bool
	//Fetcher will be used to fetch binaries.
	Fetcher fetcher.Interface
}

func fatalf(f string, args ...interface{}) {
	log.Fatalf("[overseer] "+f, args...)
}

func Run(c Config) {
	//sanity check
	if token := os.Getenv(envBinCheck); token != "" {
		fmt.Fprint(os.Stdout, token)
		os.Exit(0)
	}
	//validate
	if c.Program == nil {
		fatalf("overseer.Config.Program required")
	}
	if c.Address != "" {
		if len(c.Addresses) > 0 {
			fatalf("overseer.Config.Address and Addresses cant both be set")
		}
		c.Addresses = []string{c.Address}
	} else if len(c.Addresses) > 0 {
		c.Address = c.Addresses[0]
	}
	if c.RestartSignal == nil {
		c.RestartSignal = SIGUSR2
	}
	if c.TerminateTimeout == 0 {
		c.TerminateTimeout = 30 * time.Second
	}
	if c.MinFetchInterval == 0 {
		c.MinFetchInterval = 1 * time.Second
	}
	//os not supported
	if !supported {
		if !c.Optional {
			fatalf("os (%s) not supported", runtime.GOOS)
		}
		c.Program(DisabledState)
		return
	}
	//run either in master or slave mode
	if os.Getenv(envIsSlave) == "1" {
		sp := slave{Config: c}
		sp.logf("run")
		sp.run()
	} else {
		mp := master{Config: c}
		mp.logf("run")
		mp.run()
	}
}
