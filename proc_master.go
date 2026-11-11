package overseer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jpillora/overseer/fetcher"
	"github.com/jpillora/overseer/opanic"
)

// a overseer master process
type master struct {
	*Config
	workerID            int
	workerMux           sync.Mutex
	workerCmd           *exec.Cmd
	pendingSignals      []os.Signal
	workerExtraFiles    []*os.File
	binPath             string
	control             *workerControl
	binPerms            os.FileMode
	binHash             []byte
	upgradeMux          sync.Mutex
	activationRelease   func() // installer lock transferred from restart approval to cmd.Start
	restartMux          sync.Mutex
	restarting          bool
	restartedAt         time.Time
	restartDone         chan struct{}
	fetchOnce           sync.Once
	fetchStart          chan struct{} // releases background polling after first worker activation
	startupCandidate    bool          // protected by restartMux; transitions also hold upgradeMux
	rollbackPath        string
	rollbackHash        []byte
	eventSequence       uint64
	resetFetcher        bool
	stopping            bool
	awaitingUSR1        bool
	descriptorsReleased chan bool
	signalledAt         time.Time
	printCheckUpdate    bool
	pendingRestart      bool
	forceNext           bool
	forkedHash          []byte
	done                chan struct{}
	shutdownOnce        sync.Once
	signals             chan os.Signal
	signalsDone         chan struct{}
	disabled            bool
	ctx                 context.Context
	cancel              context.CancelFunc
}

func (mp *master) run() error {
	mp.done = make(chan struct{})
	mp.ctx, mp.cancel = context.WithCancel(context.Background())
	defer mp.shutdown()
	mp.debugf("run")
	if err := mp.checkBinary(); err != nil {
		return err
	}
	mp.setupSignalling()
	if err := mp.retreiveFileDescriptors(); err != nil {
		return err
	}
	err := mp.startupFetch()
	if err == nil {
		err = mp.forkLoop()
	}
	mp.restartMux.Lock()
	stopping := mp.stopping
	mp.restartMux.Unlock()
	if stopping {
		return fmt.Errorf("%w: %v", ErrStopped, err)
	}
	return err
}

func (mp *master) checkBinary() error {
	//get path to binary and confirm its writable
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find binary path (%s)", err)
	}
	mp.binPath = binPath
	if info, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("failed to stat binary (%s)", err)
	} else if info.Size() == 0 {
		return fmt.Errorf("binary file is empty")
	} else {
		//copy permissions
		mp.binPerms = info.Mode()
	}
	f, err := os.Open(binPath)
	if err != nil {
		return fmt.Errorf("cannot read binary (%s)", err)
	}
	//initial hash of file
	hash := sha1.New()
	io.Copy(hash, f)
	mp.binHash = hash.Sum(nil)
	f.Close()
	if mp.Config.Fetcher != nil {
		probe, err := os.CreateTemp(filepath.Dir(mp.binPath), ".overseer-write-check-*")
		if err != nil {
			return fmt.Errorf("cannot write binary directory: %w", err)
		}
		probe.Close()
		os.Remove(probe.Name())
	}
	return nil
}

func (mp *master) setupSignalling() {
	if mp.done == nil {
		mp.done = make(chan struct{})
	}
	//updater-forker comms
	mp.descriptorsReleased = make(chan bool)
	//read all master process signals. Buffer of 1 satisfies signal.Notify's
	//requirement that the channel never block; the actual queueing for the
	//fork window happens in handleSignal under workerMux.
	mp.signals = make(chan os.Signal, 1)
	mp.signalsDone = make(chan struct{})
	signal.Notify(mp.signals)
	go func() {
		defer close(mp.signalsDone)
		for {
			select {
			case <-mp.done:
				return
			case s := <-mp.signals:
				mp.handleSignal(s)
			}
		}
	}()
}

func (mp *master) handleSignal(s os.Signal) {
	mp.restartMux.Lock()
	if mp.disabled {
		mp.restartMux.Unlock()
		return
	}
	if s == os.Interrupt || s == SIGTERM {
		mp.stopping = true
	}
	mp.restartMux.Unlock()
	if (s == os.Interrupt || s == SIGTERM) && mp.cancel != nil {
		mp.cancel()
	}
	if s == mp.RestartSignal && s != os.Interrupt && s != SIGTERM {
		//user initiated manual restart
		go mp.triggerRestart()
		return
	}
	if s.String() == "child exited" {
		// will occur on every restart, ignore it
		return
	}
	// Since Go 1.14 the runtime uses SIGURG to preempt goroutines; it is
	// delivered to the master continuously and must not be proxied. Once
	// the worker exits, forwarding it races with cmdwait and the failure
	// path used to os.Exit(1), skipping OnPanic and masking the real code.
	if s.String() == "urgent I/O condition" {
		return
	}
	//**during a restart** a SIGUSR1 signals
	//to the master process that, the file
	//descriptors have been released
	if s == SIGUSR1 && s != SIGTERM {
		mp.restartMux.Lock()
		awaiting := mp.awaitingUSR1
		released := mp.descriptorsReleased
		if awaiting {
			mp.awaitingUSR1 = false
		}
		mp.restartMux.Unlock()
		if awaiting {
			mp.debugf("signaled, sockets ready")
			select {
			case released <- true:
			default:
			}
			return
		}
		// SIGUSR1 is private socket-handoff traffic. A late release from a
		// worker that already exited must never be forwarded to its successor.
		return
	}
	//while the worker process is running, proxy all signals through.
	//during the fork window (workerCmd set but cmd.Start() not yet
	//returned, so Process is still nil), queue the signal — fork() will
	//drain pendingSignals once Start succeeds. Without this, a signal
	//racing a fresh fork is silently dropped and the new worker never
	//learns systemd wants it dead, hanging unit stop until SIGKILL.
	mp.workerMux.Lock()
	if mp.workerCmd != nil && mp.workerCmd.Process == nil {
		mp.pendingSignals = append(mp.pendingSignals, s)
		mp.workerMux.Unlock()
		mp.debugf("signal queued (%s), worker starting", s)
		return
	}
	workerCmd := mp.workerCmd
	hasWorker := workerCmd != nil && workerCmd.Process != nil
	mp.workerMux.Unlock()
	if hasWorker {
		mp.debugf("proxy signal (%s)", s)
		mp.sendSignalTo(workerCmd, s)
		return
	}
	// During the startup update window, cancellation wakes run without starting
	// a worker. Other signals with no current worker have nowhere to be sent.
	mp.debugf("signal received (%s), no worker process", s)
}

func (mp *master) sendSignalTo(cmd *exec.Cmd, s os.Signal) {
	mp.workerMux.Lock()
	if cmd == nil || mp.workerCmd != cmd {
		mp.workerMux.Unlock()
		return
	}
	proc := cmd.Process
	if proc == nil {
		mp.pendingSignals = append(mp.pendingSignals, s)
		mp.workerMux.Unlock()
		return
	}
	mp.workerMux.Unlock()
	if err := mp.signalWorker(proc, s); err != nil && !errors.Is(err, os.ErrProcessDone) {
		mp.debugf("signal failed (%s)", err)
	}
}

// fetchLoop is run in a goroutine
func (mp *master) startFetching() {
	mp.fetchOnce.Do(func() {
		if mp.Fetcher == nil {
			return
		}
		if mp.fetchStart != nil {
			close(mp.fetchStart)
		} else {
			// A disabled startup check retains independent worker startup.
			go func() {
				if err := mp.Fetcher.Init(); err != nil {
					mp.warnf("fetcher init failed: %s", err)
					return
				}
				mp.printCheckUpdate = true
				mp.fetchLoop()
			}()
		}
		go func() {
			ticker := time.NewTicker(mp.MinFetchInterval)
			defer ticker.Stop()
			for {
				select {
				case <-mp.done:
					return
				case <-ticker.C:
					mp.restartMux.Lock()
					retry := mp.pendingRestart && !mp.restarting
					mp.restartMux.Unlock()
					if retry {
						mp.tryRestart()
					}
				}
			}
		}()
	})
}

func (mp *master) fetchLoop() {
	for {
		select {
		case <-mp.done:
			return
		default:
		}
		t0 := time.Now()
		mp.fetch()
		if delay := mp.MinFetchInterval - time.Since(t0); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-mp.done:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

// Record transitions while upgradeMux still protects the executable state.
func (mp *master) upgradeEvent(id string, status UpgradeStatus, err error) UpgradeEvent {
	mp.restartMux.Lock()
	defer mp.restartMux.Unlock()
	mp.eventSequence++
	return UpgradeEvent{Sequence: mp.eventSequence, ID: id, Status: status, Err: err}
}
func (mp *master) dispatchUpgrade(event UpgradeEvent) {
	mp.restartMux.Lock()
	disabled := mp.disabled
	mp.restartMux.Unlock()
	if disabled {
		return
	}
	if mp.OnUpgrade != nil {
		mp.OnUpgrade(event)
	}
}
func (mp *master) fetch() {
	mp.restartMux.Lock()
	if mp.restarting || mp.stopping {
		mp.restartMux.Unlock()
		return
	}
	reset := mp.resetFetcher
	mp.resetFetcher = false
	mp.restartMux.Unlock()
	if reset {
		if f, ok := mp.Fetcher.(fetcher.Resetter); ok {
			f.Reset()
		}
	}
	reader, err := mp.fetchReader(mp.lifecycleContext())
	mp.processFetched(reader, err, true)
}

// processFetched and fetcher acknowledgements run only on the fetching goroutine.
func (mp *master) processFetched(reader io.Reader, fetchErr error, restart bool) {
	event, installed, err := mp.installCandidate(reader, fetchErr)
	if err != nil {
		event = mp.upgradeEvent(event.ID, UpgradeRejected, err)
	}
	if receiver, ok := mp.Fetcher.(fetcher.Result); ok {
		receiver.FetchResult(err)
	}
	if err != nil {
		mp.dispatchUpgrade(event)
		return
	}
	if !installed {
		return
	}
	mp.dispatchUpgrade(event)
	if restart && !mp.NoRestartAfterFetch {
		mp.tryRestart()
	}
}

func (mp *master) fetchReader(ctx context.Context) (io.Reader, error) {
	if f, ok := mp.Fetcher.(fetcher.ContextFetcher); ok {
		return f.FetchContext(ctx)
	}
	return mp.Fetcher.Fetch()
}

func (mp *master) installCandidate(reader io.Reader, fetchErr error) (event UpgradeEvent, installed bool, result error) {
	if mp.printCheckUpdate {
		mp.debugf("checking for updates...")
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	if fetchErr != nil {
		mp.debugf("failed to get latest version: %s", fetchErr)
		return event, false, fetchErr
	}
	if reader == nil {
		if mp.printCheckUpdate {
			mp.debugf("no updates")
		}
		mp.printCheckUpdate = false
		return event, false, nil //no updates
	}
	mp.printCheckUpdate = true
	mp.debugf("streaming update...")
	if mp.isStopping() {
		return event, false, ErrStopped
	}
	tmpBin, err := os.CreateTemp(filepath.Dir(mp.binPath), ".overseer-upgrade-*"+extension())
	if err != nil {
		mp.warnf("failed to open temp binary: %s", err)
		return event, false, err
	}
	tmpBinPath := tmpBin.Name()
	defer func() {
		tmpBin.Close()
		os.Remove(tmpBinPath)
	}()
	//tee off to sha1
	hash := sha1.New()
	reader = io.TeeReader(reader, hash)
	//write to a temp file
	_, err = io.Copy(tmpBin, reader)
	if err != nil {
		mp.warnf("failed to write temp binary: %s", err)
		return event, false, err
	}
	if mp.isStopping() {
		return event, false, ErrStopped
	}
	//compare hash
	newHash := hash.Sum(nil)
	event.ID = hex.EncodeToString(newHash)
	mp.restartMux.Lock()
	unchanged := bytes.Equal(mp.binHash, newHash)
	mp.restartMux.Unlock()
	if unchanged {
		mp.debugf("hash match - skip")
		return event, false, err
	}
	//copy permissions
	if err := chmod(tmpBin, mp.binPerms); err != nil {
		mp.warnf("failed to make temp binary executable: %s", err)
		return event, false, err
	}
	if err := chown(tmpBin, uid, gid); err != nil {
		mp.warnf("failed to change owner of binary: %s", err)
		return event, false, err
	}
	if _, err := tmpBin.Stat(); err != nil {
		mp.warnf("failed to stat temp binary: %s", err)
		return event, false, err
	}
	tmpBin.Close()
	if _, err := os.Stat(tmpBinPath); err != nil {
		mp.warnf("failed to stat temp binary by path: %s", err)
		return event, false, err
	}
	if mp.Config.PreUpgrade != nil {
		if err := mp.Config.PreUpgrade(tmpBinPath); err != nil {
			mp.warnf("user cancelled upgrade: %s", err)
			return event, false, err
		}
	}
	if mp.isStopping() {
		return event, false, ErrStopped
	}
	//overseer sanity check, dont replace our good binary with a non-executable file
	tokenIn := token()
	checkTimeout := mp.SanityCheckTimeout
	if checkTimeout <= 0 {
		checkTimeout = defaultSanityCheckTimeout
	}
	parentCtx := mp.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	checkCtx, cancelCheck := context.WithTimeout(parentCtx, checkTimeout)
	defer cancelCheck()
	cmd := exec.CommandContext(checkCtx, tmpBinPath)
	cmd.Env = append(os.Environ(), envBinCheck+"="+tokenIn)
	cmd.Args = os.Args
	cmd.WaitDelay = time.Second
	tokenOut, err := cmd.CombinedOutput()
	if err != nil {
		mp.warnf("failed to run temp binary: %s (%s) output \"%s\"", err, tmpBinPath, tokenOut)
		return event, false, err
	}
	if tokenIn != string(tokenOut) {
		mp.warnf("sanity check failed")
		return event, false, errors.New("candidate sanity check failed")
	}
	// Serialize candidate replacement with activation authorization.
	mp.upgradeMux.Lock()
	mp.restartMux.Lock()
	restarting := mp.restarting || mp.stopping
	mp.restartMux.Unlock()
	if restarting {
		mp.upgradeMux.Unlock()
		return event, false, errors.New("restart in progress")
	}
	release, err := mp.lockUpgrade()
	if err != nil {
		mp.upgradeMux.Unlock()
		release()
		mp.warnf("upgrade lock: %s", err)
		return event, false, err
	}
	if mp.Config.AuthorizeUpgrade != nil {
		if err := mp.Config.AuthorizeUpgrade(tmpBinPath); err != nil {
			release()
			mp.upgradeMux.Unlock()
			mp.warnf("upgrade authorization deferred: %s", err)
			return event, false, err
		}
	}
	if mp.isStopping() {
		release()
		mp.upgradeMux.Unlock()
		return event, false, ErrStopped
	}
	if err := mp.saveRollback(); err != nil {
		release()
		mp.upgradeMux.Unlock()
		return event, false, err
	}
	if err := overwrite(mp.binPath, tmpBinPath); err != nil {
		release()
		mp.upgradeMux.Unlock()
		mp.warnf("failed to overwrite binary: %s", err)
		return event, false, err
	}
	//binHash is read by fork() and info() on other goroutines
	mp.restartMux.Lock()
	oldHash := mp.binHash
	mp.binHash = newHash
	mp.startupCandidate = len(mp.forkedHash) == 0
	mp.restartMux.Unlock()
	event = mp.upgradeEvent(event.ID, UpgradeStaged, nil)
	release()
	mp.upgradeMux.Unlock()
	mp.infof("upgraded binary (%x -> %x)", oldHash, newHash)
	return event, true, nil
}

// tryRestart is the auto-restart path invoked from the fetch loop; it
// consults Config.ShouldRestart and defers if the predicate returns false.
func (mp *master) tryRestart() {
	mp.startRestart(true)
}

// triggerRestart is the manual-restart path invoked from the signal handler
// and the exported Restart() function; it bypasses Config.ShouldRestart.
func (mp *master) triggerRestart() {
	mp.startRestart(false)
}

// forceNextRestart marks the next restart attempt to skip ShouldRestart;
// backs the exported ForceNextRestart() function.
func (mp *master) forceNextRestart() {
	mp.restartMux.Lock()
	if mp.stopping {
		mp.restartMux.Unlock()
		return
	}
	mp.forceNext = true
	mp.restartMux.Unlock()
	mp.debugf("force next restart requested")
}

// info backs the exported MasterInfo() function.
func (mp *master) info() Info {
	mp.restartMux.Lock()
	defer mp.restartMux.Unlock()
	if mp.disabled {
		return Info{}
	}
	staged := (mp.forkedHash != nil || mp.startupCandidate) && !bytes.Equal(mp.forkedHash, mp.binHash)
	stagedID := ""
	if staged {
		stagedID = hex.EncodeToString(mp.binHash)
	}
	return Info{
		ActiveID: hex.EncodeToString(mp.forkedHash), StagedID: stagedID,
		IsMaster:       true,
		UpgradeStaged:  staged,
		RestartPending: mp.pendingRestart,
		ForceRequested: mp.forceNext,
		WorkerForks:    mp.workerID,
	}
}

// startRestart atomically claims ownership of the restart and, if claimed,
// performs the graceful shutdown handshake. When checkShouldRestart is true
// the caller is the fetch loop and ShouldRestart may defer the restart.
func (mp *master) startRestart(checkShouldRestart bool) { mp.startRestartFor(nil, checkShouldRestart) }

func (mp *master) startRestartFor(expected *exec.Cmd, checkShouldRestart bool) {
	mp.upgradeMux.Lock()
	if expected != nil {
		mp.workerMux.Lock()
		current := mp.workerCmd == expected
		mp.workerMux.Unlock()
		if !current {
			mp.upgradeMux.Unlock()
			return
		}
	}
	mp.restartMux.Lock()
	if mp.restarting || mp.stopping {
		mp.restartMux.Unlock()
		mp.upgradeMux.Unlock()
		return
	}
	hasFork := mp.forkedHash != nil
	mp.restartMux.Unlock()
	release := func() {}
	staged := false
	if hasFork {
		var err error
		release, err = mp.lockUpgrade()
		if err == nil {
			var hash []byte
			hash, err = binaryHash(mp.binPath)
			if err == nil {
				mp.restartMux.Lock()
				mp.binHash = hash
				staged = !bytes.Equal(mp.forkedHash, hash)
				mp.restartMux.Unlock()
			}
		}
		if err != nil {
			mp.restartMux.Lock()
			mp.pendingRestart = true
			mp.restartMux.Unlock()
			release()
			mp.upgradeMux.Unlock()
			mp.debugf("upgrade activation deferred: %s", err)
			return
		}
	}
	if staged && mp.Config.AuthorizeUpgrade != nil {
		if err := mp.Config.AuthorizeUpgrade(mp.binPath); err != nil {
			mp.restartMux.Lock()
			mp.pendingRestart = true
			mp.restartMux.Unlock()
			release()
			mp.upgradeMux.Unlock()
			mp.debugf("upgrade activation deferred: %s", err)
			return
		}
	}
	// Call user policy without restartMux: it may inspect MasterInfo or set a
	// force request. upgradeMux still serializes this approval with activation.
	mp.restartMux.Lock()
	consult := checkShouldRestart && !mp.forceNext && mp.Config.ShouldRestart != nil
	mp.restartMux.Unlock()
	allow := !consult || mp.Config.ShouldRestart()
	mp.restartMux.Lock()
	if mp.restarting || mp.stopping {
		mp.debugf("already graceful restarting")
		mp.restartMux.Unlock()
		release()
		mp.upgradeMux.Unlock()
		return
	}
	if checkShouldRestart && !mp.forceNext && !allow {
		if !mp.pendingRestart {
			mp.debugf("restart deferred: ShouldRestart returned false")
		}
		mp.pendingRestart = true
		mp.restartMux.Unlock()
		release()
		mp.upgradeMux.Unlock()
		return
	}
	if checkShouldRestart && mp.forceNext {
		mp.debugf("restart forced, skipping ShouldRestart")
	}
	mp.pendingRestart = false
	mp.forceNext = false
	mp.workerMux.Lock()
	workerCmd := mp.workerCmd
	hasWorker := workerCmd != nil
	mp.workerMux.Unlock()
	if !hasWorker {
		mp.debugf("no worker process")
		mp.restartMux.Unlock()
		release()
		mp.upgradeMux.Unlock()
		return
	}
	mp.restarting = true
	done := make(chan struct{})
	mp.restartDone = done
	mp.awaitingUSR1 = true
	mp.signalledAt = time.Now()
	// Keep the approved path protected while the old worker drains. fork takes
	// ownership and releases only after the replacement has actually started.
	mp.activationRelease = release
	mp.restartMux.Unlock()
	mp.upgradeMux.Unlock()
	mp.infof("graceful restart triggered")
	mp.sendSignalTo(workerCmd, mp.Config.RestartSignal) //ask nicely to terminate
	select {
	case <-mp.done:
		return
	case <-done:
		//success
		mp.infof("restart success")
	case <-time.After(mp.TerminateTimeout):
		//times up mr. process, we did ask nicely!
		mp.infof("graceful timeout (%s), forcing exit", mp.TerminateTimeout)
		mp.sendSignalTo(workerCmd, os.Kill)
	}
}

// not a real fork
func (mp *master) forkLoop() error {
	//loop, restart command
	for {
		if err := mp.fork(); err != nil {
			return err
		}
	}
}

func (mp *master) fork() error {
	// A restart passes the installer lock from its pre-shutdown approval.
	// Initial master startup instead trusts the explicitly started executable.
	mp.upgradeMux.Lock()
	mp.restartMux.Lock()
	release := mp.activationRelease
	mp.activationRelease = nil
	attempt := mp.restartDone
	stopping := mp.stopping
	startupCandidate := mp.startupCandidate
	mp.restartMux.Unlock()
	if stopping {
		if release != nil {
			release()
		}
		mp.upgradeMux.Unlock()
		return ErrStopped
	}
	if release == nil {
		var err error
		release, err = mp.lockUpgrade()
		if err != nil {
			release()
			mp.upgradeMux.Unlock()
			return fmt.Errorf("worker executable lock: %w", err)
		}
	}
	unlock := func() { release(); mp.upgradeMux.Unlock() }
	hash, err := binaryHash(mp.binPath)
	// Failure recovery stays inside the activation/installer critical section.
	fail := func(cause error) error {
		id := hex.EncodeToString(hash)
		if id == "" {
			mp.restartMux.Lock()
			id = hex.EncodeToString(mp.binHash)
			mp.restartMux.Unlock()
		}
		recovered := false
		if mp.rollbackPath != "" {
			if err := mp.restoreRollback(); err != nil {
				cause = fmt.Errorf("%w; rollback failed: %v", cause, err)
			} else {
				recovered = true
			}
		}
		if recovered {
			// Retain the installer lease through the recovery fork as well. A
			// cooperating installer must not replace the restored image between
			// rollback and its acknowledged start.
			mp.restartMux.Lock()
			stopping := mp.stopping
			if !stopping {
				mp.activationRelease = release
			}
			mp.resetFetcher = true
			mp.restartMux.Unlock()
			event := mp.upgradeEvent(id, UpgradeRolledBack, cause)
			if stopping {
				release()
			}
			mp.upgradeMux.Unlock()
			mp.dispatchUpgrade(event)
			if stopping {
				return cause
			}
			return nil
		}
		event := mp.upgradeEvent(id, UpgradeRejected, cause)
		unlock()
		mp.dispatchUpgrade(event)
		return cause
	}
	if err != nil {
		return fail(fmt.Errorf("worker executable hash: %w", err))
	}
	// Initial startup normally trusts the executable explicitly launched by the
	// user. An update installed during the startup window needs activation
	// authorization too, under the same installer lease as cmd.Start.
	if startupCandidate && mp.Config.AuthorizeUpgrade != nil {
		if err := mp.Config.AuthorizeUpgrade(mp.binPath); err != nil {
			return fail(fmt.Errorf("startup upgrade activation: %w", err))
		}
		if mp.isStopping() {
			return fail(ErrStopped)
		}
	}
	mp.restartMux.Lock()
	mp.descriptorsReleased = make(chan bool, 1)
	released := mp.descriptorsReleased
	mp.restartMux.Unlock()
	mp.infof("starting %s", mp.binPath)
	cmd := exec.Command(mp.binPath)
	//mark this new process as the "active" worker process.
	//this process is assumed to be holding the socket files.
	//workerCmd is set before cmd.Start() so handleSignal can see a fork
	//is in progress (Process still nil) and queue signals instead of
	//dropping them; those queued signals are delivered below once Start
	//succeeds.
	mp.workerMux.Lock()
	mp.workerCmd = cmd
	mp.workerMux.Unlock()
	//this fork runs the current on-disk binary: record its hash so info()
	//can report whether a later upgrade is staged but not yet delivered
	mp.restartMux.Lock()
	mp.workerID++
	workerID := mp.workerID
	mp.binHash = hash
	binID := hex.EncodeToString(hash)
	mp.restartMux.Unlock()
	//provide the worker process with some state. Set both the new
	//OVERSEER_WORKER_* and legacy OVERSEER_SLAVE_* names so a pre-rename
	//child binary forked by a post-rename master still detects itself as a
	//worker and reads its id.
	e := os.Environ()
	e = append(e, envBinID+"="+binID)
	e = append(e, envBinPath+"="+mp.binPath)
	e = append(e, envWorkerID+"="+strconv.Itoa(workerID))
	e = append(e, envIsWorker+"=1")
	e = append(e, envSlaveID+"="+strconv.Itoa(workerID))
	e = append(e, envIsSlave+"=1")
	e = append(e, envNumFDs+"="+strconv.Itoa(len(mp.workerExtraFiles)))
	cmd.Env = e
	//inherit master args/stdfiles
	cmd.Args = os.Args
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	var stderrTail *opanic.TailWriter
	if mp.Config.OnPanic != nil {
		size := mp.Config.StderrTailSize
		if size <= 0 {
			size = opanic.DefaultTailSize
		}
		stderrTail = opanic.NewTailWriter(size)
		cmd.Stderr = io.MultiWriter(os.Stderr, stderrTail)
	} else {
		cmd.Stderr = os.Stderr
	}
	// Bound cmd.Wait after process exit. When cmd.Stderr is a non-*os.File
	// (e.g. our MultiWriter wrapping stderrTail), Go's exec creates a pipe
	// and runs an internal goroutine to drain it. Worker subprocesses that
	// inherit fd 2 (the pipe's write-end) and outlive the worker keep that
	// pipe open forever; without WaitDelay, cmd.Wait blocks waiting for an
	// EOF that never arrives, and the master never spawns the next worker.
	cmd.WaitDelay = mp.Config.WaitDelay
	// Set up platform-specific listener and control inheritance.
	closeControl, err := mp.prepareWorker(cmd)
	if err != nil {
		return fail(err)
	}
	defer closeControl()
	ready, closeReady, err := mp.prepareReady(cmd)
	if err != nil {
		return fail(err)
	}
	defer closeReady()
	// exec.Cmd writes Process during Start; publish it under the same lock
	// used by signal forwarding, including signals arriving during startup.
	mp.workerMux.Lock()
	startErr := cmd.Start()
	if startErr != nil {
		mp.workerCmd = nil
		mp.pendingSignals = nil
		mp.workerMux.Unlock()
		return fail(fmt.Errorf("failed to start worker process: %w", startErr))
	}
	mp.workerMux.Unlock()
	mp.workerStarted(cmd)
	cmdwait := make(chan error, 1)
	go func() { cmdwait <- cmd.Wait() }()
	mp.restartMux.Lock()
	stopping = mp.stopping
	mp.restartMux.Unlock()
	if stopping {
		_ = cmd.Process.Kill()
		<-cmdwait
		return fail(errors.New("master stopped during worker startup"))
	}
	if ready != nil {
		timer := time.NewTimer(mp.ReadyTimeout)
		var readyErr error
		exited := false
		select {
		case readyErr = <-ready:
		case err := <-cmdwait:
			readyErr = fmt.Errorf("worker exited before readiness: %v", err)
			exited = true
		case <-timer.C:
			readyErr = fmt.Errorf("worker readiness timed out after %s", mp.ReadyTimeout)
		}
		timer.Stop()
		if readyErr != nil {
			if !exited {
				_ = cmd.Process.Kill()
				<-cmdwait
			}
			return fail(readyErr)
		}
	}
	mp.restartMux.Lock()
	stopping = mp.stopping
	mp.restartMux.Unlock()
	if stopping {
		_ = cmd.Process.Kill()
		<-cmdwait
		return fail(errors.New("master stopped before worker readiness"))
	}
	mp.restartMux.Lock()
	changed := !bytes.Equal(mp.forkedHash, hash)
	mp.forkedHash = hash
	mp.startupCandidate = false
	mp.restartMux.Unlock()
	mp.discardRollback()
	mp.completeRestart(attempt)
	var event UpgradeEvent
	if changed {
		event = mp.upgradeEvent(binID, UpgradeActivated, nil)
	}
	unlock()
	if changed {
		mp.dispatchUpgrade(event)
	}
	//drain any signals that arrived during the fork window
	mp.workerMux.Lock()
	pending := mp.pendingSignals
	mp.pendingSignals = nil
	mp.workerMux.Unlock()
	for _, s := range pending {
		mp.debugf("delivering queued signal (%s)", s)
		if err := mp.signalWorker(cmd.Process, s); err != nil && !errors.Is(err, os.ErrProcessDone) {
			mp.debugf("queued signal failed (%s)", err)
		}
	}
	mp.startFetching()
	//wait....
	select {
	case err := <-cmdwait:
		mp.restartMux.Lock()
		mp.awaitingUSR1 = false
		mp.restartMux.Unlock()
		//program exited before releasing descriptors
		//proxy exit code out to master. Prefer ProcessState (always set
		//after the process exits) over the returned error, which may be
		//wrapped — e.g. when WaitDelay force-closes I/O pipes, Wait returns
		//a wrapper that doesn't directly type-assert to *exec.ExitError.
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
			if code < 0 && err != nil {
				code = 1
			}
		} else if err != nil {
			code = 1
			var exiterr *exec.ExitError
			if errors.As(err, &exiterr) {
				if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
					code = status.ExitStatus()
				}
			}
		}
		mp.infof("prog exited with %d", code)
		if err != nil && cmd.ProcessState != nil && cmd.ProcessState.Exited() &&
			strings.Contains(err.Error(), "WaitDelay") {
			mp.warnf("WaitDelay (%s) elapsed while draining I/O — a subprocess likely inherited the worker's stderr pipe", mp.Config.WaitDelay)
		}
		if stderrTail != nil && mp.Config.OnPanic != nil {
			if snap := opanic.Scan(stderrTail.Bytes(), mp.debugf); snap != nil {
				// Run synchronously with a bounded deadline: os.Exit below
				// does not wait for goroutines, so a bare `go` would race
				// with termination and the callback would silently drop on
				// a loaded master. Cap the wait on Config.TerminateTimeout
				// (the existing shutdown budget) so a buggy callback cannot
				// stall shutdown.
				done := make(chan struct{})
				go func() {
					defer close(done)
					mp.Config.OnPanic(snap)
				}()
				select {
				case <-done:
				case <-time.After(mp.Config.TerminateTimeout):
					mp.warnf("OnPanic callback exceeded %s; continuing shutdown", mp.Config.TerminateTimeout)
				}
			}
		}
		//if a restarts are disabled or if it was an
		//unexpected crash, proxy this exit straight
		//through to the main process
		mp.restartMux.Lock()
		restarting := mp.restarting
		mp.restartMux.Unlock()
		mp.restartMux.Lock()
		stopping := mp.stopping
		mp.restartMux.Unlock()
		if mp.NoRestart || !restarting || stopping {
			os.Exit(code)
		}
	case <-released:
		//if descriptors are released, the program
		//has yielded control of its sockets and
		//a parallel instance of the program can be
		//started safely. it should serve state.Listeners
		//to ensure downtime is kept at <1sec. The previous
		//cmd.Wait() will still be consumed though the
		//result will be discarded.
	}
	return nil
}

func (mp *master) debugf(f string, args ...interface{}) {
	if mp.Config.Debug {
		log.Printf("[overseer master] "+f, args...)
	}
}

func (mp *master) infof(f string, args ...interface{}) {
	if mp.Config.Debug || !mp.Config.NoWarn {
		log.Printf("[overseer master] "+f, args...)
	}
}

func (mp *master) warnf(f string, args ...interface{}) {
	if mp.Config.Debug || !mp.Config.NoWarn {
		log.Printf("[overseer master] "+f, args...)
	}
}

func token() string {
	buff := make([]byte, 8)
	rand.Read(buff)
	return hex.EncodeToString(buff)
}

// On Windows, include the .exe extension, noop otherwise.
func extension() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}

	return ""
}

func (mp *master) lockUpgrade() (func(), error) {
	if mp.Config.UpgradeLock == nil {
		return func() {}, nil
	}
	release, err := mp.Config.UpgradeLock()
	if release == nil {
		release = func() {}
	}
	return release, err
}

// binaryHash reads the executable while its cooperating installer lock is held.
func binaryHash(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func (mp *master) completeRestart(attempt chan struct{}) {
	mp.restartMux.Lock()
	defer mp.restartMux.Unlock()
	if mp.restarting && attempt != nil && mp.restartDone == attempt {
		mp.restartedAt = time.Now()
		mp.restarting = false
		if mp.restartDone != nil {
			close(mp.restartDone)
			mp.restartDone = nil
		}
	}
	mp.forceNext = false
}
