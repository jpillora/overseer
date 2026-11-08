package overseer

import (
	"errors"
	"io"
	"os/exec"
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

func TestRestartCompletionIsPerAttempt(t *testing.T) {
	first, second := make(chan struct{}), make(chan struct{})
	mp := &master{Config: &Config{}, restarting: true, restartDone: first}
	mp.completeRestart(first) // Completion is durable even after the waiter timed out.
	mp.restartMux.Lock()
	mp.restarting = true
	mp.restartDone = second
	mp.restartMux.Unlock()
	mp.completeRestart(nil)   // Initial fork must not acknowledge a later request.
	mp.completeRestart(first) // Nor may an old fork complete the next attempt.
	select {
	case <-first:
	default:
		t.Fatal("first attempt not completed")
	}
	select {
	case <-second:
		t.Fatal("old completion completed the next attempt")
	default:
	}
	mp.completeRestart(second)
	select {
	case <-second:
	default:
		t.Fatal("second attempt not completed")
	}
}

func TestStaleRestartRequestDoesNotTargetReplacement(t *testing.T) {
	old, replacement := &exec.Cmd{}, &exec.Cmd{}
	mp := &master{Config: &Config{AuthorizeUpgrade: func(string) error { t.Fatal("stale request reached hook"); return nil }}, workerCmd: replacement}
	mp.startRestartFor(old, false)
	if mp.restarting || len(mp.pendingSignals) != 0 {
		t.Fatal("stale request restarted replacement")
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

// forceNextRestart makes the next auto-restart attempt skip ShouldRestart;
// the mark is consumed by the attempt.
func TestForceNextRestart_BypassesShouldRestart(t *testing.T) {
	var consulted bool
	mp := &master{Config: &Config{
		ShouldRestart: func() bool { consulted = true; return false },
	}}
	mp.forceNextRestart()
	if !mp.info().ForceRequested {
		t.Fatal("expected ForceRequested=true after forceNextRestart")
	}
	mp.tryRestart() //workerCmd nil so no actual restart work happens
	if consulted {
		t.Fatal("ShouldRestart must not be consulted when a force is latched")
	}
	if mp.info().ForceRequested {
		t.Fatal("the force mark must be consumed by the restart attempt")
	}
	if mp.pendingRestart {
		t.Fatal("a forced restart must not defer")
	}
}

// a force latched while ShouldRestart is deferring must release the
// already-pending restart on the next retry tick.
func TestForceNextRestart_ReleasesPendingRestart(t *testing.T) {
	mp := &master{Config: &Config{
		ShouldRestart: func() bool { return false },
	}}
	mp.tryRestart()
	if !mp.pendingRestart {
		t.Fatal("expected pendingRestart=true while deferring")
	}
	mp.forceNextRestart()
	mp.tryRestart() //the fetch loop's retry tick
	if mp.pendingRestart {
		t.Fatal("force must release the deferred restart")
	}
	if mp.info().ForceRequested {
		t.Fatal("the force mark must be consumed")
	}
}

func TestMasterInfo_UpgradeStaged(t *testing.T) {
	mp := &master{Config: &Config{}}
	mp.binHash = []byte{1}
	if mp.info().UpgradeStaged {
		t.Fatal("no fork yet — nothing can be staged")
	}
	mp.forkedHash = mp.binHash
	if mp.info().UpgradeStaged {
		t.Fatal("worker forked from the current binary — not staged")
	}
	mp.binHash = []byte{2}
	if !mp.info().UpgradeStaged {
		t.Fatal("disk binary moved past the forked-from hash — staged")
	}
	mp.forkedHash = mp.binHash
	if mp.info().UpgradeStaged {
		t.Fatal("re-fork on the new binary — no longer staged")
	}
}

func TestMasterInfo_Fields(t *testing.T) {
	mp := &master{Config: &Config{}}
	mp.workerID = 3
	mp.pendingRestart = true
	info := mp.info()
	if !info.IsMaster {
		t.Fatal("expected IsMaster=true")
	}
	if info.WorkerForks != 3 {
		t.Fatalf("expected WorkerForks=3, got %d", info.WorkerForks)
	}
	if !info.RestartPending {
		t.Fatal("expected RestartPending=true")
	}
}

func TestPackageFuncs_NoProcess(t *testing.T) {
	old := currentProcess
	currentProcess = nil
	defer func() { currentProcess = old }()
	ForceNextRestart() //must not panic
	if MasterInfo() != (Info{}) {
		t.Fatal("MasterInfo without a process must be the zero Info")
	}
}

func TestWorker_InfoAndForceAreNoops(t *testing.T) {
	sp := &worker{Config: &Config{}}
	sp.forceNextRestart() //must not panic
	if sp.info() != (Info{}) {
		t.Fatal("worker info must be the zero Info")
	}
}

// Exercises the new force/info paths from many goroutines so
// `go test -race` catches any regression in their locking.
func TestForceInfo_ConcurrentRaceFree(t *testing.T) {
	mp := &master{Config: &Config{
		ShouldRestart: func() bool { return false },
	}}
	mp.binHash = []byte{1}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); mp.forceNextRestart() }()
		go func() { defer wg.Done(); _ = mp.info() }()
		go func() { defer wg.Done(); mp.tryRestart() }()
	}
	wg.Wait()
}

type resultErrorFetcher struct {
	observed, resume chan struct{}
	calls            int
}

func (f *resultErrorFetcher) Init() error { return nil }
func (f *resultErrorFetcher) Fetch() (io.Reader, error) {
	f.calls++
	return nil, errors.New("download rejected")
}
func (f *resultErrorFetcher) FetchResult(error) { close(f.observed); <-f.resume }

func TestFetchOutcomePrecedesSlowResultCallback(t *testing.T) {
	f := &resultErrorFetcher{observed: make(chan struct{}), resume: make(chan struct{})}
	events := make(chan UpgradeEvent, 1)
	mp := &master{Config: &Config{Fetcher: f, NoWarn: true, OnUpgrade: func(e UpgradeEvent) { events <- e }}}
	done := make(chan struct{})
	go func() { mp.fetch(); close(done) }()
	select {
	case <-f.observed:
	case <-time.After(time.Second):
		t.Fatal("fetch result missing")
	}
	newer := mp.upgradeEvent("newer", UpgradeActivated, nil)
	close(f.resume)
	<-done
	rejected := <-events
	if rejected.Status != UpgradeRejected || rejected.Err == nil || rejected.Sequence >= newer.Sequence {
		t.Fatalf("late result regressed outcome order: %+v after %+v", rejected, newer)
	}
}

func TestSkippedFetchDoesNotAcknowledgeResult(t *testing.T) {
	f := &resultErrorFetcher{observed: make(chan struct{}), resume: make(chan struct{})}
	close(f.resume)
	mp := &master{Config: &Config{Fetcher: f}, restarting: true}
	mp.fetch()
	if f.calls != 0 {
		t.Fatal("fetched during restart")
	}
	select {
	case <-f.observed:
		t.Fatal("acknowledged a fetch that did not happen")
	default:
	}
}

func TestShouldRestartMayInspectMasterAndRequestForce(t *testing.T) {
	mp := &master{Config: &Config{}}
	previous := currentProcess
	currentProcess = mp
	defer func() { currentProcess = previous }()
	mp.ShouldRestart = func() bool {
		if !MasterInfo().IsMaster {
			t.Error("predicate cannot inspect master")
		}
		ForceNextRestart()
		return false
	}
	done := make(chan struct{})
	go func() { mp.tryRestart(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("predicate deadlocked on public master APIs")
	}
	if mp.info().RestartPending || mp.info().ForceRequested {
		t.Fatal("force request was not consumed", mp.info())
	}
}

func TestTerminationDuringShouldRestartAbortsAttempt(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	mp := &master{Config: &Config{RestartSignal: SIGUSR2, ShouldRestart: func() bool { close(entered); <-resume; return false }}, workerCmd: &exec.Cmd{}}
	done := make(chan struct{})
	go func() { mp.tryRestart(); close(done) }()
	<-entered
	signalled := make(chan struct{})
	go func() { mp.handleSignal(SIGTERM); close(signalled) }()
	select {
	case <-signalled:
	case <-time.After(time.Second):
		close(resume)
		t.Fatal("predicate blocked termination")
	}
	close(resume)
	<-done
	if !mp.stopping || mp.restarting || mp.pendingRestart {
		t.Fatal("stopped attempt changed restart state")
	}
}
