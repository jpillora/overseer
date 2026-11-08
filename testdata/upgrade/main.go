// A small real server used to prove repeated upgrades and worker recovery.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/jpillora/overseer"
	"github.com/jpillora/overseer/opanic"
)

var version = "1"

type remote struct{ root, url string }

func (f remote) Init() error {
	for {
		if _, err := os.Stat(filepath.Join(f.root, "stall-init")); os.IsNotExist(err) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func (f remote) Fetch() (io.Reader, error) {
	for {
		if _, err := os.Stat(filepath.Join(f.root, "stall-fetch")); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(f.root, "force")); err == nil {
		overseer.ForceNextRestart()
		os.Remove(filepath.Join(f.root, "force"))
	}
	r, err := (&http.Client{Timeout: 2 * time.Second}).Get(f.url)
	if err != nil {
		return nil, err
	}
	if r.StatusCode != http.StatusOK {
		r.Body.Close()
		return nil, nil
	}
	return r.Body, nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "nested" {
		err := overseer.RunErr(overseer.Config{Required: true, NoWarn: true, Program: func(state overseer.State) {
			fmt.Printf("nested-parent:%d\n", os.Getppid())
		}})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	root := os.Getenv("OVERSEER_POC_ROOT")
	address := os.Getenv("OVERSEER_POC_ADDR")
	cfg := overseer.Config{
		Debug: true, TerminateTimeout: time.Second, WaitDelay: time.Second,
		MinFetchInterval: 100 * time.Millisecond,
		Fetcher:          remote{root, os.Getenv("OVERSEER_POC_URL")},
		ShouldRestart:    func() bool { _, err := os.Stat(filepath.Join(root, "busy")); return os.IsNotExist(err) },
		AuthorizeUpgrade: func(string) error {
			if _, err := os.Stat(filepath.Join(root, "deny")); err == nil {
				return fmt.Errorf("upgrade denied")
			}
			return nil
		},
		OnPanic: func(*opanic.Snapshot) {},
	}
	if os.Getenv("OVERSEER_POC_READY") != "" {
		cfg.ReadyTimeout = 700 * time.Millisecond
	}
	var events sync.Mutex
	cfg.OnUpgrade = func(e overseer.UpgradeEvent) {
		events.Lock()
		defer events.Unlock()
		f, err := os.OpenFile(filepath.Join(root, "events"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintf(f, "%s %s %v\n", e.Status, e.ID, e.Err)
			f.Close()
		}
		_ = overseer.MasterInfo() // Callbacks may inspect state.
	}
	if os.Getenv("OVERSEER_POC_OWN_LISTENER") == "" {
		cfg.Address = address
	}
	cfg.Program = func(state overseer.State) {
		if b, _ := os.ReadFile(filepath.Join(root, "fail-version")); string(b) == version {
			os.WriteFile(filepath.Join(root, "unready"), []byte(fmt.Sprint(os.Getpid())), 0600)
			if _, err := os.Stat(filepath.Join(root, "fail-bind")); err == nil {
				ln, err := net.Listen("tcp", state.Address)
				if err != nil {
					panic(err)
				}
				ln.Close()
				panic("binding unexpectedly succeeded")
			}
			if _, err := os.Stat(filepath.Join(root, "fail-hang")); err == nil {
				select {}
			}
			panic("candidate application initialization failed")
		}
		ln := state.Listener
		if ln == nil {
			var err error
			ln, err = net.Listen("tcp", address)
			if err != nil {
				panic(err)
			}
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"version": version, "pid": os.Getpid(), "parent": os.Getppid(), "id": state.ID, "path": state.BinPath, "args": os.Args[1:], "value": os.Getenv("OVERSEER_POC_VALUE")})
		})
		mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204); go overseer.Restart() })
		mux.HandleFunc("POST /nested", func(w http.ResponseWriter, r *http.Request) {
			path, _ := os.Executable()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, path, "nested")
			cmd.WaitDelay = time.Second
			out, err := cmd.CombinedOutput()
			if err != nil {
				http.Error(w, string(out)+err.Error(), 500)
				return
			}
			w.Write(out)
		})
		mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(204)
			go func() { time.Sleep(50 * time.Millisecond); os.Exit(0) }()
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln)
		if err := state.Ready(); err != nil {
			panic(err)
		}
		<-state.GracefulShutdown
		if _, err := os.Stat(filepath.Join(root, "hang")); err == nil {
			select {}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		srv.Shutdown(ctx)
		os.WriteFile(filepath.Join(root, fmt.Sprintf("cleaned-%d", os.Getpid())), []byte(version), 0600)
	}
	if err := overseer.RunErr(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
