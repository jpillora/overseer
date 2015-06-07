package upgrade

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kardianos/osext"
)

const (
	isProgVar     = "GO_UPGRADE_IS_PROG"
	getVersionVar = "GO_UPGRADE_GET_VERSION"
)

type Config struct {
	Program        func()        //Programs main function
	Version        string        //Current version of the program
	Fetcher        Fetcher       //Used to fetch binaries
	URL            string        //Template to create upgrade URLs
	Signal         os.Signal     //Signal to send to the program on upgrade
	FetchInterval  time.Duration //Check for upgrades at this interval
	RestartTimeout time.Duration //Restarts will only occur within this timeout
	Logging        bool          //Enable logging
}

type upsig struct {
	upgrade bool
	sig     os.Signal
}

type upgrader struct {
	Config
	signals    chan *upsig
	binPath    string
	binPerms   os.FileMode
	binUpgrade []byte
	upgradedAt time.Time
	upgraded   bool
}

func Run(c Config) {
	//validate
	if c.Program == nil {
		log.Fatalf("upgrade.Config.Program required")
	}
	if c.Fetcher == nil {
		log.Fatalf("upgrade.Config.Fetcher required")
	}

	//prepare
	u := upgrader{}
	u.signals = make(chan *upsig)
	//apply defaults
	if c.FetchInterval == 0 {
		c.FetchInterval = 30 * time.Second
	}
	if c.RestartTimeout == 0 {
		c.RestartTimeout = 10 * time.Second
	}
	u.Config = c
	u.run()
}

func (u *upgrader) run() {

	//get path to binary and confirm its writable
	binPath, err := osext.Executable()
	binModifiable := false
	if err != nil {
		u.printf("failed to find binary path")
	} else if f, err := os.OpenFile(binPath, os.O_RDWR, os.ModePerm); err == nil {
		if info, err := f.Stat(); err == nil && info.Size() > 0 {
			u.binPerms = info.Mode()
			sample := make([]byte, 1)
			if n, err := f.Read(sample); err == nil && n == 1 {
				//read 1 byte, now write
				if n, err = f.WriteAt(sample, 0); err == nil && n == 1 {
					//write success
					u.binPath = binPath
					binModifiable = true
				}
			}
		}
		f.Close()
	}

	//is the program? or failed to find bin?
	if os.Getenv(isProgVar) == "1" || !binModifiable {
		if !binModifiable {
			u.printf("binary is not writable")
		}
		u.Program()
		return
	}

	//version request
	if os.Getenv(getVersionVar) == "1" {
		fmt.Print(u.Version)
		os.Exit(0)
		return
	}

	//check loop
	go u.check()
	//fork loop
	u.fork()
}

func (u *upgrader) check() {

	first := true
	for {
		//wait till next update
		if first {
			first = false
		} else {
			time.Sleep(u.FetchInterval)
		}

		u.printf("checking for updates...")

		bin, err := u.Fetcher.Fetch(u.Version)
		if err != nil {
			u.printf("failed to get latest version: %s", err)
			continue
		}

		if len(bin) == 0 {
			continue
		}

		tmpBinPath := filepath.Join(os.TempDir(), "goupgrade")
		if err := ioutil.WriteFile(tmpBinPath, bin, 0700); err != nil {
			u.printf("failed to write temp binary: %s", err)
			continue
		}

		cmd := exec.Command(tmpBinPath)
		cmd.Env = []string{getVersionVar + "=1"}
		cmdVer, err := cmd.Output()
		if err != nil {
			err = fmt.Errorf("failed to run temp binary: %s", err)
		}
		ver := string(cmdVer)
		if ver == u.Version {
			err = fmt.Errorf("version check failed, upgrade contained same version")
		}

		//best-effort remove tmp file
		os.Remove(tmpBinPath)

		if err != nil {
			u.printf("%s", err)
			continue
		}

		//version confirmed, replace!
		if err := ioutil.WriteFile(u.binPath, bin, u.binPerms); err != nil {
			u.printf("failed to replace binary: %s", err)
			continue
		}

		//note new version
		u.Version = ver
		u.printf("upgraded prog to: %s", ver)
		u.upgraded = true
		u.upgradedAt = time.Now()

		//send the chosen signal to prog
		if u.Signal != nil {
			u.printf("sending program signal: %s", u.Signal)
			u.signals <- &upsig{upgrade: true, sig: u.Signal}
		}
	}
}

func (u *upgrader) fork() {

	var cmd *exec.Cmd = nil
	//proxy native signals through to the child proc
	nativesigs := make(chan os.Signal)
	signal.Notify(nativesigs)
	go func() {
		for sig := range nativesigs {
			u.signals <- &upsig{upgrade: false, sig: sig}
		}
	}()

	//recieve all native and upgrade signals
	go func() {
		for s := range u.signals {
			if cmd == nil || cmd.Process == nil {
				continue
			}
			//child exited was meant for go-upgrade
			if !s.upgrade && s.sig.String() == "child exited" {
				continue
			}
			if err := cmd.Process.Signal(s.sig); err != nil {
				u.printf("failed to signal: %s (%s)", s.sig, err)
			} else {
				u.printf("signaled: %s", s.sig)
			}
		}
	}()

	for {
		u.printf("starting %s", u.binPath)
		cmd = exec.Command(u.binPath)
		e := os.Environ()
		e = append(e, isProgVar+"=1")
		cmd.Env = e
		cmd.Args = os.Args
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Start(); err != nil {
			log.Fatal("failed to start fork: %s", err)
			os.Exit(1)
		}

		err := cmd.Wait()
		cmd = nil
		code := 0
		if err != nil {
			code = 1
			if exiterr, ok := err.(*exec.ExitError); ok {
				if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
					code = status.ExitStatus()
				}
			}
		}
		u.printf("prog exited with %d", code)

		//if go-upgrade recently sent a signal, then allow it restart
		if u.upgraded && time.Now().Sub(u.upgradedAt) < u.RestartTimeout {
			u.upgraded = false
			continue
		}

		os.Exit(code)
	}
}

func (u *upgrader) printf(f string, args ...interface{}) {
	if u.Logging {
		log.Printf("[go-upgrade] "+f, args...)
	}
}
