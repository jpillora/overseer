package overseer

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunStopDoesNotInvokeFallback(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunStopHelper$")
	cmd.Env = append(os.Environ(), "OVERSEER_TEST_RUN_STOP=1", "OVERSEER_TEST_RUN_ROOT="+root)
	cmd.WaitDelay = time.Second
	done := make(chan error, 1)
	var output syncBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run stop: %v\n%s", err, output.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not stop")
	}
	if _, err := os.Stat(filepath.Join(root, "unready")); err != nil {
		t.Fatal("worker was not exercised")
	}
	if _, err := os.Stat(filepath.Join(root, "fallback")); !os.IsNotExist(err) {
		t.Fatal("Run restarted application without supervision")
	}
}

func TestRunStopHelper(t *testing.T) {
	if os.Getenv("OVERSEER_TEST_RUN_STOP") != "1" {
		return
	}
	root := os.Getenv("OVERSEER_TEST_RUN_ROOT")
	published := make(chan *master, 1)
	go func() {
		mp := <-published
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(root, "unready")); err == nil {
				mp.handleSignal(os.Interrupt)
				return
			}
			if time.Now().After(deadline) {
				os.Exit(8)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	RunWithOptions(Config{TerminateTimeout: 100 * time.Millisecond, NoWarn: true,
		UpgradeLock: func() (func(), error) { published <- currentProcess.(*master); return func() {}, nil },
		Program: func(s State) {
			if !s.Enabled {
				os.WriteFile(filepath.Join(root, "fallback"), nil, 0600)
				return
			}
			os.WriteFile(filepath.Join(root, "unready"), nil, 0600)
			select {}
		},
	}, Options{ReadyTimeout: time.Second})
	os.Exit(9)
}
