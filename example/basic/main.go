package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jpillora/go-upgrade"
	"github.com/jpillora/go-upgrade/fetcher"
)

var VAR = "" //set manually or with with ldflags

//convert your 'main()' into a 'prog(state)'
func prog(state upgrade.State) {
	log.Printf("app (%s) listening...", state.ID)
	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
		fmt.Fprintf(w, "Var is: %s", VAR)
	}))
	err := http.Serve(state.Listener, nil)
	log.Printf("app (%s) exiting: %v", state.ID, err)
}

//then create another 'main' which runs the upgrades
func main() {
	upgrade.Run(upgrade.Config{
		Program: prog,
		Address: "0.0.0.0:3000",
		Fetcher: &fetcher.HTTP{
			URL:      "http://localhost:4000/myapp2",
			Interval: 1 * time.Second,
		},
		Logging: true, //display log of go-upgrade actions
	})
}

//then see example.sh for upgrade workflow
