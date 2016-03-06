// +build !linux,!darwin

package overseer

import (
	"errors"
	"os"
)

var (
	supported = false
	uid       = 0
	gid       = 0
	SIGUSR1   = os.Interrupt
	SIGUSR2   = os.Interrupt
	SIGTERM   = os.Kill
)

func move(dst, src string) error {
	return errors.New("Not supported")
}
