package overseer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpgradeAuthorizationCannotBeForced(t *testing.T) {
	for _, manual := range []bool{false, true} {
		path, _ := authorizationFixture(t, "new")
		mp := &master{Config: &Config{}, binPath: path, forkedHash: []byte("old"), binHash: []byte("new"), forceNext: true}
		calls := 0
		mp.Config.AuthorizeUpgrade = func(path string) error {
			calls++
			if path != mp.binPath {
				t.Fatalf("wrong candidate %s", path)
			}
			_ = mp.info() // Hooks may inspect master state without deadlocking.
			return errors.New("control unavailable")
		}
		if manual {
			mp.triggerRestart()
		} else {
			mp.tryRestart()
		}
		if calls != 1 || !mp.pendingRestart || !mp.forceNext || mp.restarting {
			t.Fatalf("authorization bypassed: %+v", mp.info())
		}
		mp.Config.AuthorizeUpgrade = func(string) error { return nil }
		mp.tryRestart()
		if mp.pendingRestart {
			t.Fatal("approved candidate remained deferred")
		}
	}
}
func TestSameBinaryRestartNeedsNoUpgradeAuthorization(t *testing.T) {
	path, hash := authorizationFixture(t, "same")
	mp := &master{Config: &Config{AuthorizeUpgrade: func(string) error { t.Fatal("same binary requested upgrade approval"); return nil }}, binPath: path, forkedHash: hash, binHash: []byte("stale cache")}
	mp.triggerRestart()
	if mp.pendingRestart {
		t.Fatal("same binary restart blocked")
	}
}

type upgradeReader struct{ payload string }

func (upgradeReader) Init() error                 { return nil }
func (f upgradeReader) Fetch() (io.Reader, error) { return strings.NewReader(f.payload), nil }
func TestDeniedInstallPreservesExecutable(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell fixture")
	}
	path := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(path, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	mp := &master{Config: &Config{Fetcher: upgradeReader{payload: "#!/bin/sh\nprintf %s \"$OVERSEER_BIN_CHECK\"\n"}, AuthorizeUpgrade: func(candidate string) error {
		calls++
		if candidate == path {
			t.Fatal("expected staged candidate")
		}
		return errors.New("denied")
	}}, binPath: path, binHash: []byte("original-hash-is-long-enough"), binPerms: 0755}
	mp.fetch()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "original" || calls != 1 {
		t.Fatalf("install bypassed guard: %q %v calls=%d", b, err, calls)
	}
}

func TestRestartAlreadyRunningDoesNotRequestApproval(t *testing.T) {
	mp := &master{Config: &Config{AuthorizeUpgrade: func(string) error { t.Fatal("duplicate activation approval"); return errors.New("denied") }}, forkedHash: []byte("old"), binHash: []byte("new"), restarting: true}
	mp.triggerRestart()
	if mp.pendingRestart {
		t.Fatal("duplicate restart left a pending retry")
	}
}

func authorizationFixture(t *testing.T, contents string) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidate"+extension())
	if err := os.WriteFile(path, []byte(contents), 0755); err != nil {
		t.Fatal(err)
	}
	hash, err := binaryHash(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, hash
}

func TestExternalReplacementRequiresActivationApproval(t *testing.T) {
	path, oldHash := authorizationFixture(t, "old")
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("external new"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	calls, locks, releases := 0, 0, 0
	mp := &master{Config: &Config{}, binPath: path, binHash: oldHash, forkedHash: oldHash, forceNext: true}
	mp.UpgradeLock = func() (func(), error) { locks++; return func() { releases++ }, nil }
	mp.AuthorizeUpgrade = func(string) error { calls++; _ = mp.info(); return errors.New("new target denied") }
	mp.triggerRestart()
	if calls != 1 || locks != 1 || releases != 1 || !mp.pendingRestart || mp.restarting || !mp.forceNext {
		t.Fatalf("external install bypassed approval: calls=%d locks=%d releases=%d info=%+v", calls, locks, releases, mp.info())
	}
	if !mp.info().UpgradeStaged {
		t.Fatal("external install was not reflected in metadata")
	}
}

func TestActivationProtectsExecutableThroughFork(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell fixture")
	}
	marker := filepath.Join(t.TempDir(), "started")
	path, approvedHash := authorizationFixture(t, "#!/bin/sh\nprintf '%s' \"$OVERSEER_BIN_ID\" > '"+marker+"'\nexec sleep 60\n")
	old := exec.Command("sleep", "60")
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	defer old.Wait()
	defer old.Process.Kill()
	var installer sync.Mutex
	calls := 0
	mp := &master{Config: &Config{RestartSignal: os.Interrupt, TerminateTimeout: time.Millisecond},
		binPath: path, binHash: []byte("stale old"), forkedHash: []byte("old"), workerCmd: old,
		restarted: make(chan bool), descriptorsReleased: make(chan bool)}
	mp.UpgradeLock = func() (func(), error) {
		if !installer.TryLock() {
			return nil, errors.New("installer busy")
		}
		return installer.Unlock, nil
	}
	mp.AuthorizeUpgrade = func(string) error { calls++; _ = mp.info(); return nil }
	mp.triggerRestart() // timeout deliberately tests the handoff after its caller returns
	if calls != 1 {
		t.Fatalf("approvals=%d", calls)
	}
	if installer.TryLock() {
		installer.Unlock()
		t.Fatal("installer could replace approved executable before fork")
	}
	done := make(chan error, 1)
	go func() { done <- mp.fork() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if !installer.TryLock() {
		t.Fatal("installer lock not released after cmd.Start")
	}
	installer.Unlock()
	mp.descriptorsReleased <- true
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mp.workerMux.Lock()
	proc := mp.workerCmd.Process
	mp.workerMux.Unlock()
	defer proc.Kill()
	if !bytes.Equal(mp.forkedHash, approvedHash) || !bytes.Equal(mp.binHash, approvedHash) {
		t.Fatal("fork metadata does not identify executed content")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != fmt.Sprintf("%x", approvedHash) {
		t.Fatalf("worker identity %q: %v", data, err)
	}
}

// Failed activation must return ownership even though startRestart has already
// handed its lock off and the old worker may be gone.
func TestFailedForkReleasesActivationLock(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing=%v", missing), func(t *testing.T) {
			path, oldHash := authorizationFixture(t, "not an executable image")
			if missing {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			var installer sync.Mutex
			installer.Lock()
			releases := 0
			mp := &master{Config: &Config{AuthorizeUpgrade: func(string) error { t.Fatal("approval after worker shutdown"); return nil }},
				binPath: path, forkedHash: oldHash, restarting: true,
				activationRelease: func() { releases++; installer.Unlock() }}
			if err := mp.fork(); err == nil {
				t.Fatal("broken executable unexpectedly started")
			}
			if releases != 1 {
				t.Fatalf("release count=%d", releases)
			}
			if !installer.TryLock() {
				t.Fatal("failed fork retained installer lock")
			}
			installer.Unlock()
			if mp.activationRelease != nil {
				t.Fatal("failed fork retained stale lock callback")
			}
			if !bytes.Equal(mp.forkedHash, oldHash) {
				t.Fatal("failed fork changed running content identity")
			}
		})
	}
}

func TestDeferredRestartReleasesInstallerLock(t *testing.T) {
	for _, reason := range []string{"busy", "no worker", "denied", "lock error", "missing"} {
		t.Run(reason, func(t *testing.T) {
			path, oldHash := authorizationFixture(t, "old")
			var installer sync.Mutex
			acquisitions, releases := 0, 0
			mp := &master{Config: &Config{}, binPath: path, binHash: oldHash, forkedHash: oldHash}
			mp.UpgradeLock = func() (func(), error) {
				if !installer.TryLock() {
					t.Fatal("installer lock already retained")
				}
				acquisitions++
				release := func() { releases++; installer.Unlock() }
				if reason == "lock error" {
					return release, errors.New("unavailable")
				}
				return release, nil
			}
			switch reason {
			case "busy":
				mp.ShouldRestart = func() bool { return false }
			case "denied":
				if err := os.WriteFile(path, []byte("new"), 0755); err != nil {
					t.Fatal(err)
				}
				mp.AuthorizeUpgrade = func(string) error { return errors.New("denied") }
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			mp.tryRestart()
			if acquisitions != 1 || releases != 1 || mp.restarting || mp.activationRelease != nil {
				t.Fatalf("lock leaked: acquired=%d released=%d restarting=%v", acquisitions, releases, mp.restarting)
			}
			if !installer.TryLock() {
				t.Fatal("installer blocked after deferred restart")
			}
			installer.Unlock()
		})
	}
}
