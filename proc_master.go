package overseer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jpillora/overseer/opanic"
)

var tmpBinPath = filepath.Join(os.TempDir(), "overseer-"+token()+extension())

// a overseer master process
type master struct {
	*Config
	workerID            int
	workerMux           sync.Mutex
	workerCmd           *exec.Cmd
	pendingSignals      []os.Signal
	workerExtraFiles    []*os.File
	binPath, tmpBinPath string
	binPerms            os.FileMode
	binHash             []byte
	restartMux          sync.Mutex
	restarting          bool
	restartedAt         time.Time
	restarted           chan bool
	awaitingUSR1        bool
	descriptorsReleased chan bool
	signalledAt         time.Time
	printCheckUpdate    bool
	pendingRestart      bool
}

func (mp *master) run() error {
	mp.debugf("run")
	if err := mp.checkBinary(); err != nil {
		return err
	}
	if mp.Config.Fetcher != nil {
		if err := mp.Config.Fetcher.Init(); err != nil {
			mp.warnf("fetcher init failed (%s). fetcher disabled.", err)
			mp.Config.Fetcher = nil
		}
	}
	mp.setupSignalling()
	if err := mp.retreiveFileDescriptors(); err != nil {
		return err
	}
	if mp.Config.Fetcher != nil {
		mp.printCheckUpdate = true
		mp.fetch()
		go mp.fetchLoop()
	}
	return mp.forkLoop()
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
	//test bin<->tmpbin moves
	if mp.Config.Fetcher != nil {
		if err := move(tmpBinPath, mp.binPath); err != nil {
			return fmt.Errorf("cannot move binary (%s)", err)
		}
		if err := move(mp.binPath, tmpBinPath); err != nil {
			return fmt.Errorf("cannot move binary back (%s)", err)
		}
	}
	return nil
}

func (mp *master) setupSignalling() {
	//updater-forker comms
	mp.restarted = make(chan bool)
	mp.descriptorsReleased = make(chan bool)
	//read all master process signals. Buffer of 1 satisfies signal.Notify's
	//requirement that the channel never block; the actual queueing for the
	//fork window happens in handleSignal under workerMux.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals)
	go func() {
		for s := range signals {
			mp.handleSignal(s)
		}
	}()
}

func (mp *master) handleSignal(s os.Signal) {
	if s == mp.RestartSignal {
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
	if s == SIGUSR1 {
		mp.restartMux.Lock()
		awaiting := mp.awaitingUSR1
		if awaiting {
			mp.awaitingUSR1 = false
		}
		mp.restartMux.Unlock()
		if awaiting {
			mp.debugf("signaled, sockets ready")
			mp.descriptorsReleased <- true
			return
		}
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
	hasWorker := mp.workerCmd != nil && mp.workerCmd.Process != nil
	mp.workerMux.Unlock()
	if hasWorker {
		mp.debugf("proxy signal (%s)", s)
		mp.sendSignal(s)
		return
	}
	//otherwise if not running, kill on CTRL+c
	if s == os.Interrupt {
		mp.debugf("interupt with no worker")
		os.Exit(1)
	} else {
		mp.debugf("signal discarded (%s), no worker process", s)
	}
}

func (mp *master) sendSignal(s os.Signal) {
	mp.workerMux.Lock()
	proc := (*os.Process)(nil)
	if mp.workerCmd != nil {
		proc = mp.workerCmd.Process
	}
	mp.workerMux.Unlock()
	if proc == nil {
		return
	}
	if err := proc.Signal(s); err != nil {
		// A worker that has just exited is the expected state right before
		// cmdwait runs; short-circuiting with os.Exit(1) here skips OnPanic
		// and overwrites the worker's real exit code.
		if errors.Is(err, os.ErrProcessDone) {
			return
		}
		mp.debugf("signal failed (%s)", err)
	}
}

func (mp *master) retreiveFileDescriptors() error {
	mp.workerExtraFiles = make([]*os.File, len(mp.Config.Addresses))
	for i, addr := range mp.Config.Addresses {
		a, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			return fmt.Errorf("Invalid address %s (%s)", addr, err)
		}
		l, err := net.ListenTCP("tcp", a)
		if err != nil {
			return err
		}
		f, err := l.File()
		if err != nil {
			return fmt.Errorf("Failed to retreive fd for: %s (%s)", addr, err)
		}
		if err := l.Close(); err != nil {
			return fmt.Errorf("Failed to close listener for: %s (%s)", addr, err)
		}
		mp.workerExtraFiles[i] = f
	}
	return nil
}

// fetchLoop is run in a goroutine
func (mp *master) fetchLoop() {
	min := mp.Config.MinFetchInterval
	time.Sleep(min)
	for {
		t0 := time.Now()
		mp.fetch()
		mp.restartMux.Lock()
		retry := mp.pendingRestart && !mp.restarting
		mp.restartMux.Unlock()
		if retry {
			mp.tryRestart()
		}
		//duration fetch of fetch
		diff := time.Now().Sub(t0)
		if diff < min {
			delay := min - diff
			//ensures at least MinFetchInterval delay.
			//should be throttled by the fetcher!
			time.Sleep(delay)
		}
	}
}

func (mp *master) fetch() {
	mp.restartMux.Lock()
	restarting := mp.restarting
	mp.restartMux.Unlock()
	if restarting {
		return //skip if restarting
	}
	if mp.printCheckUpdate {
		mp.debugf("checking for updates...")
	}
	reader, err := mp.Fetcher.Fetch()
	if err != nil {
		mp.debugf("failed to get latest version: %s", err)
		return
	}
	if reader == nil {
		if mp.printCheckUpdate {
			mp.debugf("no updates")
		}
		mp.printCheckUpdate = false
		return //fetcher has explicitly said there are no updates
	}
	mp.printCheckUpdate = true
	mp.debugf("streaming update...")
	//optional closer
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	tmpBin, err := os.OpenFile(tmpBinPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		mp.warnf("failed to open temp binary: %s", err)
		return
	}
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
		return
	}
	//compare hash
	newHash := hash.Sum(nil)
	if bytes.Equal(mp.binHash, newHash) {
		mp.debugf("hash match - skip")
		return
	}
	//copy permissions
	if err := chmod(tmpBin, mp.binPerms); err != nil {
		mp.warnf("failed to make temp binary executable: %s", err)
		return
	}
	if err := chown(tmpBin, uid, gid); err != nil {
		mp.warnf("failed to change owner of binary: %s", err)
		return
	}
	if _, err := tmpBin.Stat(); err != nil {
		mp.warnf("failed to stat temp binary: %s", err)
		return
	}
	tmpBin.Close()
	if _, err := os.Stat(tmpBinPath); err != nil {
		mp.warnf("failed to stat temp binary by path: %s", err)
		return
	}
	if mp.Config.PreUpgrade != nil {
		if err := mp.Config.PreUpgrade(tmpBinPath); err != nil {
			mp.warnf("user cancelled upgrade: %s", err)
			return
		}
	}
	//overseer sanity check, dont replace our good binary with a non-executable file
	tokenIn := token()
	cmd := exec.Command(tmpBinPath)
	cmd.Env = append(os.Environ(), []string{envBinCheck + "=" + tokenIn}...)
	cmd.Args = os.Args
	returned := false
	go func() {
		time.Sleep(5 * time.Second)
		if !returned {
			mp.warnf("sanity check against fetched executable timed-out, check overseer is running")
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}
	}()
	tokenOut, err := cmd.CombinedOutput()
	returned = true
	if err != nil {
		mp.warnf("failed to run temp binary: %s (%s) output \"%s\"", err, tmpBinPath, tokenOut)
		return
	}
	if tokenIn != string(tokenOut) {
		mp.warnf("sanity check failed")
		return
	}
	//overwrite!
	if err := overwrite(mp.binPath, tmpBinPath); err != nil {
		mp.warnf("failed to overwrite binary: %s", err)
		return
	}
	mp.debugf("upgraded binary (%x -> %x)", mp.binHash[:12], newHash[:12])
	mp.binHash = newHash
	//binary successfully replaced
	if !mp.Config.NoRestartAfterFetch {
		mp.tryRestart()
	}
	//and keep fetching...
	return
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

// startRestart atomically claims ownership of the restart and, if claimed,
// performs the graceful shutdown handshake. When checkShouldRestart is true
// the caller is the fetch loop and ShouldRestart may defer the restart.
func (mp *master) startRestart(checkShouldRestart bool) {
	mp.restartMux.Lock()
	if mp.restarting {
		mp.debugf("already graceful restarting")
		mp.restartMux.Unlock()
		return
	}
	if checkShouldRestart && mp.Config.ShouldRestart != nil && !mp.Config.ShouldRestart() {
		if !mp.pendingRestart {
			mp.debugf("restart deferred: ShouldRestart returned false")
		}
		mp.pendingRestart = true
		mp.restartMux.Unlock()
		return
	}
	mp.pendingRestart = false
	if mp.workerCmd == nil {
		mp.debugf("no worker process")
		mp.restartMux.Unlock()
		return
	}
	mp.restarting = true
	mp.awaitingUSR1 = true
	mp.signalledAt = time.Now()
	mp.restartMux.Unlock()
	mp.debugf("graceful restart triggered")
	mp.sendSignal(mp.Config.RestartSignal) //ask nicely to terminate
	select {
	case <-mp.restarted:
		//success
		mp.debugf("restart success")
	case <-time.After(mp.TerminateTimeout):
		//times up mr. process, we did ask nicely!
		mp.debugf("graceful timeout, forcing exit")
		mp.sendSignal(os.Kill)
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
	mp.debugf("starting %s", mp.binPath)
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
	mp.workerID++
	//provide the worker process with some state. Set both the new
	//OVERSEER_WORKER_* and legacy OVERSEER_SLAVE_* names so a pre-rename
	//child binary forked by a post-rename master still detects itself as a
	//worker and reads its id.
	e := os.Environ()
	e = append(e, envBinID+"="+hex.EncodeToString(mp.binHash))
	e = append(e, envBinPath+"="+mp.binPath)
	e = append(e, envWorkerID+"="+strconv.Itoa(mp.workerID))
	e = append(e, envIsWorker+"=1")
	e = append(e, envSlaveID+"="+strconv.Itoa(mp.workerID))
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
	//include socket files
	cmd.ExtraFiles = mp.workerExtraFiles
	if err := cmd.Start(); err != nil {
		mp.workerMux.Lock()
		mp.workerCmd = nil
		mp.pendingSignals = nil
		mp.workerMux.Unlock()
		return fmt.Errorf("Failed to start worker process: %s", err)
	}
	//drain any signals that arrived during the fork window
	mp.workerMux.Lock()
	pending := mp.pendingSignals
	mp.pendingSignals = nil
	mp.workerMux.Unlock()
	for _, s := range pending {
		mp.debugf("delivering queued signal (%s)", s)
		if err := cmd.Process.Signal(s); err != nil && !errors.Is(err, os.ErrProcessDone) {
			mp.debugf("queued signal failed (%s)", err)
		}
	}
	//was scheduled to restart, notify success
	mp.restartMux.Lock()
	wasRestarting := mp.restarting
	if wasRestarting {
		mp.restartedAt = time.Now()
		mp.restarting = false
	}
	mp.restartMux.Unlock()
	if wasRestarting {
		// Non-blocking: if startRestart already exited via TerminateTimeout,
		// nobody is reading mp.restarted. A blocking send here would hang the
		// master forever — the worker is already running, no further progress
		// is needed. Drop the notification in that case. The channel is
		// unbuffered and only one restart can be in-flight at a time
		// (guarded by mp.restarting), so racing a real receiver is not
		// possible: startRestart enters its select before this send can run
		// (it kicks off the worker death we are responding to).
		select {
		case mp.restarted <- true:
		default:
		}
	}
	//convert wait into channel
	cmdwait := make(chan error)
	go func() {
		cmdwait <- cmd.Wait()
	}()
	//wait....
	select {
	case err := <-cmdwait:
		//program exited before releasing descriptors
		//proxy exit code out to master
		code := 0
		if err != nil {
			code = 1
			if exiterr, ok := err.(*exec.ExitError); ok {
				if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
					code = status.ExitStatus()
				}
			}
		}
		mp.debugf("prog exited with %d", code)
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
		if mp.NoRestart || !restarting {
			os.Exit(code)
		}
	case <-mp.descriptorsReleased:
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
