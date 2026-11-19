package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/jpillora/overseer"
	"github.com/jpillora/overseer/fetcher"
)

var version string

func main() {
	client := &http.Client{Timeout: 2 * time.Second}
	overseer.Run(overseer.Config{
		Required: true, Address: os.Getenv("COMPAT_LISTEN"), MinFetchInterval: 200 * time.Millisecond,
		TerminateTimeout: 2 * time.Second,
		Fetcher: fetcher.Func(func() (io.Reader, error) {
			r, err := client.Get(os.Getenv("COMPAT_UPDATES"))
			if err != nil {
				return nil, err
			}
			if r.StatusCode != http.StatusOK {
				r.Body.Close()
				return nil, nil
			}
			return r.Body, nil
		}),
		Program: func(state overseer.State) {
			if ready, ok := any(state).(interface{ Ready() error }); ok {
				if err := ready.Ready(); err != nil {
					panic(err)
				}
			}
			snapshot := struct {
				Version    string
				Parent     int
				Supervised bool
			}{version, os.Getppid(), state.Enabled && os.Getenv("OVERSEER_IS_SLAVE") == "1"}
			go http.Serve(state.Listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				json.NewEncoder(w).Encode(snapshot)
			}))
			<-state.GracefulShutdown
		},
	})
}
