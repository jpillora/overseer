package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jpillora/overseer"
)

//see example.sh for the use-case

var BUILD_ID = "0"

//convert your 'main()' into a 'prog(state)'
//'prog()' is run in a child process
func prog(state overseer.State) {
	fmt.Printf("app#%s (%s) listening...\n", BUILD_ID, state.ID)
	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, _ := time.ParseDuration(r.URL.Query().Get("d"))
		time.Sleep(d)
		fmt.Fprintf(w, "app#%s (%s) says hello\n", BUILD_ID, state.ID)
	}))
	http.Serve(state.Listener, nil)
	fmt.Printf("app#%s (%s) exiting...\n", BUILD_ID, state.ID)
}

//then create another 'main' which runs the upgrades
//'main()' is run in the initial process
func main() {
	overseer.Run(overseer.Config{
		Log:     true, //display log of overseer actions
		Program: prog,
		Address: ":5001",
		// Fetcher: &fetcher.HTTP{
		// 	URL:      "http://localhost:5002/myappnew",
		// 	Interval: 1 * time.Second,
		// },
	})
}
