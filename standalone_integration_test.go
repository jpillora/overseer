package overseer

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStandaloneStartupRecoveryAndDescendants(t *testing.T) {
	binaries := map[int32][]byte{}
	for _, v := range []int32{1, 2, 3} {
		p := filepath.Join(t.TempDir(), "fixture"+extension())
		cmd := exec.Command("go", "build", "-ldflags=-X main.version="+fmt.Sprint(v), "-o", p, "./testdata/upgrade")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		binaries[v] = b
	}
	failures := []string{"exit", "timeout", "bind"}
	if runtime.GOOS == "windows" {
		failures = append(failures, "missing")
	}
	for _, failure := range failures {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "server"+extension())
			if err := os.WriteFile(path, binaries[1], 0755); err != nil {
				t.Fatal(err)
			}
			flag := func(name string, on bool) {
				t.Helper()
				p := filepath.Join(root, name)
				if on {
					if err := os.WriteFile(p, nil, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					os.Remove(p)
				}
			}
			flag("stall-init", true)
			var target atomic.Int32
			var requests atomic.Int32
			updates := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				v := target.Load()
				if v == 0 {
					w.WriteHeader(204)
					return
				}
				if v == -1 {
					w.Write([]byte("invalid candidate"))
					return
				}
				w.Write(binaries[v])
			}))
			defer updates.Close()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			ln.Close()
			var output syncBuffer
			cmd := exec.Command(path)
			cmd.Env = append(os.Environ(), "OVERSEER_POC_ROOT="+root, "OVERSEER_POC_ADDR="+addr, "OVERSEER_POC_URL="+updates.URL, "OVERSEER_POC_READY=1", "OVERSEER_POC_VALUE=preserved")
			cmd.Stdout, cmd.Stderr = &output, &output
			cmd.WaitDelay = time.Second
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() {
				cmd.Process.Kill()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("master wait stuck")
				}
			}()
			client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
			url := "http://" + addr
			waitFor := func(desc string, check func() bool) {
				t.Helper()
				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					if check() {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Fatalf("%s not observed\n%s", desc, output.String())
			}
			read := func() string {
				r, err := client.Get(url)
				if err != nil {
					return ""
				}
				defer r.Body.Close()
				b, _ := io.ReadAll(r.Body)
				return string(b)
			}
			waitFor("worker serving while Init stalled", func() bool { return strings.Contains(read(), `"version":"1"`) })
			if requests.Load() != 0 {
				t.Fatal("Init did not stall")
			}
			flag("stall-fetch", true)
			flag("stall-init", false)
			time.Sleep(150 * time.Millisecond)
			if read() == "" || requests.Load() != 0 {
				t.Fatal("blocked Fetch prevented service")
			}
			r, err := client.Post(url+"/nested", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			nested, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != 200 || !strings.Contains(string(nested), "nested-parent:") {
				t.Fatalf("independent descendant failed: %s", nested)
			}
			// Child starts a separate overseer master, rather than inheriting our role.
			n := strings.TrimSpace(strings.TrimPrefix(string(nested), "nested-parent:"))
			parent, err := strconv.Atoi(n)
			var serving struct{ PID int }
			if err := json.Unmarshal([]byte(read()), &serving); err != nil {
				t.Fatal(err)
			}
			if err != nil || parent == cmd.Process.Pid || parent == serving.PID {
				t.Fatalf("descendant reused original master: %q", nested)
			}
			flag("stall-fetch", false)
			target.Store(-1)
			events := func() string { b, _ := os.ReadFile(filepath.Join(root, "events")); return string(b) }
			waitFor("invalid rejection", func() bool { return strings.Contains(events(), "rejected") })
			if !strings.Contains(read(), `"version":"1"`) {
				t.Fatal("invalid candidate stopped service")
			}
			if err := os.WriteFile(filepath.Join(root, "fail-version"), []byte("2"), 0600); err != nil {
				t.Fatal(err)
			}
			if failure == "timeout" {
				flag("fail-hang", true)
			}
			if failure == "bind" {
				flag("fail-bind", true)
			}
			if failure == "missing" {
				flag("remove-on-shutdown", true)
			}
			target.Store(2)
			waitFor("health rollback", func() bool { return strings.Contains(events(), "rolled_back") })
			// Stop reoffering the bad candidate while checking the recovered service.
			target.Store(0)
			waitFor("known-good worker recovery", func() bool { return strings.Contains(read(), `"version":"1"`) })
			b, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(b, binaries[1]) {
				t.Fatal("rollback did not restore installed content", err)
			}
			target.Store(3)
			waitFor("upgrade after recovery", func() bool { return strings.Contains(read(), `"version":"3"`) })
			h, _ := binaryHash(path)
			waitFor("activation acknowledgement", func() bool { return strings.Contains(events(), fmt.Sprintf("activated %x", h)) })
			if strings.Contains(events(), "activated "+candidateID(binaries[2])) {
				t.Fatal("unready candidate reported active")
			}
		})
	}
	for _, s := range []os.Signal{os.Interrupt, SIGTERM} {
		t.Run("stop-during-readiness-"+s.String(), func(t *testing.T) { testStopDuringReadiness(t, binaries, s) })
	}
	t.Run("startup-updates", func(t *testing.T) { testStartupUpdates(t, binaries) })
	t.Run("outcome-sequences", func(t *testing.T) { testOutcomeSequences(t, binaries) })
	t.Run("shutdown-cancels-validation", func(t *testing.T) { testShutdownCancelsValidation(t, binaries) })
}

func testShutdownCancelsValidation(t *testing.T, binaries map[int32][]byte) {
	root := t.TempDir()
	path := filepath.Join(root, "server"+extension())
	if err := os.WriteFile(path, binaries[1], 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hang-sanity"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OVERSEER_POC_ROOT", root)
	active, _ := binaryHash(path)
	ctx, cancel := context.WithCancel(context.Background())
	mp := &master{Config: &Config{Fetcher: upgradeReader{payload: string(binaries[2])}, SanityCheckTimeout: time.Minute, NoWarn: true}, ctx: ctx, cancel: cancel, done: make(chan struct{}), binPath: path, binHash: active, binPerms: 0755}
	defer mp.shutdown()
	done := make(chan struct{})
	go func() { mp.fetch(); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(filepath.Join(root, "sanity-running")); err == nil {
			pid, err := strconv.Atoi(string(b))
			if err != nil {
				t.Fatal(err)
			}
			if proc, err := os.FindProcess(pid); err == nil {
				defer proc.Release()
				defer proc.Kill()
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("candidate sanity command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mp.shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel sanity command")
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, binaries[1]) {
		t.Fatal("shutdown changed installed executable", err)
	}
}

type blockedResultFetcher struct {
	payload          []byte
	accepted, resume chan struct{}
}

func (f *blockedResultFetcher) Init() error               { return nil }
func (f *blockedResultFetcher) Fetch() (io.Reader, error) { return bytes.NewReader(f.payload), nil }
func (f *blockedResultFetcher) FetchResult(err error) {
	if err == nil {
		close(f.accepted)
		<-f.resume
	}
}

// Activation may overtake a slow fetching-side acknowledgement. Sequence must
// identify the actual transitions even when callbacks are delivered in reverse.
func testOutcomeSequences(t *testing.T, binaries map[int32][]byte) {
	root := t.TempDir()
	path := filepath.Join(root, "server"+extension())
	if err := os.WriteFile(path, binaries[1], 0755); err != nil {
		t.Fatal(err)
	}
	active, _ := binaryHash(path)
	t.Setenv("OVERSEER_POC_ROOT", root)
	t.Setenv("OVERSEER_POC_ADDR", "127.0.0.1:0")
	f := &blockedResultFetcher{payload: binaries[2], accepted: make(chan struct{}), resume: make(chan struct{})}
	events := make(chan UpgradeEvent, 4)
	mp := &master{Config: &Config{Fetcher: f, ReadyTimeout: time.Second, NoRestartAfterFetch: true, NoWarn: true, OnUpgrade: func(e UpgradeEvent) { events <- e }}, binPath: path, binPerms: 0755, binHash: active, forkedHash: active}
	mp.fetchOnce.Do(func() {}) // This test explicitly owns the single fetch call.
	fetched := make(chan struct{})
	go func() { mp.fetch(); close(fetched) }()
	select {
	case <-f.accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("candidate was not installed")
	}
	done := make(chan error, 1)
	go func() { done <- mp.fork() }()
	var activated UpgradeEvent
	select {
	case activated = <-events:
	case <-time.After(5 * time.Second):
		close(f.resume)
		t.Fatal("candidate was not activated")
	}
	mp.workerMux.Lock()
	proc := mp.workerCmd.Process
	mp.workerMux.Unlock()
	defer proc.Kill()
	close(f.resume)
	var staged UpgradeEvent
	select {
	case staged = <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("staged event missing")
	}
	<-fetched
	if staged.Status != UpgradeStaged || activated.Status != UpgradeActivated || staged.ID != activated.ID || staged.Sequence >= activated.Sequence {
		t.Fatalf("transitions out of order: staged=%+v activated=%+v", staged, activated)
	}
	// End fork's wait without terminating the test runner via exit forwarding.
	mp.restartMux.Lock()
	released := mp.descriptorsReleased
	mp.restartMux.Unlock()
	released <- true
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func testStopDuringReadiness(t *testing.T, binaries map[int32][]byte, stop os.Signal) {
	root := t.TempDir()
	path := filepath.Join(root, "server"+extension())
	backup := filepath.Join(root, "rollback"+extension())
	for p, b := range map[string][]byte{path: binaries[2], backup: binaries[1], filepath.Join(root, "fail-version"): []byte("2"), filepath.Join(root, "fail-hang"): nil} {
		if err := os.WriteFile(p, b, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OVERSEER_POC_ROOT", root)
	t.Setenv("OVERSEER_POC_ADDR", "127.0.0.1:0")
	active, _ := binaryHash(backup)
	mp := &master{Config: &Config{RestartSignal: SIGUSR2, ReadyTimeout: time.Second, TerminateTimeout: 200 * time.Millisecond, WaitDelay: time.Second, NoWarn: true}, binPath: path, binPerms: 0755, forkedHash: active, rollbackHash: active, rollbackPath: backup, restarting: true, restartDone: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- mp.fork() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "unready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			mp.workerMux.Lock()
			if mp.workerCmd != nil && mp.workerCmd.Process != nil {
				mp.workerCmd.Process.Kill()
			}
			mp.workerMux.Unlock()
			t.Fatal("unready candidate never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mp.handleSignal(stop)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("termination scheduled recovery")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("stop during readiness stuck")
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, binaries[1]) {
		t.Fatal("stop failed to preserve known-good image", err)
	}
	// A signal between restoring the image and recovery startup must also win.
	if err := mp.fork(); err == nil {
		t.Fatal("stopping master started a recovery worker")
	}
}

func candidateID(b []byte) string { h := sha1.Sum(b); return fmt.Sprintf("%x", h) }
