package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jpillora/overseer"
	"github.com/jpillora/overseer/fetcher"
)

//see example.sh for the use-case

// BuildID is compile-time variable
var BuildID = "0"

//convert your 'main()' into a 'prog(state)'
//'prog()' is run in a child process
func prog(state overseer.State) {
	fmt.Printf("app#%s (%s) listening...\n", BuildID, state.ID)
	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, _ := time.ParseDuration(r.URL.Query().Get("d"))
		time.Sleep(d)
		fmt.Fprintf(w, "app#%s (%s) says hello\n", BuildID, state.ID)
	}))
	http.Serve(state.Listener, nil)
	fmt.Printf("app#%s (%s) exiting...\n", BuildID, state.ID)
}

//then create another 'main' which runs the upgrades
//'main()' is run in the initial process
func main() {
	overseer.Run(overseer.Config{
		Program: prog,
		Address: ":5001",
		Fetcher: &fetcher.File{Path: "my_app_next"},
		Debug:   false, //display log of overseer actions
	})
}
