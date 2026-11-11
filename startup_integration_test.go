package overseer

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStartupUpdates(t *testing.T, binaries map[int32][]byte) {
	for _, mode := range []string{"stream", "timeout", "invalid", "rollback", "activation-denied"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "server"+extension())
			if err := os.WriteFile(path, binaries[1], 0755); err != nil {
				t.Fatal(err)
			}
			if mode == "rollback" {
				if err := os.WriteFile(filepath.Join(root, "fail-version"), []byte("2"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "activation-denied" {
				if err := os.WriteFile(filepath.Join(root, "deny-activation"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			bodyStarted, releaseBody, canceled := make(chan struct{}, 1), make(chan struct{}), make(chan struct{}, 1)
			updates := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "timeout" {
					<-r.Context().Done()
					canceled <- struct{}{}
					return
				}
				w.Header().Set("ETag", "startup-candidate")
				if r.Method == "HEAD" {
					return
				}
				if mode == "invalid" {
					w.Write([]byte("invalid binary"))
					return
				}
				if mode == "stream" {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					bodyStarted <- struct{}{}
					select {
					case <-releaseBody:
					case <-r.Context().Done():
						return
					}
				}
				w.Write(binaries[2])
			}))
			defer updates.Close()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			ln.Close()
			cmd := exec.Command(path)
			cmd.Env = append(os.Environ(), "OVERSEER_POC_ROOT="+root, "OVERSEER_POC_ADDR="+addr, "OVERSEER_POC_URL="+updates.URL, "OVERSEER_POC_HTTP=1", "OVERSEER_POC_STARTUP_TIMEOUT=500ms")
			// Activation must still be authorized and recover even without
			// application readiness enabled.
			if mode != "activation-denied" {
				cmd.Env = append(cmd.Env, "OVERSEER_POC_READY=1")
			}
			var output syncBuffer
			cmd.Stdout, cmd.Stderr = &output, &output
			cmd.WaitDelay = time.Second
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() {
				cmd.Process.Kill()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("startup master wait stuck")
				}
			}()
			starts := func() string { b, _ := os.ReadFile(filepath.Join(root, "starts")); return string(b) }
			if mode == "stream" {
				select {
				case <-bodyStarted:
				case <-time.After(5 * time.Second):
					t.Fatalf("startup did not fetch a stream\n%s", output.String())
				}
				// Headers arrive within the window. Keep the body blocked past it:
				// the first application process must still wait for the download.
				time.Sleep(750 * time.Millisecond)
				if got := starts(); got != "" {
					t.Fatalf("application started during accepted download: %q\n%s", got, output.String())
				}
				close(releaseBody)
			}
			wantVersion, wantStarts := "1", "1\n"
			if mode == "stream" {
				wantVersion, wantStarts = "2", "2\n"
			} else if mode == "rollback" {
				wantStarts = "2\n1\n"
			}
			client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
			deadline := time.Now().Add(15 * time.Second)
			for {
				r, err := client.Get("http://" + addr)
				var body []byte
				if err == nil {
					body, _ = io.ReadAll(r.Body)
					r.Body.Close()
				}
				if strings.Contains(string(body), `"version":"`+wantVersion+`"`) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("startup version %s not served\n%s", wantVersion, output.String())
				}
				time.Sleep(20 * time.Millisecond)
			}
			if got := starts(); got != wantStarts {
				t.Fatalf("unexpected application starts: %q, want %q\n%s", got, wantStarts, output.String())
			}
			if mode == "timeout" {
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("startup HTTP request was not canceled")
				}
				// Cancellation must not restart the app when the old attempt ends.
				time.Sleep(300 * time.Millisecond)
				if got := starts(); got != wantStarts {
					t.Fatal("startup timeout caused a late restart", got)
				}
			}
			if mode != "stream" {
				installed, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(installed, binaries[1]) {
					t.Fatal("startup failure did not preserve original executable", err)
				}
			}
		})
	}
}
