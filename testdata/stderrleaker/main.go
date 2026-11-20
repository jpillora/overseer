// A tiny helper binary used by waitdelay_integration_test.go.
// It spawns a long-running grandchild that inherits its stderr,
// then exits cleanly. The subprocess outlives the worker
// but keeps the worker-to-master
// pipe write-end open.
//
// The grandchild prints its PID so cleanup can target only this helper.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "grandchild" {
		fmt.Println(os.Getpid())
		time.Sleep(time.Minute)
		return
	}
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	sh := exec.Command(path, "grandchild")
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
