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

	"github.com/kardianos/osext"
)

var tmpBinPath = filepath.Join(os.TempDir(), "overseer-"+token())

//a overseer master process
type master struct {
	Config
	slaveCmd            *exec.Cmd
	slaveExtraFiles     []*os.File
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
}

func (mp *master) run() {
	if err := mp.checkBinary(); err != nil {
		if !mp.Config.Optional {
			fatalf("%s", err)
		}
		//run program directly
		mp.logf("%s, disabling overseer.", err)
		mp.Program(DisabledState)
		return
	}
	if mp.Config.Fetcher != nil {
		if err := mp.Config.Fetcher.Init(); err != nil {
			mp.logf("fetcher init failed (%s)", err)
			mp.Config.Fetcher = nil
		}
	}
	mp.setupSignalling()
	mp.retreiveFileDescriptors()
	if mp.Config.Fetcher != nil {
		//TODO is required? fatalf("overseer.Config.Fetcher required")
		mp.fetch()
		go mp.fetchLoop()
	}
	mp.forkLoop()
}

func (mp *master) checkBinary() error {
	//get path to binary and confirm its writable
	binPath, err := osext.Executable()
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
	if err := move(tmpBinPath, mp.binPath); err != nil {
		return fmt.Errorf("cannot move binary (%s)", err)
	}
	if err := move(mp.binPath, tmpBinPath); err != nil {
		return fmt.Errorf("cannot move binary back (%s)", err)
	}
	return nil
}

func (mp *master) setupSignalling() {
	//updater-forker comms
	mp.restarted = make(chan bool)
	mp.descriptorsReleased = make(chan bool)
	//read all master process signals
	signals := make(chan os.Signal)
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
		mp.triggerRestart()
	} else if s.String() == "child exited" {
		// will occur on every restart
	} else
	//**during a restart** a SIGUSR1 signals
	//to the master process that, the file
	//descriptors have been released
	if mp.awaitingUSR1 && s == SIGUSR1 {
		mp.logf("signaled, sockets ready")
		mp.awaitingUSR1 = false
		mp.descriptorsReleased <- true
	} else
	//while the slave process is running, proxy
	//all signals through
	if mp.slaveCmd != nil && mp.slaveCmd.Process != nil {
		mp.logf("proxy signal (%s)", s)
		mp.sendSignal(s)
	} else
	//otherwise if not running, kill on CTRL+c
	if s == os.Interrupt {
		mp.logf("interupt with no slave")
		os.Exit(1)
	} else {
		mp.logf("signal discarded (%s), no slave process", s)
	}
}

func (mp *master) sendSignal(s os.Signal) {
	if err := mp.slaveCmd.Process.Signal(s); err != nil {
		mp.logf("signal failed (%s), assuming slave process died", err)
		os.Exit(1)
	}
}

func (mp *master) retreiveFileDescriptors() {
	mp.slaveExtraFiles = make([]*os.File, len(mp.Config.Addresses))
	for i, addr := range mp.Config.Addresses {
		a, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			fatalf("invalid address: %s (%s)", addr, err)
		}
		l, err := net.ListenTCP("tcp", a)
		if err != nil {
			fatalf(err.Error())
		}
		f, err := l.File()
		if err != nil {
			fatalf("failed to retreive fd for: %s (%s)", addr, err)
		}
		if err := l.Close(); err != nil {
			fatalf("failed to close listener for: %s (%s)", addr, err)
		}
		mp.slaveExtraFiles[i] = f
	}
}

//fetchLoop is run in a goroutine
func (mp *master) fetchLoop() {
	min := mp.Config.MinFetchInterval
	time.Sleep(min)
	for {
		t0 := time.Now()
		mp.fetch()
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
	if mp.restarting {
		return //skip if restarting
	}

	mp.logf("checking for updates...")
	reader, err := mp.Fetcher.Fetch()
	if err != nil {
		mp.logf("failed to get latest version: %s", err)
		return
	}
	if reader == nil {
		mp.logf("no updates")
		return //fetcher has explicitly said there are no updates
	}
	mp.logf("streaming update...")
	//optional closer
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	tmpBin, err := os.OpenFile(tmpBinPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		mp.logf("failed to open temp binary: %s", err)
		return
	}
	// defer func() {
	// 	tmpBin.Close()
	// 	os.Remove(tmpBinPath)
	// }()
	//tee off to sha1
	hash := sha1.New()
	reader = io.TeeReader(reader, hash)
	//write to a temp file
	_, err = io.Copy(tmpBin, reader)
	if err != nil {
		mp.logf("failed to write temp binary: %s", err)
		return
	}
	//compare hash
	newHash := hash.Sum(nil)
	if bytes.Equal(mp.binHash, newHash) {
		mp.logf("hash match - skip")
		return
	}
	//copy permissions
	if err := tmpBin.Chmod(mp.binPerms); err != nil {
		mp.logf("failed to make temp binary executable: %s", err)
		return
	}
	if err := tmpBin.Chown(uid, gid); err != nil {
		mp.logf("failed to change owner of binary: %s", err)
		return
	}
	if _, err := tmpBin.Stat(); err != nil {
		mp.logf("failed to stat temp binary: %s", err)
		return
	}
	tmpBin.Close()
	if _, err := os.Stat(tmpBinPath); err != nil {
		mp.logf("failed to stat temp binary by path: %s", err)
		return
	}
	if mp.Config.PreUpgrade != nil {
		if err := mp.Config.PreUpgrade(tmpBinPath); err != nil {
			mp.logf("user cancelled upgrade: %s", err)
			return
		}
	}
	//overseer sanity check, dont replace our good binary with a non-executable file
	tokenIn := token()
	cmd := exec.Command(tmpBinPath)
	cmd.Env = []string{envBinCheck + "=" + tokenIn}
	tokenOut, err := cmd.Output()
	if err != nil {
		mp.logf("failed to run temp binary: %s", err)
		return
	}
	if tokenIn != string(tokenOut) {
		mp.logf("sanity check failed")
		return
	}
	//overwrite!
	if err := move(mp.binPath, tmpBinPath); err != nil {
		mp.logf("failed to overwrite binary: %s", err)
		return
	}
	mp.logf("upgraded binary (%x -> %x)", mp.binHash[:12], newHash[:12])
	mp.binHash = newHash
	//binary successfully replaced
	if !mp.Config.NoRestartAfterFetch {
		mp.triggerRestart()
	}
	//and keep fetching...
	return
}

func (mp *master) triggerRestart() {
	if mp.restarting {
		mp.logf("already graceful restarting")
		return //skip
	} else if mp.slaveCmd == nil || mp.restarting {
		mp.logf("no slave process")
		return //skip
	}
	mp.logf("graceful restart triggered")
	mp.restarting = true
	mp.awaitingUSR1 = true
	mp.signalledAt = time.Now()
	mp.sendSignal(mp.Config.RestartSignal) //ask nicely to terminate
	select {
	case <-mp.restarted:
		//success
		mp.logf("restart success")
	case <-time.After(mp.TerminateTimeout):
		//times up process, we did ask nicely!
		mp.logf("graceful timeout, forcing exit")
		mp.sendSignal(os.Kill)
	}
}

//not a real fork
func (mp *master) forkLoop() {
	//loop, restart command
	for {
		mp.fork()
	}
}

func (mp *master) fork() {
	mp.logf("starting %s", mp.binPath)
	cmd := exec.Command(mp.binPath)
	mp.slaveCmd = cmd

	e := os.Environ()
	e = append(e, envBinID+"="+hex.EncodeToString(mp.binHash))
	e = append(e, envIsSlave+"=1")
	e = append(e, envNumFDs+"="+strconv.Itoa(len(mp.Config.Addresses)))
	cmd.Env = e
	//inherit master args/stdfiles
	cmd.Args = os.Args
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	//include socket files
	cmd.ExtraFiles = mp.slaveExtraFiles
	if err := cmd.Start(); err != nil {
		fatalf("failed to fork: %s", err)
	}
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
		if mp.restarting {
			//restart requested
			return
		}
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
		mp.logf("prog exited with %d", code)
		//proxy exit with same code
		os.Exit(code)
	case <-mp.descriptorsReleased:
		//if descriptors are released, the program
		//has yielded control of its sockets and
		//a new instance should be started to pick
		//them up. The previous cmd.Wait() will still
		//be consumed though it will be discarded.
	}
}

func (mp *master) logf(f string, args ...interface{}) {
	if mp.Log {
		log.Printf("[overseer master] "+f, args...)
	}
}

func token() string {
	buff := make([]byte, 8)
	rand.Read(buff)
	return hex.EncodeToString(buff)
}
