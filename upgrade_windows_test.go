package overseer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type upgradeState struct {
	Version     string
	PID, Parent int
	ID, Path    string
	Args        []string
	Value       string
}

func TestWindowsRepeatedUpgrade(t *testing.T) {
	build := t.TempDir()
	binaries := map[int32][]byte{}
	for _, version := range []int32{1, 2, 3} {
		path := filepath.Join(build, fmt.Sprintf("v%d.exe", version))
		cmd := exec.Command("go", "build", "-ldflags=-X main.version="+fmt.Sprint(version), "-o", path, "./testdata/upgrade")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		binaries[version] = b
	}
	for _, ownListener := range []bool{false, true} {
		t.Run(fmt.Sprintf("own-listener=%v", ownListener), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "space & percent% Ω")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "server Ω.exe")
			if err := os.WriteFile(path, binaries[1], 0755); err != nil {
				t.Fatal(err)
			}
			var target atomic.Int32
			updates := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v := target.Load()
				if v == 0 {
					w.WriteHeader(204)
					return
				}
				if v == -1 {
					w.Write([]byte("invalid executable"))
					return
				}
				w.Write(binaries[v])
			}))
			defer updates.Close()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := ln.Addr().String()
			ln.Close()
			url := "http://" + address
			var output syncBuffer
			cmd := exec.Command(path, "argument with spaces", "literal & % Ω")
			cmd.Env = append(os.Environ(), "OVERSEER_POC_ROOT="+root, "OVERSEER_POC_ADDR="+address, "OVERSEER_POC_URL="+updates.URL, "OVERSEER_POC_VALUE=preserved Ω")
			if ownListener {
				cmd.Env = append(cmd.Env, "OVERSEER_POC_OWN_LISTENER=1")
			}
			cmd.Stdout, cmd.Stderr = &output, &output
			cmd.WaitDelay = 2 * time.Second
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() { cmd.Process.Kill() })
			client := &http.Client{Timeout: 300 * time.Millisecond}
			read := func() (upgradeState, bool) {
				var s upgradeState
				r, err := client.Get(url)
				if err != nil {
					return s, false
				}
				defer r.Body.Close()
				err = json.NewDecoder(r.Body).Decode(&s)
				return s, err == nil
			}
			wait := func(version string, previousPID int) upgradeState {
				t.Helper()
				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					if s, ok := read(); ok && s.Version == version && s.PID != previousPID {
						return s
					}
					time.Sleep(30 * time.Millisecond)
				}
				t.Fatalf("version %s did not start; output:\n%s", version, output.String())
				return upgradeState{}
			}
			flag := func(name string, enabled bool) {
				t.Helper()
				if enabled {
					if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					os.Remove(filepath.Join(root, name))
				}
			}
			post := func(action string) {
				t.Helper()
				r, err := client.Post(url+action, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
			}
			waitFor := func(check func() bool) {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if check() {
						return
					}
					time.Sleep(30 * time.Millisecond)
				}
				t.Fatalf("condition not observed; output:\n%s", output.String())
			}
			first := wait("1", 0)
			target.Store(-1)
			waitFor(func() bool { return strings.Contains(output.String(), "failed to run temp binary") })
			if s, ok := read(); !ok || s.PID != first.PID {
				t.Fatal("invalid candidate changed worker")
			}
			flag("deny", true)
			target.Store(2)
			waitFor(func() bool {
				return strings.Contains(output.String(), "upgrade authorization deferred: upgrade denied")
			})
			if s, ok := read(); !ok || s.PID != first.PID {
				t.Fatal("denied candidate changed worker")
			}
			flag("busy", true)
			flag("deny", false)
			waitFor(func() bool {
				return strings.Contains(output.String(), "restart deferred: ShouldRestart returned false")
			})
			if s, ok := read(); !ok || s.PID != first.PID {
				t.Fatal("busy worker restarted automatically")
			}
			flag("busy", false)
			second := wait("2", first.PID)
			if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("cleaned-%d", first.PID))); err != nil {
				t.Fatal("upgrade skipped cleanup", err)
			}
			flag("busy", true)
			target.Store(3)
			waitFor(func() bool { b, err := os.ReadFile(path); return err == nil && bytes.Equal(b, binaries[3]) })
			flag("force", true)
			third := wait("3", second.PID)
			post("/restart")
			fourth := wait("3", third.PID)
			for _, s := range []upgradeState{second, third, fourth} {
				if s.Parent != cmd.Process.Pid || s.Value != "preserved Ω" || !reflect.DeepEqual(s.Args, []string{"argument with spaces", "literal & % Ω"}) {
					t.Fatalf("lost process state: %+v", s)
				}
			}
			flag("hang", true)
			post("/restart")
			fifth := wait("3", fourth.PID)
			if !strings.Contains(output.String(), "forcing exit") {
				t.Fatal("hung worker did not require forced exit")
			}
			// Death of the actual parent object must terminate its worker promptly.
			h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(fifth.PID))
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(h)
			cmd.Process.Kill()
			if result, err := windows.WaitForSingleObject(h, 5000); err != nil || result != windows.WAIT_OBJECT_0 {
				t.Fatalf("orphan worker: %v %v", result, err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("master wait hung")
			}
			t.Logf("native Windows: versions 1 -> 2 -> 3; graceful, forced, worker-requested restarts; parent-death cleanup; PIDs %d %d %d %d %d", first.PID, second.PID, third.PID, fourth.PID, fifth.PID)
		})
	}
}

func TestWindowsOverwriteRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original.exe")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := overwrite(path, path+".missing"); err == nil {
		t.Fatal("expected failed replacement")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "original" {
		t.Fatalf("original lost: %q %v", b, err)
	}
}

// Exercise the actual kernel-event watcher with a request parked behind the
// activation lock while its originating worker is replaced.
func TestWindowsStaleWorkerEvent(t *testing.T) {
	a, b := &exec.Cmd{}, &exec.Cmd{}
	mp := &master{Config: &Config{RestartSignal: SIGUSR2, TerminateTimeout: time.Second}, workerCmd: a}
	c := &workerControl{cmd: a}
	var err error
	c.restart, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.stop, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		windows.CloseHandle(c.restart)
		t.Fatal(err)
	}
	mp.control = c
	mp.upgradeMux.Lock()
	mp.workerStarted(a)
	defer func() {
		windows.SetEvent(c.stop)
		<-c.done
		windows.CloseHandle(c.stop)
		windows.CloseHandle(c.restart)
	}()
	windows.SetEvent(c.restart)
	waiting := func() bool {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		return strings.Contains(string(buf[:n]), "overseer.(*master).startRestartFor")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !waiting() {
		if time.Now().After(deadline) {
			mp.upgradeMux.Unlock()
			t.Fatal("watcher did not schedule request")
		}
		time.Sleep(time.Millisecond)
	}
	mp.workerMux.Lock()
	mp.workerCmd = b
	mp.control = &workerControl{cmd: b}
	mp.workerMux.Unlock()
	mp.upgradeMux.Unlock()
	for waiting() {
		if time.Now().After(deadline) {
			t.Fatal("stale request did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	mp.workerMux.Lock()
	queued := len(mp.pendingSignals)
	mp.workerMux.Unlock()
	mp.restartMux.Lock()
	restarting := mp.restarting
	mp.restartMux.Unlock()
	if queued != 0 || restarting {
		t.Fatal("old event restarted replacement")
	}
}

func TestWindowsReplacementSharingDenial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.exe")
	candidate := path + ".new"
	os.WriteFile(path, []byte("good"), 0600)
	os.WriteFile(candidate, []byte("new"), 0600)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := overwrite(path, candidate); err == nil {
		t.Fatal("replacement ignored sharing denial")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "good" {
		t.Fatal("sharing denial damaged installed image", err)
	}
}

func TestWindowsTerminationSignalsSetShutdownEvent(t *testing.T) {
	for _, s := range []os.Signal{os.Interrupt, SIGTERM} {
		t.Run(s.String(), func(t *testing.T) {
			h, err := windows.CreateEvent(nil, 0, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(h)
			proc, err := os.FindProcess(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			defer proc.Release()
			cmd := &exec.Cmd{Process: proc}
			mp := &master{Config: &Config{RestartSignal: SIGUSR2, NoWarn: true}, workerCmd: cmd, control: &workerControl{cmd: cmd, shutdown: h}}
			mp.handleSignal(s)
			result, err := windows.WaitForSingleObject(h, 1000)
			if err != nil || result != windows.WAIT_OBJECT_0 || !mp.stopping {
				t.Fatal("termination failed to signal worker", result, err)
			}
		})
	}
}

func TestWindowsMissingCandidateRestoresAcknowledgedImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.exe")
	backup := path + ".rollback.exe"
	good := []byte("acknowledged image")
	if err := os.WriteFile(backup, good, 0600); err != nil {
		t.Fatal(err)
	}
	hash, err := binaryHash(backup)
	if err != nil {
		t.Fatal(err)
	}
	var installer sync.Mutex
	mp := &master{Config: &Config{ReadyTimeout: time.Second, NoWarn: true}, binPath: path, rollbackPath: backup, rollbackHash: hash, forkedHash: hash, restarting: true, restartDone: make(chan struct{})}
	mp.UpgradeLock = func() (func(), error) {
		if !installer.TryLock() {
			return nil, fmt.Errorf("busy")
		}
		return installer.Unlock, nil
	}
	defer mp.shutdown()
	if err := mp.fork(); err != nil {
		t.Fatal("recovery failed", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(content, good) {
		t.Fatal("acknowledged image not restored", err)
	}
	if installer.TryLock() {
		installer.Unlock()
		t.Fatal("recovery released installer lock before starting worker")
	}
}
