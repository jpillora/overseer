//go:build linux || darwin

package overseer

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Build the same app against released modules and this checkout, then let an
// already running master install the other version over HTTP.
func TestMixedVersionUnixUpgrades(t *testing.T) {
	if os.Getenv("OVERSEER_TEST_LEGACY") != "1" {
		t.Skip("set OVERSEER_TEST_LEGACY=1 to download and test older overseer releases")
	}
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("testdata/compat/main.go")
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{"v1.2.0", "v1.5.0", "v1.5.1-0.20261016233744-37aec7a9aa1c", "current"}
	binaries := make(map[string][]byte, len(versions))
	for _, version := range versions {
		dir := t.TempDir()
		module := "module example.com/overseer-compat\n\ngo 1.23.0\n\nrequire github.com/jpillora/overseer " + version + "\n"
		if version == "current" {
			module = strings.ReplaceAll(module, " current\n", " v1.5.0\n")
			module += fmt.Sprintf("replace github.com/jpillora/overseer => %q\n", repo)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), source, 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "app")
		build := exec.Command("go", "build", "-mod=mod", "-ldflags=-X main.version="+version, "-o", path, ".")
		build.Dir = dir
		build.Env = append(os.Environ(), "GOWORK=off")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", version, err, output)
		}
		binary, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		binaries[version] = binary
	}
	for _, old := range versions[:len(versions)-1] {
		for _, versions := range [][2]string{{old, "current"}, {"current", old}} {
			from, to := versions[0], versions[1]
			t.Run(from+"_to_"+to, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "app")
				if err := os.WriteFile(path, binaries[from], 0755); err != nil {
					t.Fatal(err)
				}
				var offer atomic.Bool
				updates := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if !offer.Load() {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					w.Write(binaries[to])
				}))
				defer updates.Close()
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				addr := ln.Addr().String()
				ln.Close()
				var output syncBuffer
				cmd := exec.Command(path)
				cmd.Env = append(os.Environ(), "COMPAT_LISTEN="+addr, "COMPAT_UPDATES="+updates.URL)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				cmd.Stdout, cmd.Stderr = &output, &output
				cmd.WaitDelay = time.Second
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					_ = cmd.Wait()
				}()
				client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
				wait := func(version string) {
					t.Helper()
					deadline := time.Now().Add(12 * time.Second)
					for time.Now().Before(deadline) {
						resp, err := client.Get("http://" + addr)
						if err == nil {
							var state struct {
								Version    string
								Parent     int
								Supervised bool
							}
							err = json.NewDecoder(resp.Body).Decode(&state)
							resp.Body.Close()
							if err == nil && state.Version == version {
								if state.Parent != cmd.Process.Pid || !state.Supervised {
									t.Fatalf("inherited listener or supervision changed: %+v\n%s", state, output.String())
								}
								return
							}
						}
						time.Sleep(20 * time.Millisecond)
					}
					t.Fatalf("version %s did not serve the inherited address\n%s", version, output.String())
				}
				wait(from)
				offer.Store(true)
				wait(to)
			})
		}
	}
}
