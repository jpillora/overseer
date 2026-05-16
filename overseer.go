// Package overseer implements daemonizable
// self-upgrading binaries in Go (golang).
package overseer

import (
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/jpillora/overseer/fetcher"
	"github.com/jpillora/overseer/opanic"
)

const (
	envWorkerID = "OVERSEER_WORKER_ID"
	envIsWorker = "OVERSEER_IS_WORKER"
	// Deprecated: legacy names from before the slave→worker rename. Still
	// set by new masters and honored by new workers so rolling upgrades
	// across the rename boundary work in either direction.
	envSlaveID        = "OVERSEER_SLAVE_ID"
	envIsSlave        = "OVERSEER_IS_SLAVE"
	envNumFDs         = "OVERSEER_NUM_FDS"
	envBinID          = "OVERSEER_BIN_ID"
	envBinPath        = "OVERSEER_BIN_PATH"
	envBinCheck       = "OVERSEER_BIN_CHECK"
	envBinCheckLegacy = "GO_UPGRADE_BIN_CHECK"
)

// Config defines overseer's run-time configuration
type Config struct {
	//Required will prevent overseer from fallback to running
	//running the program in the main process on failure.
	Required bool
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
	//PreUpgrade runs after a binary has been retrieved, user defined checks
	//can be run here and returning an error will cancel the upgrade.
	PreUpgrade func(tempBinaryPath string) error
	//Debug enables all [overseer] logs.
	Debug bool
	//NoWarn disables info and warning [overseer] logs.
	NoWarn bool
	//NoRestart disables all restarts, this option essentially converts
	//the RestartSignal into a "ShutdownSignal".
	NoRestart bool
	//NoRestartAfterFetch disables automatic restarts after each upgrade.
	//Though manual restarts using the RestartSignal can still be performed.
	NoRestartAfterFetch bool
	//Fetcher will be used to fetch binaries.
	Fetcher fetcher.Interface
	//ShouldRestart, when non-nil, is consulted after a new binary has been
	//fetched and swapped but before the graceful restart is triggered. If
	//it returns false, the restart is deferred and re-checked on each
	//subsequent fetch loop iteration until it returns true. Manual restarts
	//via overseer.Restart() bypass this check.
	ShouldRestart func() bool
	//OnPanic, when non-nil, is invoked after the worker process exits if a
	//Go panic/runtime crash is detected in its stderr. The snapshot is
	//parsed by panicparse and contains the crashing goroutines. The
	//callback is awaited before the master exits, bounded by
	//TerminateTimeout so a slow or buggy callback cannot stall shutdown.
	OnPanic func(*opanic.Snapshot)
	//StderrTailSize is the number of bytes of worker stderr retained in
	//memory for panic detection. Only used when OnPanic is set. Defaults
	//to 256 KiB.
	StderrTailSize int
	//WaitDelay bounds how long cmd.Wait blocks on the worker's stderr/stdout
	//pipes after the process has exited. If a worker subprocess inherits the
	//worker's pipe fd (e.g. via cmd.Stderr = os.Stderr where the worker's
	//os.Stderr is itself the master→worker pipe) and outlives the worker,
	//the pipe never EOFs and cmd.Wait hangs forever — blocking the master
	//from spawning the next worker. Defaults to TerminateTimeout.
	WaitDelay time.Duration
}

func validate(c *Config) error {
	//validate
	if c.Program == nil {
		return errors.New("overseer.Config.Program required")
	}
	if c.Address != "" {
		if len(c.Addresses) > 0 {
			return errors.New("overseer.Config.Address and Addresses cant both be set")
		}
		c.Addresses = []string{c.Address}
	} else if len(c.Addresses) > 0 {
		c.Address = c.Addresses[0]
	}
	if c.RestartSignal == nil {
		c.RestartSignal = SIGUSR2
	}
	if c.TerminateTimeout <= 0 {
		c.TerminateTimeout = 30 * time.Second
	}
	if c.MinFetchInterval <= 0 {
		c.MinFetchInterval = 1 * time.Second
	}
	if c.WaitDelay <= 0 {
		c.WaitDelay = c.TerminateTimeout
	}
	return nil
}

// RunErr allows manual handling of any
// overseer errors.
func RunErr(c Config) error {
	return runErr(&c)
}

// Run executes overseer, if an error is
// encountered, overseer fallsback to running
// the program directly (unless Required is set).
func Run(c Config) {
	err := runErr(&c)
	if err != nil {
		if c.Required {
			log.Fatalf("[overseer] %s", err)
		} else if c.Debug || !c.NoWarn {
			log.Printf("[overseer] disabled. run failed: %s", err)
		}
		c.Program(DisabledState)
		return
	}
	os.Exit(0)
}

// sanityCheck returns true if a check was performed
func sanityCheck() bool {
	//sanity check
	if token := os.Getenv(envBinCheck); token != "" {
		fmt.Fprint(os.Stdout, token)
		return true
	}
	//legacy sanity check using old env var
	if token := os.Getenv(envBinCheckLegacy); token != "" {
		fmt.Fprint(os.Stdout, token)
		return true
	}
	return false
}

// SanityCheck manually runs the check to ensure this binary
// is compatible with overseer. This tries to ensure that a restart
// is never performed against a bad binary, as it would require
// manual intervention to rectify. This is automatically done
// on overseer.Run() though it can be manually run prior whenever
// necessary.
func SanityCheck() {
	if sanityCheck() {
		os.Exit(0)
	}
}

// abstraction over master/worker
var currentProcess interface {
	triggerRestart()
	run() error
}

func runErr(c *Config) error {
	//os not supported
	if !supported {
		return fmt.Errorf("os (%s) not supported", runtime.GOOS)
	}
	if err := validate(c); err != nil {
		return err
	}
	if sanityCheck() {
		return nil
	}
	//run either in master or worker mode. Accept either the new
	//OVERSEER_IS_WORKER or legacy OVERSEER_IS_SLAVE so a new binary can be
	//forked by an old master (and vice versa) during upgrades.
	if os.Getenv(envIsWorker) == "1" || os.Getenv(envIsSlave) == "1" {
		currentProcess = &worker{Config: c}
	} else {
		currentProcess = &master{Config: c}
	}
	return currentProcess.run()
}

// Restart programmatically triggers a graceful restart. If NoRestart
// is enabled, then this will essentially be a graceful shutdown.
func Restart() {
	if currentProcess != nil {
		currentProcess.triggerRestart()
	}
}

// IsSupported returns whether overseer is supported on the current OS.
func IsSupported() bool {
	return supported
}
