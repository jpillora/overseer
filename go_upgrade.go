// Daemonizable self-upgrading binaries in Go (golang).
package upgrade

import (
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/jpillora/go-upgrade/fetcher"
)

const (
	envIsSlave  = "GO_UPGRADE_IS_SLAVE"
	envNumFDs   = "GO_UPGRADE_NUM_FDS"
	envBinID    = "GO_UPGRADE_BIN_ID"
	envBinCheck = "GO_UPGRADE_BIN_CHECK"
)

type Config struct {
	//Optional allows go-upgrade to fallback to running
	//running the program in the main process.
	Optional bool
	//Program's main function
	Program func(state State)
	//Program's zero-downtime socket listening address (set this or Addresses)
	Address string
	//Program's zero-downtime socket listening addresses (set this or Address)
	Addresses []string
	//Signal program will accept to initiate graceful
	//application termination. Defaults to SIGTERM.
	Signal os.Signal
	//TerminateTimeout controls how long go-upgrade should
	//wait for the program to terminate itself. After this
	//timeout, go-upgrade will issue a SIGKILL.
	TerminateTimeout time.Duration
	//MinFetchInterval defines the smallest duration between Fetch()s.
	//This helps to prevent unwieldy fetch.Interfaces from hogging
	//too many resources. Defaults to 1 second.
	MinFetchInterval time.Duration
	//PreUpgrade runs after a binary has been retreived, user defined checks
	//can be run here and returning an error will cancel the upgrade.
	PreUpgrade func(tempBinaryPath string) error
	//NoRestart disables automatic restarts after each upgrade.
	NoRestart bool
	//Log enables [go-upgrade] logs to be sent to stdout.
	Log bool
	//Fetcher will be used to fetch binaries.
	Fetcher fetcher.Interface
}

func fatalf(f string, args ...interface{}) {
	log.Fatalf("[go-upgrade] "+f, args...)
}

func Run(c Config) {
	//sanity check
	if token := os.Getenv(envBinCheck); token != "" {
		fmt.Fprint(os.Stdout, token)
		os.Exit(0)
	}
	//validate
	if c.Program == nil {
		fatalf("upgrade.Config.Program required")
	}
	if c.Address != "" {
		if len(c.Addresses) > 0 {
			fatalf("upgrade.Config.Address and Addresses cant both be set")
		}
		c.Addresses = []string{c.Address}
	}
	if c.Signal == nil {
		c.Signal = syscall.SIGTERM
	}
	if c.TerminateTimeout == 0 {
		c.TerminateTimeout = 30 * time.Second
	}
	if c.MinFetchInterval == 0 {
		c.MinFetchInterval = 1 * time.Second
	}
	if c.Fetcher == nil {
		fatalf("upgrade.Config.Fetcher required")
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
