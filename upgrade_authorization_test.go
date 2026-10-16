package overseer

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeAuthorizationCannotBeForced(t *testing.T) {
	for _, manual := range []bool{false, true} {
		mp := &master{Config: &Config{}, binPath: "candidate", forkedHash: []byte("old"), binHash: []byte("new"), forceNext: true}
		calls := 0
		mp.Config.AuthorizeUpgrade = func(path string) error {
			calls++
			if path != "candidate" {
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
	mp := &master{Config: &Config{AuthorizeUpgrade: func(string) error { t.Fatal("same binary requested upgrade approval"); return nil }}, forkedHash: []byte("same"), binHash: []byte("same")}
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
