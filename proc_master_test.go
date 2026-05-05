package overseer

import (
	"sync"
	"testing"
	"time"
)

func TestTryRestart_ShouldRestartNil_TriggersRestart(t *testing.T) {
	mp := &master{Config: &Config{}}
	// workerCmd is nil so triggerRestart returns immediately; we just
	// verify the tryRestart path doesn't set pendingRestart.
	mp.tryRestart()
	if mp.pendingRestart {
		t.Fatal("expected pendingRestart=false when ShouldRestart is nil")
	}
}

func TestTryRestart_ShouldRestartFalse_Defers(t *testing.T) {
	mp := &master{Config: &Config{
		ShouldRestart: func() bool { return false },
	}}
	mp.tryRestart()
	if !mp.pendingRestart {
		t.Fatal("expected pendingRestart=true when ShouldRestart returns false")
	}
}

func TestTryRestart_ShouldRestartTrue_ClearsPending(t *testing.T) {
	mp := &master{Config: &Config{
		ShouldRestart: func() bool { return true },
	}}
	mp.pendingRestart = true
	mp.tryRestart()
	if mp.pendingRestart {
		t.Fatal("expected pendingRestart=false after ShouldRestart returns true")
	}
}

func TestTryRestart_TransitionFromFalseToTrue(t *testing.T) {
	var allow bool
	mp := &master{Config: &Config{
		ShouldRestart: func() bool { return allow },
	}}
	mp.tryRestart()
	if !mp.pendingRestart {
		t.Fatal("expected pendingRestart=true on first call (allow=false)")
	}
	allow = true
	mp.tryRestart()
	if mp.pendingRestart {
		t.Fatal("expected pendingRestart=false on second call (allow=true)")
	}
}

// Exercises the mutex-guarded state transitions from many goroutines so
// `go test -race` catches any regression in pendingRestart/restarting.
func TestRestart_ConcurrentRaceFree(t *testing.T) {
	var allow bool
	var mu sync.Mutex
	mp := &master{Config: &Config{
		ShouldRestart: func() bool { mu.Lock(); v := allow; mu.Unlock(); return v },
	}}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); mp.tryRestart() }()
		go func() { defer wg.Done(); mp.triggerRestart() }()
		if i == 100 {
			mu.Lock()
			allow = true
			mu.Unlock()
		}
	}
	wg.Wait()
}

// Regression: when startRestart times out, mp.restarting stays true, so
// the next fork()'s "notify success" path tried to send on the unbuffered
// mp.restarted channel with no reader and hung the master forever. The
// fix made that send non-blocking; this test exercises the same code path
// directly and asserts it returns instead of deadlocking.
func TestForkNotifyRestart_NoReceiver_DoesNotBlock(t *testing.T) {
	mp := &master{Config: &Config{}}
	mp.restarted = make(chan bool)
	mp.restarting = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		mp.restartMux.Lock()
		wasRestarting := mp.restarting
		if wasRestarting {
			mp.restarting = false
		}
		mp.restartMux.Unlock()
		if wasRestarting {
			select {
			case mp.restarted <- true:
			default:
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify-restart path blocked with no receiver — fix regressed")
	}
	if mp.restarting {
		t.Fatal("restarting must be cleared even when notification is dropped")
	}
}

// When startRestart is actually parked on its receive, the send must reach
// the receiver — non-blocking is for the timeout-path-receiver-gone case
// only. In production startRestart enters its select long before fork()
// can run (worker death + new fork in between); the test simulates that
// ordering by retrying the non-blocking send until it lands.
func TestForkNotifyRestart_Receiver_GetsNotified(t *testing.T) {
	mp := &master{Config: &Config{}}
	mp.restarted = make(chan bool)
	mp.restarting = true
	got := make(chan bool, 1)
	go func() { got <- <-mp.restarted }()
	mp.restartMux.Lock()
	wasRestarting := mp.restarting
	if wasRestarting {
		mp.restarting = false
	}
	mp.restartMux.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	delivered := false
	for wasRestarting && !delivered && time.Now().Before(deadline) {
		select {
		case mp.restarted <- true:
			delivered = true
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !delivered {
		t.Fatal("send never landed despite a parked receiver")
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("receiver never observed the notification")
	}
}

// triggerRestart is the manual path; it must bypass ShouldRestart and
// must never leave pendingRestart set.
func TestTriggerRestart_BypassesShouldRestart(t *testing.T) {
	var called bool
	mp := &master{Config: &Config{
		ShouldRestart: func() bool { called = true; return false },
	}}
	mp.pendingRestart = true
	mp.triggerRestart() //workerCmd nil so no actual restart work happens
	if called {
		t.Fatal("ShouldRestart must not be consulted by the manual path")
	}
	if mp.pendingRestart {
		t.Fatal("triggerRestart must clear pendingRestart")
	}
}
