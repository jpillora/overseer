package overseer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var tmpBinPath = filepath.Join(os.TempDir(), "overseer-"+token())

//a overseer parent process
type parent struct {
	*Config
	childID             int
	childCmd            *exec.Cmd
	childExtraFiles     []*os.File
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
}

func (mp *parent) run() error {
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

func (mp *parent) checkBinary() error {
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

func (mp *parent) setupSignalling() {
	//updater-forker comms
	mp.restarted = make(chan bool)
	mp.descriptorsReleased = make(chan bool)
	//read all parent process signals
	signals := make(chan os.Signal)
	signal.Notify(signals)
	go func() {
		for s := range signals {
			mp.handleSignal(s)
		}
	}()
}

func (mp *parent) handleSignal(s os.Signal) {
	if s == mp.RestartSignal {
		//user initiated manual restart
		go mp.triggerRestart()
	} else if s.String() == "child exited" {
		// will occur on every restart, ignore it
	} else
	//**during a restart** a SIGUSR1 signals
	//to the parent process that, the file
	//descriptors have been released
	if mp.awaitingUSR1 && s == SIGUSR1 {
		mp.debugf("signaled, sockets ready")
		mp.awaitingUSR1 = false
		mp.descriptorsReleased <- true
	} else
	//while the child process is running, proxy
	//all signals through
	if mp.childCmd != nil && mp.childCmd.Process != nil {
		mp.debugf("proxy signal (%s)", s)
		mp.sendSignal(s)
	} else
	//otherwise if not running, kill on CTRL+c
	if s == os.Interrupt {
		mp.debugf("interupt with no child")
		os.Exit(1)
	} else {
		mp.debugf("signal discarded (%s), no child process", s)
	}
}

func (mp *parent) sendSignal(s os.Signal) {
	if mp.childCmd != nil && mp.childCmd.Process != nil {
		if err := mp.childCmd.Process.Signal(s); err != nil {
			mp.debugf("signal failed (%s), assuming child process died unexpectedly", err)
			os.Exit(1)
		}
	}
}

func (mp *parent) retreiveFileDescriptors() error {
	mp.childExtraFiles = make([]*os.File, len(mp.Config.Addresses))
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
		mp.childExtraFiles[i] = f
	}
	return nil
}

//fetchLoop is run in a goroutine
func (mp *parent) fetchLoop() {
	min := mp.Config.MinFetchInterval
	time.Sleep(min)
	for {
		t0 := time.Now()
		mp.fetch()
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

func (mp *parent) fetch() {
	if mp.restarting {
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
	if err := move(mp.binPath, tmpBinPath); err != nil {
		mp.warnf("failed to overwrite binary: %s", err)
		return
	}
	mp.debugf("upgraded binary (%x -> %x)", mp.binHash[:12], newHash[:12])
	mp.binHash = newHash
	//binary successfully replaced
	if !mp.Config.NoRestartAfterFetch {
		mp.triggerRestart()
	}
	//and keep fetching...
	return
}

func (mp *parent) triggerRestart() {
	if mp.restarting {
		mp.debugf("already graceful restarting")
		return //skip
	} else if mp.childCmd == nil || mp.restarting {
		mp.debugf("no child process")
		return //skip
	}
	mp.debugf("graceful restart triggered")
	mp.restarting = true
	mp.awaitingUSR1 = true
	mp.signalledAt = time.Now()
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

//not a real fork
func (mp *parent) forkLoop() error {
	//loop, restart command
	for {
		if err := mp.fork(); err != nil {
			return err
		}
	}
}

func (mp *parent) fork() error {
	mp.debugf("starting %s", mp.binPath)
	cmd := exec.Command(mp.binPath)
	//mark this new process as the "active" child process.
	//this process is assumed to be holding the socket files.
	mp.childCmd = cmd
	mp.childID++
	//provide the child process with some state
	e := os.Environ()
	e = append(e, envBinID+"="+hex.EncodeToString(mp.binHash))
	e = append(e, envBinPath+"="+mp.binPath)
	e = append(e, envChildID+"="+strconv.Itoa(mp.childID))
	e = append(e, envIsChild+"=1")
	e = append(e, envNumFDs+"="+strconv.Itoa(len(mp.childExtraFiles)))
	cmd.Env = e
	//inherit parent args/stdfiles
	cmd.Args = os.Args
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	//include socket files
	cmd.ExtraFiles = mp.childExtraFiles
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Failed to start child process: %s", err)
	}
	//was scheduled to restart, notify success
	if mp.restarting {
		mp.restartedAt = time.Now()
		mp.restarting = false
		mp.restarted <- true
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
		//proxy exit code out to parent
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
		//if a restarts are disabled or if it was an
		//unexpected crash, proxy this exit straight
		//through to the main process
		if mp.NoRestart || !mp.restarting {
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

func (mp *parent) debugf(f string, args ...interface{}) {
	if mp.Config.Debug {
		log.Printf("[overseer parent] "+f, args...)
	}
}

func (mp *parent) warnf(f string, args ...interface{}) {
	if mp.Config.Debug || !mp.Config.NoWarn {
		log.Printf("[overseer parent] "+f, args...)
	}
}

func token() string {
	buff := make([]byte, 8)
	rand.Read(buff)
	return hex.EncodeToString(buff)
}
