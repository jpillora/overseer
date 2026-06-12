package overseer

import (
	"bytes"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer guards a bytes.Buffer used as cmd.Stdout: exec's pipe-copier
// goroutine keeps writing while the hang-test reads it mid-Wait.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Reproduces the master-hang bug: a worker subprocess inherits the worker's
// fd 2 (the pipe to its MultiWriter parent) and outlives the worker. Without
// WaitDelay, cmd.Wait blocks forever waiting for an EOF that never comes.
// With WaitDelay set, cmd.Wait returns shortly after the process exits even
// though the inherited pipe write-end is still held open by the grandchild.
func TestCmdWait_StderrLeak_WithWaitDelay(t *testing.T) {
	bin := buildStderrLeaker(t)
	var stdout syncBuffer
	cmd := exec.Command(bin)
	cmd.Stdin = nil
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(io.Discard, &bytes.Buffer{})
	cmd.WaitDelay = 500 * time.Millisecond
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer killGrandchildByStdout(&stdout)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("cmd.Wait did not return within 3s — WaitDelay regressed")
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("expected ProcessState.Exited(), got %v", cmd.ProcessState)
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}

// Same scenario without WaitDelay: cmd.Wait blocks past a short timeout,
// proving the bug is real before the fix. We don't wait long — just enough
// to verify it doesn't return within the same window the fixed path uses.
func TestCmdWait_StderrLeak_WithoutWaitDelay_Hangs(t *testing.T) {
	bin := buildStderrLeaker(t)
	var stdout syncBuffer
	cmd := exec.Command(bin)
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(io.Discard, &bytes.Buffer{})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer killGrandchildByStdout(&stdout)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		t.Fatal("cmd.Wait returned without WaitDelay — grandchild did not actually inherit stderr (test setup bug)")
	case <-time.After(1500 * time.Millisecond):
	}
}

func buildStderrLeaker(t *testing.T) string {
	t.Helper()
	name := "stderrleaker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", out, "./testdata/stderrleaker")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stderrleaker: %s\n%s", err, output)
	}
	return out
}

// Best-effort cleanup: the leaker prints the grandchild PID to its stdout
// before exec'ing into sleep, so we can target only the process this test
// actually spawned. Falls back gracefully if the PID line never appeared.
func killGrandchildByStdout(stdout *syncBuffer) {
	line := strings.TrimSpace(stdout.String())
	if line == "" {
		return
	}
	pid, err := strconv.Atoi(strings.Fields(line)[0])
	if err != nil || pid <= 1 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
