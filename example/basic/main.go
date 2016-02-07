package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jpillora/go-upgrade"
	"github.com/jpillora/go-upgrade/fetcher"
)

var FOO = "" //set manually or with with ldflags

//convert your 'main()' into a 'prog(state)'
func prog(state upgrade.State) {
	log.Printf("app (%s) listening...", state.ID)
	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Foo", FOO)
		w.Header().Set("Header-Time", time.Now().String())
		w.WriteHeader(200)
		time.Sleep(30 * time.Second)
		fmt.Fprintf(w, "Body-Time: %s (Foo: %s)", time.Now(), FOO)
	}))
	http.Serve(state.Listener, nil)
}

//then create another 'main' which runs the upgrades
func main() {
	upgrade.Run(upgrade.Config{
		Program: prog,
		Address: "0.0.0.0:3000",
		Fetcher: &fetcher.HTTP{
			URL:      "http://localhost:4000/myapp2",
			Interval: 5 * time.Second,
		},
		Logging: true, //display log of go-upgrade actions
	})
}

//then see example.sh for upgrade workflow
