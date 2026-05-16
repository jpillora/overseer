// A tiny helper binary used by waitdelay_integration_test.go.
// It spawns a long-running grandchild that inherits its stderr,
// then exits cleanly. Mirrors the rais vscode case where a
// subprocess outlives the worker but keeps the worker→master
// pipe write-end open.
//
// The grandchild is `sh -c "echo <pid>; exec sleep 60"`: it prints
// its own PID to stdout so the test can target it precisely for
// cleanup instead of pkill'ing every sleep on the box.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	sh := exec.Command("sh", "-c", "echo $$; exec sleep 60")
	sh.Stdin = os.Stdin
	sh.Stdout = os.Stdout
	sh.Stderr = os.Stderr
	if err := sh.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "stderrleaker: spawn failed:", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "stderrleaker: spawned grandchild, exiting")
	time.Sleep(50 * time.Millisecond)
	os.Exit(0)
}
