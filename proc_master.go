package upgrade

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
	"syscall"
	"time"

	"github.com/kardianos/osext"
)

var tmpBinPath = filepath.Join(os.TempDir(), "goupgrade")

//a go-upgrade master process
type master struct {
	Config
	slaveCmd            *exec.Cmd
	slaveExtraFiles     []*os.File
	binPath             string
	binPerms            os.FileMode
	binHash             []byte
	restarting          bool
	restartedAt         time.Time
	restarted           chan bool
	descriptorsReleased chan bool
	signalledAt         time.Time
	signals             chan os.Signal
}

func (mp *master) run() {
	mp.readBinary()
	mp.setupSignalling()
	mp.retreiveFileDescriptors()
	mp.fetch()
	go mp.fetchLoop()
	mp.forkLoop()
}

func (mp *master) readBinary() {
	//get path to binary and confirm its writable
	binPath, err := osext.Executable()
	binFound := false
	binWritable := false
	if err != nil {
		mp.logf("failed to find binary path")
	} else if f, err := os.OpenFile(binPath, os.O_RDWR, os.ModePerm); err == nil {
		if info, err := f.Stat(); err == nil && info.Size() > 0 {
			mp.binPath = binPath
			binFound = true
			//initial hash of file
			hash := sha1.New()
			io.Copy(hash, f)
			mp.binHash = hash.Sum(nil)
			//copy permissions
			mp.binPerms = info.Mode()
			//test write
			sample := make([]byte, 1)
			if n, err := f.ReadAt(sample, 0); err == nil && n == 1 {
				//read 1 byte, now write
				if n, err = f.WriteAt(sample, 0); err == nil && n == 1 {
					//write success
					binWritable = true
				}
			}
		}
		f.Close()
	}
	//is the program? or failed to find the writable binary path?
	if !binWritable {
		var err error
		if !binFound {
			err = fmt.Errorf("binary path not found: %s", binPath)
		} else if !binWritable {
			err = fmt.Errorf("binary path not writable: %s", binPath)
		}
		if err != nil {
			if mp.Config.Optional {
				mp.logf("%s, disabling go-upgrade. ", err)
			} else {
				fatalf("%s", err)
			}
		}
		mp.Program(DisabledState)
		return
	}
}

func (mp *master) setupSignalling() {
	//updater-forker comms
	mp.restarted = make(chan bool)
	mp.descriptorsReleased = make(chan bool)
	//read all master process signals
	mp.signals = make(chan os.Signal)
	signal.Notify(mp.signals)
	go func() {
		for s := range mp.signals {

			if s.String() == "child exited" {
				continue
			}

			//**during a restart** a SIGUSR1 signals
			//to the master process that, the file
			//descriptors have been released
			if mp.restarting && s == syscall.SIGUSR1 {
				mp.descriptorsReleased <- true
				continue
			}

			if mp.slaveCmd != nil && mp.slaveCmd.Process != nil {
				mp.logf("proxy signal (%s)", s)
				if err := mp.slaveCmd.Process.Signal(s); err != nil {
					mp.logf("proxy signal failed (%s)", err)
					os.Exit(1)
				}
			} else if s == syscall.SIGINT {
				mp.logf("interupt with no slave")
				os.Exit(1)
			} else {
				mp.logf("signal discarded (%s), no slave process", s)
			}
		}
	}()
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

func (mp *master) fetchLoop() {
	minDelay := time.Second
	time.Sleep(minDelay)
	for {
		t0 := time.Now()
		mp.fetch()
		diff := time.Now().Sub(t0)
		if diff < minDelay {
			delay := minDelay - diff
			//ensures at least minDelay
			//should be throttled by the fetcher!
			time.Sleep(delay)
		}
	}
}

func (mp *master) fetch() {
	mp.logf("checking for updates...")
	reader, err := mp.Fetcher.Fetch()
	if err != nil {
		mp.logf("failed to get latest version: %s", err)
		return
	}
	if reader == nil {
		return //fetcher has explicitly said there are no updates
	}
	//optional closer
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	tmpBin, err := os.Create(tmpBinPath)
	if err != nil {
		mp.logf("failed to open temp binary: %s", err)
		return
	}
	defer os.Remove(tmpBinPath)
	//tee off to sha1
	hash := sha1.New()
	reader = io.TeeReader(reader, hash)
	//write to temp
	_, err = io.Copy(tmpBin, reader)
	if err != nil {
		mp.logf("failed to write temp binary: %s", err)
		return
	}
	//compare hash
	newHash := hash.Sum(nil)
	if bytes.Equal(mp.binHash, newHash) {
		return
	}
	//copy permissions
	if err := tmpBin.Chmod(mp.binPerms); err != nil {
		mp.logf("failed to make binary executable: %s", err)
		return
	}
	tmpBin.Close()
	//sanity check
	buff := make([]byte, 8)
	rand.Read(buff)
	tokenIn := hex.EncodeToString(buff)
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
	//replace!
	if err := os.Rename(tmpBinPath, mp.binPath); err != nil {
		mp.logf("failed to replace binary: %s", err)
		return
	}
	mp.logf("upgraded binary (%x -> %x)", mp.binHash[:12], newHash[:12])
	mp.binHash = newHash
	//binary successfully replaced, perform graceful restart
	mp.restarting = true
	mp.signalledAt = time.Now()
	mp.signals <- mp.Config.Signal //ask nicely to terminate
	select {
	case <-mp.restarted:
		//success
	case <-time.After(mp.TerminateTimeout):
		//times up process, we did ask nicely!
		mp.logf("graceful timeout, forcing exit")
		mp.signals <- syscall.SIGKILL
	}
	//and keep fetching...
	return
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
	cmd.Args = os.Args
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = mp.slaveExtraFiles
	if err := cmd.Start(); err != nil {
		fatalf("failed to fork: %s", err)
		os.Exit(1)
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
		//them up
	}
}

func (mp *master) logf(f string, args ...interface{}) {
	if mp.Logging {
		log.Printf("[go-upgrade master] "+f, args...)
	}
}
