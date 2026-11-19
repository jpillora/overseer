package overseer

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunFallbackDetachesSupervisor(t *testing.T) {
	root := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	ln.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunFallbackHelper$")
	cmd.Env = append(os.Environ(), "OVERSEER_TEST_FALLBACK=1", "OVERSEER_TEST_FALLBACK_ROOT="+root, "OVERSEER_TEST_FALLBACK_ADDR="+address)
	var output syncBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "fallback")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("fallback failed: %v\n%s", err, output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("fallback did not start\n%s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, "unready")); err != nil {
		t.Fatal("worker did not run")
	}
	// Windows os.Process.Signal cannot send console termination to another
	// process. The helper checks handler shutdown on every platform; Unix also
	// exercises actual SIGTERM delivery to the fallback process.
	if runtime.GOOS == "windows" {
		err = cmd.Process.Kill()
	} else {
		err = cmd.Process.Signal(SIGTERM)
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("fallback ignored termination\n%s", output.String())
	}
}

func TestRunFallbackHelper(t *testing.T) {
	if os.Getenv("OVERSEER_TEST_FALLBACK") != "1" {
		return
	}
	root, address := os.Getenv("OVERSEER_TEST_FALLBACK_ROOT"), os.Getenv("OVERSEER_TEST_FALLBACK_ADDR")
	fail := func(err any) { fmt.Fprintln(os.Stderr, err); os.Exit(7) }
	RunWithOptions(Config{Address: address, TerminateTimeout: 100 * time.Millisecond, NoWarn: true, Program: func(s State) {
		if s.Enabled {
			os.WriteFile(filepath.Join(root, "unready"), nil, 0600)
			select {}
		}
		if MasterInfo() != (Info{}) {
			fail("disabled fallback still exposes master state")
		}
		ForceNextRestart()
		Restart()
		if MasterInfo() != (Info{}) {
			fail("disabled fallback re-enabled supervisor")
		}
		mp := currentProcess.(*master)
		select {
		case <-mp.signalsDone:
		default:
			fail("master signal handler still running")
		}
		mp.workerMux.Lock()
		worker := mp.workerCmd
		mp.workerMux.Unlock()
		if worker != nil || len(mp.workerExtraFiles) != 0 || mp.activationRelease != nil {
			fail("master retained worker or listener resources")
		}
		ln, err := net.Listen("tcp", address)
		if err != nil {
			fail(fmt.Errorf("master retained listener: %w", err))
		}
		ln.Close()
		if err := os.WriteFile(filepath.Join(root, "fallback"), nil, 0600); err != nil {
			fail(err)
		}
		for {
			time.Sleep(time.Minute)
		}
	}}, Options{ReadyTimeout: 500 * time.Millisecond})
	os.Exit(9)
}
