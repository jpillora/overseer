package main

import (
	"log"
	"os"
	"time"

	"github.com/jpillora/go-upgrade"
)

var VERSION = "0.0.0" //set with ldflags

//change your 'main' into a 'prog'
func prog() {
	log.Printf("Running version %s...", VERSION)
	select {}
}

//then create another 'main' which runs the upgrades
func main() {
	upgrade.Run(upgrade.Config{
		Program: prog,
		Version: VERSION,
		Fetcher: upgrade.BasicFetcher(
			"http://localhost:3000/myapp_{{.Version}}",
		),
		FetchInterval: 2 * time.Second,
		Signal:        os.Interrupt,
		//display logs of actions
		Logging: true,
	})
}

//then see example.sh for upgrade workflow
